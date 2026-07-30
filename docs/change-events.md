# Change events: what a store reports, and why `Op` is `set`/`delete`

This note records why `entity.Op` is two-valued (`set`, `delete`) rather than
create/update/delete, why `Since` is a pure read, and why that keeps verse Storage
on a portable baseline instead of an exotic one.

You watch the stream this describes in
[tutorial §4](tutorial.md#4-charters--relating-entities-to-processes), and
[reference/store.md](reference/store.md) sketches the store it
comes out of.

## The question

A change event needs to carry enough for a trigger to react. The rich shape —
`Op ∈ {create, update, delete}` plus full before/after payloads — is trivial for a store
that holds prior state in memory, but expensive for a durable, remote one. The logd-backed
store originally faked it with a mutable `seen` set threaded through both the live watch and
`Since`, which made the authoritative catch-up path impure, mislabelled creates inside a
replay window, and diverged from the interface. Those were symptoms of one thing: the
store synthesising *edge* semantics the substrate doesn't carry. So: what should a change
event actually carry, and is matching edge- or level-triggered?

## What logd's watch actually delivers (measured)

A watch on the root streams one event per commit, each carrying the commit's
`Patch`. Probing a real logd (create then update the same key, then a partial patch):

```
CREATE demo.pr.1           {verse:{demo:{pr:{"1":{stage:open,      x:1}}}}}
UPDATE demo.pr.1 (full)    {verse:{demo:{pr:{"1":{stage:reviewing, x:1}}}}}
UPDATE demo.pr.1 (partial) {verse:{demo:{pr:{"1":{stage:done}}}}}
```

Two facts settle the design:

1. Create and update are byte-identical. Adding the new key `"1"` looks exactly
   like replacing an existing `"1"`. There is no add-vs-replace marker, no new-node
   flag — create-vs-update is simply not in the stream.
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
both without prior state: a tombstone (null leaf) → `Delete`, any value →
`Set`. That is the whole of `Op`.

## The decision

- `Op` is `set` | `delete`. Create-vs-update is dropped — it needs prior
  existence the substrate doesn't provide, and requiring it would tie the store to
  an uncommon capability (below). A create-or-update is `Set` either way, so a charter
  does not depend on which it was.
- **`Since` is a pure read.** It derives `Op` from the delta alone and mutates no
  store state, so it is idempotent and safe under concurrent/repeated callers. The
  `seen` set is gone.
- **`To` is the ENTITY as of that commit, not the delta.** *When* an event happens is an
  edge (one per commit); *what it says* is level (the whole entity at that commit). See
  "Why `To` is level" below — this is the one decision here that was reversed, and it was
  reversed by measurement.

## Why `To` is level (and how a delta answered a question nobody asked)

`To` used to be the delta — the writer's patch for that commit — on the reasoning that
verse only ever wrote whole entities, so the delta *was* the entity. That reasoning held
exactly as long as the premise did. The moment a write can address a field
(`demo:pr:1.stage`), the delta stops being the entity, and a consumer matching on `To` is
matching a fragment.

Measured, on both substrates — a deep write of one field, watched:

```
logd   patch at demo:pr:1.stage -> event To = {stage: reviewed}
docd   patch at demo:pr:1.stage -> event To = {stage: reviewed}
```

The entity at that commit is `{stage: reviewed, env: prod}`. `env` is simply not in the
event. And `entity.Pattern.MatchEvent` matches `To`, so a trigger whose `when` reads two
fields fired on a whole-entity write and stayed **silent** on the deep write that reached
the identical state:

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

One assumption remains, unchanged from before: the **set of changed refs** still comes from
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
merge-style walk cannot pull the appeared key out of a `!replace`. That is exactly the
bug that once made an N-file `verse connect` fire N−1 validators (the first scoped
delta, the `!replace`, was dropped).

The contract is therefore: apply the delta, don't decode it. A subtree watcher
maintains the watched slice as a document, applies each event to it
([`tony.Patch`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony#Patch) for a
Patch, replace for a State — opaquely, whatever ops it carries), and reads entities out
of the *reconstructed object*, diffing against the last snapshot for per-entity
`Set`/`Delete`. tony owns patch semantics (merge vs `!replace` vs `!delete`/`!insert`,
field order); verse applies them and never depends on the wire shape of a diff. This is
what `LogdStore.WatchSubtree` does — and now also what the root watch and the `Since`
replay do, to level their payloads (`applyDelta`). The residual: those two still take the
*set of changed refs* from the delta's structure (`walkEntities`) rather than from a diff of
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

**This is now load-bearing rather than merely convenient.** A trigger is evaluated at every
commit that touches what it reads, so what ends a rule is that its own effect raises no further
evaluation: a latching action writes a new value once and then selects nothing, and one
re-writing what is already there is inert. Measured, and pinned by
`entity.TestSameValueWriteRaisesNoCommit`: the revision does not advance and neither watch
reports anything. Note it is not a compare-and-swap result — `entity.Modify` retries on a failed
precondition, so it prevents lost updates rather than repeated work. The substrate simply
declines to record a change that is not one.

**One commit in, one commit out.** The subtree watch used to coalesce a burst — apply every
queued delta, diff once — which is exact for a level and lossy for an EDGE: a value written at
one commit and overwritten at the next left only the later one, and a transition the stream
never reported cannot be recovered downstream. Since a trigger's cause is a rising edge, that
is a firing that never happens, so the coalescing is gone and the cost is a subtree walk per
commit rather than per burst.

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
`entity.Event` names exactly one `Ref`, so the store normalises: `walkEntities` recurses
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

The subtlety (and its cost), as it stood when this was written — see Residuals, where it is
now closed. This normalisation was deliberately lossy in one way: the reactive grain was the
entity, not the transaction. A commit that atomically sets
`demo:pr:1` *and* `demo:pr:2` surfaces as two independent events; the engine dispatches
them one at a time and fires each trigger with its own subject. The shared `Rev` is the
*only* trace that they were one tx — and nothing in the engine groups by `Rev`. So a
well-defined subject is guaranteed, but cross-entity, transaction-scoped reactivity does
not exist: you cannot write a trigger that reacts to "these two changed together in one
tx" as a unit. The store can *write* atomically across entities; the engine can only
*observe* the result as separate entity edges. This is subtle and may warrant revisiting —
see Residuals.

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

- ~~Transaction-scoped reactivity.~~ **CLOSED.** The framing this called for is now the unit
  of observation: `Observe`, `Since` and `WatchSubtree` all deliver an `entity.Commit`
  (`{Rev, Changes}`), and the trigger engine is asked once per commit rather than once per
  change. It had to come from the store, exactly as this residual suspected the raw material
  was there but unusable: changes of one commit share a `Rev`, but a consumer reading them one
  at a time cannot tell whether the next belongs to the same commit without waiting for the one
  after it — while the store, which built them from a single delta, knows. The per-entity
  normalisation below is unchanged; what changed is that it is now reported inside its
  transaction rather than instead of it.
- **Cheap re-entry suppression** — for the *observer*. The write side is settled: a
  create-only write is `entity.Created()`, a conditional patch that fails if anything is
  already there, checked with the write in one step. What is still missing is the event
  saying *which* it was, so a trigger cannot say "fire only on first entry"; the latch idiom
  above remains the answer, and no charter has needed more yet.
- **Truncation detection.** logd *silently under-delivers* a `Since` range it has
  compacted (empty index range → no error), so `ok=false` can't come from an error.
  The store guards with a low-watermark: `Since(from)` returns `ok=false` when `from`
  predates `openCommit` (the commit this instance synced at), forcing a resync — this
  catches a cursor carried across a restart. A cursor *within* this instance's
  lifetime that logd nonetheless compacts is an uncaught residual; closing it needs
  an upstream "earliest retained commit" query (an etcd-compaction-revision
  equivalent), which logd doesn't expose yet.
