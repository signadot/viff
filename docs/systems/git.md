# git

The `git` system is a repository that a task has checked out. The runner fetches the repository
from the forge into the task's workspace, on a branch of the task's own. The verbs of `git` act
on the remote. The runner performs them with its credential, and the task never holds it.

## What a charter sees

`iod git` writes a repository's refs, its head and its commits into the store:

| Entity | What it is |
|---|---|
| `git.<repo>.ref.<name>` | a branch or a tag: its sha, and the tree of each directory the iod names |
| `git.<repo>.head` | where the repository's checkout stands, and whether it is dirty |
| `git.<repo>.commit.<sha>` | a commit: its message, author, parents and trailers |

A rule reacts to a ref that moves as it reacts to any entity:

```tony
condition:
  subject:
    path: "git.app.ref.main"
  on: {sha: changed}
```

[iod git](../reference/iod.md#git) has the flags and the whole payload.

## What a charter writes

Refs. With `-out`, the iod carries a rule's writes back to the repository. A rule writes an
input under `git.<repo>.out`, and the iod makes the tag or moves the branch. The iod also
installs the charters that a tree holds
([the out half of git](../reference/iod.md#the-out-half-of-git)). So a rule can tag a commit
that passed, or move a branch forward, with no task.

## What a task can do

A task gets a repository with `with.git`. The runner checks the repository out from the branch
that `from` names, onto a branch of the task's own. That is a fact of the spawn, not a rule.
The policy says what may leave:

```tony
spawn:
  doer:
    agent: verse
    with:
      git: [{repo: acme/app, from: main}]
      network: {mode: open}
  prompt: "Make the failing check on $subject pass."
  policy:
  - {match: {system: git, verb: push, ref: refs/heads/main}, then: deny}
  - {match: {system: git, verb: push, ref: "refs/heads/verse/auto/*", exists: "false"}, then: allow}
  - {match: {system: git, verb: push, ref: "refs/heads/verse/auto/*", own: "true"}, then: allow}
  - {match: {system: git, verb: push}, then: ask}
  - {then: allow}
```

Two of a push's fields are the runner's to fill, from the forge: `exists`, whether the branch is
there before the push, and `own`, whether its tip is a commit this task made (every commit a
task delivers carries its trailers). They are how a row keeps a task to its own branches: the
second row above allows a branch the task makes, the third one it made, and another task's
`verse/auto/*` branch is neither, so a push onto it, a rewrite of it or its deletion falls to
the row after. A row with no `exists` or `own` decides by the ref alone, so `{ref:
"refs/heads/verse/auto/*"}` lets a task push onto every task's branch; the merge train's
`delete: "true"` row is written that way on purpose, to retire branches that landed.

| Tool | What it does | Step |
|---|---|---|
| `git-push` | pushes the task's committed work to a branch of the remote, or deletes a branch there; fast-forward unless `force`, and a branch this run created is its own to rewrite | `git.push`, with `repo`, `ref`, `delete`, `force`, and `exists` and `own`, which the runner fills |
| `git-fetch` | updates the checkout's view of the remote: the `mirror/*` refs move, and nothing of the task's own is touched | `git.fetch`, with `repo` |

A push sends what the task **committed**, and nothing it left uncommitted. Each commit carries
trailers that name the task. The runner first puts the work in its own mirror, so a push that
does not reach the remote is tried again, not lost. In a row, `repo` is the repository as `host/owner/name` in lower
case, and `ref` is a full ref such as `refs/heads/main`, however the spawn wrote them. A `*`
stays within one `/`: `refs/heads/*` is every branch, and `**` every ref. A script calls the same
tools with `verse-call-tool`, and the same rows decide them.

## Where the credential lives

**The iod** needs no credential for a repository on disk. For a mirror of a private remote, it
uses the git credential that git finds where the iod runs.

**The task** holds no credential. The runner fetches and pushes from its own mirror, with its
token for the forge. The token is in the variable that `systems.git.credential` names in the
runner's configuration, or in `GITHUB_TOKEN`.

**The operator** sets a limit on what any task may push, in the runner's configuration. A
repository on another forge than the runner's is refused.

```tony
repos:
  mirrors: /var/lib/verse-runner/mirrors
systems:
  git:
    ceiling:
    - {match: {verb: push, ref: refs/heads/release/**}, then: deny}
    - {match: {repo: "github.com/acme/**"}, then: allow}
    - {then: deny}
```

Verse refuses to install a spawn whose policy allows or asks about pushes the ceiling refuses
every one of, naming the policy's row and the ceiling's. A row wider than the ceiling installs:
what is fixed at install is that the task has `git.push` at all, and each push is decided at the
act, by the row and then by the ceiling, which the runner reads again
([the verse runner](../reference/runnerd.md#what-it-offers)).
