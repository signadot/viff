# The store

Everything in verse coordinates through one observable, cursored entity store. You never have
to touch it to *use* verse — `up` brings it up for you — but it is where verse's deeper
properties come from, and they are hard to see spelled out anywhere else.

The store defaults to [docd](https://signadot.github.io/tony-format/docd/), the tony-format
document store, over logd. What that buys:

- **Copy-on-write scopes.** An overlay whose writes do not touch baseline, dropped in one
  operation ([connect.md](connect.md#scopes)).
- **Conditional and coordinated writes.** Atomic CAS, so two writers advancing one entity
  cannot lose each other's fields ([entities.md](entities.md#conditional-writes)).
- **Edge-faithful watches.** The change stream *is* each writer's applied patch, so no reader
  reconstructs, reorders or drops an edit.
- **Compactions**, which keep the log bounded while preserving current state.

With no store flags, `up` brings up logd and docd on a fresh temp data dir via `o sys up` and
points the store at docd's client face; `-data <dir>` uses a durable dir, and
`-docd-addr`/`-docd-mount-addr` attach to an already-running docd — with
`-docd-advertise-addr`/`-docd-mount-advertise-addr` to publish a different pair on `/status`
when a controller reaches docd by another address than verse does. See
[up.md](up.md#the-store).

## The mapping

The store maps each entity to a kinded path `verse.<system>.<kind>.<id>`, reads and writes via
Match/Patch, and drives the change stream and revision cursor from a single logd watch. That
mapping is the whole of what `verse data` publishes: the document it answers with *is* this one,
addressed by the same path ([entities.md](entities.md#reading)).

**Absence is a missing key, not a null.** A removal is `!delete`, which logd executes by taking
the key out — so a deleted ref reads back not_found, and a null is a payload like any other. Since
go-tony v0.0.201 that holds for a path that was NEVER written too, at every depth and whether or
not an ancestor of it resolves: a read answers null iff a null was written there. Before it, a
path with nothing above it answered a bare null — the same answer an entity holding one gives —
and verse asked the parent whether the key stood to tell them apart. The
two walks say the same thing at different times: a *materialized* subtree (`walkEntityDoc`, what a
read and a snapshot see) has no removals in it to find, while a *delta* (`walkEntityChanges`) has
no absence to observe and spells a removal with the `!delete` tag.

`Since` is served natively by a `FromCommit` replay watch, so a long hold costs a cursor
rather than a buffer. Commit numbers are monotonic but **sparse**, so the engine treats a
revision as an opaque cursor and reconciles via `Since`. Writes are atomic via CAS
(`PatchIf`), so concurrent modifies do not lose updates. `Op` is `set` or `delete`, with create
identical to update, since logd delivers the writer's applied patch verbatim.

## The reactive grain is the entity, not the transaction

This one is subtle, and it is the load-bearing assumption worth naming.

A single docd commit can atomically patch many entities, but the store normalises each commit
into **one change event per entity** — `walkEntityChanges`, at path-depth `system/kind/id`,
which is the depth an address has unless a reader asks for another — all
sharing that commit's revision. That normalisation is exactly what guarantees a trigger's
subject is always one entity: dispatch is per-entity-edge, so `$subject`, and the flow or goal
keyed on it, is never ambiguous even under a broad match, since a broad pattern just fans out
to one firing per matching change.

Less is lost to that than it first sounds, because the commit lands *whole* before any of its
events dispatch: the store advances its watched document by the entire patch, then walks it
for events, and each event's `to` is read back out of the already-advanced document. So when
the edge for one entity fires, every entity that moved with it is already at its new value —
in the level state on the event, and in the verse a gate or a set-valued subject reads at fire
time. "When A opens, and B is green" is expressible today, and it does not care whether B went
green in the same commit or an hour earlier.

What is genuinely flattened is the commit as a *unit*. Two entities changing together produce
two firings, not one, so a rule matching both fires twice and any dedup is yours; and nothing
can ask "did these move together?", since the shared revision is the only trace and nothing
groups by it. Worth revisiting only if a reaction ever needs the grouping itself rather than
the joint state. See [change events](../change-events.md).

## What follows for a charter

Two properties of the store are what make the reactive loop converge, and both are things an
author has to know:

- **A set-to-the-same-value is inert.** Writing a field the value it already holds produces no
  commit, so it raises no evaluation. This is what stops a completion hook that re-states a
  subject's stage from looping.
- **A watch is level-triggered at the open**, so an installed trigger acts on the backlog
  standing in the verse, and a restart re-reaches the same verdicts rather than depending on
  what this engine happened to observe. Which is why a rule wants a *latch* rather than an
  edge; see [convergence](../convergence.md).
- **A write is a patch, so a match operator cannot be written as data.** `!irtype`, `!or`,
  `!glob`, `!let` and the rest of the match vocabulary are operations, and a payload carrying one
  is executed by the store rather than stored by it — which does not fail at the write, it fails
  every later read, leaving the store unreadable rather than the entity wrong. Such a write is
  refused now, naming the operator and the field.

  What to do instead is say the subtree is data: `entity.Raw` (`!raw`) on the way in, which is
  what a charter rule already rides on — a rule is dense with operators and is stored verbatim
  under that escape. `entity.AsData` is the same thing for a record being read, edited and put
  back, which `entity.Modify` does for its callers. Read [the trigger
  model](../trigger-model.md#the-charter-is-durable) for the longer version, since a charter is
  the payload that needed it first.
