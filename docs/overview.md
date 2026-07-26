# Verse — architecture overview

*Read this first.* It gives the shape of the system and the reasoning behind the load-bearing
decisions; the other docs in this directory are deep-dives on single topics (linked at the
end). Audience: an engineer comfortable with Go, HTTP, and basic distributed-systems ideas
(cursors, CAS, at-least-once delivery) who wants the map before the territory.

## What it is

Verse is a small runtime for **SDLC as emergent flows on a dial of agentic autonomy**. You
don't author a workflow. You install a *charter* of trigger rules; the runtime reacts to
changes in a shared, observable **entity store** and does the work with governed **agent
processes**. Every reaction passes a **gate** — *auto*, *ask*, or *park* — so how much runs
on its own is turned up or down per action. The flow is not written down anywhere; it
*emerges* from triggers reacting to state that other actions write.

The entire system is one feedback loop:

```
   entity change ──▶ trigger matches ──▶ gate resolves ──▶ action fires
        ▲                                                      │
        └───────────── actions write entities ◀───────────────┘
```

A process completing is itself an entity change (its state is *mirrored* into the store), so
a completion can trigger the next reaction. That is how multi-step flows close — through the
store, not through a scheduler.

## The model (five nouns)

- **Entity** — anything the SDLC refers to, named by a `system:kind:id` triple
  (`demo:pr:1`, `github:pr:org/repo#42`, `process:instance:review-1`) with an arbitrary
  payload value. The **entity store** is the shared, observable, cursored log of these.
- **Trigger** — one charter rule: *when the condition holds and its gates resolve, an action
  fires.* `condition` (a `when` — a point-in-time **match** over system/kind/id/op/fields, or
  a temporal formula — plus the `subject` the firing is about), `gates` (conjunctive),
  `action`. See `docs/trigger-model.md`.
- **Gate** — `auto` (fire) · `ask` (a human/agent must approve) · `park` (hold). This is the
  autonomy dial, expressed per action.
- **Action** — *data, not code*: `set` fields on an entity (a state transition) and/or
  `spawn` a process (a `launch.Spec`). It acts on the condition's `subject` — it carries no
  target of its own; template bindings (`$subject`, and every field of the changed payload)
  parameterize it.
- **Process** — a governed unit of agentic work, hosted by a runtime, observable and
  steerable. Processes are themselves entities (`process:instance:<id>`), so the fleet's
  lifecycle is visible by watching the store.

Two derived notions matter: a **subject** is the entity a piece of work is *about* (the PR
moving through its lifecycle); a **flow** is a subject's in-flight processes, steerable as a
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
      entity.Store                    trigger.Engine            controlPolicy
   (MemStore | logd/docd)         (reactive, per-subtree      (how a process RUNS)
   Put/Modify(CAS)/Get/watch       watches, gate registry)     ├─ local  (processd, in-proc)
           ▲                                │  spawn           ├─ remote (controlpolicy.HTTP)
           │  mirror (process → entity)     ▼                  └─ broker (dial-in worker)
           └──────────────  process runtime (process/processd)  ◀──┘
                            Process · Supervisor · Vector · Event stream · gate registry
```

- **`entity`** — the store. `Put` is last-write-wins; `Modify` is a compare-and-swap
  read-modify-write (no lost updates); `Get` reads a ref and a `Lister` enumerates; `Observe`
  is a *low-latency hint* that may drop, while the cursored `watch … since=<rev>` replay is
  the *source of truth*.
  `MemStore` for dev/test; a **logd/docd** backing for durability, copy-on-write **scopes**,
  and controller federation. See `change-events.md`, `entity-values.md`.
- **`trigger`** — the reactive engine. It observes the store through **per-subtree watches**
  (each trigger watches exactly the `system.kind` it matches — there is *no* root watch),
  matches installed `Def`s, resolves gates through a shared registry, and fires actions. A
  spawn action goes through the engine's **Spawner**, which hosts the process via the
  controlPolicy and mirrors it back.
- **`process` / `process/processd`** — the process runtime. A `Process` runs a **driver**
  (`Next() → ToolUse`; `scripted` or `llm`) under a `Supervisor` governed by a `Vector` (the
  per-process autonomy defaults). It emits a **cursored, ring-buffered, resumable event
  stream** and posts gate decisions to a registry. `processd` is the HTTP daemon face of that
  runtime.
- **`mirror`** — projects a process's ephemeral read plane onto its durable
  `process:instance:<id>` entity (status, a `say` preview, last tool output, final `result`,
  the resume `cursor`, the `policy`, and a broker's `dialURL`). One-way — *an index, not a
  warehouse.* This is the bridge that lets process completion drive the next trigger.
- **`controlpolicy` / `broker`** — the two off-box controlPolicies (see below).
- **`versed`** — the assembly and HTTP surface. Also owns **scopes** (per-scope engines over
  a COW store view), hosts brokers under `/broker/<nonce>`, and runs **resume-or-orphan**
  reconcile at startup.
- **`source`** — federates external state *in*: `verse connect` reflects a working tree into
  a scope (file metadata as entities + contents served on demand through a docd controller),
  and the same pattern generalizes to git/GitHub/etc. See `connect.md`, `sources.md`,
  `content-federation.md`.

## The load-bearing decisions

**Reactivity rides a durable, cursored store — not an in-memory bus.** The store is the
truth; the change stream is a cursor into it. `Observe` may drop under load (it's a
doorbell); correctness comes from replaying `since=<rev>` — a long hold costs a cursor, not a
buffer. Writes are CAS (`Modify` / docd `PatchIf`), so concurrent modifiers never lose an
update. This is what lets the engine, a restart, or a second reader converge on the same
state without coordination.

**Observation and control are separate planes, and control has priority.** Reads (watch,
get, list) can lag; steering (`gate approve|deny`, `flow pause/cancel/say`) is out-of-band and
serviced *ahead* of observation, never riding the change stream. An intervention that has to
queue behind a backlog of observations is an intervention defeated — so a runaway flow can
always be stopped in time. See `deployment-and-steering.md`.

**Actions are data.** A trigger's action is a declarative `set`/`spawn`, not a callback. The
charter is inspectable, diffable, and installable at runtime; the "when" (matches) is kept
small because that's where complexity hides.

**No orchestrator — flows emerge.** Nothing sequences "review → reviewed." The review process
completing becomes a `process:instance` change; a separate `reviewed` trigger reacts to it and
advances the subject. Composition is by triggers reacting to shared state, which is why the
model scales to many concurrent, independent flows with no central plan. (Triggers must opt
in to `process:*` matches, so the loop can't ignite on its own machinery.)

**Processes are entities.** Because `mirror` projects the read plane into the store, the whole
fleet is observable by watching `process:instance:*`, completions are ordinary change events,
and a process's outcome survives its ephemeral runtime.

**How a process *runs* is a seam: the controlPolicy.** `ControlPolicy{Spawn, Attach, Ref}`
returns a `ProcHandle{Subscribe, Pause/Resume/Cancel/Steer/SetVector/Result}`. The engine
drives every process through that handle and never touches the runtime directly — there is
**no privileged local path**. Three implementations sit behind it:

- **local** — the in-process `processd` supervisor (a Go call).
- **remote** — `controlpolicy.HTTP(baseURL)`, driving another verse/host over the
  verse-facing contract (`GET /processes`, `GET /events?since`, control POSTs).
- **broker** — a process whose runtime is an external **dial-in worker** (a laptop agent, a
  firewalled pod, or a human). The worker connects *out* to the broker (`claim`/`inbox`/
  `report`); the broker glues that leg to verse's handle. It is the `ProcHandle` turned
  inside-out: the same process behind two handles, one where it runs, one where it's observed.

Because the seam is uniform, `verse processes` and `verse process get/attach/cancel/say/approve` work whether a
process is local, remote, or brokered, and a **human executes** a process via `verse process
do` exactly as an agent would. See `control-policy.md`.

**Gates are the dial, and they point up.** An executor that reaches a decision it isn't
authorized to make **raises** a gate to versed rather than deciding; the verse governs who
resolves it (auto, a human, later a vote or reviewer). The executor never resolves its own
gate — governance stays on versed's side of the mirror.

## Data & failure model

- **Refs and cursors.** `system:kind:id` plus a monotonic-but-sparse store revision treated
  as an opaque cursor; the process event stream carries its own per-process cursor for
  resume. Nothing assumes dense or gap-free numbering; everything reconciles via `since`.
- **Scopes.** A scope is a copy-on-write overlay: reads see the scope layered on baseline,
  writes land in the scope only. Local-dev federation and a dev's in-flight flows live in a
  private scope, isolated from baseline and from other devs, dropped in one `DeleteScope`.
- **Resume, not orphan.** On restart, `versed.reconcileProcesses` re-attaches a non-terminal
  process whose work outlived verse (a remote policy) — replaying to recover a terminal
  reached while down — and orphans one whose in-process runtime died with the previous run.
- **CAS at every contended write.** Store writes (`Modify`/`PatchIf`); a broker's
  **terminal-once** latch (first terminal writer wins; post-terminal reports are frozen
  idempotent no-ops) and **claim fence** (each claim mints an epoch; a stale generation is
  rejected, so a displaced or zombie worker can't corrupt the log).

## Extension points

Each seam is where you plug in without touching the core:

- **Driver** — how a process thinks (`scripted`, `llm`, your own `Next`).
- **controlPolicy** — where/how a process runs (k8s Job, lambda, a broker for humans).
- **Store** — `MemStore` vs logd/docd; anything implementing the `Store` interface.
- **Source / docd controller** — reflect an external system into an owned slice of the verse
  (`sources.md`, `controllers.md`).

## Deployment shape

Today `verse up` runs the engine, process runtime, and HTTP surface in one process and either
brings up logd+docd as children or attaches to a running docd. The pieces — store, engine
(`versed`), process runtime (`proc`) — are separable services behind these same interfaces;
the controlPolicy already lets processes run off-box. See `deployment-and-steering.md`.

## Where to go next

- **Hands-on:** `tutorial.md` — from zero to a running, steerable, self-driving flow.
- **The store & change model:** `change-events.md`, `entity-values.md`.
- **Running/governing processes:** `control-policy.md` (contract), `control-policy-plan.md`
  (build state), `external-processes.md`.
- **Steering vs observation:** `deployment-and-steering.md`.
- **Bringing the outside in:** `connect.md` (federate a working tree via docd), `sources.md`,
  `controllers.md`, `content-federation.md`, `write-back.md`.
