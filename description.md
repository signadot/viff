# task: a cancel while the driver is in Next ends the run failed, not cancelled

A review of the claude runner (2026-10-21, on verse/claude-runner) found that `fold`'s `inDriver` case in `task/machine.go` exits Failed whenever `v.err != nil`, even when `inCancel` has already latched. The `executed` case does honour the latch.

A model-driven agent spends nearly all of its wall time in `Next`: claude, codex, and the verse llm driver alike. So `verse task cancel` on a running task mostly reports **failed**, and a charter's `onFailed` fires. The cause is kept ("cancelled by me: stop"), but the status is wrong.

The reviewer reproduced it with a loop-level probe: `reason=failed err="cancelled by me: stop"`.

The suggested fix: in `inDriver`, `if v.err != nil && s.cancelling { return s.exit(s.exitStatus) }`, mirroring `executed`. That keeps the `fail` tool's Failed when no cancel is latched.

Out of scope for the claude runner PR because it changes every agent's outcome. It needs its own tests in `task/`.