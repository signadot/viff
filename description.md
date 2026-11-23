# Task end: follow-ups left from PR #16

Left out of PR #16 (zm4kqgzsh12krkmtznn0, "a task ends on its record, a claim is what versed owes, and a runner keeps a lease"). Daniel's review is https://github.com/signadot/verse/pull/16#issuecomment-7111833795. Line numbers below are as of `b9f181f5`.

## Daniel's 8: checks that moved off the charter lock

- `uninstallOne` checks `mayUninstall` before it registers the uninstall, with no lock between the two (`packages.go:577-581`).
- `charterttl.go:191`: a renewing install that lands after the re-read gets 201, and is then removed. The comment at `:168-171` says this does not happen.
- `RemoveTriggerAs` does not check closing. A rule removed just after `Close()` leaves its task's `onCancelled` with no rule to run.

## Daniel's 10: at the 1m lease floor, two unanswered subscribes cost a healthy task

Each subscribe can block for `ControlTimeout` (30s, not scaled to the lease). A subscribe accepted at t0 has its retry fail at about t0+75s, but the lease ran out at t0+60s. The floor covers missed resubscribes only when they fail fast.

## Daniel's 11: smaller items

- The 429 for a package being uninstalled carries no `Retry-After`. The 503 paths set one.
- `verse task attach` prints "the stream closed and the task has not ended" when its snapshot read failed (`cmd/verse/task.go:526-531`). That is most often versed restarting.
- A launch that joins a live task keeps the task's first lease on the runner, while versed writes the new spec's lease on the record and waits by it.
- `landEnd` retries forever on any error except `Canceled` and `ErrForbidden`, and says so once.
- After `Close`, a hook that spawns is refused whole, its patch too. That is documented in `stop.go`, but "cancels its tasks so that their hooks run" reads as more than that.
- Tests to add:
  - an unreachable runner during an uninstall;
  - the old stream being closed on a resubscribe (`leasedHandle` never closes one, so dropping the `unsub()` at `follow.go:182` still passes);
  - a heal before the wait that replays the runner's own `failed`.

## A viewer that holds one stream past a whole lease

A viewer that holds one stream open past a whole lease (`verse task attach`, the TUI) can see the task's own `done` where the runner ended the task `failed` and orphaned. The record is right: the mirror closes its stream every quarter lease, and its next subscribe gets the runner's end. Closing this for viewers means serving live streams from the runner's own copy.

## `dropSystemAddress` takes a failed listing for "nothing installed"

`dropSystemAddress` (`internal/versed/systemprune.go`) takes a failed listing to mean "nothing installed under it", so it can remove the data of a package nested under a dropped system address. This predates PR #16. Its sibling in PR #16, a failed charter listing at install, is refused with a 503 (`d325bd55`). The same answer likely fits here.