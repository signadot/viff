# Suspend's surface: implementation plan

Status: plan, nothing built. The design is [suspend.md § The surface](suspend.md#the-surface):
suspend, resume, say and cancel as verbs of `verse flow`, addressed by `--task`, `--charter` and
`--subject`, with no flag defaulting to "every". This is the order it lands in, each step green.

## What stays

The record, the tables, holding/withdrawing/parking/ordered lift, authority at application, scope
ownership, `heldBy` (trigger/suspend.go, versed/suspend.go S1–S6). Only the addressing, the verbs,
the wire and the docs move.

## Steps

Each commit green: `make check`, the suite excluding scratchpad, then `./smoke.sh` alone.

**1. The record reads the new addressing** — `trigger/suspend.go`
- `NewSuspension(charter, rule, subject string, recursive bool)`: charter `*` explicit (no empty-
  means-every); `recursive` replaces the trailing `.*` and means the charter AT the address and
  beneath (`CoversCharter` changes `beneath` → `atOrBeneath`); record gains `recursive: true`, id
  spells it `<address>.**`.
- Subject is a flow selector: reuse `trigger.ParseExpr` / `Match.MatchRef` (trigger/match.go) for
  `CoversSubject`, so `*` in any segment and the listing's filter are one grammar; refuse a payload
  predicate (`Match.HasPredicate`). Canonical spelling from the parsed segments.
- `SubjectPlace` = the pattern's literal head; `Within` updated for segment wildcards.
- Tests (trigger/suspend_test.go): per-segment coverage, recursive covers the node itself, a
  predicate refused; existing S2–S6 tests re-spelled.

**2. The wire: `/flows` lists rows, `/flow/*` acts on flags** — `api/api.go`, `versed/server.go`,
`versed/suspend.go`, `versed/owns.go`, `trigger/engine.go`, `trigger/flowtree.go`, `schema/root.tony`
- `api.FlowSelect{Task, Charter, Subject, Recursive}` (query on GET, body on POST, plus `Why`,
  `Text`, `By`); `api.FlowRow` — `{kind: task, task, charter, subject, heldBy}` or `{kind:
  suspension, …api.Suspension}`.
- `GET /flows?task&charter&subject&recursive` → rows across `flowEngines(scope)` and the scope's
  table. Engine selection gains charter/rule filters: extend `matchFlow` into `selectFlow(sel)`
  (rule = `mirror.TriggerOf(fp.chain)`, charter = engine `atSegs`).
- `POST /flow/suspend|resume`: `--task` → today's `taskControl` path (scope, control, 409 while a
  suspension holds it) setting `heldBy: operator`; charter+subject → `hold`/`release` with
  `suspensionOwnHere` and `maySuspend`. Narrowing enforced here (400 with the usage sentence).
- `POST /flow/say` any flag, per-task `control` as P1 (`steer`); `POST /flow/cancel` requires task.
- Remove `GET /suspensions`, `POST /suspend|/resume|/flow/pause`, and `PauseFlow`/`ResumeFlow`'s
  subject-addressed callers (keep the engine methods the task path uses).
- `api.Status.Flows` → `{tasks, subjects}`; `FlowTree` gains top-level `suspended`.
- Tests: rewrite `versed/flows_test.go` (steering acts on its own, asks control, hand-spawned is a
  flow) and `versed/suspend_test.go` onto the new routes; `schema_test` route rows and root;
  status shape; narrowing refusals (bare act → 400, `'*'`+`'*'` → 400, cancel without task → 400).
- **Contract** (main has `flow pause|resume|cancel <subject>` and `/flows` as strings): `!`,
  `Breaks:`, and a new `Notice:` git-issue filed before the commit (local; pushed when asked).

**3. The CLI** — `cmd/verse/flow.go`, delete `cmd/verse/suspend.go`, `cmd/verse/main.go`
- `verse flow [--task|--charter|--subject|-r] [--args]`; verbs `suspend --why`, `resume`, `say
  <text…>`, `cancel`, `show <subject>` unchanged; `--as` as `gate approve`.
- `--args` prints each row as the flags naming it, quoted so `xargs -L1` and a shell both read it back
  (reuse the shell-quoting of `entity.JoinSteps`, entity/write.go, extended for xargs).
- Tests (`cmd/verse/flow_test.go`): flags reach the wire; narrowing refused client-side with usage;
  `--args` round-trips through the system `xargs -L1 printf '%s\n'` for a quoted subject.
  Contract, same notice.

**4. Docs** — new `docs/control.md` (what an operator stops and for how long: a run's hold vs a
durable suspension; whose hold; who may; what each reaches; scopes; gates); link from
`docs/README.md` (a Steering row) and `mkdocs.yml` nav under Steering. Trim
`docs/trigger-model.md` § Suspending to rule semantics plus a link; rewrite the tutorial's
§3 Steering around the fork; update `docs/reference/README.md`, `reference/tasks.md` Steering,
`perms.md`, `reference/connect.md`, `overview.md:154`, `deployment-and-steering.md`,
`task-control.md`. Contract (docs), same notice.

**5. Smoke and scripts** — `smoke.sh` suspend section on the new verbs, plus a `verse flow --args
--charter smoke-susp | xargs -L1 verse flow resume` round trip and the bare-verb refusal;
`scripts/charter-shapes.sh suspend|resume <shape>` on `--charter :shape-<x> --subject '*'`.

**6. The landing, as contributing.md defines one** — not a status line. `docs/` already says what is
true after step 4, so the working files go:
- delete `docs/working/suspend.md`, `suspend-plan.md` and `suspend-surface-plan.md` (and undo
  105418e0's "landed" status line by deleting rather than keeping it);
- write `docs/working/suspend-residual.md`, first line naming `docs/control.md` and the trigger
  model's Suspending as what it is a residual of, carrying what was named and not built:
  automatic suspension (S8, the throttle trip as a system hold); a scope deleted with its
  suspensions (git-issue `y51f9ce5`); a parked launch lost across a restart; a lapsed holder's
  held work not released; suspending the system charter from outside; stored names as references
  (a moved package or renamed rule leaving `covers: false`); and the found-along-the-way items not
  closed by P1/P3/P4 (a hook's selection blocking the reap loop, `reconcileTasks` adopting before
  the charter loads, `clearPriorAnswer` on the answered path, the throttle's dormant chain-break);
- the index edits: a row in `docs/working/README.md`'s residual table (and its count), and remove
  any link to the deleted notes from `docs/` (`git grep suspend.md suspend-plan.md`);
- the neighbours: `contributing.md`-style check that no doc still describes `verse suspend` or
  `flow pause`.
One commit, tier fix per contributing (working notes owe nothing), docs links checked by
`make check-docs`.

## Verification

- Edit loop: `go test ./trigger ./versed ./cmd/verse -run 'Suspend|Flow|Surface'`.
- Per commit: `make check && go test -timeout 300s $(go list ./... | grep -v /scratchpad)`, then
  `./smoke.sh` alone; each new test run with its mechanism disabled, failing for the stated reason.
- By hand on a local verse (`VERSE_ADDR` named on each command): `verse flow suspend` bare → usage,
  nothing written; suspend a package, `verse flow --args | xargs -L1 verse flow resume` lifts it.
- Known sweep flakes to tell apart (fail at 50adb2ac too): TestAWorkerRaisedGateIsHeldOutToWhomItNames,
  TestTheTriageDriveMarksWhatMovedAndOnlyThat.
