# pkg: two runners with different package managers look the same to verse

## The problem

A spawn's `pkg` shapes are written against one package index, and nothing in the spawn or in a runner's offer says which.

Every stock runner's offer for `pkg` says it builds `{pkg: null, as: null}`: any package, under any name (`pkgBuilds`, `pkg/runner/runnerd/stock/pkgconfig.go`). The manager and the index are not in it. Today that is harmless, since every runner that performs `pkg` wraps apt over a Debian index. Once two runners perform `pkg` with different managers (say apt on one, brew or apk on another), or with different suites of one manager, verse cannot tell them apart, and a spawn is within both offers.

## What happens then

Take a spawn whose environment says `pkg: {install: {pkg: jq, version: "1.7.1-1"}}`, written for a bookworm runner, taken by a runner with another index:

- **A pinned install fails at launch.** That index has no `jq 1.7.1-1`, and the launch is refused naming the package. The spawn was accepted where it was written and fails where it runs.
- **An unpinned install succeeds with something else.** `{pkg: jq}` gives whatever that index calls `jq`, at its own version, with its own dependencies. Nothing reports the difference.
- **A pattern admits a different set.** `{pkg: !glob "lib*"}` matches different packages on each runner; the spawner had one index in mind.
- **Ceilings do not help.** Each operator's ceiling bounds its own runner. Neither says which manager a spawn meant.
- **A person approving cannot tell.** `pkg.install pkg=jq` reads the same on both.

## What would fix it

Both sides have to say which index they mean:

- **The offer names its manager and index**, for example manager `apt`, suite `bookworm`, architecture `amd64`, as part of what the runner builds.
- **A spawn's `pkg` block names the manager it was written for**, as an attribute of the block, the way a `git` block names its `repo`.

Then verse can refuse, or route, a spawn whose block no offer matches, at the place the spawn is written instead of at launch.

The shape of both is open: which of manager, suite and architecture belong in the attribute, and whether a block that names none means "any" or is refused once a second manager exists.

## Until then

With two managers deployed, the only control is operator convention: keep them apart by doer or by deployment, so a spawn naming one agent can reach runners with one manager only. Verse does not check that.

## Not in scope

Two managers on one runner. That needs more (the cellar's key, the order of two install sources on PATH) and does not look worth building.

## Where this came from

The review of PR #8 (the `pkg` system, issue `2k5art3xh12ksjcyy1n0`): its question "what's the stock runner's pkg, and what's pkg in general?" and its idea for later, "the offer could name its index". `docs/systems/pkg.md` now says the stock runner performs `pkg` with apt on a Debian or Ubuntu image and that another runner may perform it otherwise; this issue is the part that sentence does not solve.