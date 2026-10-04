# Change events

`entity.Op` has two values, `set` and `delete`. It does not have the three values create, update
and delete. `Since` is a pure read. This page says why. It also says why these two choices let
verse keep its data in any ordinary [store](glossary.md#store), and not only in an unusual one.

[The tutorial](tutorial.md#watch) shows the stream that this
page describes. [reference/store.md](reference/store.md) describes the store that the stream
comes out of.

## The question

A change event must carry enough for a [rule](glossary.md#rule) to act on it. The rich shape is
`Op ∈ {create, update, delete}` with the full payload before and after the change. A store that
holds the earlier state in memory can give this shape easily. A durable, remote store pays a lot
for it.

Verse could build the rich shape over the logd-backed store. It would need a mutable `seen` set,
passed through both the live watch and `Since`. That set has three faults:

- The catch-up path, which is the one verse trusts, is no longer pure.
- A create inside a replay window gets the wrong label.
- The store no longer matches its interface.

All three faults have one cause. The store would invent *edge* meaning that the layer under it
does not carry. So the question is: what does a change event carry, and does matching react to
edges or to levels?

## What the watch actually delivers (measured)

A watch on the root sends one event per [commit](glossary.md#commit). Each event carries the
commit's `Patch`. The production path is verse → docd → logd. Through that path, a create, a full
update, a partial update, a delete and a re-create give these events:

```
CREATE   demo.pr.1         {verse:{demo:{pr:{"1":{stage:open,      x:1}}}}}
UPDATE   demo.pr.1 (full)  {verse:{demo:{pr:{"1":{stage:reviewing, x:1}}}}}
UPDATE   demo.pr.1 (deep)  {verse:{demo:{pr:{"1":{stage:done}}}}}
DELETE   demo.pr.1         {verse:{demo:{pr:{"1":null}}}}
RECREATE demo.pr.1         {verse:{demo:{pr:{"1":{stage:reopened}}}}}
```

docd adds nothing to these events, and this follows from its design. The client side of docd is
*the logd session protocol verbatim*. A client cannot tell which of the two it talks to. When docd
routes a watch and does not compose it, the watch is logd's stream, forwarded. docd changes when a
watch is available. It does not change what a watch says. See the mount hazard below.

Two facts decide the design:

1. **Create and update are the same bytes.** Adding the new key `"1"` looks exactly like
   replacing an existing `"1"`. There is no marker for add or replace, and no flag for a new
   node. The stream does not say whether a write was a create or an update. The re-create shows
   the same fact from the other side. The commit that brings `demo.pr.1` back after its
   tombstone is a plain merge. Nothing tells it apart from any other write of `stage`.
   `WatchEvent` carries `{commit, path, state, patch, replayComplete, ended, endReason}`. It has
   no field for the value before the change, and no watch option asks for one.
2. **The delta is the patch the writer applied, verbatim.** It is not a diff that logd computes
   from the old value to the new one. The full update sent the unchanged `x:1` again, and a diff
   would have left it out. logd forwards *what this commit wrote*, and it knows nothing about
   the earlier state.

   This holds for a watch on baseline. A watch in a [scope](glossary.md#scope) over a
   copy-on-write overlay is different. There, logd computes the scope's view again and sends a
   *computed* diff between two states of the scope. Its first delta is diffed from an empty or
   null state, so it is a `!replace` and not an additive merge. logd chooses how to encode a
   delta. See "Consuming a delta" below.

So the watch is already a stream of edges. One event per commit is one transition, and the delta
*is* the edge. Verse does not need to react to levels only and lose short-lived states, because
logd gives it edges.

A delta carries exactly two facts that a reader can get without the earlier state. A removal is a
`Delete`, and any value is a `Set`. That is all of `Op`.

A removal is the **`!delete` tag**, not a null. A patch has no absence to observe, so a delta must
spell a removal. Verse writes the tag (`entity.Removed`), and `tony.Diff` emits it for a key that
went away. A bare null in a delta is an ordinary value that is set. `payload: null` is a legal
payload. If verse read a null as a removal, no entity could hold that payload (`entity/change.go`,
`mergeInto`).

## The decision

- `Op` is `set` or `delete`. It does not say create or update. That would need to know whether the
  node existed before, and the layer under the store does not give this. It would also tie the
  store to a rare capability, as the section on portability says below. A create and an update are
  both `Set`, so a [charter](glossary.md#charter) does not depend on which one it was.
- **`Since` is a pure read.** It derives `Op` from the delta alone. It changes no state of the
  store. So it gives the same answer every time, and it is safe when callers run it at the same
  time or run it again. The store keeps no `seen` set.
- **`To` is the entity as of that commit, not the delta.** *When* an event happens is an edge:
  one per commit. *What the event says* is a level: the whole entity at that commit. See "Why `To`
  is level" below. That is the one decision on this page that a test decides.

## Why `To` is level (and what the delta would answer instead)

The delta is the writer's patch for the commit. It looks like the right value for `To`. If verse
only ever writes whole entities, the delta *is* the entity. But a write can address one field,
such as `demo.pr.1.stage`. Then the delta is not the entity, and a consumer that matches on `To`
matches a fragment.

A deep write of one field, watched on both logd and docd, gives this event:

```
logd   patch at demo.pr.1.stage -> event To = {stage: reviewed}
docd   patch at demo.pr.1.stage -> event To = {stage: reviewed}
```

The [entity](glossary.md#entity) at that commit is `{stage: reviewed, env: prod}`. The event does
not contain `env`. `entity.Pattern.MatchEvent` matches `To`. So if `To` were the delta, take a rule
whose `when` reads two fields. The rule fires on a write of the whole entity. It does **not** fire
on a deep write that reaches the same state:

```
whole-entity write to the matching state: fired = 1
deep write to the same state:             fired = 0
```

No error appears. The rule never runs.

A rule about the world must be evaluated against the world. So the store gives levels: a change
carries the node as it is after the commit (`entity.changesAt`). The store does this work once,
and each consumer does not have to.

**As of the commit, not "now".** The store reads the level state at the event's own commit. So an
event reports the world as it was when it changed. A state that a later write has already replaced
is still dispatched.

A level read against the *current* state would be simpler. Verse does not use it, and for the same
reason it does not use the shape `Event{Ref, Op, Rev}` in which the consumer reads the state
itself. A workflow engine that skips a step because the subject has moved on fails, and nobody
sees the failure.

**It costs nothing per event.** The store builds the level from the watched document that it
already keeps. The watch's first `State` seeds the document, and each delta moves it forward. This
is the same rule as below: apply the delta, do not decode it. So the store sends no read per
event. A `Since` replay reads the document once, at `from`, and moves it forward with the deltas
that it replays.

The other design is a read at one point in time for each changed entity. It is exact too. But it
puts one network round trip per entity on the goroutine of the watch, and the stream then waits
behind its own work.

The delta only prunes the walk. The store decides which nodes changed by comparing the document
before the commit with the document after it, at the addresses the observer asked about. The delta
says which branches are worth comparing. A branch that the delta does not name is a branch that
the commit did not touch.

## Consuming a delta: apply it, do not decode it

Verse derives `Op` from a delta. But *how* a consumer reads the delta matters. The encoding varies,
and logd decides it, not verse:

| Watch | What its deltas are |
|---|---|
| a watch on baseline | the writer's patch, verbatim: a plain object merge |
| a scoped copy-on-write watch | a *computed* diff. The first event is a `!replace`, diffed from null. Later events are merges. |
| a foreign writer | it might use `!delete`/`!insert` |

A consumer can inspect the *structure* of a patch to see which entities appeared. That consumer
misses some, and it does not say so. A walk that expects a merge cannot pull the new key out of a
`!replace`. A consumer that reads deltas this way fires N−1 validators for an N-file
`verse connect`, because it drops the first scoped delta, the `!replace`.

So the contract is: **apply the delta, do not decode it.** A subtree watcher keeps the watched
subtree as a document. It applies each event to the document, whatever operations the event
carries:

- a Patch, with [`tony.Patch`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony#Patch)
- a State, by replacing the document

The watcher then reads entities out of the *rebuilt object*. It diffs against the last snapshot to
get a `Set`/`Delete` per entity. tony owns the meaning of a patch: merge, `!replace`,
`!delete`/`!insert`, and the order of fields. Verse applies patches. It never depends on how a
diff looks on the wire.

That diff compares payloads with `ir.Node.DeepEqual`, and it must. A node carries
`Parent`/`ParentIndex`/`ParentField`. These say where the node sits in the document, not what it
holds. A document rebuilt from a fresh read sets them again. `reflect.DeepEqual` compares them, so
it calls a value unequal to itself. The documentation of ir's `DeepEqual` says that it does not
compare them.

Under `reflect`, every entity in a subtree would diff as changed after any resync. Each new watch
would send the whole subtree again, and every rule over the subtree would fire again for nothing.

Three consumers work this way:

- `LogdStore.WatchSubtree`
- the root watch, which levels its payloads with `applyDelta`
- the `Since` replay, which also levels with `applyDelta`

All three get the set of changed refs from the same comparison of the document before the commit
with the document after it (`changesAt`). The commit's delta only prunes the walk. A branch that
the delta does not name is a branch that the commit did not change.

**A State and a Patch are not rooted alike, and neither one says so.** A Patch is the commit's
delta, rooted at the document (`verse.demo.pr.1: {…}`). A State carries the value *at the watched
address*. logd extracts that value, and docd's composed read builds it relative to the address.

If a watcher took a State verbatim, the State would seed a document whose first segments are
missing. Every read of it would go from the document's root to the watched address and find
nothing. The seed would give no entities. The level state would shrink to *whatever was patched
after the watch opened*, and nothing would say so. A deep write of one field would then report an
entity of one field. A rule would bind `$note` from a payload that has no note. The result is not a
crash: it is a task whose prompt reads `$note` literally. So both watchers re-root the State
(`entity.stateDoc`), and the document has one shape.

The diff makes the subtree watch send an edge only on a real change. An entity whose rebuilt
content is unchanged sends nothing. So a write that changes no content is *not a reactive edge*.
This stops a reactive re-write from looping. Take a rule whose effect touches its own cause again.
It writes the *same value*, so no edge fires, and the rule does not run again.

**This is necessary, not only convenient**, because it is one of the things that end a rule. See
[convergence](convergence.md#what-ends-a-firing). `entity.TestSameValueWriteRaisesNoCommit` checks
it: the [revision](glossary.md#revision) does not advance, and neither watch reports anything. The
layer under the store does not record a change that is not one.

**One commit in, one commit out.** The subtree watch does not merge a burst of commits by applying
every queued delta and diffing once. That would be exact for a level, but it would lose *edges*.
Take a value written at one commit and replaced at the next. Only the later value would remain. A
transition that the stream never reported cannot be recovered further down.

The cause of a rule is a rising edge. So a lost edge is a [firing](glossary.md#firing) that never
happens. The watch therefore walks the subtree once per commit, not once per burst.

## The seed is replayed: a subtree watch is level-triggered at the open

The subtree watch diffs its first snapshot against an *empty* one. So everything that already
stands in the subtree arrives as a `Set` before the first live change. A rule installed over a
populated store therefore acts on the backlog that already matches it.

This follows from the level reading. `To` is the entity and not the delta, because a rule is a
statement about the world. So *which* entities a rule is about cannot depend on who was listening
when they were written. Take a task that has had `stage: ready` since before its charter existed.
It matches the rule. Without the replay it fires nothing, until somebody writes it again to
announce a state it has always held. That write works around the store. Nobody wanted to do it.

A watch that is *re-established* does not replay. The `State` of a reconnect is diffed against the
snapshot that the previous watch left, so it sends only the gap. Replay belongs to the open of a
watch, not to the `State` event.

The same diff lets a watch repair itself. A subtree watch re-establishes itself in two cases:

- after an end that it was told about
- after an interval with no events

A watch can stop for reasons that the consumer never hears. For example, logd drops a watcher whose
buffer it cannot empty. A consumer that waits to be told waits forever when the message itself is
what fails.

The resync costs one `State`. The diff is against the snapshot the watch already holds, so the
resync sends nothing when the subtree was only idle.

`Observe` (the root watch) starts "from now" and does not replay. It is the watch of the whole
store behind the debug stream. Its consumer, the dispatch loop, starts from `store.Rev()` and
catches up with `Since`. A level read at the open belongs to the watch that *is* what a rule
observes.

The charter's author carries the cost of the replay, and it is the same cost that re-entry already
has. What fires is "matches now". Take a non-idempotent action behind a state that a subject can
enter again. It fires again when the engine restarts over a store that is still in that state.

The [latch](glossary.md#latch) answers this: match a field that the action advances. A re-write to
the same value stays inert (see the edge on a real change, above).

## One commit, many entities: the entity is the reactive grain

A logd commit is a single atomic transaction. It can patch many branches at once, at several
addresses anywhere in the tree. But an `entity.Event` names exactly one `Ref`. So the store splits
a commit into changes, **at the addresses the observer asked about**.

An observer (`Observe`, `Since`, `WatchSubtree`) names one or more patterns. A commit's `Changes`
are the nodes that those patterns reach, where the node differs between the document before the
commit and the document after it (`entity.changesAt`). For example:

- Patch five branches in one transaction. A pattern that reaches all five gives five events.
- Patch the subtree above them. The same pattern still gives one event for each node, because
  each of those nodes differs.
- A pattern that reaches the subtree itself gives one event there. The commit is the same, but
  the question is different.

A consumer with no pattern asks `Commit.Touches` about an address that it holds.

The stream picks no depth for everybody, and it cannot. A stream that picked a depth would deliver
a change to a deeper node as a change to some ancestor. A consumer that compared that change with
an address it already holds would compare an ancestor with a descendant. The question carries the
depth, here as everywhere ([reference/entities.md](reference/entities.md)).

This makes the [subject](glossary.md#subject) of a rule well defined. One event drives a firing,
and that event carries one `Ref`. So `$subject`, and the flow keyed on it, is always exactly one
entity. A match *pattern* is not limited to one entity: `demo.*.*` matches every demo entity. But
dispatch is per edge of an entity. A broad pattern gives one independent firing per matching
change.

The store reports the split *inside* its transaction, not in place of it. So the entity grain does
not lose the transaction. `Observe`, `Since` and `WatchSubtree` all deliver an `entity.Commit`
(`{Rev, Changes}`). Take a commit that sets `demo.pr.1` *and* `demo.pr.2` in one step. It arrives
as one commit with two changes, one per entity. The rule engine is asked once per commit, not once
per change. Each change still drives its own firing with its own subject. The commit adds one
thing: a consumer can see which changes were one transaction.

That grouping must come from the store. The changes of one commit share a `Rev`. But a consumer
that reads them one at a time cannot tell whether the next change belongs to the same commit,
without waiting for the one after it. The store built them from a single delta, so it knows. See
[the commit is the unit](trigger-model.md#the-commit-is-the-unit) for what the engine does with a
commit.

## A history-bearing watch, and a mount

Everything above assumes that the watch *has* a history behind it. A watch whose address is a
strict ancestor of a [controller](glossary.md#controller) mount is **composed**. docd sends it to
the base owner and to every mount below, and merges them into one stream.

A composed watch cannot honour `FromCommit`. The sub-streams have separate commit sequences, so no
single commit to resume from replays them all. The watch does not refuse. It drops the request for
the replay and never signals that the replay is complete. So a `Since` over such an address
returns `ok=true` with zero events, after its own timeout runs out. The engine's catch-up then
looks successful but delivers too little. This is the worst form the failure could take.

A consumer could be built to handle mounts that arrive and leave under it. Verse splits the
namespace instead, because that is simpler. Mounted places appear under a sibling of the root of
the entities, so no watch that verse opens is an ancestor of a mount. Verse keeps one root watch,
and `Since` does not change.

The cost is that a mounted place fires no rules. That cost is correct: a task drives a mount, and a
mount has no history to observe. See [controllers](controllers.md).

## Why this is the portable choice (not an exotic dependency)

Verse needs an ordered, resumable change stream with revisions: a watch and `Since(cursor)`. This
is the normal feature of a modern store, not something special to logd. These all provide it:

- etcd watches
- FoundationDB
- Kafka offsets
- Cockroach and Mongo changefeeds
- Postgres logical replication
- a ring buffer in memory

What *would* tie verse to a rare feature is create-vs-update or the earlier value in the
interface. etcd exposes these, as `CreateRevision` and `PrevKv`. Many stores do not, and logd does
not either, as the events above show. So keeping create and update out of the contract is the
portable choice. It is also the choice that the layer under the store can honour.

The asymmetry is deliberate. Create-vs-update is not available when you *observe* a change, but it
is fully available when you *make* one. A write states its own precondition (`entity.Created()` /
`entity.Updated()`), and the store checks it atomically with the write. You cannot ask the stream
"was that a create?". You can require, when you write, that the write is one.

## What dropping create-vs-update costs (and the idiom that recovers it)

This is what a charter would use `Op` to say, and what it gets instead:

| | |
|---|---|
| **What stays exact** | `delete` is exact, because it is a tombstone. So teardown on removal still works. The delta is an edge, so "fire when a field *becomes* X" fires once per transition, at the commit that set it. It does not fire on every unrelated change. |
| **The one real loss** | You cannot tell "created with `stage=open`" from "re-opened to `stage=open`". Both deltas set `stage=open`, and both fire. So you lose built-in suppression of re-entry: "act on the first entry into a state, never on re-entry". |
| **What recovers it** | Express "once" as a latch on the state, not as an edge. Match a field that the action then advances. `stage=open` fires an action that sets `stage=reviewing`, and it cannot fire again. Verse's demo works this way: the subject's stage *is* the latch. This is cleaner than an implicit edge, and it is how systems that react to levels get "once". |
| **The risk to know** | Take a non-idempotent *gated* or *spawning* action that matches a state the subject can really enter again: a real round trip such as `reviewed`→`open`→`reviewed`. It fires again on each real entry. A re-write to the *same* value does not fire, because an edge fires only on a real change. So a reactive re-write that touches its own cause does not loop (see "Consuming a delta" above). For a real re-entry you still need a latch. This is a rule for writing charters, not a limit of the engine. |

The harm is small and local. You keep an exact delete, one firing per transition, and every flow
that depends on state. You give up a built-in "once, ever", and a latch recovers it.

## Residuals (punted, by design)

**Cheap suppression of re-entry, for the *observer*.** The write side is decided. A create-only
write is `entity.Created()`: a conditional patch that fails if anything already stands there,
checked with the write in one step. The event still does not say *which* one the write was. So a
rule cannot say "fire only on the first entry". The latch idiom above is the answer.

**Truncation detection is upstream, in logd.** logd keeps a **replay floor**: the highest commit whose delta history
compaction has erased. logd refuses a `FromCommit` watch below the floor. It sends a terminal
`replay_compacted` event *before* any state.

A state read below the floor is correct only as a snapshot. Giving a client a state it cannot trust
and then an error is worse than the error alone.

libctl raises that terminal event as a `WatchEndedError`. So it reaches `Since` as
`w.Err() != nil`, and `Since` turns it into `entity.ErrReplayTooOld`.
`entity.TestSinceBelowReplayFloorResyncs` checks this through the production path, verse → docd →
logd: with the floor at 5, `Since(0)` fails as `ErrReplayTooOld` with zero commits.

The separate code is the point. `replay_failed` reads as a failure that passes. A consumer told
that retries the same cursor, which can never succeed, forever.

**That is one failure among several, and `Since` names which.** A catch-up is a watch. So
everything that can refuse a watch refuses a catch-up:

- a cursor that has expired
- a consumer that cannot keep up with its own replay
- a mount that arrives or leaves
- an establish that never completes
- a store that cannot be reached

`ErrReplayTooOld` is the only failure that means the history is gone and the answer is a resync.
Every other failure means: ask again. It leaves the cursor where it was. A resync over any of those
other failures would drop every commit in the gap.

**A client cannot ask for the floor in advance, but the refusal names it.** The only way to learn
the floor is to ask for a replay and be refused. There is no client query for it: the session
protocol has nothing like etcd's compaction revision. The refusal says "delta history is retained
only from commit *n*". That is enough to pick a cursor that works.

A watch's ending carries a message next to its reason code for exactly this case. The code is the
fixed vocabulary that a consumer switches on. The message carries what does not fit in a code.

A consumer that does not want to name a number asks with a relative cursor instead: the last *n*
commits. The store resolves that cursor against its watermark and **clamps** it to the floor. It
does not refuse it.
