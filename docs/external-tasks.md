# External tasks

Viff hosts work that it does not run in its own process. The work can be one of two kinds:

- a command on another machine;
- an agent that runs elsewhere.

Both follow one model. Three criteria decide the model:

| Criterion | What it asks for |
|---|---|
| elegance and simplicity | one small model, not one per kind of work |
| unix process semantics where they are available | stdout, stderr and an exit, and where possible stdin and a pty |
| permissions stay possible | the model does not prevent a later permission layer |

[Task control](task-control.md) is the contract that follows from the model.
[reference/tasks.md](reference/tasks.md#where-a-task-runs-is-a-seam) shows how to use it.

## A task is a contract, not a runtime

Viff does not define a [task](glossary.md#task) as "an LLM loop". Viff defines a task as a
contract, and anything can fulfil the contract. The contract has six parts:

| Part | What it does |
|---|---|
| a driver | proposes the next step: `Driver.Next(ctx, prev) → (ToolUse, more)`. It is what an agent, or a script, runs as. |
| an [executor](glossary.md#executor) | performs a step after the gates approve it: `Executor.Execute(ctx, tu) → StepResult` |
| the [environment](glossary.md#environment) and the [autonomy](glossary.md#autonomy) | decide a step before it runs: one outside the environment is refused, and otherwise the first row of the autonomy that matches it denies it, asks about it or allows it. A step is what an action is about to do, such as `commands.run` with its command. |
| the [event stream](glossary.md#event-stream) | streams structured `Event`s: `EventSay` narration, `EventExec` results, `EventPending` waits on a gate, `EventStatus` lifecycle and `EventBoundary` cursor |
| the [control plane](glossary.md#control-plane) | applies steering at a [boundary](glossary.md#boundary): `Pause`, `Resume`, `Cancel`, `Say` and `SetAutonomy`. Its rule is "stop between actions, never undo one". |
| the [mirror](glossary.md#mirror-of-tasks) | writes the task into the [store](glossary.md#store) as a `mirror.task.instance.*` [entity](glossary.md#entity). The task ends with a terminal outcome and a result. |

The contract does not say *where* the driver or the executor runs. An external task is
therefore not a second system. **An external task is the same contract, fulfilled by an
external party.** The only design question is where the [seam](glossary.md#seam) is, and how
the external party connects.

## Where the seam can go

**Seam 1, an external leaf.** Viff runs the `Task` loop, the gates and the event stream
locally. Only `Driver.Next` or `Executor.Execute` calls out. A unix command is an `Executor`
that `exec`s the command and streams its output. A remote model is a `Driver` that makes an RPC
to a host.

Boundaries, gates, cancel between actions and the mirror all work as before, because they are
local. But the external party must answer for each step. A `Next` or an `Execute` can block for
a long time. It then holds a goroutine and a context for that time, and it does not survive a
restart of viff. Seam 1 therefore suits live, short work. It does not suit long work.

**Seam 2, an external loop.** The whole `Task` runs elsewhere, on another host. The external
party reports events, gate requests and its terminal into the store. It receives control from
viff. The side of viff is a proxy over the mirror.

Seam 2 survives a lost connection and a restart, because the durable state is only entities.
It is the natural design for remote agents. Seam 2 gives up gating in real time of each action
of the external party. Viff sees the effects and checkpoints that the party reports, not each
syscall.

Seam 1 is a special case of Seam 2, where the external party never disconnects. Viff therefore
builds the general design on Seam 2. Seam 1 follows from it for a party that is always
connected.

## How the external party connects

Viff reaches a [runner](glossary.md#runner) at a URL, and holds a live stream from it. Viff
relays control one way and reads events the other way in real time. A live connection makes
real unix semantics possible:

- live stdout and stderr;
- a pty;
- stdin;
- a `say` with low latency.

A live connection needs connectivity, and a rule that viff already follows says what the
connection is. **A live stream is a hint, and the entity state is the truth.** It is the same
split as `Observe : Since` and `webhook : reconcile`. The durable entity contract is the
authority. The live attachment is the way to go fast, and it is never the source of record. On
a disconnect, viff connects again from the cursor it kept on the entity, and the runner
replays what it missed. There is one truth, with two speeds of access.

## unix and agent on one contract

| concept | unix process | agent |
|---|---|---|
| output | stdout | `EventSay` / driver narration |
| diagnostics | stderr | `EventExec` err, blocked |
| terminal | exit code | outcome + `Result()` |
| input | stdin | a `say` |
| interactive | pty | (n/a) |
| pause | SIGSTOP | `Pause` (hold at boundary) |
| resume | SIGCONT | `Resume` |
| cancel | SIGTERM | `Cancel` (ctx) |
| force | SIGKILL | tombstone the mirror |
| capability | the syscall it makes | derived `Effect` |
| context | env / cwd / argv | `launch.Spec` |

Two things follow from the table.

First, **the control plane of viff is job control.** A [hold](glossary.md#hold), a resume and
a cancel at a boundary match STOP, CONT, and TERM at a safe point. "Stop between actions, never
undo" means the same as "deliver signals between syscalls". Viff therefore steers a unix
process and an agent in the same way, as long as the control plane does not depend on the
transport.

Second, the durable contract holds records, and a pty and stdin exist only on the live
connection. An exit code is a poor terminal for an agent, which wants a status and a result.
The terminal is therefore a status and a result, and never only an integer. On the
[instance record](glossary.md#instance-record) it is three fields: `status` (`done`, `failed`
or `cancelled`), `result` (text) and `err` (the failure detail). The terminal has no
field for an exit code.

`stdout` and `stderr` are one output stream in the journal. Each entry is appended and tagged
with its channel, and the journal is durable. A pty is a live capability of the connection. It
is not part of the record.

Every external kind declares its boundaries. A boundary is a safe point where control applies:

| Kind | Its boundaries |
|---|---|
| a unix process | between commands, or at exit |
| an agent | at each turn |

The boundary is where steering lands, for every kind. How far apart the boundaries are sets the
latency of control.

## The two shapes, and the one viff instance runs

**A, in-process external leaf.** An executor runs a command, streams stdout and stderr as
events, and turns the exit into a step result. A driver makes an RPC to a model host. Shape A
is the simplest, and it reuses everything. But it blocks on each step, and it does not survive
a restart. Shape A is the unix adapter, not the model.

**B, attached remote runtime.** `runnerd`, or any program serving the same contract, hosts a
task. It exposes the event stream over SSE and control over HTTP, and viff connects to it.
Shape B is rich, live and native to unix: it gives a pty, stdin and an exit. A lost connection
does not lose the truth, because viff mirrors the stream into the entity as it reads it and
resumes from the cursor it kept.

| | A leaf | B attached |
|---|---|---|
| elegance | high | medium |
| unix semantics (pty/exit) | partial | full |
| restart-proof | no | yes, from the mirror |
| control granularity | per-step | per-step |
| new machinery | little | a stream |

Viff runs shape B, with one rule and no exceptions. **The entity is the only truth, and the
stream is never the authority.**

## Permissions

This section is about what the contract must not prevent.

A task on a runner is a separate [principal](glossary.md#principal), and the boundary of trust
is the runner, not the task's process.

- **The runner performs what crosses a credential.** A push, a pull request, a tracker's or
  Slack's act is made by the runner's own process, with a credential the task never holds, and
  decided there again by the task's environment, its current autonomy and the runner's ceiling. A row that refuses
  such a step prevents it. A step the runner can only stop inside the run (a file write, a
  command) is held by stopping what the agent proposes: a prompt, said so on
  [inside](systems/inside.md), until a seam below the task makes it a bound.
- **What the runner reports is the record.** Every call is an event on the task's stream,
  naming the step and its fields, the row that decided it, and whoever answered. Viff holds
  the stream and the entity; it does not re-derive a step from outside the process.
- **The existing mechanism carries it.** The task's `Autonomy`, the `Registry` and the `Resolver`
  let a person answer an agent's question, and a task on a runner raises the same question
  through the runner. It is the same mechanism, and an agent or a person answers it.

Two things keep policy a layer over the data model and not a change to it: an identity on the
runner, and events on the stream that name who made them.
