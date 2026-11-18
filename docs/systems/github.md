# github

The `github` system is GitHub as the forge of a repository that a task checked out. Its verbs
are about pull requests, which belong to the forge and not to git. The runner performs them
with its credential for the repository.

## What a charter sees

`iod github` writes a repository at GitHub into the store: its pull requests, issues,
discussions, workflow runs and comments, and with `-alerts` its Dependabot alerts.

| Address | What it is |
|---|---|
| `github.<org>.<name>.pr.<n>` | a pull request: its state, base, head and sha, its checks, labels and assignees, and whom a review is waiting on (`requestedReviewers`, `requestedTeams`) |
| `github.<org>.<name>.run.<id>` | a workflow run: its status, conclusion, branch and sha |
| `github.<org>.<name>.alert.<n>` | with `-alerts`, a Dependabot alert: its `state` (`open`, `fixed`, `dismissed`, `auto_dismissed`), the advisory's `severity` and `summary`, the `package` and its `ecosystem` and `manifest`, the `vulnerableRange`, the `patched` version where there is one, and `ghsa` and `cve` |
| `github.<org>.<name>.comment.<n>.<id>` | a comment, on the pull request or issue that `on` names. `kind` is `conversation` or `review`, one on a line of the diff, which also says where it is and what it answers: `path`, `line`, `startLine`, `startSide`, `side`, `commitId`, `inReplyTo` (its thread's first comment, on a reply), and `diffHunk`, capped and then marked `diffHunkTruncated: true` |

`requestedReviewers` holds a person until they submit a review, and again when one is requested
of them anew, so a login in it means the pull request is waiting on that person. Their reviews
are not reflected: a task reads them with `github-read-pr`.

A review comment's `diffHunk` ends at the line it is on, so a task asked to address "rename
this" reads what "this" is. The iod keeps the hunk's last 20 lines, or a multi-line comment's
lines and 3 before them ([what github reflects](../reference/iod.md#what-github-reflects)). An
outdated comment has no `line`, and its hunk is the code it was on. A reply's `inReplyTo` is the
thread's first comment, which `github-reply-comment` answers in.

A rule reacts to a pull request as it reacts to any data. Its `checks` is what CI last said
about its head: the newest run's conclusion (`success`, `failure`) or what it is still doing
(`in_progress`). Here, an open pull
request whose checks failed:

```tony
condition:
  subject:
    path: "github.acme.app.pr.*"
    value: {state: open, checks: failure}
```

[iod github](../reference/iod.md#github) has the flags and the whole payload. The issues are
the [`github-issue`](github-issue.md) system's.

## What a charter writes

Comments, reviews, merges, a commit status, a new pull request. With `-out`, the iod carries a
rule's writes back to GitHub. A rule writes an input under `github.<org>.<name>.out`, or in an
outbox its package declares, and the iod does what the input says
([the out half of github](../reference/iod.md#the-out-half-of-github)). So a rule can merge a
pull request once its checks pass, or comment to say why not, with no task.

## What a task can do

A task gets the forge's pull requests with a `github` block for a repository its `git` block
checks out, where the runner's forge is GitHub. The block's verbs and shapes say which pull
requests are within reach; the autonomy decides among them.

```tony
spawn:
  agent: {name: verse}
  prompt: "Make the failing check on $[subject.ref] pass, and open a pull request."
  environment:
    systems:
      network: {mode: open}
      git:
      - repo: acme/app
        from: main
        fetch: {}
        push:
        - {ref: !glob "refs/heads/verse/auto/*", exists: false}
        - {ref: !glob "refs/heads/verse/auto/*", own: true}
      github:
      - repo: acme/app
        create-pr: {base: main, own: true}
  autonomy:
  - {match: {system: github, verb: create-pr}, then: ask}
  - {then: allow}
```

`draft` is whether the pull request opens as a draft, `true` or `false`. A shape naming
`draft: true` (`create-pr: {base: main, own: true, draft: true}`) gives a task drafts only: it
can propose, and a person marks the proposal ready.

`own` on a pull request is the runner's word that the head's tip is a commit this task made, as
it is on a push ([git](git.md#what-a-task-can-do)). The runner refuses a pull request from a
branch this run did not push whatever the rows say.

| Tool | What it does | Step |
|---|---|---|
| `github-create-pr` | opens a pull request from a branch the task pushed into a base, as a draft when it says `draft: true`; if one is already open from that branch, it finds that one | `github.create-pr`, with `repo`, `base`, `head`, `draft` |
| `github-update-pr` | changes the open pull request from a branch the task pushed: its title, body or base. What it does not name stays as it is. | `github.update-pr`, with `repo`, `head`, and `base` when it moves the base |
| `github-list-prs` | reads the open pull requests, most recently changed first: each one's number, title, author, branches, last change, and whom a review is requested from. It stops at 500 and says so. | `github.list-prs`, with `repo` |
| `github-read-pr` | reads one pull request by its number: its fields, whom a review is requested from, the reviews given so far, what CI says of its head (check runs and commit statuses: failing and unfinished by name and link, passing and skipped counted), and its description | `github.read-pr`, with `repo`, `pr` |
| `github-comment-pr` | comments on a pull request's conversation, by its number, open, closed or merged. A number that is an issue is refused, naming `github-issue-comment`, and one that is nothing is refused as no pull request. | `github.comment-pr`, with `repo`, `pr` |
| `github-reply-comment` | replies in the thread of an inline review comment, by the pull request's number and the comment's id. A reply to a reply goes into its thread, and the answer names both: `replied in the thread of comment 900 (902 is a reply in it)`. A conversation comment has no thread and is refused, naming `github-comment-pr`; so is a comment whose thread is on another pull request than `pr`, and an id that is no comment. | `github.reply-comment`, with `repo`, `pr`, `comment` |

A pull request must come from a branch this run pushed. A pull request from any other branch
is refused. Its commits carry the task's trailers, so the iod's copy of the pull request can be
tied to the task that made it. A script calls the same tools with `verse-call-tool`.

The reads are steps too, as a Slack read is: what the forge holds is not the task's until it is
read, and a row can keep a task from it. A description and a review are other people's words,
and they come into the task's context as written. A pull request that is not there is refused,
and the task re-plans on it.

A block that names no verbs gives every verb, the reads and the writes alike. A task that only
reads names them: `github: [{repo: acme/app, read-pr: {}}]`.

A comment carries its pull request in its step, and a reply its comment's id as the task gave
it, so a row or a shape can hold a task to one pull request. A task woken by a review comment
on pull request 71 can answer there and nowhere else:

```tony
github:
- repo: acme/app
  read-pr: {pr: 71}
  comment-pr: {pr: 71}
  reply-comment: {pr: 71}
```

or, with a block that gives more, by its rows:

```tony
autonomy:
- {match: {system: github, verb: comment-pr, pr: 71}, then: allow}
- {match: {system: github, verb: reply-comment, pr: 71}, then: allow}
- {match: {system: github}, then: ask}
- {then: allow}
```

The row decides on the `pr` the task named, before the runner reads anything at the forge. The
runner then checks that the comment's thread is on that pull request, and refuses a reply whose
thread is on another, so a task held to 71 cannot reply on 72 by naming 72's comment.

A pull request's conversation is an issue's at GitHub, but a comment on it is this system's
verb: `github-issue-comment` refuses a pull request's number, naming `github-comment-pr`
([github-issue](github-issue.md)).

## Where the credential lives

**The iod** needs a fine-grained token or a GitHub App for the repositories it reads. For its
out half, it needs write access on pull requests and issues.

**The task** holds no credential. The runner opens, updates, reads and comments on pull requests
with its token for the forge, `GITHUB_TOKEN`. For a GitHub Enterprise Server, set `repos.api: https://<host>/api/v3`
in the runner's configuration, and `repos.host` to the host.

**The operator** sets a limit on what any task may propose, in the runner's configuration, under each doer whose tasks may use it:

```tony
agents:
  claude:
    systems:
      github:
        ceiling: {repo: !regexp "^github\\.com/acme/", create-pr: {base: main}, update-pr: {}}
```

Verse refuses to install a spawn whose environment gives proposals beyond the ceiling, saying
what the ceiling gives; the runner holds each proposal at the act to the environment, the row,
and then the ceiling.

`comment-pr` and `reply-comment` write at the forge, and a ceiling gives them as it gives any
verb. A ceiling that lists its verbs, as the one above, lacks them until the operator adds them
(`comment-pr: {}, reply-comment: {}`). A ceiling block that names no verb
(`github: {ceiling: {}}`) allows every verb of the system, so an operator who wrote one gives
tasks these two writes as well, with no change to the configuration.
