# External tasks

Verse hosts work it does not run in-process: a command on another machine, an agent hosted
elsewhere, or a human doing a task by hand. All three run under one model. Four criteria shaped
it: elegance and simplicity; one interface for humans and agents, so a human can take over a
runaway agent; unix process semantics where they are available, meaning stdout, stderr, exit, and
where possible stdin and a pty; and permissions left reachable rather than precluded.

[Task control](task-control.md) is the resulting contract, and
[reference/tasks.md](reference/tasks.md#executing-one-by-hand) drives it.

## A task is a contract, not a runtime

Verse does not define a task as "an LLM loop". It defines it as a contract that anything can
fulfil:

- a driver proposes the next step, `Driver.Next(ctx, prev) → (ToolUse, more)`;
- an executor performs an approved step, `Executor.Execute(ctx, tu) → StepResult`;
- every step's typed effects (`Verb × Resource`, such as `exec` on a `task`) are gated by a
  vector, the autonomy dial, before it runs;
- a read plane streams structured `Event`s: `EvSay` narration, `EvExec` results, `EvPending`
  gate-waits, `EvStatus` lifecycle, `EvBoundary` cursor;
- a control plane applies steering at boundaries — `Pause`, `Resume`, `Cancel`, `Say`,
  `SetVector` — under the rule "stop between actions, never undo one";
- the task is mirrored into the verse as a `verse.task.instance.*` entity, and ends with a
  terminal outcome and result.

None of that says *where* the driver or executor run. So an external task is not a parallel
system. It is the same contract, fulfilled by an external party. The design question is only
where the seam is, and how the external party connects.

## Where the seam can go

**Seam 1, an external leaf.** Verse runs the `Task` loop, the gates, and the read plane locally;
only `Driver.Next` or `Executor.Execute` calls out. A unix command is an `Executor` that `exec`s
and streams; a remote model is a `Driver` that RPCs a host. Boundaries, gates,
cancel-between-actions and the mirror all keep working unchanged, because they are local. But the
worker must answer per step. A `Next` or `Execute` that blocks for a day, as a human does, pins a
goroutine and a context, and does not survive a verse restart. That is fine for live, short work
and wrong for offline, long, or human work.

**Seam 2, an external loop.** The whole `Task` runs elsewhere, on another host or in a person's
head, and reports read-plane events, gate requests and its terminal into the verse while
receiving control out. Verse's side is a proxy over the mirror. It survives disconnection and
restarts, and it is the natural home for remote agents and for humans who claim work and go
offline, because the durable state is just entities. What it gives up is real-time gating of the
worker's individual actions: verse sees reported effects and checkpoints, not each syscall.

Seam 1 is a special case of Seam 2 where the worker never disconnects, so the general design is
built on Seam 2 and lets Seam 1 fall out for the always-connected case.

## How the external party connects

**Attached** is a live stream. The worker holds a bidirectional connection and verse relays output
against control in real time. This is what makes real unix semantics possible: live stdout and
stderr, a pty, stdin, low-latency say. It needs connectivity, and a dropped connection loses
everything not already mirrored.

**Detached** is claim-and-report over entities. The worker claims the task with a CAS write on a
claim field (`PatchIf`), does the work, reports by appending to a journal and writing a result
entity, and reads control by polling entity fields such as a `cancel` or `say` field it honours at
its own checkpoints. There is no live stream and no pty, and control latency is the poll interval.
In exchange it survives everything, works offline, is identical for a human and an agent, and is
entities, gates and CAS, with nothing new.

The doctrine verse already runs resolves the tension: a live stream is a hint, the entity state is
the truth, the same split as `Observe : Since` and `webhook : reconcile`. The durable entity
contract is authoritative and a live attachment is an optional accelerator that never becomes the
source of record. On disconnect you degrade to detached; on reconnect you re-attach. One truth,
two access speeds.

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

Two things fall out of the table.

Verse's control plane already *is* job control. Pause, resume and cancel-at-a-boundary map onto
STOP, CONT and TERM-at-a-safe-point. "Stop between actions, never undo" is "signals delivered
between syscalls". So a unix process and an agent are steered the same way, and a human is *asked*
the same way, as long as the control plane stays transport-neutral.

The durable contract is record-oriented, and pty and stdin are attached-only. An exit code is a
poor terminal for an agent or a human, who want a status and a result payload, so the terminal is
`{status: done|failed|cancelled, code?: int, result: text}` and is never collapsed to an int.
`stdout` and `stderr` are an appended, channel-tagged output stream in the journal, which is
durable. A pty is a live capability of an attachment, not part of the record.

Every external kind declares its boundaries, the safe points where control applies. A unix process:
between commands, or at exit. A human: at each report. An agent: at each turn. The boundary is the
universal "where steering lands", and its coarseness sets control latency.

## The four shapes, and the one verse runs

**A, in-process external leaf.** A `unixExec` executor that runs a command, streams stdout and
stderr as events, and turns exit into a step result, and a `remoteDriver` that RPCs a model host.
Simplest, and reuses everything, but per-step blocking, with no offline, no restart-survival, and
no human. It is the unix adapter, not the model.

**B, attached remote runtime.** `taskd` already hosts a task and exposes a read plane over SSE and
control over HTTP. Invert the roles: a worker dials in, pushes events, and pulls control over one
live connection. Rich, live, and unix-native, giving pty, stdin and exit, but fragile to
disconnection, and unless it also mirrors, the truth dies with the socket. It is the attachment
layer.

**C, detached claim-and-report over entities.** The work item is an entity. A claim is a CAS on a
claim field, a report is an append to a journal plus a result write, and control is a poll of a
`cancel` or `say` field. Maximally uniform, so a human and an agent are byte-identical; offline;
restart-proof; permission-legible; and it needs nothing new. But it is coarse: poll-latency
control, no live stream, no pty. It is the human-native core.

**D, hybrid.** This is what verse runs. The durable entity contract — claim, journal, pending
gates, terminal — is the source of record and always works, and a connected worker may hold a live
attachment for real-time I/O and a pty, strictly derived from and reconciled to the entity truth.
A unix runner is a thin adapter that attaches while online and writes the terminal to the entity.
A human uses a CLI that claims and reports, and may attach for a live console. An agent, local or
remote, is the same. The cost is two I/O paths, stream and entity, that must not drift, which the
non-negotiable rule holds apart: the entity is sole truth and the stream is never authoritative.

| | A leaf | B attached | C claim/report | D hybrid |
|---|---|---|---|---|
| elegance | high | medium | high | medium-high |
| human = agent | no | partial | yes | yes |
| unix semantics (pty/exit) | partial | full | exit only | full (attached) |
| offline / restart-proof | no | no | yes | yes |
| control granularity | per-step | per-step | per-checkpoint | per-checkpoint (+ live) |
| new machinery | little | a stream | ~none | a stream over entities |

## Replacing a runaway agent

Because a task is the contract plus the durable subject and flow, taking over is not a special
feature. It is a claim transfer. `Cancel` the agent, then a human re-claims the same task id, reads
its journal — exactly what the agent did and saw, so the handoff context is already durable — and
continues against the same subject with the same read and control surface. The flow persists and
the driver is swappable mid-flight.

This works only because the claim is first-class and transferable, carrying an identity, and the
journal is the durable record. It is also why the detached core carries the design: a live attach
cannot survive the very failure — an agent out of control, cut off — that takeover is most needed
for.

## Permissions

What matters here is what the contract must not preclude.

An in-process agent is trusted by construction: verse built its executor, derives its effects, and
gates each one. An external worker is a separate principal, and the boundary moves.

- **From interception to authorization.** You cannot gate every syscall of a remote or human
  worker. Authorize the claim — who or what may drive this task, as an identity plus policy — then
  spot-gate the effects the worker chooses to checkpoint. External autonomy is coarser-grained by
  nature. That is the price of offline, remote and human work, and it is stated rather than hidden.
- **Reported effects are attestations.** A worker's `Effect`s are claims about the world verse
  cannot verify, since it cannot know the human really ran only that command. They are recorded as
  attributed, audited attestations, and verification comes later.
- **The existing mechanism carries it.** The gate `Vector`, `Registry` and `Resolver` already let a
  human answer an agent's gate. Symmetrically, a human worker raising a gate — "about to deploy,
  approve?" — is the same mechanism, answered by an agent or a human. Scopes and CAS already give
  per-claimant isolation and safe hand-off.

An identity on the claim and attributed effects on the journal are what keep policy — who may claim
what, which attested effects need co-sign — a later layer rather than a data-model change.
