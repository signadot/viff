# a claude task that fails "Not logged in" ends failed with no err, so onFailed's $err does not resolve

Seen on a local run of deploy/charters/example/repo/app/stale-review.tony against a claude runner (`runnerd verse -sandbox none`) whose claude had no credential. The claude CLI under the runner answered `Not logged in · Please run /login`.

The task record ended:

```
status: failed
result: "Not logged in · Please run /login"
```

It had no `err`. The rule's `onFailed: {patch: {stage: failed, err: $err}}` then did not fire:

```
WARN a rule's firing went wrong rule=review kind=failed chain=review.onFailed subject="signadot.stale-review.day.\"2026-10-23\"" why="$err is not present in the payload that fired this action"
WARN completion hook failed: the firing wrote nothing chain=review.onFailed
```

**Impact.** Every charter in the tree writes `err: $err` in onFailed: comment-drift, merge, pr, stale-review. When this failure shape occurs, none of them records the failure, and the subject's latch (`stage`) stays open. On the next verse start the rule selects the subject again and the task runs again. That happened here: the same task id failed twice, once per start.

**Precondition.** A claude task whose failure arrives as the turn's result text rather than as an error. "Not logged in" is the case seen. `claude.go` turns a result with `is_error` into an error ("claude: the turn failed: …"), so either the CLI did not set `is_error` here, or the failure went through a path that sets `result` and not `err`. That is not yet determined.

**Rule to state:** a task that ends `failed` carries `err`, whatever ended it. Fixing the producer, not teaching hooks to fall back to `$result`.