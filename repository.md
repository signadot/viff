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

At each point, a git note on the corresponding `verse/*` branch commit is
encouraged to help track the status of the branch.


