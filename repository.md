# Repository

## Git Issues

This repository contains git issues, an in-repository distributed
issue tracker.

```
go install github.com/signadot/tony-format/go-tony/cmd/git-issue@latest
```

It is a git plugin, so `git issue x` calls `git-issue x` resolved
via your $PATH.

```
git issue pull # gets remote issues, merging with local
git issue push [<xidr>] [-all | -n ] # pushes your local changes to remote origin
git issue show|create|comment|label|unlabel|close|list|... # operations on your local view of the issues
```

## Branches

### `main`

`main` is ff-only and in normal operation is used for deployments to shared
instances.  Intended to reflect group disposition.

### `verse/*`

`verse/*` The expectation of these branches are that they contain shared work.

`verse/*` branches are articulation points for merging to main, as
how work lands is generally iterative, eg final docs land after implementation,
prerequesites that fix defects land before, the branch is rebased on `main` or merges
`main` to re-sync work.  All of these are articulation points with `main`.

Each `verse/*` branch has one git issue tracking its status: titled with the
branch name and labelled `branch`.  At each articulation point, comment on it
with where the branch stands and link the commit.

```
git issue create verse/<name>        # once, when the branch is pushed
git issue label <id> branch
git issue comment <id> "<status>"    # at each articulation point
git issue link <id> <commit>
git issue list --label branch        # every verse/* branch and its status
```

An issue travels with `git issue push`/`pull`, survives a rebase of the
branch, and is reflected into verse like any other issue.



## Commits

Optional: a commit may declare what it is with git trailers, so the log can be
queried for it.

```
Tier: change | contract       # omit for an ordinary fix
Checked: <what you ran>
Breaks: <what someone on main must do differently>
```

`contract` is a change that is not backward compatible, and is the one worth
declaring, with `Breaks:`, and a `!` after the subject's prefix (`api!: ...`).
Declare it where the work lands on `main`, not on each branch commit.

Trailers parse only as the final paragraph of the message, every line in it a
trailer (`Co-Authored-By:` included).  Check with
`git log -1 --format='%(trailers)'`.
