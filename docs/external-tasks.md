# External tasks

Verse hosts work that it does not run in its own process. The work can be one of three kinds:

- a command on another machine;
- an agent that runs elsewhere;
- a person who does a task by hand.

All three follow one model. Four criteria decide the model:

| Criterion | What it asks for |
|---|---|
| elegance and simplicity | one small model, not one per kind of work |
| one interface for people and agents | a person can take over from an agent that is out of control |
| unix process semantics where they are available | stdout, stderr and an exit, and where possible stdin and a pty |
| permissions stay possible | the model does not prevent a later permission layer |

[Task control](task-control.md) is the contract that follows from the model.
[reference/tasks.md](reference/tasks.md#executing-one-by-hand) shows how to use it.

## A task is a contract, not a runtime

Verse does not define a [task](glossary.md#task) as "an LLM loop". Verse defines a task as a
contract, and anything can fulfil the contract. The contract has six parts:

| Part | What it does |
|---|---|
| a [driver](glossary.md#driver) | proposes the next step: `Driver.Next(ctx, prev) → (ToolUse, more)` |
| an [executor](glossary.md#executor) | performs a step after the gates approve it: `Executor.Execute(ctx, tu) → StepResult` |
| the [vector](glossary.md#vector) | gates the typed effects of every step before the step runs. An effect is a `Verb × Resource`, such as `exec` on a `task`. The vector is the autonomy dial. |
| the [event stream](glossary.md#event-stream) | streams structured `Event`s: `EvSay` narration, `EvExec` results, `EvPending` waits on a gate, `EvStatus` lifecycle and `EvBoundary` cursor |
| the [control plane](glossary.md#control-plane) | applies steering at a [boundary](glossary.md#boundary): `Pause`, `Resume`, `Cancel`, `Say` and `SetVector`. Its rule is "stop between actions, never undo one". |
| the [mirror](glossary.md#mirror-of-tasks) | writes the task into the [store](glossary.md#store) as a `verse.task.instance.*` [entity](glossary.md#entity). The task ends with a terminal outcome and a result. |

The contract does not say *where* the driver or the executor runs. An external task is
therefore not a second system. **An external task is the same contract, fulfilled by an
external party.** The only design question is where the [seam](glossary.md#seam) is, and how
the external party connects.

## Where the seam can go

**Seam 1, an external leaf.** Verse runs the `Task` loop, the gates and the event stream
locally. Only `Driver.Next` or `Executor.Execute` calls out. A unix command is an `Executor`
that `exec`s the command and streams its output. A remote model is a `Driver` that makes an RPC
to a host.

Boundaries, gates, cancel between actions and the mirror all work as before, because they are
local. But the external party must answer for each step. A `Next` or an `Execute` can block for
a day, as a person does. It then holds a goroutine and a context for that day, and it does not
survive a restart of verse. Seam 1 therefore suits live, short work. It does not suit offline
work, long work or work by a person.

**Seam 2, an external loop.** The whole `Task` runs elsewhere: on another host, or in the head
of a person. The external party reports events, gate requests and its terminal into the store.
It receives control from verse. The side of verse is a proxy over the mirror.

Seam 2 survives a lost connection and a restart, because the durable state is only entities.
It is the natural design for remote agents, and for people who claim work and then go offline.
Seam 2 gives up gating in real time of each action of the external party. Verse sees the
effects and checkpoints that the party reports, not each syscall.

Seam 1 is a special case of Seam 2, where the external party never disconnects. Verse therefore
builds the general design on Seam 2. Seam 1 follows from it for a party that is always
connected.

## How the external party connects

**Attached** is a live stream. The [worker](glossary.md#worker) holds a connection in both
directions. Verse relays output one way and control the other way in real time. An attached
connection makes real unix semantics possible:

- live stdout and stderr;
- a pty;
- stdin;
- a `say` with low latency.

An attached connection needs connectivity. When the connection drops, everything that is not
yet in the mirror is lost.

**Detached** is claim and report over entities. The steps are these:

1. The worker [claims](glossary.md#claim-of-a-worker) the task with a CAS write on a claim
   field (`PatchIf`).
2. The worker does the work.
3. The worker reports: it appends to a journal and writes a result entity.
4. The worker reads control by polling entity fields, such as a `cancel` or `say` field. It
   acts on them at its own checkpoints.

A detached worker has no live stream and no pty. The latency of control is the poll interval.
In exchange, a detached worker survives every failure and works offline. It is the same for a
person and for an agent. It needs only entities, gates and CAS, and nothing new.

A rule that verse already follows settles the choice. **A live stream is a hint, and the
entity state is the truth.** It is the same split as `Observe : Since` and
`webhook : reconcile`. The durable entity contract is the authority. A live attachment is an
optional way to go faster, and it is never the source of record. On a disconnect, the worker
falls back to detached. On a reconnect, it attaches again. There is one truth, with two speeds
of access.

## unix, agent and human on one contract

| concept | unix process | agent | human |
|---|---|---|---|
| output | stdout | `EvSay` / driver narration | notes / a written report |
| diagnostics | stderr | `EvExec` err, blocked | "here's what went wrong" |
| terminal | exit code | outcome + `Result()` | "done / failed" + summary |
| input | stdin | a `say` | a task update / message |
| interactive | pty | (n/a) | a live console (attached only) |
| pause | SIGSTOP | `Pause` (hold at boundary) | "hold here" |
| resume | SIGCONT | `Resume` | "carry on" |
| cancel | SIGTERM | `Cancel` (ctx) | "stop, and hand back" |
| force | SIGKILL | tombstone the mirror | reassign the claim |
| capability | the syscall it makes | derived `Effect` | an effect it attests |
| context | env / cwd / argv | `launch.Spec` | the assignment brief |

Two things follow from the table.

First, **the control plane of verse is job control.** A [hold](glossary.md#hold), a resume and
a cancel at a boundary match STOP, CONT, and TERM at a safe point. "Stop between actions, never
undo" means the same as "deliver signals between syscalls". Verse therefore steers a unix
process and an agent in the same way. Verse asks a person in the same way too, as long as the
control plane does not depend on the transport.

Second, the durable contract holds records, and a pty and stdin exist only when attached. An
exit code is a poor terminal for an agent or a person, who want a status and a result. The
terminal is therefore a status and a result, and never only an integer. On the
[instance record](glossary.md#instance-record) it is three fields: `status` (`done`, `failed`,
`cancelled` or `orphaned`), `result` (text) and `err` (the failure detail). The terminal has no
field for an exit code.

`stdout` and `stderr` are one output stream in the journal. Each entry is appended and tagged
with its channel, and the journal is durable. A pty is a live capability of an attachment. It
is not part of the record.

Every external kind declares its boundaries. A boundary is a safe point where control applies:

| Kind | Its boundaries |
|---|---|
| a unix process | between commands, or at exit |
| a person | at each report |
| an agent | at each turn |

The boundary is where steering lands, for every kind. How far apart the boundaries are sets the
latency of control.

## The four shapes, and the one verse runs

**A, in-process external leaf.** An executor runs a command, streams stdout and stderr as
events, and turns the exit into a step result. A driver makes an RPC to a model host. Shape A
is the simplest, and it reuses everything. But it blocks on each step. It has no offline mode,
does not survive a restart, and has no place for a person. Shape A is the unix adapter, not the
model.

**B, attached remote runtime.** `runnerd` hosts a task. It exposes the event stream over SSE and
control over HTTP. Shape B reverses the roles: a worker connects to verse, pushes events and
pulls control over one live connection. Shape B is rich, live and native to unix: it gives a
pty, stdin and an exit. But a lost connection breaks it. Unless it also writes the mirror, the
truth is lost with the socket. Shape B is the attachment layer.

**C, detached claim and report over entities.** The work item is an entity. The parts map like
this:

- a claim is a CAS on a claim field;
- a report is an append to a journal, and a write of the result;
- control is a poll of a `cancel` or `say` field.

Shape C is as uniform as possible: the records of a person and of an agent are identical to the
byte. It works offline, survives a restart, makes permissions easy to read, and needs nothing
new. But it is coarse. Control has the latency of a poll, and there is no live stream and no
pty. Shape C is the core that suits a person.

**D, hybrid.** Verse runs shape D. The durable entity contract is the source of record, and it
always works. Its parts are the claim, the journal, the pending gates and the terminal. A
connected worker may also hold a live attachment for real-time I/O and a pty. The attachment
only derives from the entity truth, and it is reconciled to it.

| Worker | How it takes part in shape D |
|---|---|
| a unix runner | a thin adapter. It attaches while online and writes the terminal to the entity. |
| a person | a CLI that claims and reports. The person may attach for a live console. |
| an agent, local or remote | the same as the others |

Shape D has one cost: two I/O paths, the stream and the entity, that must not disagree. One
rule, with no exceptions, keeps them apart. **The entity is the only truth, and the stream is
never the authority.**

| | A leaf | B attached | C claim/report | D hybrid |
|---|---|---|---|---|
| elegance | high | medium | high | medium-high |
| human = agent | no | partial | yes | yes |
| unix semantics (pty/exit) | partial | full | exit only | full (attached) |
| offline / restart-proof | no | no | yes | yes |
| control granularity | per-step | per-step | per-checkpoint | per-checkpoint (+ live) |
| new machinery | little | a stream | ~none | a stream over entities |

## Replacing a runaway agent

A task is the contract, plus a durable subject and flow. Taking over a task is therefore not a
special feature. **Taking over is a transfer of the claim.** The steps are these:

1. `Cancel` the agent.
2. A person claims the same task id again.
3. The person reads the journal of the task. The journal holds exactly what the agent did and
   saw, so the context for the handover is already durable.
4. The person continues against the same subject, with the same event stream and control.

The flow stays. The driver changes while the task runs.

A takeover works only because of two facts. The claim is a first-class value that can be
transferred, and it carries an identity. The journal is the durable record.

The detached core carries the design for the same reason. Takeover is most needed when an agent
is out of control and cut off. A live attachment cannot survive that failure.

## Permissions

This section is about what the contract must not prevent.

An agent in the verse process is trusted by construction. Verse built its executor, derives its
effects, and gates each effect. An external worker is a separate
[principal](glossary.md#principal), and the boundary of trust moves.

- **From interception to authorization.** Verse cannot gate every syscall of a remote worker or
  a person. Verse authorizes the claim instead: who or what may drive this task, as an identity
  and a policy. Verse then gates, at chosen points, the effects that the worker reports at its
  checkpoints. The autonomy of an external worker is coarser by nature. That is the price of
  offline, remote and human work, and this page states it openly.
- **Reported effects are attestations.** The `Effect`s of a worker are claims about the world
  that verse cannot verify. Verse cannot know that the person ran only that command. Verse
  records them as attestations, with the worker named and an audit trail. Verification comes
  later.
- **The existing mechanism carries it.** The gate `Vector`, the `Registry` and the `Resolver`
  already let a person answer the gate of an agent. In the same way, a person who is a worker
  can raise a gate, such as "about to deploy, approve?". It is the same mechanism, and an agent
  or a person answers it. Scopes and CAS already isolate each claimant and make a handover safe.

Two things keep policy a later layer and not a change to the data model:

- an identity on the claim;
- effects on the journal that name who made them.

Policy here means who may claim what, and which attested effects need a second signature.
