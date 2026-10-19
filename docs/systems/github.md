# github

The `github` system is GitHub as the forge of a repository that a task checked out. Its verbs
are about pull requests, which belong to the forge and not to git. The runner performs them
with its credential for the repository.

## What a charter sees

`iod github` writes a repository at GitHub into the store: its pull requests, issues,
discussions, workflow runs and comments.

| Entity | What it is |
|---|---|
| `github.<org>.<name>.pr.<n>` | a pull request: its state, base, head and sha, its checks, labels and assignees |
| `github.<org>.<name>.run.<id>` | a workflow run: its status, conclusion, branch and sha |
| `github.<org>.<name>.comment.<n>.<id>` | a comment, on the pull request or issue that `on` names |

A rule reacts to a pull request as it reacts to any entity. Its `checks` is what CI last said
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

A task gets the forge with the repository. `with.git` checks the repository out, and where the
runner's forge is GitHub, `github-create-pr` and `github-update-pr` come with it. Nothing else in the spawn names
the forge.

```tony
spawn:
  doer:
    agent: verse
    with:
      git: [{repo: acme/app, from: main}]
      network: {mode: open}
  prompt: "Make the failing check on $subject pass, and open a pull request."
  policy:
  - {match: {system: git, verb: push, ref: "refs/heads/verse/auto/*", exists: "false"}, then: allow}
  - {match: {system: git, verb: push, ref: "refs/heads/verse/auto/*", own: "true"}, then: allow}
  - {match: {system: github, verb: create-pr, base: main, own: "true"}, then: ask}
  - {match: {system: git, verb: push}, then: deny}
  - {then: allow}
```

`own` on a pull request is the runner's word that the head's tip is a commit this task made, as
it is on a push ([git](git.md#what-a-task-can-do)). The runner refuses a pull request from a
branch this run did not push whatever the rows say.

| Tool | What it does | Step |
|---|---|---|
| `github-create-pr` | opens a pull request from a branch the task pushed into a base; if one is already open from that branch, it finds that one | `github.create-pr`, with `repo`, `base`, `head` |
| `github-update-pr` | changes the open pull request from a branch the task pushed: its title, body or base. What it does not name stays as it is. | `github.update-pr`, with `repo`, `head`, and `base` when it moves the base |

A pull request must come from a branch this run pushed. A pull request from any other branch
is refused. Its commits carry the task's trailers, so the iod's copy of the pull request can be
tied to the task that made it. A script calls the same tools with `verse-call-tool`.

## Where the credential lives

**The iod** needs a fine-grained token or a GitHub App for the repositories it reads. For its
out half, it needs write access on pull requests and issues.

**The task** holds no credential. The runner opens and updates pull requests with its token for
the forge: the variable that `systems.git.credential` names in its configuration, or
`GITHUB_TOKEN`. For a GitHub Enterprise Server, set `systems.github.api: https://<host>/api/v3`
in the runner's configuration, and `repos.host` to the host.

**The operator** sets a limit on what any task may propose, in the runner's configuration:

```tony
systems:
  github:
    ceiling:
    - {match: {verb: create-pr, base: main}, then: allow}
    - {match: {verb: update-pr}, then: allow}
    - {then: deny}
```

Verse refuses to install a spawn whose policy allows or asks about proposals the ceiling refuses
every one of, naming the policy's row and the ceiling's; a wider row installs, and the runner
decides each proposal at the act, by the row and then by the ceiling.
