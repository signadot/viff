# Deployment and steering

One rule shapes the surface of verse: *steering outranks observation*.
[Steering](glossary.md#steering) is acting on work in progress. An intervention that arrives
late has failed. [Tutorial §3](tutorial.md#steering) shows steering from the operator's side.
This page explains why verse is built this way.

## Two planes

Everything coordinates through the [store](glossary.md#store), but not everything is
coordination. Verse separates two kinds of work:

| Kind | What it is | How it travels |
|---|---|---|
| *Data coordination* | Actions write entities, and [rules](glossary.md#rule) react to them. | Through the store. It may lag: it is eventual, queued and recoverable. |
| *Control* | Holding a run, cancelling it, answering a gate, changing the [vector](glossary.md#vector) (the autonomy of a task), or sending an instruction with `say`. | Not through the store at all. |

What an operator can stop, and for how long, is in [control](control.md).

**Control is separate from observation of the store, and verse serves it first.** An
intervention is never a store write. The surface shows the same split: the control endpoints
are separate from the read endpoints, so an operator cannot steer by writing.

"Coordinate through the store, with no orchestrator" suggests a different design. In that
design an intervention is a store write: a command entity that something reacts to. Then the
intervention travels on the one channel that it must outrank. It waits in the queue behind the
backlog. Under load, the bounded buffer of the store can drop it. The operator would act, and
nothing would happen. For this reason verse makes the split explicit.

## A hold costs a cursor, not a buffer

A flow that waits at a gate nobody has answered must not collect a growing backlog of unread
changes. An engine that is paused must not either. Neither does, because observation follows a
revision cursor. Every change carries a `Rev` that only increases. A consumer keeps only the
highest `Rev` that it has processed. It catches up with `Since(cursor)`, and it holds no
buffer. A [hold](glossary.md#hold) costs one int, however long it lasts.

The revision cursor makes a dropped event safe:

- `Observe` is a hint with low latency. It may drop events under backpressure.
- `Since` is the source of truth. A drop becomes a gap in the cursor that verse replays. It is
  not a silent loss.
- A hold and a resume, a long gate and dropped events all recover in the same way.

`TestDropRecoveryViaReplay` checks this. In the test, a pause that lasts longer than the live
buffer drops events, and all 300 changes still dispatch on resume.

The cursor is also the only model that works when the store is across a network. A client
cannot hold a buffer on the far side of a network.

## Where it is enforced

| Where | What verse does |
|---|---|
| The task runtime | A [boundary](glossary.md#boundary) serves control before the next action. Cancel beats pause, and pause beats say. The read plane sends and does not wait, so it can never block control. |
| Gate resolution | Verse *observes* a pending gate through the store, because `mirror` projects it. Verse *answers* the gate out of band, through the [resolver](glossary.md#resolver). Firing runs off the observe goroutine, so a gate that waits for a person does not stall observation. |
| The engine | The engine drains a control channel at the top of every loop iteration, before any dispatch. While paused, the loop serves only control and consumes no observations. So an operator can halt a runaway flow, however far behind the change stream is. Control closures run on the loop goroutine, so they run in order with dispatch and do not race it. `TestControlOutranksObservation` checks this. |

At the surface, control is per [scope](glossary.md#scope). A live thing runs in one engine.
The engine of a scope holds only what that scope created. So a steer with `-scope` reaches the
scope's own work and nothing inherited. When it cannot reach the work, it says which scope holds
the work. It does not steer baseline from a scope, or a scope from baseline. What a scope owns
is in [scopes](reference/scopes.md#what-a-scope-owns). That page also says why a control
write cannot be copy-on-write, as a write to the world is.

A [suspension](glossary.md#suspension) is the one intervention with a durable record. Even so,
it does not take effect by being observed:

- The suspensions of each scope are one table in memory.
- Only the route that writes their records writes the table. It writes both under one lock and
  in the same order.
- No watch races the route. So a suspension holds without waiting for an answer from the store.
- Verse refuses, at install, a rule that writes a suspension record, because no table would see
  that write.
- The table loads in the first phase of a restart. That is before verse attaches tasks again or
  restores the charter. Until the table loads, it holds everything.

The reason for the last point: a suspension that failed open on a restart would resume work
that somebody stopped on purpose ([control](control.md),
[the trigger model](control.md#suspending)).

## What runs where

`verse up` runs the engine, the task runtime and the HTTP surface in one process. It either
starts logd and docd as child processes or attaches to a docd that is already running.
Everything else is a client of that process.

The pieces can be split into separate services behind the interfaces that they use today:

- The store is reached through a client. It is not embedded.
- The task runtime is an HTTP control plane and read plane.
- [Task control](task-control.md) runs a task on another machine.

So a split is a change of deployment, not a new design.

Next: [the reference](reference/README.md#commands) for the commands of this design, and
[scopes](reference/scopes.md) for how a developer works beside a running verse without touching it.
