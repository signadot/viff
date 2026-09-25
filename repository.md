# Repository

## Documentation

Design and documentation on `main` are authoritative.  Documentation lives
mostly under `docs/` as markdown files presented by
[mkdocs](https://www.mkdocs.org/); use `mkdocs serve` to view in a browser.

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

Centralised or coordinated issue tracking with git issue is opt-in.  So,
the git issues may be partial.  Where they exist, they track

- design asks, sketches, documents, and residuals of implemented designs (which are often also bugs)
- implementation plans
- state of work (see branches below)

An issue travels with `git issue push`/`pull`, independent of branch rebases, merges etc
and is reflected into verse like any other issue.

## Branches

### `main`

`main` is ff-only and in normal operation is used for deployments to shared
instances.  Intended to reflect group disposition.

### `verse/*`

`verse/*` branches are expected to contain shared work.

`verse/*` branches are articulation points for merging to main, as
how work lands is generally iterative, eg final docs land after implementation,
prerequisites that fix defects land before, the branch is rebased on `main` or merges
`main` to re-sync work.  All of these are articulation points with `main`.

Each `verse/*` branch is encouraged to have a git issue tracking its status: titled with the
branch name and labelled `branch`.

```
git issue create verse/<name>        # once, when the branch is pushed
git issue label <id> branch
```

At each articulation point, comment on it with where the branch stands and link
the commit.

```
git issue comment <id> "<status>"    # at each articulation point
git issue link <id> <commit>
```

Show git issues for `verse/*` branches.
```
git issue list --label branch        # verse/* branches using this mechanism and their status
```

## Commits

Optional: a commit may declare what it is with git trailers, so the log can be
queried for it.

```
Tier: change | contract | docs | design      # omit for an ordinary fix, comma separated
Checked: <what you ran>
Breaks: <what someone on main must do differently>
```

`contract` is a change that is not backward compatible, and is the one worth
declaring, with `Breaks:`, and a `!` after the subject's prefix (`api!: ...`).
Declare it where the work lands on `main`, not on each branch commit.

Trailers parse only as the final paragraph of the message, every line in it a
trailer (`Co-Authored-By:` included).  Check with
`git log -1 --format='%(trailers)'`.

**Agents.** A coding agent appends `Co-Authored-By:` by habit, often as the only
trailer and often after a blank line that leaves the others as prose.  If an
agent commits for you and you want the trailers, tell it to check them on the
commit with the command above.
