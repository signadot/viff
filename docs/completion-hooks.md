# Plan: `onX` completion hooks — visible process chaining

## Context

Today a trigger's `Action` can `set` fields and/or `spawn` a process, but **what happens when
that process finishes is invisible in the charter**. The tutorial teaches the completion step as
a *second, decoupled* trigger that matches `process:instance status=done` and pattern-matches the
subject via the process's `flow`. That emergent two-trigger pattern is powerful, but for a plain
linear chain (review → reviewed → merge → …) it scatters one intent across separate rules and
forces the author to reason about the reflected `process:instance` namespace.

Goal: let a `spawn` declare, inline, what to do when it completes — so a linear chain reads top-to-
bottom in one trigger. The shape is **one named field per terminal status** on the `Action`:
`onDone` (success), `onFailed`, `onCancelled`, `onOrphaned`. Each is itself a full `Action`
(`set` + `spawn` + its own `onX`), so chains nest; `onDone` is the common success hook. The
decoupled two-trigger pattern still works and remains preferred for fan-in / cross-subject /
different-author completions.

Terminal states (`process/status.go`, `TerminalStatus`): `done` (success), `failed` (errored),
`cancelled` (operator-stopped), `orphaned` (runtime died).

### Companion rename: `Completed` → `Done`

So the status the success hook reacts to lines up with the hook's name (`onDone` ↔ status `done`),
rename the terminal success status:
- Go identifier `process.Completed` → `process.Done`; the `Terminal()` switch case; doc comments
  (`process/status.go`, `process/loop.go`).
- Status **value** `"completed"` → `"done"` — the string executors report and completion triggers
  match. Update every `status=completed` matcher and literal: `cmd/verse/process.go` (report +
  the terminal-status `case`), the `status: "completed"` test literals across
  `trigger/{condition,fanout,flow,flowquery,spawn}_test.go`, `broker/broker_test.go`,
  `controlpolicy/http_test.go`, `process/tony_test.go`, and the docs
  (`docs/{tutorial,trigger-model,external-processes,control-policy}.md`).

Treated as a clean internal rename — the wire/stored-value break (external executors reporting the
old value, persisted `process:instance` entities) is **out of scope at this stage**.

## Design

The hooks live on the trigger `Action` as four `*Action` fields — **NOT** inside `launch.Spec`,
which would create a `launch`→`trigger` import cycle. `onX` runs when the Action's own `Spawn`
process reaches that terminal status.

```go
//tony:schemagen=verse-action,notag
type Action struct {
	Set         map[string]any `json:"set,omitempty"         tony:"field=set,omitzero"`
	Spawn       *launch.Spec   `json:"spawn,omitempty"       tony:"field=spawn,omitzero"`
	OnDone      *Action        `json:"onDone,omitempty"      tony:"field=onDone,omitzero"`      // status done
	OnFailed    *Action        `json:"onFailed,omitempty"    tony:"field=onFailed,omitzero"`
	OnCancelled *Action        `json:"onCancelled,omitempty" tony:"field=onCancelled,omitzero"`
	OnOrphaned  *Action        `json:"onOrphaned,omitempty"  tony:"field=onOrphaned,omitzero"`
}
```

**Why named fields, not `on map[status]*Action`.** The terminal set is closed and tiny, and each
`*Action` field is structurally identical to the existing `Spawn *launch.Spec`, which tony-codegen
already handles. This sidesteps everything a map would cost under tony:
- A `map[process.Status]*Action` (typed string key) **won't compile** — tony-codegen hardcodes
  `map[string]…` keys with no key conversion; `map[string]*Action` would work but is opaque in the
  schema and loses the status typing.
- `onDone` is a **first-class field**, so there is no sugar to fold — no custom `UnmarshalJSON`
  (dead code anyway: charters decode via `codec`→`gomap.FromTonyIR` for every format, never
  `encoding/json`) and no `codec=custom` hand-written codec. The struct stays fully generated.
- Validation shrinks: an author literally cannot write a non-terminal hook (there is no such field).
- The schema (`schema_gen.tony`) lists the four hooks explicitly.

Trade: a new terminal status would need a new field + regen (a stable four-member enum doesn't), and
a misspelled `onDoen` is silently dropped like any unknown field (consistent with the rest of the
model).

Per-spawn scoping comes for free: the engine already tracks every spawned process in the in-memory
`live` registry (`flowProc`, keyed by process id, carrying the subject). Storing the hooks **with
the flowProc** means completion is matched to the exact process this trigger spawned — no marker on
`process:instance`, no subject-pattern re-derivation.

### Load-bearing runtime fact

`versed` **always** runs `Engine.ObserveScoped` (versed/server.go:303,321), never `Engine.Start`.
So the store-driven `reap`/`dispatch` loop is dormant in production — it runs only in unit tests
that call `Engine.Start`. In scoped mode the only completion signal today is `reapOnDone`
(engine.go:849), which is **racy for a fast process** (bus `Subscribe` is live-from-now, no replay).
A leaked `live` entry is cosmetic today, but a **missed completion hook would stall the chain** —
so the hook must ride a race-free, store-driven signal.

## Implementation

### 1. Data model — `trigger/trigger.go`, `trigger/engine.go`

- `Action` (trigger.go:174): add the four `OnX *Action` fields above, then **regenerate**
  `trigger/trigger_gen.go` + `trigger/schema_gen.tony` via tony-codegen (fully generated — no custom
  codec). Add a small helper:
  ```go
  func (a *Action) hookFor(s process.Status) *Action {
      switch s {
      case process.Done:      return a.OnDone
      case process.Failed:    return a.OnFailed
      case process.Cancelled: return a.OnCancelled
      case process.Orphaned:  return a.OnOrphaned
      }
      return nil
  }
  ```
- `flowProc` (engine.go:33): add `hooks *Action` (the spawning action; nil for `Adopt`) and
  `chain string` (chain name at this level, for deterministic nested spawnIDs).

### 2. Race-free application path — `trigger/engine.go`

Add a dedicated, always-on `process:instance` subtree watcher in `ObserveScoped`, mirroring the
existing `watchTriggerSubtree` goroutine (engine.go:152-191). It is opened **before any spawn** and
`WatchSubtree` replays current entities then edges-on-change, so it is race-free for fast processes;
old terminal entities on replay are harmless because the `live` gate rejects untracked ids. This
also makes scoped-mode reaping race-free (a latent bug fixed in passing).

- `ObserveScoped` (engine.go:130): after installing trigger watches, if the store is an
  `entity.SubtreeWatcher`, `WatchSubtree(mirror.System, "instance")` and `go e.reapLoop(ctx, ch, unsub)`.
- `reapLoop`: on each event, read `status := ev.To["status"]`; if `process.TerminalStatus(status)`
  and `fp, ok := e.retire(ev.Ref.ID); ok` → `e.applyHook(fp, process.Status(status), ev)`.

**Once-guard** (whichever of reapLoop / reap / reapOnDone wins the atomic delete applies the hook):
```go
func (e *Engine) retire(id string) (flowProc, bool)                       // lock; delete; return fp,ok
func (e *Engine) retireHandle(id string, h process.ProcHandle) (flowProc, bool) // delete iff same handle
```

**Apply**:
```go
func (e *Engine) applyHook(fp flowProc, status process.Status, ev entity.Event) {
    if fp.hooks == nil { return }
    hook := fp.hooks.hookFor(status); if hook == nil { return }
    subj, err := entity.ParseRef(fp.subject); if err != nil { return }
    // as landed; the chain is now a kpath and descends by hook FIELD — see §4 and
    // docs/flow-shape-kpath-plan.md: hook.apply(fp.chain+"."+hookField(status), …)
    hook.apply(fp.chain+":"+string(status), ev, []entity.Ref{subj}, e) // recurses via apply→spawnHooked
}
```

Wire the two existing paths through the guard too:
- `reap(ev)` (engine.go:966, test-only Start mode): after the terminal check →
  `if fp, ok := e.retire(ev.Ref.ID); ok { e.applyHook(fp, process.Status(status), ev) }`.
- `reapOnDone(fp)` (engine.go:849, backstop): replace the bare delete with
  `if got, ok := e.retireHandle(fp.id, fp.h); ok { e.applyHook(got, process.Status(ev.Text), e.procEvent(fp.id)) }`,
  where `procEvent(id)` synthesizes an event by reading the mirror entity
  (`e.store.Get(mirror.Ref(id))` → `ev.To = payload`) so hook bindings still see `$status`/`$flow`/
  any `$result`. (Bonus: the hook can lift the completed process's payload onto the subject.)

### 3. Thread hooks through spawn — `trigger/engine.go`, `trigger/trigger.go`

Keep the public `Spawn(id, spec)` signature stable (external + test callers unchanged); delegate to
a private core:
```go
func (e *Engine) Spawn(id string, spec launch.Spec) process.ProcHandle { return e.spawnHooked(id, spec, nil, "") }
func (e *Engine) spawnHooked(id string, spec launch.Spec, hooks *Action, chain string) process.ProcHandle
```
`spawnHooked` tracks `flowProc{..., hooks, chain}` when `spec.Subject != ""`. `Adopt` (engine.go:834)
passes `hooks:nil, chain:""` (resume limitation — see Risks). `apply`'s spawn call (trigger.go:218)
passes the spawning action as the hooks carrier: `e.spawnHooked(spawnID(name, subj), spec, &a, name)`.

### 4. spawnID under recursion — deterministic, collision-free

*(Landed as described, then reshaped: the chain is now a **kpath** and the id joins it to the
subject with `@`. See docs/flow-shape-kpath-plan.md for why — the `-` join could not be decoded
back, since trigger names spend `-` too.)*

Child chain = `parentChain + "." + hookField(status)` — the hook's FIELD name, so the chain is
the address of the action inside the trigger rather than a private scheme:

| level | chain | spawnID for `demo:pr:1` |
|---|---|---|
| top `review` | `review` | `review@demo:pr:1` |
| `onDone`→merge | `review.onDone` | `review.onDone@demo:pr:1` |
| nested `onDone`→approve | `review.onDone.onDone` | `review.onDone.onDone@demo:pr:1` |
| `onFailed` sibling | `review.onFailed` | `review.onFailed@demo:pr:1` |

Deterministic → idempotent re-fire (Spawn dedups by id). The chain is also published on the
process entity (`chain`, plus its root as `trigger`), so "every process of this trigger" is a
payload predicate rather than a glob over ids.

### 5. Validation — `trigger/trigger.go`, `trigger/trigger_api.go`

Add `func (a Action) validate() error`, called from `AddTrigger` (trigger_api.go, after the
condition/gate checks) as `t.Action.validate()`. Strict, install-time (mirrors `Condition.validate`):
- any `onX` set while `a.Spawn == nil` → error (`onX requires a spawn — a completion hook needs a
  process to complete`).
- recurse into each non-nil `onX` (`sub.validate()`).

No terminal-key checking is needed — the fields *are* the four terminal statuses. Goal-owned triggers
install through `AddTrigger` (goal.go), so they are covered.

### 6. Docs — `docs/trigger-model.md`, `docs/tutorial.md`

- **trigger-model.md**: add an Action-grammar note for the `onX` fields (four terminal hooks;
  recursive; each a full Action; rides the live registry for per-spawn scoping). In "The two demo
  rules, before and after", add the collapsed single-trigger form and keep the decoupled variant with
  a "when to prefer it" note (fan-in / cross-subject / different author).
- **tutorial.md**: replace the two-trigger charter with one trigger whose `action.onDone` sets
  `reviewed` (keep the same task and `ask` gate so the walkthrough up to the gate resolve is
  unchanged). Revise the "nobody wrote reviewing→reviewed" prose: the second transition now comes
  from the same trigger's `onDone`. Add a short callout that the emergent two-trigger pattern is
  still valid and when to reach for it. Show charters in **tony** (the baseline; JSON/YAML still parse).

Collapsed charter (target, tony):
```tony
name: review
condition:
  when: { system: demo, fields: { stage: open } }
gates: [ { name: review, mode: ask } ]
action:
  set: { stage: reviewing }
  spawn: { driver: scripted, task: "echo reviewing $subject; sleep 1" }
  onDone:
    set: { stage: reviewed }
```

## Critical files

- `trigger/trigger.go` — the four `OnX` fields; `hookFor`; `apply` spawn call; `Action.validate`.
- `trigger/trigger_gen.go` + `trigger/schema_gen.tony` — **regenerated** after the struct change.
- `trigger/engine.go` — `flowProc` fields; `spawnHooked`; `retire`/`retireHandle`/`applyHook`/
  `procEvent`/`reapLoop`; `ObserveScoped` watcher; wire `reap` + `reapOnDone`.
- `trigger/trigger_api.go` — `AddTrigger` calls `Action.validate`.
- `process/status.go`, `process/loop.go`, `cmd/verse/process.go` — `Completed`→`Done` / `"completed"`→`"done"`.
- `docs/tutorial.md`, `docs/trigger-model.md` — collapse to `onDone`; document the grammar.

## Tests — `trigger/onhook_test.go` (MemStore + custom Spawner + `ObserveScoped`)

1. `onDone` fires on completion (one trigger: review→reviewed).
2. Recursion (review→merge→approve; assert distinct spawnIDs `review.onDone…`).
3. `onFailed` applies on a driver error; `onDone` does not.
4. Per-process scoping (two spawns, same subject, different hooks — each applies to its own process).
5. Fast-process race: an immediate driver under `ObserveScoped` — store watcher still applies (race-free); plus a slow driver for the backstop path.
6. Once-only (duplicate/replayed terminal delivery, or Start's reap + reapOnDone — effect applied once).
7. Validation errors: `onX`-without-spawn; nested invalid hook.
8. Codec round-trip: a charter with `onDone`/`onFailed` in tony and json decodes to the same Action and re-encodes canonically.
9. `Adopt` drops hooks (guards the documented resume limitation).

Isolated stores only (`entity.NewMemStore()` / spare-port docd). **Never** the dev server on `:9797`.

## Verification

- `go build ./...`; `go test -race ./...` (the rename touches several packages; retire/reapLoop/
  reapOnDone are concurrency-sensitive).
- `make smoke` (isolated in-memory server on a free port) — no core regression.
- End-to-end on an isolated server (`--store memory`, spare port): install the collapsed charter,
  `verse entity put demo:pr:1 stage=open`, `verse gate resolve …`, `verse watch demo:pr:1` → observe
  `reviewing` then `reviewed` driven by the single `review` trigger. Add a nested `onDone` (merge)
  and confirm the chain advances through `merged`.

## Known limitations / risks

- ~~**Resume drops hooks.**~~ **Fixed**, and not the way this predicted. It guessed at persisting
  the hooks themselves; what landed instead persists the process's CHAIN — the kpath of the action
  that spawned it — and `AdoptChain` navigates the installed charter to find the action again. The
  hooks were never the thing to store, only the address. See docs/flow-shape-kpath-plan.md.
- **`onOrphaned` is still inert, for a different reason now.** Rehydration did not fix it, because
  orphaning and adopting are the two ALTERNATIVES in reconcile: a process that can be re-attached is
  adopted (and never marked orphaned), and one that cannot is orphaned (and never tracked). So no
  tracked process is ever marked `orphaned`, whatever its hooks. Making it fire would mean tracking a
  process whose runtime is gone — i.e. with no handle — which is a different change. `onDone` /
  `onFailed` / `onCancelled` all work within a run.
- **Non-`SubtreeWatcher` store in scoped mode** degrades to the racy `reapOnDone` backstop —
  acceptable; matches today's behavior. MemStore and LogdStore both implement it.
- **No track, no hook**: if the Spawner returns nil or provider selection doesn't resolve
  (trigger.go, the `continue`), nothing is tracked and the hook never fires — correct (no process),
  worth a one-line doc note.
