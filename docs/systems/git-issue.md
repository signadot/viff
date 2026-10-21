# git-issue

[`git issue`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/cmd/git-issue) keeps a
repository's issues inside the repository, as refs, so the issues travel with the code. The
`git-issue` system is that tracker.

## What a charter sees

`iod git-issue` writes each issue into the store as the entity `git-issue.<repo>.issue.<id>`:
its `title`, its `status` (`open` or `closed`), its labels, its body and its discussion. A rule
reacts to an issue as it reacts to any entity:

```tony
condition:
  subject:
    path: "git-issue.app.issue.*"
    value: {status: open, keys: {triage: needed}}
```

[iod git-issue](../reference/iod.md#git-issue) has the flags and the whole payload.

## What a charter writes

Nothing directly. This iod has no out half: it reads the tracker and does not write to it. A
charter changes the tracker as a person does. It spawns a task, and the task files, comments
and closes in a checkout, then pushes.

## What a task can do

A task gets a repository's tracker with a `git-issue` block for a repository its `git` block
checks out. A block that names no verb gives all of them.

```tony
spawn:
  agent: {name: verse}
  prompt: "File an issue for each TODO in the README, labelled triage=needed."
  environment:
    systems:
      network: {mode: open}
      git: [{repo: acme/app, fetch: {}}]
      git-issue: [{repo: acme/app}]
  autonomy:
  - {match: {system: git-issue, verb: push, repo: github.com/acme/app}, then: ask}
  - {then: allow}
```

| Tool | What it does | Step |
|---|---|---|
| `git-issue-create` | files an issue in the checkout's tracker, a write there as a commit is | `git-issue.create`, with `repo` |
| `git-issue-comment` | comments on an issue there, by its id | `git-issue.comment`, with `repo` and `issue` |
| `git-issue-close` | closes an issue there, and names the commit that closed it if there is one | `git-issue.close`, with `repo` and `issue` |
| `git-issue-pull` | brings the remote's tracker into the checkout | `git-issue.pull`, with `repo` |
| `git-issue-push` | sends every issue filed or changed in the checkout to the remote | `git-issue.push`, with the param `repo` |

Only a push leaves the task. So the autonomy above lets the agent file, comment, close and pull
freely, and asks before the issues reach `github.com/acme/app`; the `git` block gives no push, so
the task has no `git-push` at all. In a row or a shape, `repo` is the repository as
`host/owner/name`, however the spawn named it. A script calls the same tools with
`verse-call-tool`, and the same environment and rows decide them.

## Where the credential lives

**The iod** needs no credential for a repository on disk. For a mirror of a private remote, it
uses the git credential that git finds where the iod runs, as `iod git` does.

**The task** holds no credential. It files, comments and closes in its checkout. The runner
makes each pull and push from its own mirror, with its git token: the variable that
`systems.git.credential` names in the runner's configuration, or `GITHUB_TOKEN`.

**The operator** sets a limit on what any task may do with the tracker, in the runner's
configuration:

```tony
systems:
  git-issue:
    ceiling: {repo: "github.com/acme/**", create: {}, comment: {}, close: {}, pull: {}, push: {}}
```

Verse refuses to install a spawn whose environment gives a tracker of another repository. The
refusal says what the ceiling gives ([the verse runner](../reference/runnerd.md#what-it-offers)).
