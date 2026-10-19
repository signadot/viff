# runnerd: the runner holds a task's current policy, and checks a replacement itself

Found by Daniel's review of PR #24 (Medium 1 and 3).

The delivery server decides and re-checks each act by `tk.Launch.Policy`, the launch handed to `delivery.Open` when the task's repos were prepared, and nothing updates it: after `verse task policy`, a relaxed deny is still refused by the runner, and a tightened row holds only in the task's own process. `Salvage` and `Deliverable` read the launch's policy too.

And for a standalone runner, `PUT /tasks/{id}/policy` is forwarded to the task's process, where `CheckPolicy` runs (`inprocess.go`): the containment check against the runner's ceilings runs in the process being constrained, and the runner process never sees the new rows.

The fix: the delivery channel keeps the task's current policy, read by the server at each act and by settle; the subprocess handler checks a replacement against its own description before forwarding it, and sets the channel's copy once the task has taken it. Tests: a push a replacement allows is performed and one it denies is refused, at the channel; a slide past the ceiling is refused by the runner process with 400 and never reaches the task.