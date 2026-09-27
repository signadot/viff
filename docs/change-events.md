# Change events

`entity.Op` is two-valued, `set` or `delete`, rather than create/update/delete. `Since` is a pure
read. This note says why, and why those keep verse's storage on a portable baseline rather than an
exotic one.

You watch the stream this describes in
[tutorial §4](tutorial.md#4-charters--relating-entities-to-tasks), and
[reference/store.md](reference/store.md) sketches the store it comes out of.

## The question

A change event needs to carry enough for a trigger to react. The rich shape —
`Op ∈ {create, update, delete}` plus full before/after payloads — is trivial for a store that holds
prior state in memory and expensive for a durable, remote one. Faking it over the logd-backed store
takes a mutable `seen` set threaded through both the live watch and `Since`, which makes the
authoritative catch-up path impure, mislabels creates inside a replay window, and diverges from the
interface. Those are all symptoms of one thing: the store synthesising *edge* semantics the
substrate does not carry. So what should a change event carry, and is matching edge- or
level-triggered?

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

Nothing is added by going through docd, and that is structural rather than a coincidence. docd's
client face is *the logd session protocol verbatim*, so a client cannot tell which it is talking
to, and a watch that docd routes rather than composes is logd's stream forwarded. What docd changes
about watches is availability rather than vocabulary; see the mount hazard below.

Two facts settle the design:

1. Create and update are byte-identical. Adding the new key `"1"` looks exactly
   like replacing an existing `"1"`. There is no add-vs-replace marker, no new-node
   flag — create-vs-update is simply not in the stream. The re-create says the same
   thing from the other side: the commit that brings `demo.pr.1` back after its
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

So the watch is already an edge stream — one event per commit is one transition — and the delta
*is* the edge. There is no need to choose level-triggering and lose transients, because logd
hands over edges. And a delta carries exactly two recoverable facts, both without prior state: a
removal is a `Delete`, any value is a `Set`. That is the whole of `Op`.

A removal is the **`!delete` tag**, not a null. A delta is where a removal has to be spelled,
because there is no absence to observe in a patch, and the tag is what verse writes
(`entity.Removed`) and what `tony.Diff` emits for a key that went away. A bare null in a delta is
an ordinary value being set: `payload: null` is a legal payload, and reading it as a removal would
make it one no entity could hold (`entity/change.go`, `mergeInto`).

## The decision

- `Op` is `set` or `delete`. Create-vs-update is out: it needs prior existence the substrate does
  not provide, and requiring it would tie the store to an uncommon capability, as below. A
  create-or-update is `Set` either way, so a charter does not depend on which it was.
- **`Since` is a pure read.** It derives `Op` from the delta alone and mutates no store state, so
  it is idempotent and safe under concurrent or repeated callers, and the store keeps no `seen`
  set.
- **`To` is the entity as of that commit, not the delta.** *When* an event happens is an edge, one
  per commit; *what it says* is level, the whole entity at that commit. See "Why `To` is level"
  below, which is the one decision here that measurement settles.

## Why `To` is level (and what the delta would answer instead)

The delta, the writer's patch for that commit, is the tempting choice: if verse only ever writes
whole entities, the delta *is* the entity. That reasoning holds exactly as long as its premise. The
moment a write can address a field (`demo.pr.1.stage`), the delta stops being the entity, and a
consumer matching on `To` is matching a fragment.

Measured, on both substrates — a deep write of one field, watched:

```
logd   patch at demo.pr.1.stage -> event To = {stage: reviewed}
docd   patch at demo.pr.1.stage -> event To = {stage: reviewed}
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
against the world, so the store levels: a change carries the node as it stands after the commit
(`entity.changesAt`), and the store — not each consumer — is where that is paid for.

**As of the commit, not "now".** The level state is read at the event's own commit, so an event
reports the world as it was when it changed, and a transient state that has already been
overwritten is still dispatched. Levelling against *current* state would be simpler, and it is why
the doorbell shape — `Event{Ref, Op, Rev}`, with the consumer reading state — is not the design: a
workflow engine that skips a step because the subject moved on fails in a way nobody notices.

**It costs nothing per event.** The store levels from the watched document it already
maintains — seeded by the watch's initial `State`, advanced by each delta (the same
apply-rather-than-decode discipline as below) — so no read is issued per event. A `Since` replay
reads the document once, at `from`, and rolls it forward with the deltas it is replaying;
the alternative, a point-in-time read per changed entity, is exact too but puts a network
round trip per entity on the watch goroutine, where the stream stalls behind its own
bookkeeping. (It also matters that those reads are not cheap on a grown log: go-tony
`bvm163tyh12krwcqcsn0`.)

The delta only PRUNES the walk. Which nodes changed is decided by comparing the document before
the commit with the document after it, at the paths the observer asked about; the delta says which
branches are worth comparing, and a branch it does not name is one the commit did not touch.

## Consuming a delta: apply it, do not decode it

`Op` is derived from a delta, but *how* a consumer reads the delta matters. The
encoding varies and is logd's business, not ours: the baseline watch forwards the
writer's patch verbatim (a plain object merge); a scoped COW watch emits a *computed*
diff whose first event is a `!replace` (diffed from null) and whose later events are
merges; a foreign writer might use `!delete`/`!insert`. A consumer that inspects the *structure*
of the patch to see which entities appeared silently misses some, because a merge-style walk
cannot pull the appeared key out of a `!replace`. That is what makes an N-file `verse connect`
fire N−1 validators: the first scoped delta, the `!replace`, is the one dropped.

The contract is therefore: apply the delta, do not decode it. A subtree watcher
maintains the watched subtree as a document, applies each event to it
([`tony.Patch`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony#Patch) for a
Patch, replace for a State — opaquely, whatever ops it carries), and reads entities out
of the *reconstructed object*, diffing against the last snapshot for per-entity
`Set`/`Delete`. tony owns patch semantics (merge vs `!replace` vs `!delete`/`!insert`,
field order); verse applies them and never depends on the wire shape of a diff.

That diff compares payloads with `ir.Node.DeepEqual`, and the choice is load-bearing rather
than stylistic: a node carries `Parent`/`ParentIndex`/`ParentField`, which say where it sits
in the document rather than what it holds, and a document rebuilt from a fresh read wires
those up anew. `reflect.DeepEqual` compares them and so calls a value unequal to itself;
ir's `DeepEqual` documents that it does not. Under `reflect` every entity in a subtree diffs
as changed after any resync, so re-establishing a watch re-emits the whole subtree and every
trigger over it fires again for nothing. This is
what `LogdStore.WatchSubtree` does — and also what the root watch and the `Since`
replay do, to level their payloads (`applyDelta`). All three take the set of changed refs from
the same comparison of the document before the commit with the document after it (`changesAt`).
The commit's delta only prunes the walk: a branch the delta does not name is a branch the
commit did not change.

**A State and a Patch are not rooted alike, and neither one says so.** A Patch is the
commit's delta rooted at the document (`verse.demo.pr.1: {…}`); a State carries the value
*at the watched path* (logd extracts it; docd's composed read builds it relative to the
path). Taken verbatim, a State seeds a document whose leading segments are missing — so every
read of it navigates from the document's root to the watched path and finds nothing, the seed contributes no
entities, and the level state silently degenerates to *whatever has been patched since the
watch opened*. A deep write of one field then reports an entity of one field, and a trigger
binds `$note` from a payload that has no note in it: not a crash, a task whose prompt reads
literally `$note`. Both watchers re-root the State (`entity.stateDoc`) so the document has
one shape.

The diff makes the subtree watch edge-on-real-change: an entity whose reconstructed content
is unchanged emits nothing, so a write that changes no content is *not a reactive edge*.
That is what keeps a reactive re-write from looping: a trigger whose effect re-touches its own
cause writes the *same value*, so no edge fires and the trigger does not re-run.

**This is load-bearing rather than merely convenient**, because it is one of the things that ends
a rule: see [convergence](convergence.md#what-ends-a-firing). Measured, and pinned by
`entity.TestSameValueWriteRaisesNoCommit`, the revision does not advance and neither watch reports
anything. The substrate declines to record a change that is not one.

**One commit in, one commit out.** The subtree watch does not coalesce a burst by applying every
queued delta and diffing once. That would be exact for a level and lossy for an *edge*: a value
written at one commit and overwritten at the next would leave only the later one, and a transition
the stream never reported cannot be recovered downstream. Since a trigger's cause is a rising edge,
that is a firing that never happens, so the watch pays a subtree walk per commit rather than per
burst.

## The seed is replayed: a subtree watch is level-triggered at the open

The subtree watch diffs its opening snapshot against an *empty* one, so everything already
standing in the subtree arrives as a `Set` before the first live change. A trigger installed
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
nothing at all when the subtree merely happened to be idle.

`Observe` (the root watch) stays "from now" and does not replay — it is the whole-verse
doorbell behind the debug stream, and its consumer, the dispatch loop, starts from
`store.Rev()` and reconciles with `Since`. Level-at-open belongs to the watch that *is* a
trigger's observation.

The cost is the charter author's to carry, and it is the cost re-entry already had: what
fires is "matches now", so a non-idempotent action behind a state a subject can re-enter
fires again when the engine restarts over a verse still in that state. The latch idiom
answers it — match a field the action advances — and a re-write to the same value stays inert
(see edge-on-real-change, above).

## One commit, many entities: the entity is the reactive grain

A logd commit is a single, atomic transaction that can patch many branches at once —
several paths, anywhere in the tree. But an `entity.Event` names exactly one `Ref`, so the store
normalises — **at the paths the observer asked about**. An observer (`Observe`, `Since`,
`WatchSubtree`) names one or more patterns, and a commit's `Changes` are the nodes those patterns
reach whose node differs between the document before the commit and the document after it
(`entity.changesAt`). Patch five branches in one tx, and a pattern reaching all five yields five
events; patch the subtree above them, and the same pattern still yields one event apiece, because
each of those nodes differs. A pattern that reaches the subtree itself yields one event there
instead — the same commit, a different question. A consumer with no pattern asks
`Commit.Touches` about a path it holds.

There is no depth the stream picks for everybody, and there cannot be: a stream that picked one
would deliver a change to a deeper node as a change to some ancestor, and a consumer comparing
that with an address it already holds is comparing an ancestor with a descendant. The question
carries the depth, here as everywhere ([reference/entities.md](reference/entities.md)).

This is what makes a trigger's subject well-defined. A firing is driven by one event, which carries
one `Ref`, so `$subject`, and hence the flow keyed on it, is always exactly one entity. Not because
a match *pattern* is constrained to one entity — it is not, since `demo.*.*` matches
every demo entity — but because dispatch is per-entity-edge. A broad pattern fans out to one
independent firing per matching change.

The normalisation is reported *inside* its transaction rather than instead of it, which is
what keeps the entity grain from costing the tx. `Observe`, `Since` and `WatchSubtree` all
deliver an `entity.Commit` (`{Rev, Changes}`), so a commit that atomically sets `demo.pr.1`
*and* `demo.pr.2` arrives as one commit carrying two per-entity changes, and the trigger
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

A consumer could be made resilient to mounts arriving and leaving under it. Verse takes a namespace
split instead, because it is simpler: mounted places render under a sibling of the entity root,
so no watch verse takes is ever an ancestor of a mount. One root watch, `Since` untouched. The price
is that a mounted place fires no triggers, which is the right price, since a mount is task-driven
and has no history to observe in the first place. See [controllers](controllers.md).

## Why this is the portable choice (not an exotic dependency)

The capability verse leans on — an ordered, resumable, revisioned change stream
(watch + `Since(cursor)`) — is the modern-store baseline, not a logd peculiarity:
etcd watches, FoundationDB, Kafka offsets, Cockroach/Mongo changefeeds, Postgres
logical replication, or an in-memory ring all provide it. What *would*
tie verse to an uncommon feature is requiring create-vs-update or prior-value in
the interface. etcd exposes it, as `CreateRevision` and `PrevKv`; many stores do not, and logd,
measured above, does not either. Keeping create/update out of the contract
is therefore both the portable choice and the one the substrate can actually honour.

Note the asymmetry, which is not an accident: create-vs-update is unavailable when
*observing* a change but perfectly available when *making* one. A write states its own
precondition (`entity.Created()` / `entity.Updated()`), and the store checks it atomically
with the write. You cannot ask the stream "was that a create?"; you can insist, at the
moment of writing, that it be one.

## What dropping create-vs-update costs (and the idiom that recovers it)

What a charter would reach for `Op` to say, and what it gets instead:

- Kept for free: `delete` is exact, being a tombstone, so teardown-on-removal is unaffected, and
  because the delta is an edge, "fire when a field *becomes* X" is once-per-transition — the commit
  that set it — rather than once-per-incidental-change.
- The one real loss: you cannot distinguish "created with `stage=open`" from "re-opened to
  `stage=open`", since both deltas set `stage=open` and both fire. You lose native re-entry
  suppression, meaning "act on the first entry into a state, never on re-entry".
- The idiom that recovers it: express "once" as a state latch rather than an edge, matching a field
  the action then advances, so `stage=open` fires an action that sets `stage=reviewing` and cannot
  re-fire. Verse's demo works this way, and the subject's stage *is* the latch. This is cleaner than
  an implicit edge, and it is how level-triggered systems get once-semantics.
- The footgun to know: a non-idempotent *gated* or *spawning* action that matches a state it can
  genuinely re-enter — a real round-trip like `reviewed`→`open`→`reviewed` — re-fires on each real
  entry. A re-write to the *same* value does not, because edge-on-real-change makes it inert, so a
  reactive re-write that re-touches its own cause does not loop; see "Consuming a delta" above. For
  a genuine re-entry you still want a latch. That is charter-authoring discipline rather than an
  engine limit.

The harm is low and localised. You keep delete-exact, once-per-transition, and every state-driven
flow, and you give up native "once, ever", which a latch recovers.

## Residuals (punted, by design)

- **Cheap re-entry suppression** — for the *observer*. The write side is settled: a
  create-only write is `entity.Created()`, a conditional patch that fails if anything is
  already there, checked with the write in one step. What is still missing is the event
  saying *which* it was, so a trigger cannot say "fire only on first entry"; the latch idiom
  above remains the answer, and no charter has needed more yet.
- **Truncation detection** is handled upstream. logd persists a **replay floor**, the highest
  commit whose delta history compaction has erased, and refuses a `FromCommit` watch below it with
  a terminal `replay_compacted` event sent *before* any state: a state read below the floor is only
  snapshot-accurate, and handing a client an untrustworthy state and then an error is worse than
  the error alone. libctl raises that terminal event as a `WatchEndedError`, so it arrives at
  `Since` as `w.Err() != nil` and becomes `entity.ErrReplayTooOld`. Measured through the
  production path, verse → docd → logd, and pinned by `entity.TestSinceBelowReplayFloorResyncs`:
  floor at 5, `Since(0)` fails as `ErrReplayTooOld` with zero commits. The distinct code is the
  point, because `replay_failed` reads as transient and a consumer told that retries the same
  doomed cursor forever.
- **That is one failure among several, and `Since` names which.** A catch-up is a watch, so
  everything that can refuse a watch refuses a catch-up: an expired cursor, a consumer that
  cannot keep up with its own replay, a mount arriving or leaving, an establish that never
  completes, a store that cannot be reached. `ErrReplayTooOld` is the only one that means the
  history is gone and the answer is to resync; every other failure means ask again, and leaves
  the cursor where it was. A consumer told only "no" resynced over all of them, which drops
  every commit in the gap — and a person was told their cursor was too old while their store
  was down (git-issue `3qf2z59dh12ksdazg9n0`).
- **No way to ask the floor in advance, but the refusal names it.** It is discoverable only by
  asking for a replay and being refused — there is no client query for it, etcd's compaction
  revision having no equivalent in the session protocol — and the refusal says "delta history is
  retained only from commit *n*", which is enough to pick a cursor that works. A watch's ending
  carries a MESSAGE beside its reason code for exactly this: the code is the fixed vocabulary a
  consumer switches on, the message the part that does not fit in one. A consumer that would
  rather not name a number asks with a RELATIVE cursor instead: the last *n* commits, which the
  store resolves against its watermark and **clamps** to the floor rather than refusing.
