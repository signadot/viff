# runnerd: a task's result is missing from its record when the mirror reads the snapshot before the run has returned

Seen on staging 2026-10-20 with the claude runner's first task (run-c80f12f57ba6): the read plane carries the agent's final `say` and the terminal `status`, but `verse task get` shows no `result:`.

Two followers read a task's stream. The runner's `supervise` (runner/runnerd/subprocess.go) sees the terminal status frame and waits, up to `settleTimeout`, for the task process's snapshot to agree before remembering it as the task's last word. versed's mirror (mirror/mirror.go, `case task.EventStatus`) sees the same frame and at once asks the runner for `h.Result()`, a GET of `/tasks/{id}`. In that window `forward` has no final snapshot, so it proxies to the task process, whose `hosted.snapshot()` reports `h.result` — and `h.result = tk.Run()`'s `tk.Result()` is assigned only after `Run` returns (runner/runnerd/inprocess.go:291), while the status frame is emitted from inside `Run` (task/loop.go:776). The mirror reads "" and the record ends with no result. The comment beside the mirror's `err` handling describes the same race for the reason, which was fixed on the frame side; the result has no frame to ride.

Every doer has it, not claude alone; whether a given run shows it is timing.

Fix: the snapshot reports the task's live answer, `h.task.Result()` (task.Task exposes the driver's Resulter "so a hosted task's answer is observable over the wire"), which the driver has set before the terminal frame; the copy taken after Run returns goes away.

Branch `verse/result-race` in `.worktrees/result-race`, off main.