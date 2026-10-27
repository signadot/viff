# runnerd: a push answers "nothing to push" while its target branch is not where the push would put it — the train never reaches verse/train

A `git-push` answers OK with "nothing to push" when the task has no commits of its own beyond its base, whatever the target branch holds at the forge. `git push` succeeds only when its postcondition holds: the remote branch is where the push would put it. The runner's OK claims that postcondition without checking it, so a script that trusts the OK, as it should trust git's, carries on with a branch that isn't there.

## Seen on staging

`signadot.pr:train-push@signadot.pr.train.f751c0b0…` (`deploy/charters/example/repo/app/pr.tony`, rule `train-push`), on 2026-10-27 at 21:32:

```
Current branch _car is up to date.
Updating f751c0b03..e03f63434
Fast-forward
nothing to push: signadot has no commits beyond the base it started from; commit what you want delivered, then push
refused: verse/train isn't at the forge: push it first, then open a pull request from it
```

The one car already sat on the train's base, so its rebase was a no-op and its commit kept its SHA, `e03f63434`. The fast-forward moved the task's HEAD to that commit, which is a mirror branch's tip. `AuthoredWork` (`runner/runnerd/workspace/workspace.go`) stops its walk at every commit already upstream, counted none as the task's, and `push` (`runner/runnerd/delivery/server.go`) answered OK with "nothing to push". `verse/train` did not exist at GitHub, so `github-create-pr` was refused and the script exited 3. GitHub already had `e03f63434`. All the push had to do was create the branch.

## Fix

When the task has no commits of its own beyond its base, decide against the target branch:

- **HEAD is still the base:** the task did nothing, so the answer stays "nothing to push", and no branch is made (`TestAPushOfNothingSaysSo`).
- **The target is at the forge at HEAD:** it is up to date. Answer OK, push nothing, and count the branch as this run's push, so a pull request from it is this run's to open.
- **Otherwise:** HEAD has moved onto commits already upstream, so push HEAD to the target. None of those commits is the task's own, so there are no trailers to check. The autonomy has already decided the push on its ref, `exists` and `own`.

Commits the task did write are checked for trailers as before.

Not in this issue: a train car that is behind master is rebased into new commits carrying the car task's `Verse-Task:`, and the push refuses them as not the train's. That is a separate question, of what counts as a task's own work.
