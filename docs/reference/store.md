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
points the store at docd's client face; `--data <dir>` uses a durable dir, and
`--docd-addr`/`--docd-mount-addr` attach to an already-running docd — with
`--docd-advertise-addr`/`--docd-mount-advertise-addr` to publish a different pair on `/status`
when a controller reaches docd by another address than verse does. See
[up.md](up.md#the-store).

## The mapping

The store maps each entity to a kinded path `verse.<system>.<kind>.<id>`, reads and writes via
Match/Patch, and drives the change stream and revision cursor from a single logd watch.

`Since` is served natively by a `FromCommit` replay watch, so a long hold costs a cursor
rather than a buffer. Commit numbers are monotonic but **sparse**, so the engine treats a
revision as an opaque cursor and reconciles via `Since`. Writes are atomic via CAS
(`PatchIf`), so concurrent modifies don't lose updates. `Op` is `set` or `delete`, with create
identical to update, since logd delivers the writer's applied patch verbatim.

## The reactive grain is the entity, not the transaction

This one is subtle, and it is the load-bearing assumption worth naming.

A single docd commit can atomically patch many entities, but the store normalises each commit
into **one change event per entity** — `walkEntities`, at path-depth `system/kind/id` — all
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
