# Deployment shape & the steering/observation split

*Design note for the internal dogfood — running real SDLC flows (e.g. releases)
over the verse prototype. It fixes the service topology and, most importantly, the
one invariant that shapes the daemons: **steering outranks observation.***

## What we're building

Not a demo — a small internal deployment that actually drives a flow in our SDLC
(Ani's ask: get it running to do something real, like run releases). That means
two long-lived services, fronted by one `verse` CLI, and it means the steering
principle from the representation doc has to be real, not aspirational:

> **Steering outranks observation. Intervention that lands late is intervention
> defeated.**

## Three logical roles

| Role | What it is | Deploys as |
|---|---|---|
| **entity-verse (store)** | shared coordination state + change stream | in-memory now (behind `entity.Store`); an **external docd/logd service** later — versed embeds the *client*, not the storage engine |
| **proc / processd** | the governed-process runtime — runs agents in sandboxes | a daemon (already an HTTP control+read plane) |
| **versed** | the reactive engine/gateway — observes the verse, matches triggers, gates, spawns | a daemon (to build) |
| **connect** (local) | federates the laptop's working tree/git into a *scope* of the verse | a local client loop (`verse connect`) |

Placement: the store lives **in `versed`'s process in-memory now**, and later moves
behind an **external docd/logd service** that `versed` reaches through an embedded
**client** (not the storage engine). Either way `versed` owns the store handle; a
broad shared SCM index is the thing that would be a separate service in its own
right. `proc` reports process state back into the verse via `mirror`
(in-process now; over `processd`'s SSE when split out).

## The two planes — and why they must stay apart

Everything coordinates through the verse, but **not everything is coordination.**
There are two distinct planes, and conflating them is what breaks the invariant:

- **Data coordination** — actions write entities, triggers react. Flows through the
  store; eventual, queued, drop-with-gap. This is the emergent loop and it is *fine*
  for it to lag.
- **Control / steering** — pause, cancel, redirect a flow, slide a gate's autonomy,
  answer a gate, inject an instruction. This must **not** flow through the store's
  observation stream.

**The trap:** the "coordinate through the store, no orchestrator" instinct tempts you
to model intervention as *a store write* (a command entity). But then intervention
rides the same single observation channel it is supposed to outrank — it queues
behind the backlog and, under load, the store's bounded buffer can even *drop* it.
The dial wouldn't turn when you turn it. So the emergent-coordination philosophy and
the steering-priority invariant genuinely pull apart here, and the split has to be
explicit: **control is out-of-band from store-observation and serviced with
priority. Intervention is never a store write.**

## Holds cost a cursor, not a buffer

A flow parked at a long-waiting gate — or a paused engine — must not accumulate a
growing backlog of unread changes. It doesn't: **observation is revision-cursored.**
Every change carries a monotonic `Rev`; a consumer tracks only the highest `Rev` it
has processed and catches up by asking the store `Since(cursor)` rather than holding
a buffer. A hold costs an int, not memory, however long it lasts.

- `Observe()` is a low-latency *hint* that may drop under backpressure; `Since` (a
  query by revision) is the source of truth — so drops become *recoverable* (a cursor
  gap → replay) instead of silent.
- The engine reconciles level-triggered: every non-paused iteration, `if cursor <
  Rev(): catchUp` via `Since`. Pause/resume, a long gate, and dropped live events all
  heal the same way — by replaying from the cursor.
- This is docd-native and the only sane model once the store is a **remote service**:
  you can't hold a buffer on the far side of a network, so you hold a cursor and query.
  `Since(cursor)` maps onto a docd query-by-revision (`FromCommit`/`ReadStateAt`).

`TestDropRecoveryViaReplay` pins it: a pause that outlasts the live buffer drops live
events, yet all 300 changes still dispatch on resume, replayed from the cursor.

## Where the invariant is realized (status)

| Plane | Realizes "steering outranks observation"? | How |
|---|---|---|
| **Process substrate** (`process`, `processd`) | **Yes** | boundary services control before the next action (`ctx.Err()` → pause hold → fold steer → `Next`); cancel > pause > say; the read plane is fire-and-forget and never blocks control; `processd` exposes control as **separate endpoints** from `GET /events` |
| **Gate resolution** (engine) | **Yes** | a pending gate is *observed* via the store (`mirror` projects `pending`), but *answered* out-of-band via the `Resolver`/`Registry` — never a store write; and firing is off the observe goroutine so a blocking human gate doesn't even stall observation |
| **Engine flow-level steering** (`versed`) | **Yes (now built)** | the engine has a control plane drained with strict priority — see below |

### The engine control plane (built)

`trigger.Engine` now has a control channel serviced *ahead of* observation:

- the loop calls `drainControl()` at the top of every iteration — **all pending
  control runs before any change is dispatched**;
- while **paused**, the loop services control *only* and does not consume
  observations at all (they buffer in the store, dispatch on resume) — so a runaway
  flow can be halted regardless of how backed-up the change stream is;
- control closures run on the loop goroutine, so they **serialize with dispatch**
  and never race it. `Pause` / `Resume` are synchronous (return once applied); `Do`
  is the generic steering primitive until typed ops land.

`TestControlOutranksObservation` pins it: a `Pause` takes effect ahead of a burst of
20 observations (none fire while paused), and the burst dispatches on `Resume`.

## The `verse` CLI

One binary, one surface. `up` is the only daemon verb; everything else is a client
that connects to whatever is running. Output is tony-format (`-o`), so entities
round-trip (`verse entity get … | verse entity put`).

```
verse
├── up|down [engine|proc|all]     start/stop daemon(s); `up all` = monolith in one process
├── status                        what's running + current store rev
│
│   (the plural lists a collection; the singular acts on one member of it)
├── processes (ps) / process      ── process runtime (proc) ──
│   run <spec> [--wait] · get · attach · say · pause|resume · cancel
├── triggers / trigger            ── the charter (engine rules) ──
│   add · show · remove
├── flows / flow                  ── engine steering, per subject (control plane) ──
│   pause|resume [subject] · cancel <subject> · say <subject> <text>
├── gates / gate                  ── human-in-the-loop decisions ──
│   approve|y <id> [--say <text>] · deny|n <id> [--say <text>]
├── entities / entity (e)         ── the store ──
│   get · put
├── watch [--system --kind --since <rev>]   stream store changes, cursored
└── connect [path] / disconnect   ── local-dev federation (see below) ──

global:  --scope <name>  (baseline default) · -o <tony-format> · --config
```

**The tree encodes the plane split.** *Observe* (`watch`, `process attach`, `get`,
`list`, `status`) hits the read plane; *steer* (`flow`, `gate approve|deny`, `process
cancel|say`) hits the out-of-band control plane — the operator can't accidentally
"steer via a write." Saying something to a running agent (`process say` / `flow say`,
a re-prompt) is one such steering gesture: out-of-band, folded in at the process's next
boundary, never a store write. `flow` (a subject's whole lifecycle) and `process` (one governed
unit) are different scopes on purpose; `watch` (store-wide) and `process attach` (one
process) are coarse vs fine observation. `gate` is where the release approver lives.

The load-bearing rule for versed's HTTP surface: **control endpoints are distinct
from the observation/read endpoint**, mirroring `processd`. A `verse flow pause`/`gate
resolve` hits the control plane and is applied with priority; it is never expressed as
a change written into the verse. `up all` runs engine+proc+store in one process
(monolith); split mode runs them apart and clients target each via `--config`.

## Local dev: federating the working tree

`signadot local connect`, for the entity-verse. A developer's working tree, git
state, and local processes are state the SDLC cares about but that lives off-store.
`verse connect .` federates it into an isolated per-session **scope**: it writes scoped
file metadata (`local:dir-status-<scope>:*`), serves file **contents on demand** via a
docd content controller (`local:dir-content-<scope>:*`, nothing stored), and installs a
scoped trigger that spawns a validator per file — so a validation flow reacts to
*uncommitted* changes with full org context, without polluting the shared baseline, and
the validator reads contents *through the verse* rather than a shared disk.

- **Scopes are the mechanism** — their correct, narrow use (the SCOPES doc's "isolated
  view for sandbox interop"): a scoped session's writes land in the scope, the
  baseline is untouched, and scoped reads/watches see baseline layered with the scope.
  `entity.Store` reserves the seam — `Scope(name) Store` (in-memory returns a
  non-isolating passthrough; a docd-backed store does real copy-on-write).
- **One-way per kind** (anti-loop, like `mirror`): local FS is source-of-truth for
  `local:*` (projected up); shared entities are read-mostly locally (projected down).
  A kind federated both ways re-ignites.
- **Local topology:** the laptop runs `connect` + a local `verse up proc` (so
  validation processes can actually touch local files), both clients of the remote,
  scope-aware store and the baseline `versed`. That is the fourth deployment role.

This settles the store shape: local dev requires an **external, scope-aware store**
that multiple clients embed (versed on baseline, each dev's connector on a scope) —
exactly the docd-client-not-embedded-engine model.

## Built vs. next

**Built (this note's change):** the engine control plane — priority drain,
pause-holds-dispatch, `Pause`/`Resume`/`Do`, race-clean, with the invariant test; and
**revision-cursored observation** (`Event.Rev`, `Store.Since`/`Rev`, engine catch-up)
so holds cost a cursor, not a buffer — with a drop-recovery test.

**Also realized since:** the single **`verse` CLI** (subcommands per the CLI section);
`up` wiring the engine + proc + store in one process (spawning logd+docd, or attaching
to a running docd); versed's HTTP surface with the control/observation split; and typed
per-flow control ops — **pause/resume**, **cancel a flow**, **say to a flow** (inject an
instruction) — each
carrying a subject ref, plus `verse status`. Gate answers ride the out-of-band resolver
path (`gate approve` / `gate deny` → `POST /gate/resolve`).

**Next, for the dogfood:**
- **Rebind autonomy** at runtime (slide a gate) as a first-class control op.
- Wire the gate resolver to a real surface — Slack — for the release approver.
- `verse down`; a **git Source** (the first generic external Source).
- **Realized:** the store. A **logd client adapter** (the service is external —
  versed embeds the client) gives durability + replay, mapping `Since` →
  query-by-revision via a `FromCommit` replay watch, with atomic CAS writes
  (`PatchIf`). It is now fronted by **docd** and is `up`'s **default**: with no store
  flags `up` brings logd+docd up on a temp data dir via `o sys up`; `--data <dir>` for
  a durable dir, `--docd-addr`/`--docd-mount-addr` to attach to a running docd,
  `--store memory` to opt out (in-memory `entity.Store`, no daemons).
- **Realized:** `verse connect` + the global `--scope`, and real copy-on-write
  scopes on the docd/logd store — `Store.Scope(name)` returns an isolating child
  (writes to the scope, reads overlaid on baseline) and `disconnect --scope` drops
  the overlay via `DeleteScope`. The in-memory store keeps a non-isolating
  passthrough, so federation was an addition, not a rewrite.
- **Realized:** the full local-federation loop — `verse connect` writes scoped file
  metadata to docd, runs a **content controller** serving bytes on demand, and installs
  a scoped trigger; a **per-scope engine** fires a validator per file, and the agent's
  `read_file` resolves content *through the verse* (so it needn't share a disk). Scoped
  engines observe via **per-trigger subtree watches** — each trigger watches exactly the
  `system.kind` it matches, not a root watch (docd routes a subtree watch at/below a
  mount; whole-tree root watches are out of scope by design). See
  `docs/content-federation.md`.

## Why this ordering

The invariant is cheap to honor now and expensive to retrofit: if intervention ever
becomes a store write, unwinding it touches every control path. Validating it before
building the daemons means `versed`'s HTTP surface is born with the split, and a
release we dogfood can actually be *stopped in time* — which is the whole point of
steering a real flow.
