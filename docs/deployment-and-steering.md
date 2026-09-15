# Deployment and steering

One invariant shapes verse's surface: *steering outranks observation*. An intervention that
lands late is an intervention defeated. [Tutorial §3](tutorial.md#steering) drives it from the
operator's side. This is why it is built that way.

## Two planes

Everything coordinates through the entity store, but not everything is coordination.

*Data coordination* is actions writing entities and triggers reacting to them. It flows through
the store, and it is fine for it to lag: eventual, queued, recoverable.

*Control* is holding a run, cancelling it, answering a gate, sliding autonomy, injecting an
instruction. It does not flow through the store at all. What an operator can stop, and for how
long, is [control](control.md).

"Coordinate through the store, no orchestrator" tempts you to model an intervention as a store
write, a command entity that something reacts to. Then the intervention rides the one channel it
is supposed to outrank: it queues behind the backlog, and under load the store's bounded buffer
can drop it. The dial would not turn when you turned it.

So the split is explicit. Control is out-of-band from store observation, serviced ahead of it,
and an intervention is never a store write. It shows in the surface too: control endpoints are
distinct from read endpoints, so an operator cannot steer by writing.

## A hold costs a cursor, not a buffer

A flow parked at a gate nobody has answered, or a paused engine, must not accumulate a growing
backlog of unread changes. It does not, because observation is revision-cursored. Every change
carries a monotonic `Rev`, and a consumer keeps only the highest one it has processed, catching
up with `Since(cursor)` rather than holding a buffer. A hold costs an int, however long it lasts.

That is what makes dropping safe. `Observe` is a low-latency hint that may drop under
backpressure; `Since` is the source of truth, so a drop becomes a cursor gap to replay rather
than a silent loss, and pause, resume, a long gate and dropped events all heal the same way. It
is also the only model that works once the store is across a network, where you cannot hold a
buffer on the far side. `TestDropRecoveryViaReplay` pins it: a pause that outlasts the live
buffer drops events, and all 300 changes still dispatch on resume.

## Where it is enforced

In the task runtime, a boundary services control before the next action, cancel beats pause beats
say, and the read plane is fire-and-forget so it can never block control.

In gate resolution, a pending gate is *observed* through the store, because `mirror` projects it,
but *answered* out-of-band through the resolver. Firing runs off the observe goroutine, so a gate
blocking on a human does not stall observation.

In the engine, a control channel is drained at the top of every loop iteration, ahead of any
dispatch. While paused the loop services control only and consumes no observations at all, so a
runaway flow can be halted no matter how backed up the change stream is. Control closures run on
the loop goroutine, so they serialize with dispatch instead of racing it.
`TestControlOutranksObservation` pins that one.

At the surface, control is per scope. A live thing runs in one engine, and a scope's engine
holds only what the scope created, so a steer made with `-scope` reaches the scope's own work
and nothing inherited — and says which scope holds the work when it cannot, rather than steering
baseline from a scope or a scope from baseline. What a scope owns, and why a control write
cannot be copy-on-write the way a world write is, is in
[connect](reference/connect.md#what-a-scope-owns).

A suspension is the one intervention with a durable record, and it still does not take effect by
being observed. Each scope's suspensions are one table in memory, written only by the route that
writes their records, under one lock and in the same order: there is no watch to race the route,
so nothing waits on the store answering for a suspension to hold, and a rule writing the record
is refused at install because no table would see it. The table loads in the first phase of a
restart, before tasks are re-attached or the charter restored, and until it has it holds
everything — a suspension that failed open on a restart would resume what somebody stopped on
purpose ([control](control.md), [the trigger model](trigger-model.md#suspending)).

## What runs where

`verse up` runs the engine, the task runtime, and the HTTP surface in one process, and either
brings up logd and docd as children or attaches to a running docd. Everything else is a client of
it.

The pieces are separable services behind the interfaces they already use: the store is reached
through a client rather than embedded, the task runtime is already an HTTP control and read
plane, and [task control](task-control.md) already runs a task off-box. Splitting them is a
deployment change, not a redesign.

Next: [the reference](reference/README.md#commands) for the commands this shape produces, and
[connect](reference/connect.md) for how a developer's working tree joins a running verse.
