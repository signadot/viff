# github system: a task cannot read a repository's pull requests

The `github` system's verbs only write (`create-pr`, `update-pr`). A task on the claude runner cannot find open pull requests or see whom a review was requested from and what reviews were given: it holds no GitHub credential by design, and `gh` is not installed (ENG-1296). Daniel's advice: extend the system, so each read is a step the environment, autonomy and ceiling decide.

## Plan

Two verbs, built the way `slack.read` is: a step the autonomy decides, performed by the runner with its token, bounded by the ceiling.

- **`github.list-prs {repo}`**: the open pull requests, each with number, title, author, draft, base, head, `updatedAt`, requested reviewers and teams. Pages 100 at a time to a cap, and says when it hit the cap.
- **`github.read-pr {repo, pr}`**: one pull request with the same fields plus its reviews (author, state, `submittedAt`). Reviews are a second call, which is why they are here and not in the iod.

Where: `systems/documents/github.tony` (verbs), `task/step.go` (steps), `runner/runnerd/delivery` (ops and the GitHub calls), `runner/runnerd/verserunner/repotools.go` (tools, offered where the runner's forge is GitHub). The claude runner gets them over its MCP server and `verse-call-tool` by name, unchanged.

A pull request's title and body are written by whoever opened it; the tools say it enters the task's context as written, as `slack.read` does.

Offered, as `create-pr` is, for the repositories the task's `git` block checks out. Lifting that is a follow-up.

Check: whether the staging claude runner's `github: {ceiling: {repo: "github.com/signadot/**"}}` grants the new verbs or needs them named.