# Change events: what a store reports, and why `Op` is `set`/`delete`

This note records why `entity.Op` is two-valued (`set`, `delete`) rather than
create/update/delete, why `Since` is a pure read, and why that keeps verse Storage
on a portable baseline instead of an exotic one.

You watch the stream this describes in
[tutorial §4](tutorial.md#4-charters--relating-entities-to-tasks), and
[reference/store.md](reference/store.md) sketches the store it
comes out of.

## The question

A change event needs to carry enough for a trigger to react. The rich shape —
`Op ∈ {create, update, delete}` plus full before/after payloads — is trivial for a store
that holds prior state in memory, but expensive for a durable, remote one. Faking it over the
logd-backed store takes a mutable `seen` set threaded through both the live watch and
`Since`, which makes the authoritative catch-up path impure, mislabels creates inside a
replay window, and diverges from the interface — all symptoms of one thing, the
store synthesising *edge* semantics the substrate doesn't carry. So: what should a change
event actually carry, and is matching edge- or level-triggered?

## What the watch actually delivers (measured)

A watch on the root streams one event per commit, each carrying the commit's
`Patch`. Probing the production path — verse → docd → logd — with a create, a full
update, a partial one, a delete, and a re-create:

```
CREATE   demo.pr.1         {verse:{demo:{pr:{"1":{stage:open,      x:1}}}}}
UPDATE   demo.pr.1 (full)  {verse:{demo:{pr:{"1":{stage:reviewing, x:1}}}}}
UPDATE   demo.pr.1 (deep)  {verse:{demo:{pr:{"1":{stage:done}}}}}
DELETE   demo.pr.1         {verse:{demo:{pr:{"1":null}}}}
RECREATE demo.pr.1         {verse:{demo:{pr:{"1":{stage:reopened}}}}}
```

Nothing is added by going through docd, and that is structural rather than a
coincidence of this version: docd's client face is *the logd session protocol verbatim*,
so a client cannot tell which it is talking to, and a watch that docd routes rather than
composes is logd's stream forwarded. What docd changes about watches is availability, not
vocabulary — see the mount hazard below.

Two facts settle the design:

1. Create and update are byte-identical. Adding the new key `"1"` looks exactly
   like replacing an existing `"1"`. There is no add-vs-replace marker, no new-node
   flag — create-vs-update is simply not in the stream. The re-create says the same
   thing from the other side: the commit that brings `demo:pr:1` back after its
   tombstone is a plain merge, indistinguishable from any other write of `stage`.
   `WatchEvent` carries `{commit, path, state, patch, replayComplete, ended, endReason}`
   and nothing else — no pre-image field to read, and no watch option that asks for one.
2. The delta is the writer's applied patch, verbatim — not a computed old→new
   diff. The full update re-emitted the unchanged `x:1`; a diff would have dropped
   it. logd forwards *what was written this commit*, carrying zero knowledge of prior
   state. (This holds for the baseline watch. A scoped watch over a
   copy-on-write overlay is different: logd recomputes the scope's view and emits a
   *computed* diff between scope states, whose first delta — diffed from an empty/null
   baseline — is a `!replace`, not an additive merge. The encoding of a delta is logd's
   to choose; see "Consuming a delta" below.)

So the watch is already an edge stream (one event per commit = one transition),
and the delta *is* the edge. We don't have to choose level-triggering and lose
transients — logd hands us edges. And a delta carries exactly two recoverable facts,
both without prior state: a removal → `Delete`, any value → `Set`. That is the whole
of `Op`.

A removal is the **`!delete` tag**, not a null. A delta is where a removal has to be
spelled, because there is no absence to observe in a patch — and the tag is what verse
writes (`entity.Removed`) and what `tony.Diff` emits for a key that went away. A bare
null in a delta is an ordinary value being set: `payload: null` is a legal payload, and
reading it as a removal is what made it one no entity could hold (`entity/logdstore.go`,
`walkEntityChanges`).

## The decision

- `Op` is `set` | `delete`. Create-vs-update is dropped — it needs prior
  existence the substrate doesn't provide, and requiring it would tie the store to
  an uncommon capability (below). A create-or-update is `Set` either way, so a charter
  does not depend on which it was.
- **`Since` is a pure read.** It derives `Op` from the delta alone and mutates no
  store state, so it is idempotent and safe under concurrent/repeated callers, and the
  store keeps no `seen` set.
- **`To` is the ENTITY as of that commit, not the delta.** *When* an event happens is an
  edge (one per commit); *what it says* is level (the whole entity at that commit). See
  "Why `To` is level" below, which is the one decision here that measurement settles.

## Why `To` is level (and what the delta would answer instead)

The delta — the writer's patch for that commit — is the tempting choice: if verse only ever
writes whole entities, the delta *is* the entity. That reasoning holds exactly as long as its
premise. The moment a write can address a field
(`demo:pr:1.stage`), the delta stops being the entity, and a consumer matching on `To` is
matching a fragment.

Measured, on both substrates — a deep write of one field, watched:

```
logd   patch at demo:pr:1.stage -> event To = {stage: reviewed}
docd   patch at demo:pr:1.stage -> event To = {stage: reviewed}
```

The entity at that commit is `{stage: reviewed, env: prod}`. `env` is simply not in the
event. And `entity.Pattern.MatchEvent` matches `To`, so with `To` as the delta a trigger
whose `when` reads two fields fires on a whole-entity write and stays **silent** on the deep
write that reaches the identical state:

```
whole-entity write to the matching state: fired = 1
deep write to the same state:             fired = 0
```

Nothing errors. The trigger simply never runs. A rule about the world has to be evaluated
against the world, so the store levels: `LogdStore.eventsFromDelta` reports the entity as of
the commit, and the store — not each consumer — is where that is paid for.

**As of the commit, not "now".** The level state is read at the event's own commit, so an
event still reports the world as it was when it changed: a transient state that has already
been overwritten is still dispatched. Levelling against *current* state would have been
simpler still and is why the doorbell shape (`Event{Ref, Op, Rev}`, consumer reads state)
was rejected — a workflow engine that skips a step because the subject moved on fails in a
way nobody notices.

**It costs nothing per event.** The store levels from the watched document it already
maintains — seeded by the watch's initial `State`, advanced by each delta (the same
apply-don't-decode discipline as below) — so no read is issued per event. A `Since` replay
reads the document once, at `from`, and rolls it forward with the deltas it is replaying;
the alternative, a point-in-time read per changed entity, is exact too but puts a network
round trip per entity on the watch goroutine, where the stream stalls behind its own
bookkeeping. (It also matters that those reads are not cheap on a grown log: go-tony
`bvm163tyh12krwcqcsn0`.)

One assumption remains: the **set of changed refs** still comes from
walking the delta's structure, and only the payloads come from the applied document. That is
safe for a baseline delta (a verbatim merge) and is the residual noted at the end of the
next section.

## Consuming a delta: apply it, don't decode it

`Op` is derived from a delta, but *how* a consumer reads the delta matters. The
encoding varies and is logd's business, not ours: the baseline watch forwards the
writer's patch verbatim (a plain object merge); a scoped COW watch emits a *computed*
diff whose first event is a `!replace` (diffed from null) and whose later events are
merges; a foreign writer might use `!delete`/`!insert`. A consumer that inspects the
*structure* of the patch to see which entities appeared will silently miss some — a
merge-style walk cannot pull the appeared key out of a `!replace`. That is exactly what makes
an N-file `verse connect` fire N−1 validators: the first scoped delta, the `!replace`, is the
one dropped.

The contract is therefore: apply the delta, don't decode it. A subtree watcher
maintains the watched slice as a document, applies each event to it
([`tony.Patch`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony#Patch) for a
Patch, replace for a State — opaquely, whatever ops it carries), and reads entities out
of the *reconstructed object*, diffing against the last snapshot for per-entity
`Set`/`Delete`. tony owns patch semantics (merge vs `!replace` vs `!delete`/`!insert`,
field order); verse applies them and never depends on the wire shape of a diff.

That diff compares payloads with `ir.Node.DeepEqual`, and the choice is load-bearing rather
than stylistic: a node carries `Parent`/`ParentIndex`/`ParentField`, which say where it sits
in the document rather than what it holds, and a document rebuilt from a fresh read wires
those up anew. `reflect.DeepEqual` compares them and so calls a value unequal to itself;
ir's `DeepEqual` documents that it does not. Under `reflect` every entity in a slice diffs
as changed after any resync, so re-establishing a watch re-emits the whole slice and every
trigger over it fires again for nothing. This is
what `LogdStore.WatchSubtree` does — and also what the root watch and the `Since`
replay do, to level their payloads (`applyDelta`). The residual: those two still take the
*set of changed refs* from the delta's structure (`walkEntityChanges`) rather than from a diff of
the reconstructed document, which is safe only because a baseline delta is always a verbatim
merge, never a scope-computed `!replace`. A root watch over a scope would need the diff.

**A State and a Patch are not rooted alike, and neither one says so.** A Patch is the
commit's delta rooted at the document (`verse.demo.pr.1: {…}`); a State carries the value
*at the watched path* (logd extracts it; docd's composed read builds it relative to the
path). Taken verbatim, a State seeds a document whose root segment is missing — so every
read of it navigates root → system → kind and finds nothing, the seed contributes no
entities, and the level state silently degenerates to *whatever has been patched since the
watch opened*. A deep write of one field then reports an entity of one field, and a trigger
binds `$note` from a payload that has no note in it: not a crash, a task whose prompt reads
literally `$note`. Both watchers re-root the State (`entity.stateDoc`) so the document has
one shape.

The diff makes the subtree watch edge-on-real-change: an entity whose reconstructed content
is unchanged emits nothing, so a write that changes no content is *not a reactive edge*.
That is what keeps a reactive re-write from looping: a trigger whose effect re-touches its own
cause writes the *same value*, so no edge fires and the trigger does not re-run.

**This is load-bearing rather than merely convenient.** A trigger is evaluated at every
commit that touches what it reads, so what ends a rule is that its own effect raises no further
evaluation: a latching action writes a new value once and then selects nothing, and one
re-writing what is already there is inert. Measured, and pinned by
`entity.TestSameValueWriteRaisesNoCommit`: the revision does not advance and neither watch
reports anything. Note it is not a compare-and-swap result — `entity.Modify` retries on a failed
precondition, so it prevents lost updates rather than repeated work. The substrate simply
declines to record a change that is not one.

**One commit in, one commit out.** The subtree watch does not coalesce a burst — apply every
queued delta, diff once — which is exact for a level and lossy for an EDGE: a value written at
one commit and overwritten at the next would leave only the later one, and a transition the
stream never reported cannot be recovered downstream. Since a trigger's cause is a rising edge,
that is a firing that never happens, so the watch pays a subtree walk per commit rather than
per burst.

## The seed is replayed: a subtree watch is level-triggered at the open

The subtree watch diffs its opening snapshot against an *empty* one, so everything already
standing in the slice arrives as a `Set` before the first live change. A trigger installed
over a populated verse therefore acts on the backlog that already matches it. This is the
level reading taken to its conclusion: if `To` is the entity rather than the delta because a
rule is a statement about the world, then *which* entities a rule is about cannot depend on
who happened to be listening when they were written. A task sitting at `stage: ready` since
before the charter existed matches perfectly and — without the replay — fires nothing, until
somebody re-writes it to announce a state it has held all along. That re-write is a
workaround for the substrate, not work anyone wanted to do.

A *re-established* watch does not replay: a reconnect's `State` is diffed against the
snapshot the previous one left, so it emits only the gap. Replay is a property of the open,
not of the `State` event.

That is also what makes a watch able to heal itself. A subtree watch re-establishes after an
interval of silence as well as after an end it was told about — because a watch can stop for
reasons the consumer never hears (logd drops a watcher whose buffer it cannot drain), and a
consumer that can only ever be told waits forever whenever the telling is what breaks. The
resync costs one `State` and, since the diff is against the snapshot already held, emits
nothing at all when the slice merely happened to be idle.

`Observe` (the root watch) stays "from now" and does not replay — it is the whole-verse
doorbell behind the debug stream, and its consumer, the dispatch loop, starts from
`store.Rev()` and reconciles with `Since`. Level-at-open belongs to the watch that *is* a
trigger's observation.

The cost is the charter author's to carry, and it is the cost re-entry already had: what
fires is "matches now", so a non-idempotent action behind a state a subject can re-enter
fires again when the engine restarts over a verse still in that state. The latch idiom
answers it — match a field the action advances — and a re-write to the same value stays inert
(see edge-on-real-change, below).

## One commit, many entities: the entity is the reactive grain

A logd commit is a single, atomic transaction that can patch many branches at once —
several `verse.<system>.<kind>.<id>` paths, across systems and kinds. But an
`entity.Event` names exactly one `Ref`, so the store normalises: `walkEntityChanges` recurses
the commit's delta and, at path-depth 3 (`system/kind/id`), emits one `Event` per
entity — all sharing that commit's number as their `Rev` (`LogdStore.eventsFromDelta`).
Patch five branches in one tx → five per-entity events; patch a whole kind subtree, or
above it → it descends to each `id` and still yields one event apiece. The "one entity per
change" grain is imposed *here*, at the logd↔store seam, not assumed by the engine.

This is what makes a trigger's subject well-defined: a firing is driven by one event,
which carries one `Ref`, so `$subject` — and hence the flow, and a goal keyed on it — is
always exactly one entity. Not because a match *pattern* is constrained to one entity (it
isn't; `{system:"demo"}` matches every demo entity), but because dispatch is
per-entity-edge. A broad pattern simply fans out to one independent firing per matching
change.

The normalisation is reported *inside* its transaction rather than instead of it, which is
what keeps the entity grain from costing the tx. `Observe`, `Since` and `WatchSubtree` all
deliver an `entity.Commit` (`{Rev, Changes}`), so a commit that atomically sets `demo:pr:1`
*and* `demo:pr:2` arrives as one commit carrying two per-entity changes, and the trigger
engine is asked once per commit rather than once per change. Each change still drives its own
firing with its own subject; what the commit adds is that a consumer can see which changes
were one transaction.

That grouping has to come from the store. Changes of one commit share a `Rev`, but a consumer
reading them one at a time cannot tell whether the next belongs to the same commit without
waiting for the one after it — while the store, which built them from a single delta, knows.
See [the commit is the unit](trigger-model.md#the-commit-is-the-unit) for what the engine does
with one.

## A history-bearing watch, and a mount

Everything above assumes the watch *has* a history behind it. A watch whose path is a
strict ancestor of a controller mount is **composed** — docd fans it across the base owner
and every mount below, and merges them into one stream — and a composed watch cannot
honour `FromCommit`: the sub-streams have independent commit sequences, so no single
resume commit replays them. It does not refuse. It drops the replay request and never
signals that the replay is complete, so a `Since` over such a path returns `ok=true` with
zero events once it runs out its own timeout — a successful-looking under-delivery of the
engine's catch-up, which is the worst shape the failure could take.

A consumer could be made resilient to mounts arriving and leaving under it. verse takes a
namespace split instead, because it is simpler: mount-owned slices render under a sibling
of the entity root, so no watch verse takes is ever an ancestor of a mount. One root watch,
`Since` untouched. The price is that a mounted slice fires no triggers — which is the right
price, since a mount is task-driven and has no history to observe in the first place. See
[controllers](controllers.md).

## Why this is the portable choice (not an exotic dependency)

The capability verse leans on — an ordered, resumable, revisioned change stream
(watch + `Since(cursor)`) — is the modern-store baseline, not a logd peculiarity:
etcd watches, FoundationDB, Kafka offsets, Cockroach/Mongo changefeeds, Postgres
logical replication, or an in-memory ring all provide it. What *would*
tie verse to an uncommon feature is requiring create-vs-update or prior-value in
the interface — etcd exposes it (`CreateRevision`, `PrevKv`), many stores don't,
and logd (measured above) doesn't either. Keeping create/update out of the contract
is therefore both the portable choice and the one the substrate can actually honour.

Note the asymmetry, which is not an accident: create-vs-update is unavailable when
*observing* a change but perfectly available when *making* one. A write states its own
precondition (`entity.Created()` / `entity.Updated()`), and the store checks it atomically
with the write. You cannot ask the stream "was that a create?"; you can insist, at the
moment of writing, that it be one.

## What dropping create-vs-update costs (and the idiom that recovers it)

`Op` only ever surfaced to charter authors via `EventMatch.Op`. The losses:

- Kept for free: `delete` is exact (tombstone), so teardown-on-removal is
  unaffected; and because the delta is an edge, "fire when a field *becomes* X" is
  still once-per-transition (the commit that set it), not once-per-incidental-change.
- The one real loss: you can't distinguish "created with `stage=open`" from
  "re-opened to `stage=open`" — both deltas set `stage=open`, so both fire. You lose
  native re-entry suppression ("act on the first entry into a state, never on
  re-entry").
- The idiom that recovers it: express "once" as a state latch, not an edge —
  match a field the action then advances (`stage=open` → action sets
  `stage=reviewing`, so it can't re-fire). verse's demo already works this way; the
  subject's stage *is* the latch. This is cleaner than an implicit edge, and it's how
  level-triggered systems get once-semantics.
- The footgun to know: a non-idempotent *gated* or *spawning* action that matches a state
  it can genuinely re-enter — a real round-trip like `reviewed`→`open`→`reviewed` — re-fires
  on each real entry. (A re-write to the *same* value does not: edge-on-real-change makes it
  inert, so a reactive re-write that re-touches its own cause does not loop — see "Consuming a
  delta" above.) For a genuine re-entry you still want a latch — charter-authoring discipline,
  not an engine limit.

Net: low, localised harm — you keep delete-exact, once-per-transition, and every
state-driven flow; you give up native "once, ever," recoverable with a latch.

## Residuals (punted, by design)

- **Cheap re-entry suppression** — for the *observer*. The write side is settled: a
  create-only write is `entity.Created()`, a conditional patch that fails if anything is
  already there, checked with the write in one step. What is still missing is the event
  saying *which* it was, so a trigger cannot say "fire only on first entry"; the latch idiom
  above remains the answer, and no charter has needed more yet.
- **Truncation detection** — *closed upstream*, and worth re-reading if you remember it
  as open. logd persists a **replay floor** (the highest commit whose delta history
  compaction has erased) and refuses a `FromCommit` watch below it with a terminal
  `replay_compacted` event, sent *before* any state — a state read below the floor is
  itself only snapshot-accurate, and handing a client an untrustworthy state and then an
  error is worse than the error alone. libctl raises that terminal event as a
  `WatchEndedError`, so it arrives at `Since` as `w.Err() != nil` and becomes `ok=false`
  with no special-casing. Measured through the production path (verse → docd → logd)
  and pinned by `entity.TestSinceBelowReplayFloorResyncs`: floor at 5,
  `Since(0)` → `ok=false`, zero commits. The distinct code is the point — `replay_failed`
  reads as transient, and a consumer told that retries the same doomed cursor forever.
- **No way to ask the floor in advance.** What is left of the above is smaller and
  different in kind: the floor is discoverable only by asking for a replay and being
  refused. There is still no client query for it (etcd's compaction revision has no
  equivalent in the session protocol). That is enough for `Since`, whose only decision is
  ok-or-resync, and not enough for a consumer that wants to know how much history it has
  before it needs it. The store's low-watermark — `Since(from)` returns `ok=false` when
  `from` predates `openCommit`, the commit this instance synced at — therefore stays, but
  it is now the cheap first guard rather than the only one, and a deliberately
  conservative one: after a restart it refuses cursors logd would still have served.
