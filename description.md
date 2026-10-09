# cli: `verse task get` shows a failed task without its reason, which the runner's snapshot and the terminal frame both carry

Observed 2026-10-09 against a local `verse up` with a coderunner installed as `verse.runners.codex`, on task `run-6f1c62293691`, whose turn failed on the model account.

`verse task get run-6f1c62293691` printed:

```
ended: "2026-10-09T09:00:44Z"
id: run-6f1c62293691
occurrence: 159
runner: http://localhost:9802
runnerName: verse.runners.codex
started: "2026-10-09T09:00:41Z"
status: failed
```

No reason. The runner's own snapshot had it:

```
$ curl -s localhost:9802/tasks/run-6f1c62293691
cursor: 2
driver: codex
err: "codex: the turn failed: Quota exceeded. Check your plan and billing details."
...
status: failed
```

and so did the terminal frame on the read plane:

```
{err: "codex: the turn failed: Quota exceeded. Check your plan and billing details." kind: status task: "" text: failed}
```

So the reason reaches verse and is dropped between the frame and what `task get` prints. A failed task whose reason is only on the runner is a failure an operator has to go to the runner to read, which is the thing the mirror exists to avoid. `verse ps` has the same gap, and `prompt` and `driver` are missing from `task get` too, while the snapshot carries them.

Not the codex driver's: the same path is taken by every remote runner's failure. Found while running the first real codex task (pvk89kvah12kry4rs1n0).