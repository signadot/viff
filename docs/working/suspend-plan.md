# Suspend: implementation plan

Status: plan, nothing built. The design is [suspend.md](suspend.md) (git-issue
`xr69yacwh12kr4qamsn0`); this is how it lands, A to Z — prerequisites, the record, every place
work waits, the surfaces, the docs, the scripts — with what each step must prove and what fails
before it. Read the design first: this restates its decisions in a line each, not its argument.

Verified against `087f2208` (holds, which contains main at `1ce142cf`) on 2026-09-14. The design was
written against `ead39d3d`; every mechanism it cites was re-found ([below](#does-the-design-hold)).

## Does the design hold?

**Yes, with five adjustments, none to the model.** Each site the design names exists and does what
the design says: the firing check's home before `firstSight` (`trigger/edge.go:56`), the throttle's
side effect it must precede (`engine.go:756`), `fireOne` (`:781`), the two backstops `spawnHooked`
(`:1222`) and `patchEntity` (`:1762`); the gate wait that blocks in `Registry.Resolve` and leaves the
docket only on context cancel (`engine.go:859-867`, `task/registry.go:90-125`); `untilMoot` to hang
the per-pose context under (`:932`); `claimFlight` refusing a replay's firing over a waiting one
(`edge.go:224`); `armTriggerSubtree` and `registerWatchers` for the re-arm (`engine.go:352`, `:494`);
`flowTask` with no `heldBy` yet (`:36`); `PauseFlow`/`ResumeFlow` unconditional (`:1596`); a rule
record with `spec`, `scope`, `at` and no installer (`trigger/charter.go:133`); ids carrying rule
name and subject only (`trigger_api.go:344`, `:572`); `Condition.Covers` as the uniform test
(`resolver/resolver.go:130`); `launchSpawn` as the one entry a resumed selection re-takes
(`select.go:287`); the startup phases and `awaiting` (`versed/startup.go:20-29`, `:122`); the sources
waiting on the charter phases (`sources.go:841`); the perms seed (`versed/perms.go:29`); a scope's
perms mirror as the attribution's `children` (`entity/attribution.go:95`); ownership through
`scopeOf`, `flowEngines`, `inherited` (`versed/owns.go`).

The adjustments:

1. **Hooks land; only launches park** — already folded into the design after the review. The
   parked-hook list, the mark, the restart sweep and `at` on task records are gone from this plan.
2. **A hand-spawned task is reached by adopting it, not by attaching through its runner.**
   git-issue `e0zr80p5h12kr1zvmxn0` (a hand-spawned task is no flow) is a prerequisite here: once
   `POST /tasks` adopts into the spawning scope's engine, a suspension holds it through the same
   `heldBy` as a rule's task, and the design's "handle attached through its runner" is not needed.
3. **`control` on the flow routes first.** The design's item 7 and its "found along the way": a
   suspension checks `control` at every application and `flow resume` refuses a covered task, so
   the flow verbs and the runtime's task pause/resume must check `control` too, or a suspension is
   stricter than the steer beside it. A prerequisite commit.
4. **The scope's table load fails closed, the perms mirror keeps failing soft.** `scopeEngine`'s
   `sync.Once` now also tracks the scope's perms mirror and logs a failure (`versed/server.go:939`);
   that stays, since an unloaded mirror reads per key and is right. The table is different: unloaded
   answers *suspended*, so a failed load must retry with the engine unregistered, as the design
   says, and the Once gains that distinction.
5. **Line numbers.** The gate wait is at `engine.go:859`, not `:910`; `AllGates` are posed at
   `:811`; nothing else moved by more than a few lines.

One thing the design leaves to `deleteScope` — cancelling a scope's live tasks — is git-issue
`y51f9ce5h12kr2zvmxn0`, sequenced after the core here so a suspension removed with its scope has
something to follow.

## Prerequisites (each its own commit, landable before the rest)

- **P1. `control` on the flow verbs and the runtime's pause/resume.** `steer` (`versed/server.go`)
  asks `entity.Check(scopedStore, principal, Control, mirror.Ref(id), "steering it")` for each task
  the address selects, after the ownership check, and refuses the whole steer naming the first task
  refused — a steer is one act, not a partial one. `/tasks/{id}/pause|resume` on the runtime mount
  go through `taskControl`'s check the way cancel/say/vector do. Test: a stranger's `flow cancel`
  is 403 and no handle is signalled; the owner's reaches. `Tier: change`.
- **P2. Hand-spawned tasks are flows** (`e0zr80p5`). `spawnTask` adopts the handle into
  `s.Eng` or `scopeEngine(scope)` (`Adopt`, subject from the body) so `verse flow` lists it and the
  verbs reach it; a task with no subject is adopted under its id. Test: `task run --subject demo.pr.1`
  then `flow pause demo.pr.1` reaches 1; `--scope me` likewise in the scope. `Tier: change`.
- **P3. Ids carry the charter address and the scope.** `spawnID` and the gate id
  (`trigger_api.go:344`, `:572`) gain `<at>` and `<scope>` segments; `mirror.TriggerOf(chain)`,
  `ownerOf`'s split on `task.GateSep` and `taskHoldingGate` keep working since neither segment
  contains the separator. Two engines' same-named rules about one subject stop sharing a question.
  Wire-visible: task ids and question ids change shape. `Tier: contract`, `Breaks:` the id shape,
  with a notice; the smoke's id regexes updated. Test: two engines, same rule name, one subject,
  two questions on the docket.
- **P4. A rule record carries its installer.** `record` writes `by` beside `spec`, `scope`, `at`
  (`charter.go:133`); `LoadCharter` reads it back; the charter transaction's rollback
  (`versed/charter.go:779`) reinstalls under the original `by`, not as the system store. A rule the
  system charter installed is `by: system:charter`. Test: install as alice, restart, the record
  still says alice; a failed re-apply leaves `by` as it was. `Tier: change`.

## The core (in order; each step's test fails before it)

### S1. The record, the table, the route

- **Record** `verse.trigger.suspension."<charter>:<rule>@<subject>"` with `charter`, `rule`,
  `subject`, `scope`, `held: {<holder>: {why, since, occurrence}}`. Parsed and compared by segment
  (`entity.PathSegs`), never as strings. `reservedWrite` already refuses a raw put; `validateDerive`
  (`trigger/stratum.go:1234`) refuses a derive whose target is under `verse.trigger.suspension`.
  Seed row: `read: ["*"]` beneath the kind (`versed/perms.go:29`).
- **Table** `versed/suspend.go`: one per scope — baseline's on the server, a scope's beside its
  engine in `scopeEngine` — an `RWMutex`, the entries, a `draining` set, and `loaded`. Readers
  never hold the writer's lock across a lift. Unloaded answers *suspended*.
- **Load.** `phaseSuspensions`, the first phase (`startup.go:20`), read with the phase's retry;
  `phaseTasks`, `phaseCharter` and `phasePackages` `awaiting` it. A scope's inside `scopeEngine`'s
  Once before `restoreScopeCharter`, retried with backoff and the engine unregistered until it
  loads (adjustment 4). Dropped in `deleteScope` beside the perms mirror.
- **Route** `POST /suspend`, `POST /resume`, `GET /suspensions`, scope from `?scope` as every
  control route; ownership first (`inherited` when the charter named is another scope's), then
  `control` from the charter side or the subject side ([design](suspend.md#authority-is-decided-when-it-applies)),
  `'*' about '*'` refused. The write and the table change under one lock; `occurrence` from the
  write's returned revision. A system holder is added by engine code calling the same function.
- **CLI** `verse suspend [<charter>[:<rule>]] [<subject>] --why …`, `verse resume …`, bare
  `verse suspend` lists — holders, why, age, movement since (`occurrence` vs `storeRev`), what is
  held now, and whether it still covers anything. `--as` as `gate approve` takes it.
- **Tests** (`versed/suspend_test.go`): the three shapes make three records and the same shape
  twice is one record with two holders; a system resume lifts only the system's holder; a scope's
  record is refused from baseline and vice versa (409, the `inherited` sentence); `why` required;
  the table is loaded before tasks are reconciled (a request during restore answers suspended);
  `verse data --put` at the kind is refused; a derive at it is refused at install.

### S2. Firings do not start

- The predicate `suspended(scope, at, rule, subject) bool` on the engine, reading its scope's table
  and asking `entity.Holds(mirror, holder, Control, …)` per holder from the charter side or the
  subject side; a check the mirror cannot answer refuses (stays held).
- Asked at the top of `evaluate` before `firstSight` (`edge.go:56`), in `fireSubjects` before
  `throttled` (`engine.go:756`), on entry to `fireOne` (`:781`), in `spawnHooked` (`:1222`) and
  `patchEntity` (`:1762`). Never in subject selection.
- **Tests** (`trigger/suspend_test.go`): a covered level rule's commit is skipped and the watermark
  does not advance (`firstSight` true after the lift); the throttle counts no trip; a covered
  `patchEntity` from any path is refused; an uncovered rule about the same subject in another scope
  fires; a wildcard holder stops covering a subject the moment somebody owns it (authority at
  application).

### S3. Questions are withdrawn and re-posed; clocks stop

- `fireOne`'s gate wait gets a per-pose context under `untilMoot`'s (`engine.go:859`); a suspension
  cancels it with itself as cause, so the record goes `orphaned` with `why` naming the suspension
  and keeps ballots and `deadline.at`; the goroutine keeps its flight claim and waits on the table;
  on lift, if `stillSelects`, it re-poses the same id (P3's), joining what was cast, `askedAgain`.
  A per-engine map from (rule, subject) to the firing's withdraw and resume replaces `inflight`'s
  bool (`:138`).
- Deadline: on re-pose, re-armed at lift + (`deadline.at` − `orphanedAt`); one already past when
  withdrawn settles on the re-pose (`armDeadline`, `settleByDeadline`, `versed/server.go:2634`,
  `:2677`).
- **Tests**: a firing at its gate is withdrawn with the suspension's why and a ballot cast before
  survives; lifted, the same id is on the docket again and the earlier ballot counts; a question
  withdrawn with 10m left and lifted 1h later has 10m left; mootness during a suspension ends the
  firing and the lift re-poses nothing; nobody can answer a withdrawn question (404, not 204).

### S4. Tasks hold, and know whose hold it is

- `heldBy` on `flowTask`; `PauseFlow` marks `operator`, the table marks the suspension; `ResumeFlow`
  refuses a task the table covers, naming the suspension (409); the lift resumes only what it
  paused. A task re-attached after a restart is held at `track` when the table covers it; a
  hand-spawned one arrives by P2. `flow show` and the task record say `heldBy`.
- **Tests**: `flow pause`, suspend, `flow resume` → refused, task still held; lift → still held
  by the operator; suspend, `flow pause`, lift → still held; restart under a suspension →
  re-attached task held before the charter is restored.

### S5. Hooks land, launches park

- `launchSpawn` (`trigger/trigger.go:926`) checks the table at entry — after the hook's patches and
  derives, before `selectProvider` — and parks its arguments in a per-engine list; the lift
  re-enters it with each, as `ResumeSelections` does (`select.go:287`). `seq` steps take the same
  path. Nothing on the task record; a parked launch does not survive a restart (design, "what it
  does not solve").
- **Tests**: a covered task finishes → its `onDone` patch is on the subject, its spawn is not live,
  the reap loop is not blocked (a second task in the same engine finishes and hooks); lift → the
  spawn starts before the covered rule re-arms; a hook with `provider: {select: ask}` under a
  suspension poses no selection.

### S6. Resume is ordered

- Lift: delete the record (a commit); mark the entry draining (firings still refused); release —
  tasks the suspension paused, parked launches in order, withdrawn questions re-posed and re-armed;
  remove the entry; re-arm each covered rule through `armTriggerSubtree` (the seed replays at the
  head, admitted because the skip never advanced the watermark). A wide lift is a restart's worth
  of evaluation and never runs in a loop.
- **Tests** — the design's own list of unchecked interleavings: draining refuses a firing while a
  parked launch starts and a sibling subject's commit re-evaluates the rule; the per-pose context
  withdraws on both suspension and mootness and re-poses on only one; a scope's table load precedes
  its first `addTrigger`; a level rule whose condition came and went during the suspension does
  nothing on lift, one still matching acts once, an `on` edge that rose meanwhile is not seen.

### S7. Surfaces

- `status`: `suspensions` count (`api.Status`, beside `flows`, `gates`); the perms census unchanged.
- Charter listing: a rule a suspension covers whole is marked `suspended: [<id>…]`.
- `flow show`: a `FlowNode` step that is held says `suspended: <id>`; a withdrawn question's node
  says so (`trigger/flowtree.go:93`).
- `GET /suspensions` is the listing behind bare `verse suspend`.
- **Tests**: `schema_test.go` route rows; status counts the list; census and tree fields present.

### S8. Automatic suspension (optional, after S7)

The cycle throttle's trip becomes a system-held suspension of the rule instead of an indefinite
pace, lifted by the system when the chain is repaired; a person's holder is never touched. Separate
decision; the holder map makes it a small change.

## Docs (with the step that makes each true)

- `docs/trigger-model.md`: a section **Suspending** beside "Flow — a subject-match over live tasks"
  (`:1579`): the record, the three shapes, what a suspended subject means in each form, resume as
  re-arm, whose pause it is. (S1–S6)
- `docs/perms.md`: `control` covers suspending (`:185`), authority from the charter side and the
  subject side, decided when it applies; the seed row. (S1, S2)
- `docs/reference/README.md`: rows for `suspend` and `resume` beside `flow` (`:281`); `flow resume`
  refusing a covered task. (S1, S4)
- `docs/reference/connect.md#what-a-scope-owns`: a suspension is a control write and covers what
  its scope created. (S1)
- `docs/reference/tasks.md` Steering: `verse flow pause` beside `verse suspend`, and the refusal.
  (S4)
- `docs/deployment-and-steering.md`: a suspension takes effect by the route, never by observation —
  one table per scope, one writer. (S1)
- `docs/task-control.md`: a pause names its holder. (S4)
- `docs/tutorial.md`: a paragraph after the flow section: suspend the demo charter, move the world,
  resume, watch it act once on current state. (S6)
- `docs/working/suspend.md`: status line → landed, with the commits; the "found along the way"
  items closed by P1, P3, P4 struck. (last)

## Scripts and smoke

- `smoke.sh`: a section **suspend** after "gate: a threshold gate holds…" (`:391`): install a level
  rule and a gated rule; `verse suspend <charter>` with `--why`; write the level rule's subject —
  no firing, `status` says `suspensions: 1`; open the gated rule's subject — no question on the
  docket; `verse resume` — the level rule acts once, the question appears; `verse suspend` lists
  nothing. A scoped step in the connect section: `--scope $SCOPE suspend` of a baseline rule is the
  409 sentence. (S1–S6)
- `scripts/charter-shapes.sh`: `suspend <shape>` and `resume <shape>` verbs, so `drive` can be
  shown against a suspended shape; the usage line and `docs/reference/README.md:344`'s script table.
- `scripts/seed-demo.sh`: unchanged; the tutorial's suspend paragraph runs against it.
- `smoke.sh` id regexes for P3's new id shape.

## Commits, by path, in this order

1. P1 `control` on the flow verbs and runtime pause/resume — `change`.
2. P2 hand-spawned tasks are flows — `change`; closes `e0zr80p5`.
3. P3 ids carry the charter address and the scope — `contract`, `Breaks:` + `Notice:`.
4. P4 rule records carry `by` — `change`.
5. S1 record, table, route, CLI, seed — `change`.
6. S2 firings — `change`.
7. S3 questions and clocks — `change`.
8. S4 tasks — `change`.
9. S5 launches — `change`.
10. S6 resume ordering and its interleaving tests — `change`.
11. S7 surfaces — `change`.
12. Docs (the list above) — one commit.
13. Scripts and smoke — one commit.
14. `docs/working/suspend.md` status → landed.

`y51f9ce5` (deleteScope cancels the scope's tasks) follows, and a suspension removed with its scope
cancels through it. S8 is a separate decision.

## Verification

- Edit loop: `go test ./trigger ./versed -run 'Suspend|Suspension'`; `go test ./trigger -run
  'TestFire|TestGate'` after S2/S3 for the paths they sit in.
- Before each commit: `make check && go test $(go list ./... | grep -v /scratchpad)`; then
  `./smoke.sh` alone.
- Each new test run against the parent commit in a throwaway worktree, and must fail for the
  stated reason (a firing that should have been skipped; a question still on the docket; a task
  resumed by the wrong hand; a hook's spawn live under a suspension; a 200 where a 409 is due).
- Staging after the ship: `verse suspend verse-dev.drift --why "…"`, move a range, `verse suspend`
  says what it held, `verse resume`, one review-range run; `status` counts go back to 0.

## Decisions taken (say otherwise)

- `verse flow pause` stays as the forgetful quick spelling; `heldBy` tells the two apart.
- Baseline's charter has no address, so it is not spelled: bare `verse suspend <subject>` is
  `'*'` about the subject, and a baseline rule is `:<rule>` — `verse suspend :claim
  git.verse.head`. A package is its address; a scope's rules are addressed from the scope.
- Refusals are 409 with the `inherited` sentence for ownership, 403 for `control`, in that order.
- A steer refused `control` on one of its tasks is refused whole.
- No `until`; `why` required; `occurrence` on every holder.
