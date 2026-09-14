# Scopes own what they create: the control plane under one model

Status: plan, nothing built. Branch `flow-steer` off main (ead39d3d).
Closes git-issue h7wwzxtnh12ksc4yh5n0 (flow steering ignores `--scope`); lands the same model on every control route.

## Context

A scope has two halves and the code treats only one of them correctly.

**World half (logd's).** `LogdStore.Scope` is a copy-on-write child: a scoped write lands in the
overlay, a scoped read sees the overlay on baseline. Correct and complete.

**Program half (verse's).** Charter rules, tasks, gates and flows are records in the store AND
live things: a rule is a watch, a task is a handle, a gate is an awaiter, a flow is the set of
handles about a subject. A scope's engine holds only what it created — `LoadCharter` restores
its own rules by the `scope` stamp (`trigger/charter.go:126 ownRecord`) and its live map has only
what it spawned. Everything else it sees through the overlay **by reference**: the record is
readable, the live thing belongs to baseline's engine. And every scope engine is built on the
SAME registry and the same taskd as baseline (`trigger.New(scoped, s.Reg)`, `versed/server.go:922`).

So a scoped write to an inherited program object cannot be a copy-on-write write. Today each
control route does a different wrong thing:

| route | with `?scope=me` on a baseline object |
|---|---|
| `POST /flow/{pause,resume,cancel,say}` (`server.go:4645`) | scope not read; `s.Eng` steered |
| `POST /tasks/{id}/{cancel,say}`, `PUT /tasks/{id}/vector` (`taskControl`, `server.go:4501`) | scope used for the perms check only; handle found by id via taskd/attach → baseline's task signalled |
| `POST /gate/resolve`, `/gate/escalate`, `POST /tasks/{id}/resolve` (`answerQuestion`, `server.go:2455`; `settle`, `:2603`) | **split**: `s.Reg.Answer` delivers to baseline's awaiter, the answer record is written through the scoped store into the overlay; baseline's record stays `pending` |
| `DELETE /charter/{name}`, `DELETE /charter?path=` (`charter.go:338`, `:886`) | scope engine does not hold the rule → 404 "not found", while `verse data get` in the scope finds it; the CLI (`cmd/verse/charter.go:570`) and a test comment describe a tombstone that is never written |
| `POST /entity`, `DELETE /entity` | already refused for `verse.*` by `reservedWrite` (`server.go:1843`), scope or not — nothing to do |

**The rule.** A control-plane write may name a scope only for an object that scope created. Named
for an inherited object it is REFUSED with a message saying which plane owns it, so the caller
re-addresses (drops `--scope`, or names the scope's own object). Nothing passes through, nothing
is copied. Symmetrically, baseline acting on a scope's object is refused with a pointer to the
scope when the server can tell (taskd knows `Launch.Scope`), and not-found otherwise (baseline
cannot see the overlay record). Creation in a scope (`charter -a --scope`, `task run --scope`)
stays as it is: that is what "created within the scope" means.

**Perms are the third kind, and they take the world's rule, not the program's.** A grant is
live too — the mirror (`entity.Track`) is what every check reads — and a scope reads it by
reference: a scope is governed by baseline's grants, and opening one is not a way out of them.
But a grant is not subject to control. Nothing waits on it, nothing runs it; the act on it is an
ordinary write, and the mirror is a cache of records, which can be layered exactly as the records
are. The discriminator between the two rules is whether the object has a live half that cannot be
copied: an awaiter, a handle, a watch — yes; a perms record — no. So a scoped grant is
copy-on-write like any world write: it lands in the overlay, governs that scope's checks layered
on baseline's, and never governs baseline.

Today it does neither. One `Attribution` is shared process-wide: `s.Store` is attributed at
construction (`server.go:197`), `attributed.Scope` re-wraps with the same `attr`
(`entity/attribution.go:243`) and `As` re-derives sharing it (`:113`). The mirror is loaded from
and watched on BASELINE (`Track(ctx, s.Store)`, `server.go` Start), and `loaded` means a key not
in it is absent with no read (`recordAt`, `:903`). Two consequences:
- a scoped `PUT /perms` — and the owner stamp on EVERY entity a scoped write creates
  (`a.record`, `:236`) — lands in the overlay and is read back through the overlay into the
  shared mirror by `invalidate` (`:934`), so it governs baseline's checks in this process until a
  baseline event on that key or a restart repairs it;
- after a restart the scope's grants and owner records are not in the mirror at all, so a
  scope's own creations answer as nobody's.
No test covers scoped perms.

**Ownership is decidable from what exists**, no new state: a rule by the engine that `Knows` it;
a task or question by which store holds its record (the record is written into the spawning
store and "the store it lands in already says which federation it belongs to", `task/vector.go:80`);
a flow by which engine's live map holds handles about the subject. Packages are baseline's plane
(`restorePackages`), not scopes.

## Plan

### 1. The model, written down (docs first, own commit)

- `docs/reference/connect.md`, after "The charter a scope runs": a section "What a scope owns".
  World by copy; program by reference; a scope's engine holds only what it created; the refusal
  rule and its one-line rationale (the live thing and the record would otherwise part ways).
  Remove the tombstone claim; say a shadow is an install under the inherited name.
- `docs/deployment-and-steering.md`, under "Where it is enforced": control is per plane —
  a scoped control write reaches only what that scope created, and says so when it cannot.
- `docs/task-control.md` / `docs/reference/tasks.md`: one sentence each where `--scope` on
  cancel/say/vector/answer could be read as "in the overlay".
- `docs/perms.md`: a "Scopes" paragraph — a scope is governed by baseline's grants; a grant made
  in a scope is copy-on-write, governs the scope's writes layered on baseline's, never baseline's;
  why perms take the world's rule while gates take the program's (no live half to copy).

### 2. One ownership answer on the server (`versed/plane.go`, new)

```go
type plane int // planeNone, planeBaseline, planeScope
// planeOf says which plane holds a program record relative to the request's scope:
// baseline's store has it → baseline (packages included); else the scope's overlay has it → scope.
func (s *Server) planeOf(scope string, ref entity.Ref) (plane, error)
// notThisPlane writes the refusal: "<ref> is baseline's; scope me did not create it —
// act on it without --scope" / "<ref> is scope me's — say --scope me". Status 409.
func notThisPlane(w, ref, have, want) 
```

- An UNREADABLE record refuses the write (`unreadable(w, r, err)`), never falls through to
  "not found ⇒ baseline's". One or two reads per control write; control writes are human-rate.
- Rules use `Engine.Knows` on `engFor(scope)` vs `s.Eng`/`packageEngines()` rather than the store.
- Flows use the engine set: `flowEngines(scope)` = `engFor(scope)` alone, or baseline +
  `packageEngines()` for `""`. `flowSubjects` is rewritten on top of it, so listing and steering
  select engines through the same function.

### 3. Flow steering (closes h7wwzxtn) — `versed/server.go` flowPause/Resume/Cancel/Say

One `steer(w, r, verb, func(*trigger.Engine, trigger.Match) int)`:
- decode body via `flowAddress`; `scope := r.URL.Query().Get("scope")`.
- no scope: sum the verb over `flowEngines("")`. `Affected` is already a task count, so a subject
  busy in baseline and a package sums correctly.
- scope: the scope engine's own handles only. If it holds NONE for the subject and
  `flowEngines("")` holds some → `notThisPlane` (409). A subject busy in both planes steers the
  scope's own and reports that count (that write is legitimately the scope's).
- an unopened scope: no engine, `affected: 0` (never create an engine on a steer).

### 4. Task control by id — `taskControl` (`server.go:4501`)

Before `SnapshotOf`/`attach`: `planeOf(scope, mirror.Ref(id))`; refuse on mismatch. For a
local task, taskd can also say the launch scope — add `Daemon.ScopeOf(id) (string, bool)`
(`task/taskd/daemon.go`, it already threads `l.Scope` to `onDrop`) so baseline refusing a
scope's task can name the scope instead of 404.

### 5. Gates — `answerQuestion` (`server.go:2455`) and `escalateGate` (`:2399`)

After the `by/choice` validation and before `entity.Check`: `planeOf(scope, mirror.QuestionRef(id))`;
refuse on mismatch. Fixes the split (delivery to baseline's awaiter + record in the overlay).
`/tasks/{id}/resolve` goes through the same function, so it is covered. `settleByDeadline` and
the docket's own settles run with the store of the question's plane already (`storeForScope`),
untouched.

### 6. Charter removal in a scope — `deleteCharterItem`, `deleteCharterAt` (`charter.go`)

When the scope's engine does not hold the name but `s.Eng` (or a package engine) `Knows` it:
refuse naming baseline instead of 404. Shadowing stays what it already is: install your own under
the name. `cmd/verse/charter.go:570` message: "removed the rule X in scope me" (drop the
tombstone sentence). `versed/scopecharter_test.go:144` comment: state the shadow as the install.

### 6b. Perms: a mirror per scope, layered as the store is (`entity/attribution.go`)

- `Attribution` gains `children map[string]*Attribution` (guarded by `mu`); `attributed.Scope(name)`
  hands back a view over `a.Store.Scope(name)` with the CHILD attribution for that name, created
  on first use, sharing `rootDefault` and `groups` with the parent. `As` keeps re-deriving off
  whatever attr the store carries, so per-request re-wrapping stays free.
- `Track` per scope: a child is loaded from and watched on the SCOPED store — logd answers both
  overlaid (`LogdStore.Scope` doc: reads/observe see the scope on baseline) — so it holds
  baseline's grants and its own with no parent fallback, and baseline's mirror never sees the
  overlay. Triggered from `scopeEngine` alongside `restoreScopeCharter` (one dev connecting pays
  one list + one watch), and on a scoped request before the engine exists via the same
  `sync.Once`; torn down in `deleteScope` with the engine. A child whose load did not happen
  refuses checks as unread (`Unread`), the way baseline's does — never falls open.
- `invalidate` and `apply` act on the attribution the view carries, so a scoped write repairs the
  scope's mirror and nobody else's.
- Order in every control route: plane check (§2) first, then `entity.Check`: "not yours to
  address" precedes "not allowed".

### 7. Tests (`versed/`)

- `flows_test.go`: give `idleHandle` per-verb counters. New
  `TestFlowSteeringActsOnItsPlane`: package task → each verb reaches 1 and the package's handle
  counts it; baseline adopts the same subject → 2; `?scope=me` with only baseline busy → 409,
  no handle touched; the scope's own adopted task → 1 and only it counted; `?scope=nobody` → 0.
- new `scopedcontrol_test.go` (helpers exist: `spawnScoped`, `questionsIn`, `firingQuestions`
  in `scopedgate_test.go`; `addScopedRule` in `scopecharter_test.go`):
  - task cancel/say/vector in scope on a baseline task → 409, handle untouched; on the scope's
    own → reaches. Baseline on a scope's local task → 409 naming the scope.
  - gate resolve in scope on a baseline question → 409; baseline record still `pending`, awaiter
    unanswered. Scope's own question → answered, record in the scope's store only.
  - charter remove in scope of an inherited rule → 409 naming baseline, baseline still holds it;
    install under the same name still shadows (existing restart test stays green).
- `schema_test.go` route table: unchanged unless a refusal body type is added (it is not: plain
  `http.Error` text, as the other refusals).
- new `scopedperms_test.go` (logd-backed, `entitytest.StoreAt`): a grant made `?scope=me` governs
  a scoped write and NOT a baseline one to the same address, in-process (fails today: the shared
  mirror admits it); a baseline grant governs the scope's write (read by reference); an entity
  created in the scope answers as its creator's in the scope after a restart (fails today:
  nobody's); a baseline write to the same key after a scoped grant leaves the scope's grant
  standing. `entity/attribution_test.go`: `Scope` yields a distinct attribution per name and the
  same one twice.

### 8. Commits, by path, in this order (each `Tier: change`)

1. docs: the model (§1).
2. plane helper + flow steering + flows test (§2, §3, §7a). Links h7wwzxtn.
3. task control + gates + tests (§4, §5, §7b).
4. charter removal refusal + CLI message + test comment (§6, §7b last case). Carries a `Notice:`
   line: `--scope` control writes on inherited objects now refuse instead of acting on baseline.
5. perms mirror per scope + scoped perms tests + `docs/perms.md` paragraph (§6b, §7c). `Notice:`
   a grant made with `--scope` no longer reaches baseline's checks.

Then `git issue` link the commit and close h7wwzxtn with a note pointing at the model section.

## Verification

- Edit loop: `go test ./versed -run 'TestFlow|TestScoped|TestAScope'`; for §6b also
  `go test ./entity -run 'TestScope|TestAttribut'`.
- Before each commit: `make check && go test $(go list ./... | grep -v /scratchpad)`; then
  `./smoke.sh` on its own.
- Each new test must fail on main for the stated reason (steer answers 0 / answer lands in the
  overlay / delete answers 404) — check by running it against `git stash`-free main worktree or
  by reading the pre-change assertion.
- Local poke (optional): `VERSE_CONFIG=~/.config/verse-local.tony verse flow cancel --scope me demo.pr.1`
  prints the refusal naming baseline.

## Decisions taken (say otherwise)

- Refusal status is **409 Conflict** with a text body, not 403: it is "wrong plane", not
  "not allowed". `refused()` stays for perms.
- Baseline acting on a scope's object: refused with a pointer when taskd knows the launch scope,
  not-found otherwise. Not silently done.
- A subject busy in both planes under `--scope me` steers the scope's own only, no refusal.
- Out of scope, noted not fixed: a hand-spawned task (`POST /tasks`) is tracked in no engine's
  live map, so it is not a flow on any plane; resolver verdict/position routes; `deleteScope`
  does not cancel the scope's live tasks; the `flow show` residual from vp67q7ha.
