# github iod: a pull request's payload does not say whom a review was requested from

The github iod's pull request entity (`github.<org>.<name>.pr.<n>`) carries author, assignees, labels, base/head, checks and timestamps, but not the requested reviewers. A charter cannot match "open pull requests waiting on X's review" (ENG-1296, Heidi's stale-review alert).

GitHub answers `requested_reviewers` (users) and `requested_teams` (teams) on both the pulls listing and the single-pull read, so a full pass and a targeted pass write the same entity; this is not the `comments` case the `pull` struct's comment warns about.

## Plan

- `pull` decodes `requested_reviewers` and `requested_teams`.
- `pullPayload` writes `requestedReviewers` (logins) and `requestedTeams` (slugs) when non-empty, as `assignees` is.
- Semantics, stated in the docs: GitHub removes a reviewer from the list once they submit a review, and adds them back on a re-request, so membership means "waiting on this person".
- Docs: docs/reference/iod.md's payload table, docs/systems/github.md's entity table.
- Test: the fake-server test covers both passes.

Reviews stay out of the iod: they cost a call per pull request per pass, and belong to the task verb `github.read-pr`.

Cost on deploy: every pull request entity with requested reviewers is rewritten once on the first pass.