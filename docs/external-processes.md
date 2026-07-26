# External processes: remote agents, unix processes, and humans

Status: design exploration. How should verse host work it does **not** run in-process
— a command on another machine, an agent hosted elsewhere, or a **human** doing a task
by hand — under one model? Criteria: (1) elegance/simplicity; (2) **one interface for
humans and agents** (so a human can take over a runaway agent); (3) **unix process
semantics** when available (stdout/stderr/exit, maybe stdin/pty); (4) keep
**permissions** on the long-term radar.

## The key observation: a process is already a contract, not a runtime

verse does not define a process as "an LLM loop." It defines it as a **contract** that
anything can fulfil:

- a **driver** proposes the next step — `Driver.Next(ctx, prev) → (ToolUse, more)`;
- an **executor** performs an approved step — `Executor.Execute(ctx, tu) → StepResult`;
- every step's **typed effects** (`Verb × Resource`, e.g. `exec` on a `process`) are
  gated by a **vector** — the autonomy dial — before it runs;
- a **read plane** streams structured `Event`s (`EvSay` narration, `EvExec` results,
  `EvPending` gate-waits, `EvStatus` lifecycle, `EvBoundary` cursor);
- a **control plane** applies steering **at boundaries** — `Pause`/`Resume`/`Cancel`/
  `Say`/`SetVector` — "stop between actions, never undo one";
- the process is **mirrored** into the verse as a `process:instance:*` entity, and ends
  with a terminal **outcome + result**.

None of that says *where* the driver or executor run. So "external process" is not a
parallel system — it is the **same contract, fulfilled by an external party**. The
whole design question is *where the seam is* and *how the external party connects*.
Everything below reuses the contract rather than forking it; that is the elegance
budget we are trying to protect.

## Two places the seam can go

**Seam 1 — external leaf (loop stays in verse).** verse runs the `Process` loop, the
gates, and the read plane locally; only `Driver.Next` and/or `Executor.Execute` call
out. A unix command is an `Executor` that `exec`s and streams; a remote model is a
`Driver` that RPCs a host.
- *Wins:* boundaries, gates, cancel-between-actions, mirror — all keep working
  unchanged, because they are local. Smallest change.
- *Loses:* the worker must answer **per step**. A `Next`/`Execute` that blocks for a
  day (a human) pins a goroutine and a context, and doesn't survive a verse restart.
  Fine for live/short work; wrong for offline/long/humans.

**Seam 2 — external loop (verse holds a proxy).** The whole `Process` runs elsewhere —
another host, or a person's head — and **reports** read-plane events, gate requests,
and its terminal **into** the verse while **receiving** control **out**. verse's side
is a proxy over the mirror.
- *Wins:* survives disconnection and restarts; natural home for remote agents and for
  **humans claiming work and going offline**. The durable state is just entities.
- *Loses:* verse can't gate the worker's *individual* actions in real time — it sees
  reported effects and checkpoints, not each syscall. Control is coarser (below).

Seam 1 is a special case of Seam 2 where the worker never disconnects. A general
design should be built on Seam 2 and let Seam 1 fall out for the always-connected case.

## Two ways the external party connects (mostly Seam 2)

**Attached — a live stream.** The worker holds a bidirectional connection; verse relays
output ↔ control in real time. This is what makes real **unix semantics** possible:
live stdout/stderr, a pty, stdin, low-latency say. But it needs connectivity and a
dropped connection loses everything not already mirrored.

**Detached — claim-and-report over entities.** The worker **claims** the process
(a CAS write on a claim field — `PatchIf`, which verse already has), does the work,
**reports** by appending to a journal / writing a result entity, and reads control by
**polling** entity fields (a `cancel`/`say` field it honours at its own checkpoints).
No live stream, no pty; control latency is the poll interval. But it survives
everything, works offline, is identical for a human and an agent, and is *just
entities + gates + CAS* — nothing new.

The doctrine verse already runs resolves this tension: **a live stream is a hint; the
entity state is the truth** — the same `Observe : Since` / `webhook : reconcile` split.
So: **the durable entity contract is authoritative; a live attachment is an optional
accelerator that never becomes the source of record.** On disconnect you degrade to
detached; on reconnect you re-attach. One truth, two access speeds.

## Mapping: unix ⋅ agent ⋅ human onto one contract

| concept | unix process | agent (today) | human |
|---|---|---|---|
| output | stdout | `EvSay` / driver narration | notes / a written report |
| diagnostics | stderr | `EvExec` err, blocked | "here's what went wrong" |
| terminal | exit code | outcome + `Result()` | "done / failed" + summary |
| input | stdin | a `say` | a task update / message |
| interactive | pty | (n/a) | a live console (attached only) |
| **pause** | **SIGSTOP** | `Pause` (hold at boundary) | "hold here" |
| **resume** | **SIGCONT** | `Resume` | "carry on" |
| **cancel** | **SIGTERM** | `Cancel` (ctx) | "stop, and hand back" |
| force | SIGKILL | tombstone the mirror | reassign the claim |
| capability | the syscall it makes | derived `Effect` | an effect it **attests** |
| context | env / cwd / argv | `launch.Spec` | the assignment brief |

Two things fall out of the table:

- **verse's control plane already *is* job control.** pause/resume/cancel-at-a-boundary
  map onto STOP/CONT/TERM-at-a-safe-point. "Stop between actions, never undo" is
  "signals delivered between syscalls." So a unix process and an agent are steered the
  same way, and a human is *asked* the same way — criterion 2, for free, if we keep the
  control plane transport-neutral.
- **Keep the durable contract record-oriented; make pty/stdin attached-only.** Exit code
  is a poor terminal for an agent or a human (they want a status + a result payload), so
  the terminal is `{status: done|failed|cancelled, code?: int, result: text}` —
  never collapsed to an int. `stdout`/`stderr` become an appended, channel-tagged
  **output stream** in the journal (durable); a **pty** is a live capability of an
  attachment, not part of the record.

Every external kind needs to declare its **boundaries** — the safe points where control
applies. A unix process: between commands, or at exit. A human: at each report. An agent:
at each turn (already). The boundary is the universal "where steering lands," and its
coarseness sets control latency.

## Designs, compared

**A. In-process external leaf.** A `unixExec` executor (run a command, stream
stdout/stderr as events, exit → step result) and/or a `remoteDriver` (RPC a model
host). *Simplest; reuses everything.* But per-step blocking, no offline, no
restart-survival, no human. Good as the **unix adapter**, not as the model.

**B. Attached remote runtime (inverted processd).** `processd` already hosts a process
and exposes a read plane (SSE) + control (HTTP). Invert the roles: a worker **dials in**,
pushes events, pulls control over one live connection. *Rich, live, unix-native
(pty/stdin/exit).* But fragile to disconnection, and unless it also mirrors, the truth
dies with the socket. Good as the **attachment layer**.

**C. Detached claim-and-report over entities.** The work item is an entity
(`proc:task:<id>` or the extended `process:instance`); **claim** = CAS on a claim field;
**report** = append to a journal + write a result; **control** = poll a `cancel`/`say`
field. *Maximally uniform — human and agent are byte-identical; offline; restart-proof;
permission-legible; needs nothing new (entities + gates + CAS + scopes).* But coarse:
poll-latency control, no live stream, no pty. This is the **human-native core**, and the
natural fit for the claim → offline → report-back theme.

**D. Hybrid — C is the contract, B is an optional attachment (recommended).** The
durable entity contract (claim, journal, pending gates, terminal) is the source of
record and always works; a connected worker *may* hold a live attachment for real-time
I/O and a pty, strictly derived from and reconciled to the entity truth. A **unix
runner** is a thin adapter that attaches while online and writes the terminal to the
entity; a **human** uses a CLI/UI that claims + reports (and optionally attaches for a
live console); an **agent** — local or remote — is the same. *Meets all four criteria.*
*Cost:* two I/O paths (stream + entity) that must not drift — mitigated by the
non-negotiable rule that the **entity is sole truth and the stream is never
authoritative** (verse's existing hint/truth discipline).

| | A leaf | B attached | C claim/report | **D hybrid** |
|---|---|---|---|---|
| elegance | high | medium | **high** | medium-high |
| human = agent | no | partial | **yes** | **yes** |
| unix semantics (pty/exit) | partial | **full** | exit only | **full (attached)** |
| offline / restart-proof | no | no | **yes** | **yes** |
| control granularity | per-step | per-step | per-checkpoint | per-checkpoint (+ live) |
| new machinery | little | a stream | ~none | a stream over entities |

## Replacing a runaway agent (criterion 2, sharp form)

Because a process is the contract plus the durable **subject/flow**, "take over" is not
a special feature — it is a **claim transfer**. `Cancel` (TERM) the agent, then a human
**re-claims** the same process id, reads its **journal** (exactly what the agent did and
saw — the handoff context is already durable), and continues against the same subject
with the same read/control surface. The flow persists; the driver is swappable
mid-flight. This works *only* if the claim is first-class and transferable (with an
identity) and the journal is the durable record — both of which the hybrid gives you.
It is also the argument for building the **detached** core first: live-attach can't
survive the very failure ("agent is out of control, cut it off") you most need takeover
for.

## Permissions on the radar (not now, but don't preclude)

The in-process agent is trusted by construction — verse built its executor, derives its
effects, and gates each one. An external worker is a **separate principal**, and the
boundary moves:

- **From interception to authorization.** You can't gate every syscall of a remote or
  human worker. Authorize the **claim** (who/what may drive this process — an identity +
  policy), then **spot-gate** the effects the worker chooses to checkpoint. External
  autonomy is *coarser-grained by nature*; that is the price of offline/remote/human
  work, and it should be stated, not hidden.
- **Reported effects are attestations.** A worker's `Effect`s are *claims about the
  world* verse can't verify (did the human really run only that command?). Record them
  as attributed, audited attestations; enforcement/verification comes later.
- **Reuse what exists.** The gate `Vector`/`Registry`/`Resolver` already lets a human
  answer an agent's gate; symmetrically a **human worker raising a gate** ("about to
  deploy — approve?") is the *same* mechanism answered by an agent or a human. Scopes
  and CAS already give per-claimant isolation and safe hand-off.

Minimum to not paint ourselves into a corner: put an **identity on the claim** and
**attributed effects on the journal** now, even with enforcement stubbed. Then policy
("who may claim what," "which attested effects need co-sign") layers on later without a
data-model change.

## Recommendation

Build **D**, detached-core-first:

1. **Extend the `process:instance` mirror into the full durable contract** — add
   `claim {by, identity, at}`, a channel-tagged **journal** (the durable output stream),
   `pending` gates, and a terminal `{status, code?, result}`. This *is* the external
   process's API; it needs only entities + CAS you already have.
2. **A worker SDK/CLI over that contract** — `claim`, `append`, `raise-gate`, `report`,
   `poll-control` — identical for a human (interactive), a unix runner (wraps a command),
   and a remote agent. This is the shared interface.
3. **Add attachment (B) as an accelerator** for live stdout/stderr/pty/say, reconciled
   to the journal and never authoritative.
4. **Keep the leaf adapter (A)** for the trivial always-connected unix case.

This lands the async, out-of-verse lifetime that `effect.go` already reserves a
"spawn/go" verb for: an external process is a concurrent, monitored, detached unit
whose join is a terminal entity write — structured concurrency with the entity-verse as
the join point. Start with the claim/report core (uniform, offline, human = agent),
prove takeover, and add liveness where a connection allows.
