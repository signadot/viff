# A push is a capability with destinations, not one canned act

Status: proposal, nothing here runs. Found while writing
[merge-gate.tony](merge-gate.tony), whose rules cannot work as written — and the first fix I
reached for was wrong in a way worth recording, because it is the same mistake one layer out.

## What is there

The coding runner's delivery is already argued for correctly. `deliver.go`:

> That channel is NOT the task-control contract and does not pretend to be. It is **a capability
> the parent holds and the task may ask it to exercise** — the same shape as the egress proxy,
> and with the same reasoning about what makes it safe: the task can ask, and asking cannot
> redirect anything. Where the work goes is the environment's to say, the trailers are the
> runner's to write, and the token is never on this side.

Every clause of that is right and none of it is what is implemented. The protocol is

```go
type deliverRequest struct {
	Op     string
	Result string
}
```

— one op, no target. A capability with exactly one thing to ask for is not a capability, it is a
button. And the reason it is a button is good: **the task must not choose where the work goes**,
because a task that can name a ref can push anywhere, and the credential is on the other side
precisely so it cannot.

## What is wrong

The button admits one flow. Anything else — merge this branch into main, retire a branch that can
no longer land, move a promotion ref — has no way to be asked for, so each one needs its own op:
`merge`, `retire`, `promote`. Three buttons, then four. Each is a new thing to design, gate,
audit and document, and each bakes a policy into a mechanism the way `deliver` bakes in "push my
working branch to my auto ref".

That is the shape `task.Finisher` already argued against one layer down — *"a hook is a fourth
thing to supervise, and an action is already supervised"*. A menu of ops is a menu of hooks.

And verse's own vocabulary is richer than the mechanism it supervises. `task.Push` is a verb,
`GateMatch.OnPush` matches it, `GateMatch.Branches` globs the ref, `RepoContext.PushTarget` and
`NextRef` name destinations. A charter can already say *"ask before anything touches a release
branch"* — about an action no task can propose.

## What to do instead

**The environment declares DESTINATIONS; the request names one; the parent resolves it.**

```tony
environment: verse-fix
destinations:
- name: auto            # what `deliver` means today
  ref: "refs/heads/verse/auto/$task"
- name: main
  ref: "refs/heads/main"
  ffOnly: true          # a property of the DESTINATION, enforced in Go
```

- The task proposes an action naming a destination by NAME, never a ref. "Where the work goes is
  the environment's to say" survives intact: the environment says which destinations exist and
  what each permits, and a name the environment does not declare is refused before anything is
  gated.
- Deriving stamps it `Verb: Push` with `Resource{KindGitRef, <resolved ref>}`, so the vector gates
  it on the ref it will actually touch — which is what makes `OnPush` and `branches:` mean
  something for the first time.
- The confined executor forwards it over fd 5, as it forwards nothing else today. The credential
  does not move: the request crosses the boundary, never the token.
- The parent resolves the name, enforces the destination's rules, pushes, and answers with the
  refs.

`deliver` stops being special: it is the finisher proposing a push to `auto`. A merge to `main` is
the same action with a different destination, gated ASK because a charter said so. Retiring a
branch is a delete against a destination that allows it.

**`ffOnly` moves from a prompt to the runner.** In the merge-gate charter it is currently a
sentence — *"`--ff-only` is not optional and must not be worked around"* — with the model as the
party enforcing it, which is the wrong party. As a destination property it is a line of Go, and a
model that wanted to merge some other way has nothing to call. That is the single biggest thing
this buys, and it is why the fix belongs here rather than in the charter.

## What it costs, and what it must not break

It touches `deliver.go` (both sides), `taskproc.go`, the environment config, and the harness's
tool set. The properties that must survive are the ones the current design was built for, and
none of them is what changes:

- the token is never in the sandbox — only a request crosses;
- the task cannot name a ref, only a declared destination;
- the channel is an inherited connection rather than a path, so the agent's shell cannot reach it
  and call it directly, bypassing the gate;
- the trailers stay the runner's to write.

## What it unblocks

[merge-gate.tony](merge-gate.tony) and its
[residual](merge-gate-residual.md), which is where this was found: an agent's branch becomes a
proposal, a gate decides it, and the stale ones retire — none of which a task can ask for today.
