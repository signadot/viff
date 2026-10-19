# delivery: a push to another task's branch is decided by the policy alone; the runner fills `exists` and `own`

Found by Daniel's review of PR #24 (High).

The runner's push path decides a `git.push` by the policy and the ceiling on `{repo, ref, delete, force}` and nothing else (`runner/runnerd/delivery/server.go`, `push`); the branch's state at the forge is read only to let a branch this run created be rewritten. #22's `Repo.Permit` refused a push to a branch at the forge that this run did not create; e24d5e15 took that out and nothing replaced it.

So under drift.tony's row (`ref: refs/heads/verse/auto/*`, `force: "false"`, `delete: "false"`) a task can fast-forward another task's `verse/auto/*` branch, and under the row docs/systems/git.md recommends it can force and delete it. Staging's charters push unattended.

The fix, in the systems model: two facts the runner knows and fills on every `git.push`, `exists` (the branch is at the forge) and `own` (its tip is a commit this task made: `Tree.Made`, by trailers, which also survives a resume), so a row can say `{ref: "refs/heads/verse/auto/*", exists: "false"}` for a create and `{…, own: "true"}` for an update. The task's push tool reads the same two from the runner before its own decision (a `state` request on the channel), and the runner computes them again at the act. `own` is filled for a pull request's head too. The documents, the charters, the docs and the examples follow. A test with two tasks: t1's fast-forward, force and delete of t0's branch are refused under the recommended rows, and t1's own create and update go.

A delete of another task's branch stays a thing a row may allow: the merge train retires landed `verse/auto/*` branches by design, and says so with `delete: "true"` and no `own`.