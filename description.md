# versed: TestAnsweringBeforeTheDeadline is flaky under the full suite

One `go test ./...` failed at

    --- FAIL: TestAnsweringBeforeTheDeadline (0.38s)

and it passes on its own (4 runs) and under three consecutive full-package runs of `./versed/`. So it fails under whatever load the whole-repo run puts on the machine, not on its own logic.

WHY IT MATTERS. It is the check that an answer given BEFORE a deadline is the answer that counts, which is a real property — a deadline that fires anyway would settle a question somebody had already decided. A flaky version of that check is the worst of both: it cannot be trusted when it fails, and it is the thing that would tell us the property broke.

NEXT. Find what it waits on. A deadline test that races is usually one comparing against a wall clock with a margin sized for an unloaded machine; the fix is to make the boundary observable rather than to widen the margin.