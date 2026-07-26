# Plan: `action.on` completion hooks — visible process chaining

## Context

Today a trigger's `Action` can `set` fields and/or `spawn` a process, but **what happens when
that process finishes is invisible in the charter**. The tutorial teaches the completion step as
a *second, decoupled* trigger that matches `process:instance status=completed` and pattern-matches
the subject via the process's `flow`. That emergent two-trigger pattern is powerful, but for a
plain linear chain (review → reviewed → merge → …) it scatters one intent across separate rules
and forces the author to reason about the reflected `process:instance` namespace.

Goal: let a `spawn` declare, inline, what to do when it completes — so a linear chain reads top-to-
bottom in one trigger. Decided shape (with the user): a **status-keyed map on the Action**,
`on: { <terminal-status>: <Action> }`, applied when the spawned process reaches that terminal
status. It is **recursive** (each `on[status]` is a full `Action` with its own `set`/`spawn`/`on`),
so chains nest; `on.completed` is the "done" hook; `onDone` is sugar for `on.completed`. The
decoupled two-trigger pattern still works and remains preferred for fan-in / cross-subject /
different-author completions.

Terminal states (`process/status.go`, `TerminalStatus`): `completed` (success), `failed` (errored),
`cancelled` (operator-stopped), `orphaned` (runtime died). All four are valid keys; unlisted ones
do nothing.

## Design

The hook lives on the trigger `Action` as `On map[process.Status]*Action`, a **sibling of `Spawn`**
— NOT inside `launch.Spec`, which would create a `launch`→`trigger` import cycle (hook values are
`trigger.Action`s). `On` runs when the Action's own `Spawn` process reaches that terminal status.

Per-spawn scoping comes for free: the engine already tracks every spawned process in the in-memory
`live` registry (`flowProc`, keyed by process id, carrying the subject). Storing the hooks **with
the flowProc** means completion is matched to the exact process this trigger spawned — no marker on
`process:instance`, no subject-pattern re-derivation.

### Load-bearing runtime fact

`versed` **always** runs `Engine.ObserveScoped` (versed/server.go:303,321), never `Engine.Start`.
So the store-driven `reap`/`dispatch` loop is dormant in production — it runs only in unit tests
that call `Engine.Start`. In scoped mode the only completion signal today is `reapOnDone`
(engine.go:837), which is **racy for a fast process** (bus `Subscribe` is live-from-now, no replay).
A leaked `live` entry is cosmetic today, but a **missed completion hook would stall the chain** —
so the hook must ride a race-free, store-driven signal.

## Implementation

### 1. Data model — `trigger/trigger.go`, `trigger/engine.go`

- `Action` (trigger.go:167): add `On map[process.Status]*Action \`json:"on,omitempty"\``.
  `process.Status` is `type Status string`, so this marshals with string keys natively — no custom
  map codec.
- `flowProc` (engine.go:~31): add `on map[process.Status]*Action` (hooks for this process; nil for
  `Adopt`) and `chain string` (chain name at this level, for deterministic nested spawnIDs).

### 2. Race-free application path — `trigger/engine.go`

Add a dedicated, always-on `process:instance` subtree watcher in `ObserveScoped`, mirroring the
existing `watchTriggerSubtree` goroutine (engine.go:150-191). It is opened **before any spawn** and
`WatchSubtree` replays current entities then edges-on-change, so it is race-free for fast processes;
old terminal entities on replay are harmless because the `live` gate rejects untracked ids. This
also makes scoped-mode reaping race-free (a latent bug fixed in passing).

- `ObserveScoped` (engine.go:128): after installing trigger watches, if the store is an
  `entity.SubtreeWatcher`, `WatchSubtree(mirror.System, "instance")` and `go e.reapLoop(ctx, ch, unsub)`.
- `reapLoop`: on each event, read `status := ev.To["status"]`; if `process.TerminalStatus(status)`
  and `fp, ok := e.retire(ev.Ref.ID); ok` → `e.applyHook(fp, status, ev)`.

**Once-guard** (whichever of reapLoop / reap / reapOnDone wins the atomic delete applies the hook):
```go
func (e *Engine) retire(id string) (flowProc, bool)                       // lock; delete; return fp,ok
func (e *Engine) retireHandle(id string, h process.ProcHandle) (flowProc, bool) // delete iff same handle
```

**Apply**:
```go
func (e *Engine) applyHook(fp flowProc, status process.Status, ev entity.Event) {
    hook := fp.on[status]; if hook == nil { return }
    subj, err := entity.ParseRef(fp.subject); if err != nil { return }
    hook.apply(fp.chain+":"+string(status), ev, []entity.Ref{subj}, e) // recurses via apply→spawnHooked
}
```

Wire the two existing paths through the guard too:
- `reap(ev)` (engine.go:952, test-only Start mode): after the terminal check →
  `if fp, ok := e.retire(ev.Ref.ID); ok { e.applyHook(fp, process.Status(status), ev) }`.
- `reapOnDone(fp)` (engine.go:837, backstop): replace the bare delete with
  `if got, ok := e.retireHandle(fp.id, fp.h); ok { e.applyHook(got, process.Status(ev.Text), e.procEvent(fp.id)) }`,
  where `procEvent(id)` synthesizes an event by reading the mirror entity
  (`e.store.Get(mirror.Ref(id))` → `ev.To = payload`) so hook bindings still see `$status`/`$flow`/
  any `$result`. (Bonus: the hook can lift the completed process's payload onto the subject.)

### 3. Thread hooks through spawn — `trigger/engine.go`, `trigger/trigger.go`

Keep the public `Spawn(id, spec)` signature stable (external + test callers unchanged); delegate to
a private core:
```go
func (e *Engine) Spawn(id string, spec launch.Spec) process.ProcHandle { return e.spawnHooked(id, spec, nil, "") }
func (e *Engine) spawnHooked(id string, spec launch.Spec, on map[process.Status]*Action, chain string) process.ProcHandle
```
`spawnHooked` tracks `flowProc{..., on, chain}` when `spec.Subject != ""`. `Adopt` (engine.go:822)
passes `on:nil, chain:""` (resume limitation — see Risks). `apply`'s spawn call (trigger.go:210)
becomes `e.spawnHooked(spawnID(name, subj), spec, a.On, name)`.

### 4. spawnID under recursion — deterministic, collision-free

Child chain name = `parentChain + ":" + status`. `safeSeg` (trigger_api.go:102) does **not** rewrite
`:`, so no hash suffix and ids stay deterministic per (chain, subject):

| level | chain | spawnID for `demo:pr:1` |
|---|---|---|
| top `review` | `review` | `review-demo:pr:1` |
| `on.completed`→merge | `review:completed` | `review:completed-demo:pr:1` |
| nested `on.completed`→approve | `review:completed:completed` | `review:completed:completed-demo:pr:1` |
| `on.failed` sibling | `review:failed` | `review:failed-demo:pr:1` |

Deterministic → idempotent re-fire (Spawn dedups by id).

### 5. Validation — `trigger/trigger.go`, `trigger/trigger_api.go`

Add `func (a Action) validate() error`, called from `AddTrigger` (trigger_api.go, after the
condition/gate checks) as `t.Action.validate()`. Strict, install-time (mirrors `Condition.validate`):
- `len(a.On) > 0 && a.Spawn == nil` → error (`action.on requires a spawn`).
- per `status, sub := range a.On`: `!status.Terminal()` → error (rejects `running`/`unclaimed`/
  garbage); `sub == nil` → error; recurse `sub.validate()`.

Goal-owned triggers install through `AddTrigger` (goal.go), so they are covered.

### 6. `onDone` sugar — `trigger/trigger.go`

Pure parse-time sugar via a custom `Action.UnmarshalJSON`: unmarshal into an alias plus an
`onDone *Action` field; fold `onDone` into `On[process.Completed]`; error if both `onDone` and
`on.completed` are set. No custom `MarshalJSON` — canonical output is `on.completed`. Adds
`encoding/json` import to trigger.go.

### 7. Docs — `docs/trigger-model.md`, `docs/tutorial.md`

- **trigger-model.md**: add an Action-grammar note for `on` (status-keyed completion hooks; four
  terminal keys; recursive; rides the live registry for per-spawn scoping; `onDone`→`on.completed`).
  In "The two demo rules, before and after" (~232-261), add the collapsed single-trigger form and
  keep the decoupled variant with a "when to prefer it" note (fan-in / cross-subject / different author).
- **tutorial.md**: replace the two-trigger `charter.json` (~98-118) with one trigger whose
  `action.on.completed` sets `reviewed` (keep the same `echo…; sleep 1` task and `ask` gate so the
  walkthrough up to the gate resolve is unchanged). Revise the "nobody wrote reviewing→reviewed"
  prose (~158-165): the second transition now comes from the same trigger's `on.completed`. Add a
  short callout that the emergent two-trigger pattern is still valid and when to reach for it.

Collapsed charter (target):
```json
{ "name": "review",
  "condition": { "when": { "system": "demo", "fields": { "stage": "open" } } },
  "gates":     [ { "name": "review", "mode": "ask" } ],
  "action": {
    "set":   { "stage": "reviewing" },
    "spawn": { "driver": "scripted", "task": "echo reviewing $subject; sleep 1" },
    "on":    { "completed": { "set": { "stage": "reviewed" } } } } }
```

## Critical files

- `trigger/trigger.go` — `Action.On`; `apply` spawn call; `Action.validate`; `Action.UnmarshalJSON`.
- `trigger/engine.go` — `flowProc` fields; `spawnHooked`; `retire`/`retireHandle`/`applyHook`/
  `procEvent`/`reapLoop`; `ObserveScoped` watcher; wire `reap` + `reapOnDone`.
- `trigger/trigger_api.go` — `AddTrigger` calls `Action.validate` (`spawnID` unchanged, verified).
- `docs/tutorial.md`, `docs/trigger-model.md` — collapse to `on.completed`; document the grammar.

## Tests — `trigger/onhook_test.go` (MemStore + custom Spawner + `ObserveScoped`)

1. Hook fires on completion (one trigger: review→reviewed).
2. Recursion (review→merge→approve; assert distinct spawnIDs).
3. `on.failed` applies on a driver error; `on.completed` does not.
4. Per-process scoping (two spawns, same subject, different hooks — each applies to its own process).
5. Fast-process race: an immediate driver under `ObserveScoped` — store watcher still applies (race-free); plus a slow driver for the backstop path.
6. Once-only (duplicate/replayed terminal delivery, or Start's reap + reapOnDone — effect applied once).
7. Validation errors: on-without-spawn; non-terminal key (`on.running`); nested invalid hook.
8. `onDone` sugar unmarshals to `on.completed`; both-set errors; round-trips to `on.completed`.
9. `Adopt` drops hooks (guards the documented resume limitation).

Isolated stores only (`entity.NewMemStore()` / spare-port docd). **Never** the dev server on `:9797`.

## Verification

- `go build ./...`; `go test -race ./trigger/...` (retire/reapLoop/reapOnDone are concurrency-sensitive).
- `make smoke` (isolated in-memory server on a free port) — no core regression.
- End-to-end on an isolated server (`--store memory`, spare port): install the collapsed charter,
  `verse entity put demo:pr:1 stage=open`, `verse gate resolve …`, `verse watch demo:pr:1` → observe
  `reviewing` then `reviewed` driven by the single `review` trigger. Add a nested `on.completed`
  (merge) and confirm the chain advances through `merged`.

## Known limitations / risks

- **Resume drops hooks.** `reconcileProcesses`→`Adopt` re-tracks with `on:nil`, so a process that
  completes after a restart fires no hook. Future mitigation (out of scope): persist hooks on the
  `process:instance` entity at spawn and rehydrate on reconcile.
- **`on.orphaned` is effectively inert** until the above lands — `orphaned` is written only by
  startup reconcile on a fresh engine (empty `live` / nil-hook Adopt), so no tracked flowProc exists
  within the spawning run. `on.completed` / `on.failed` / `on.cancelled` all work within a run.
- **Non-`SubtreeWatcher` store in scoped mode** degrades to the racy `reapOnDone` backstop —
  acceptable; matches today's behavior. MemStore and LogdStore both implement it.
- **No track, no hook**: if the Spawner returns nil or provider selection doesn't resolve
  (trigger.go:204 `continue`), nothing is tracked and the hook never fires — correct (no process),
  worth a one-line doc note.
