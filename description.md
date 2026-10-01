# taskd: TestACancelledTaskIsNotReportedAsFailed is flaky under the full suite

Seen once in `go test ./...` on `verse/github-out` (2026-10-01), on a change that does not touch taskd:

    --- FAIL: TestACancelledTaskIsNotReportedAsFailed (1.04s)
        cancelstatus_test.go:51: a cancelled task ended "done": …
        cancelstatus_test.go:56: the cancel's reason was dropped along with the wrong status

It passes 20 out of 20 runs alone and in two package runs. The test spawns a hosted task (`spawnOne`, prompt "work") and then cancels it. Under full-suite load, the task can finish before the cancel lands, so it ends `done` and the assertion about a cancelled status has nothing to check.

Likely fix: make the task wait (a driver that blocks until cancelled) so the cancel always meets a running task, in the same way the test's other waits assert what arrived rather than racing it.