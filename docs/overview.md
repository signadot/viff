# Verse — architecture overview

Read this first. It gives the shape of the system and the reasoning behind the load-bearing
decisions; the other docs here are deep-dives on single subjects, indexed in
[the docs index](README.md). It assumes an engineer comfortable with Go, HTTP, and basic
distributed-systems ideas — cursors, CAS, at-least-once delivery — who wants the map before
the territory.

## What it is

Verse is a small runtime for SDLC as emergent flows on a dial of agentic autonomy. You don't
author a workflow. You install a *charter* of trigger rules; the runtime reacts to changes in
a shared, observable entity store and does the work with governed agent processes. Every
reaction passes a gate — *auto*, *ask*, or *park* — so how much runs on its own is turned up
or down per action. The flow is not written down anywhere; it emerges from triggers reacting
to state that other actions write.

The entire system is one feedback loop:

```
   entity change ──▶ trigger matches ──▶ gate resolves ──▶ action fires
        ▲                                                      │
        └───────────── actions write entities ◀───────────────┘
```

A process completing is itself an entity change — its state is mirrored into the store — so a
completion can trigger the next reaction. That is how multi-step flows close: through the
store, not through a scheduler.

## The model, in five nouns

An *entity* is anything the SDLC refers to, named by a `system:kind:id` triple (`demo:pr:1`,
`github:pr:org/repo#42`, `process:instance:review-1`) and carrying an arbitrary payload
value. The entity store is the shared, observable, cursored log of these.

A *trigger* is one charter rule: when the condition holds and its gates resolve, an action
fires. It has a `condition` — the `subject` the firing is about, which is required, plus an
optional `when` (a point-in-time match over system/kind/id/op/fields, or a temporal formula)
for when the cause is not the subject itself — a conjunctive list of `gates`, and an `action`.
See [the trigger model](trigger-model.md).

A *gate* is `auto` (fire), `ask` (a human or agent must approve), or `park` (hold). This is
the autonomy dial, expressed per action.

An *action* is data, not code: `patch` an entity's payload for a state transition, and/or
`spawn` a process from a `launch.Spec`. It acts on the condition's subject and carries no
target of its own; template bindings — `$subject`, and every field of the changed payload —
parameterize it.

A *process* is a governed unit of agentic work, hosted by a runtime, observable and
steerable. Processes are themselves entities (`process:instance:<id>`), so the fleet's
lifecycle is visible by watching the store.

Two derived notions matter. A *subject* is the entity a piece of work is about — the PR
moving through its lifecycle. A *flow* is a subject's in-flight processes, steerable as a
unit.

## Components

One process today (`verse up`) assembles these; each is a package behind an interface, so
they separate into services without changing callers.

```
        cmd/verse (CLI)  ─────────────── HTTP ───────────────┐
                                                             ▼
   ┌─────────────────────────────  versed.Server  ─────────────────────────────┐
   │  observation plane          │           control plane                      │
   │  watch · get · list         │  gate approve · flow steer · process control │
   └───────┬───────────────────────────────┬───────────────────────┬───────────┘
           │                                │                        │
      entity.Store                    trigger.Engine           process control
      (logd/docd)                 (reactive, per-subtree      (how a process RUNS)
   Write(conditional)/Get/watch    watches, gate registry)     ├─ local  (processd, in-proc)
           ▲                                │  spawn           ├─ remote (runner.HTTP)
           │  mirror (process → entity)     ▼                  └─ broker (dial-in worker)
           └──────────────  process runtime (process/processd)  ◀──┘
                            Process · Supervisor · Vector · Event stream · gate registry
```

`entity` is the store, and it has ONE write: a value merges in at a place — a whole entity
or one field inside it — if every precondition holds, checked with the write in a single
step. Creating, updating, patching a field, removing one, and claiming an address are that
call with different arguments; `entity.Modify` builds compare-and-swap out of it, so there
are no lost updates. `Get` reads a ref and `List` enumerates. `Observe` is a low-latency
hint that may drop, while the cursored `watch … since=<rev>` replay is the source of truth,
and every event carries the entity as of its commit. logd/docd gives durability,
copy-on-write scopes, and controller federation. See [change events](change-events.md).

`trigger` is the reactive engine. It observes the store through per-subtree watches — each
trigger watches exactly the `system.kind` it matches, and there is no root watch — matches
installed `Def`s, resolves gates through a shared registry, and fires actions. A spawn action
goes through the engine's `Spawner`, which hosts the process via process control and
mirrors it back.

`process` and `process/processd` are the process runtime. A `Process` runs a driver
(`Next() → ToolUse`, either `scripted` or `llm`) under a `Supervisor` governed by a `Vector`,
the per-process autonomy defaults. It emits a cursored, ring-buffered, resumable event stream
and posts gate decisions to a registry. `processd` is the HTTP daemon face of that runtime.

`mirror` projects a process's ephemeral read plane onto its durable `process:instance:<id>`
entity: status, a `say` preview, last tool output, final `result`, the resume `cursor`, the
`runner`, and a broker's `dialURL`. It is one-way, and an index rather than a warehouse. This
is the bridge that lets process completion drive the next trigger.

`runner` and `broker` are the two off-box process-control backends, described below.

`versed` is the assembly and the HTTP surface. It also owns scopes — per-scope engines over a
copy-on-write store view — hosts brokers under `/broker/<nonce>`, and runs resume-or-orphan
reconcile at startup.

`source` federates external state in. `verse connect` reflects a working tree into a scope,
writing file metadata as entities and serving contents on demand through a docd controller,
and the same pattern generalizes to git, GitHub and the rest. See [connect](working/connect.md),
[sources](sources.md), and [content federation](content-federation.md).

## The load-bearing decisions

### Reactivity rides a durable, cursored store, not an in-memory bus

The store is the truth and the change stream is a cursor into it. `Observe` may drop under
load, because it is a doorbell; correctness comes from replaying `since=<rev>`, so a long
hold costs a cursor rather than a buffer. A contended write states its precondition and the
store checks it with the write, so concurrent modifiers never lose an update. This is what
lets the engine, a restart, or a second reader converge on the same state without
coordination.

### Observation and control are separate planes, and control has priority

Reads — watch, get, list — can lag. Steering (`gate approve|deny`, `flow pause/cancel/say`)
is out-of-band and serviced ahead of observation, never riding the change stream. An
intervention that has to queue behind a backlog of observations is an intervention defeated,
so a runaway flow can always be stopped in time. See
[deployment and steering](deployment-and-steering.md).

### Actions are data

A trigger's action is a declarative `patch`/`spawn`, not a callback. The charter is
inspectable, diffable, and installable at runtime, and the "when" is kept small because that
is where complexity hides.

### No orchestrator — flows emerge

Nothing sequences "review, then reviewed." The review process completing becomes a
`process:instance` change, and either the trigger's own `onDone` hook or a separate
`reviewed` trigger reacts to it and advances the subject. Composition is triggers reacting to
shared state, which is why the model scales to many concurrent, independent flows with no
central plan. Triggers must opt in to `process:*` matches, so the loop cannot ignite on its
own machinery.

### Processes are entities

Because `mirror` projects the read plane into the store, the whole fleet is observable by
watching `process:instance:*`, completions are ordinary change events, and a process's
outcome survives its ephemeral runtime.

### How a process runs is a seam

`process.Runner{Spawn, Attach, Ref}` returns a
`ProcHandle{Subscribe, Pause/Resume/Cancel/Steer/SetVector/Result}`. The engine drives every
process through that handle and never touches the runtime directly. There is no privileged
local path. Three implementations sit behind it:

- *local* — the in-process `processd` supervisor, reached by a Go call.
- *remote* — `runner.HTTP(baseURL)`, driving another verse or host over the
  verse-facing contract: `GET /processes`, `GET /events?since`, and control POSTs.
- *broker* — a process whose runtime is an external dial-in worker: a laptop agent, a
  firewalled pod, or a human. The worker connects out to the broker (`claim`, `inbox`,
  `report`), and the broker glues that leg to verse's handle. It is the `ProcHandle` turned
  inside-out — the same process behind two handles, one where it runs and one where it is
  observed.

Because the seam is uniform, `verse processes` and
`verse process get/attach/cancel/say/approve` work whether a process is local, remote, or
brokered, and a human executes a process via `verse process do` exactly as an agent would.
See [process control](process-control.md).

### Gates are the dial, and they point up

An executor that reaches a decision it is not authorized to make raises a gate to versed
rather than deciding, and the verse governs who resolves it — auto, a human, later a vote or
a reviewer. The executor never resolves its own gate; governance stays on versed's side of
the mirror.

## Data and failure model

*Refs and cursors.* A ref is `system:kind:id`; a store revision is monotonic but sparse and
is treated as an opaque cursor. The process event stream carries its own per-process cursor
for resume. Nothing assumes dense or gap-free numbering, and everything reconciles via
`since`.

*Scopes.* A scope is a copy-on-write overlay: reads see the scope layered on baseline, writes
land in the scope only. Local-dev federation and a dev's in-flight flows live in a private
scope, isolated from baseline and from other devs, and dropped in one `DeleteScope`.

*The charter is durable.* An installed trigger is an entity (`trigger:rule:<name>`), and a
goal's record carries the definition it was installed from, so a restart reinstalls the rules
and the goals standing in the verse before anything observes — the program comes back with
the state it acts on. Restoring replays, exactly as installing does: a change that arrived
while verse was down is acted on, and a goal reached while it was down latches on the way back
up. See [the trigger model](trigger-model.md#the-charter-is-durable).

*Resume, not orphan.* On restart, `versed.reconcileProcesses` re-attaches a non-terminal
process whose work outlived verse — one on a remote runner — replaying to recover a terminal
reached while down, and orphans one whose in-process runtime died with the previous run.

*CAS at every contended write.* A store write carries its preconditions, checked with it in
one step (docd's `PatchIf`). A broker adds a terminal-once latch, where the first terminal
writer wins and post-terminal reports are frozen idempotent no-ops, and a claim fence, where
each claim mints an epoch and a stale
generation is rejected — so a displaced or zombie worker cannot corrupt the log.

## Extension points

Each seam is where you plug in without touching the core.

- *Driver* — how a process thinks: `scripted`, `llm`, or your own `Next`.
- *Process control* — where and how a process runs: a k8s Job, a lambda, a broker for humans.
- *Store* — logd/docd, or anything implementing the `Store` interface.
- *Source, or docd controller* — reflect an external system into an owned slice of the verse
  ([sources](sources.md), [writing a controller](controllers.md)).

## Deployment shape

Today `verse up` runs the engine, process runtime, and HTTP surface in one process, and
either brings up logd and docd as children or attaches to a running docd. The pieces — store,
engine (`versed`), process runtime (`proc`) — are separable services behind these same
interfaces, and process control already lets processes run off-box. See
[deployment and steering](deployment-and-steering.md).

## Where to go next

[The tutorial](tutorial.md) takes you from zero to a running, steerable, self-driving flow.
[The docs index](README.md) pairs each of its sections with the notes behind that subject.
