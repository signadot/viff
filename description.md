# A script cannot deliver a commit it made: it is never told its trailers

`git-push` delivers only commits that end with the task's provenance trailers, `Verse-Task: <id>` and `Verse-Principal: <principal>`, checked for equality with the launch's own values (`workspace.MissingTrailers`, called by `Tree.AuthoredWork` from `delivery.Server.push`). The push adds none.

An agent is told the exact lines in its brief (`repoBrief`, `stock/runner.go`). A script has no brief, and nothing in its environment carries them: it gets the launch's `vars`, its credential file and `VERSE_TOOLS`. The id is in its checkout's branch only as `SafeSeg(id)`, which does not invert. So a script can push commits already upstream (the `--ff-only` merges in docs/asking.md and docs/reference/tasks.md), and every commit it makes itself is refused.

The docs say a script calls the same tools and the same rows decide them; it can't meet the check that follows the decision.

**Fix:** give a script with a checkout its trailers in its environment, `VERSE_TRAILERS`, the lines as the last paragraph of a commit message must hold them, so a script commits with `git commit -m <subject> -m "$VERSE_TRAILERS"`. It is what an agent's brief already tells it, and nothing to forge with: the check refuses any value but the launch's own. The runner's value is set after the launch's `vars`, so a spawn cannot shadow it.

Found while automating the maintainers' silence flip for PR #6 (a script editing MAINTAINERS and pushing to `verse/charter`).