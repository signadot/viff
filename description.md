# trigger: a suspension that lands as a spawn launches skips the spawn, and what follows it runs

A spawn whose launch starts nothing because a suspension landed late is taken for one that
spawned nothing, and what follows it runs.

## What happens

An action's launch checks for a suspension twice. The first check (`launchSpawn`) parks the
launch and answers `errParked`, and every caller treats that as "in flight". The second is the
backstop in `startHooked` (`internal/trigger/fire.go`): it answers a nil handle and no error. A
suspension that lands between the two reaches only the backstop, and the launch can wait up to
250ms in between (`SettledOwnerOf`).

A nil handle with no error is also what an action that spawns nothing answers. So:

- **In a sequence**, `advanceSeq` (`internal/trigger/seq.go`) publishes `step: i+1` and runs the
  next elements. For `seq: [build, patch, ship]` with the suspension landing in that window,
  `build` is skipped, `patch` is written, and `ship` parks.
- **For a hooked top-level spawn**, the action's patch has landed, the claim is released
  (`internal/trigger/trigger.go`, the `hooked && h == nil` branch), and nothing is parked. The
  subject stands at the stage the rule set, with no task and no hook owed.

The lift of a suspension handles the same race for a parked launch (`releaseLaunches`, in
`internal/trigger/suspend.go`). The first launch does not.

## How it was found

A review of the task-end stack (zm4kqgzsh12krkmtznn0), reading `internal/trigger`. The
behaviour was reproduced with a spawner that answers nil: the element's patch was written and
the third element started. The suspension's timing itself was read, not reproduced. `main`
behaves the same; `errParked` closed the hole beside this one.

## What a fix has to do

The backstop has to answer as the first check does: park the launch and say `errParked`, so
that a sequence waits at the element and a hooked spawn keeps its claim.