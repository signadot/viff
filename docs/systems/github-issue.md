# github-issue

The `github-issue` system is the issues of a repository at GitHub. Each verb is a call that the
runner makes to GitHub with its credential for the repository.

## What a charter sees

`iod github` writes each issue into the store as the data at `github.<org>.<name>.issue.<n>`:
its `title`, its `state`, its `labels` (a list of names, absent when there are none), its body
and its comments. A rule reacts to an issue as it reacts to any data, here to an open issue
nobody has labelled yet:

```tony
condition:
  subject:
    path: "github.acme.app.issue.*"
    value: !and [{state: open}, !not {labels: null}]
```

[iod github](../reference/iod.md#github) has the flags and the whole payload.

## What a charter writes

Comments, a new issue, a closed issue. With `-out`, the iod carries a rule's writes back to
GitHub. A rule writes an input under `github.<org>.<name>.out` with `to: issue.<n>`, and the
iod does what it says ([the out half of github](../reference/iod.md#the-out-half-of-github)).
So a rule can comment when it labels an issue, or close an issue once its pull request merged,
with no task.

## What a task can do

A task gets the forge's issues with a `github-issue` block for a repository its `git` block
checks out, where the runner's forge is GitHub.

```tony
spawn:
  agent: {name: viff}
  prompt: "File an issue for each TODO in the README, and close each open TODO issue whose TODO is gone."
  environment:
    systems:
      network: {mode: open}
      git: [{repo: acme/app, fetch: {}}]
      github-issue: [{repo: acme/app, create: {}, close: {}}]
  autonomy:
  - {match: {system: github-issue, verb: close}, then: ask}
  - {then: allow}
```

| Tool | What it does | Step |
|---|---|---|
| `github-issue-create` | opens an issue at the repository, with a title and body; the task reads back its number and url | `github-issue.create`, with `repo` |
| `github-issue-comment` | comments on an issue there, by its number. A pull request's number is refused, naming `github-comment-pr`. | `github-issue.comment`, with `repo`, `issue` |
| `github-issue-close` | closes an issue there, by its number. A pull request's number is refused: this system does not close a pull request. | `github-issue.close`, with `repo`, `issue` |

GitHub keeps a pull request's conversation as an issue's, so its issue routes take a pull
request's number too. The runner reads the number before a comment or a close, and refuses one
that is a pull request: a comment on a pull request is the [`github`](github.md) system's
`comment-pr`, and `github-issue` closes no pull request. A number that is nothing is refused as
no issue. So a doer given `github-issue` and no `github` block cannot comment on a pull
request, as it could before.

Every verb is a step, because each one uses the runner's credential. So an environment can leave
any of them out (above, `comment`), and an autonomy can refuse or ask about any it gives. In a
row or a shape, `repo` is the repository as `host/owner/name`, and `issue` is the number. A
script calls the same tools with `viff-call-tool`, and the same environment and rows decide
them.

## Where the credential lives

**The iod** needs a token or a GitHub App with write access on issues for its out half.

**The task** holds no credential. The runner files, comments and closes with its token for the
forge, `GITHUB_TOKEN`. It calls the API of `github.com`, or the one that `repos.api` names.

**The operator** sets a limit on what any task may do with a repository's issues, in the
runner's configuration, under each doer whose tasks may use it:

```tony
agents:
  claude:
    systems:
      github-issue:
        ceiling: {repo: !regexp "^github\\.com/acme/", create: {}, comment: {}, close: {}}
```
