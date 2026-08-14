# A push is a capability with destinations, not one canned act

Status: proposal, nothing here runs. Found while writing [merge-gate.tony](merge-gate.tony),
whose rules cannot work as written — and the first fix reached for was wrong in a way worth
recording, because it is the same mistake one layer out.

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
no longer land, move a promotion ref — has no way to be asked for.

The obvious repair is a second op, then a third: `merge`, `retire`, `promote`. **That repair is
the actual defect, and the reason is not aesthetic.** Each op is a new parser, a new
authorization decision, a new thing to review and audit, and it bakes a policy into a mechanism
the way `deliver` bakes in "push my working branch to my auto ref". A protocol that grows one op
per flow is a complexity multiplier, and complexity multiplication is itself a security hazard —
a bigger one, at that growth rate, than the thing the button was protecting against.

It is also the shape `task.Finisher` already argued against one layer down: *"a hook is a fourth
thing to supervise, and an action is already supervised."* A menu of ops is a menu of hooks.

Meanwhile verse's own vocabulary is richer than the mechanism it supervises. `task.Push` is a
verb, `GateMatch.OnPush` matches it, `GateMatch.Branches` globs the ref, `RepoContext.PushTarget`
and `NextRef` name destinations. A charter can already say *"ask before anything touches a release
branch"* — about an action no task can propose.

## What to do instead

**The parent hands out CREDENTIALS, never performs OPERATIONS — and hands one over when the gate
approves, chosen by what was approved.**

The environment declares credentials by name and destinations that carry them. Credentials are
OPERATOR-SUPPLIED and read from the environment exactly as `TokenAuth` does today — nothing here
creates one:

```tony
environment: verse-fix
credentials:
- name: gh-verse
  env: GITHUB_TOKEN       # a PAT somebody made by hand; verse never mints and never rotates
destinations:
- name: auto              # what `deliver` means today
  ref: "refs/heads/verse/auto/$task"
  credential: gh-verse
- name: main
  ref: "refs/heads/main"
  credential: gh-verse
```

1. The task proposes an action naming a destination. "Where the work goes is the environment's to
   say" survives intact: a name the environment does not declare is refused before anything is
   gated.
2. Deriving stamps it `Verb: Push` with `Resource{KindGitRef, <resolved ref>}`, so the vector
   gates it on the ref it will actually touch — which is what makes `OnPush` and `branches:` mean
   something for the first time.
3. **On approval, and only on approval, the parent sends the credential that destination names**,
   over the socket it already holds. The gate is not advisory: the task has no credential until
   the vector says yes, so approval is what releases the capability.
4. The task runs git itself. There is no operation-shaped RPC, so the protocol never grows: a new
   flow is a destination in config, and the marginal protocol cost of every future flow is zero.

`deliver` stops being special — it is the finisher asking for `auto`. A merge is the same action
with a different destination. Retiring a branch is a delete against a destination that allows one.

### The mapping is the mechanism, and it is not about git

What generalises is **capability grant → named credential**, declared rather than derived. A
destination is one kind of capability; an egress rule that needs an API key is another. The
runtime's job is to know which secret a granted capability carries and to release it at the
moment the grant is exercised — not to know what the secret is for.

That is what keeps this from being a git feature. Nothing in the mechanism reads a ref.

### Not minted, deliberately

An earlier draft had the parent MINT a short-lived credential scoped to the approved destination.
It is out, on an operational constraint that is worth stating as a constraint rather than a
compromise: the credentials here are hand-made GitHub PATs, replacing that costs real time, and
giving anything in the system the authority to mint PATs automatically is a larger thing to
hand over than the problem it solves. A system that can create credentials is a system whose
compromise creates credentials.

So the mechanism maps grants to credentials that already exist, and what a credential permits is
whatever the operator gave it. **This is the design, not a placeholder** — if minting ever becomes
cheap on the forge in use, it narrows the window below and changes nothing else here.

### What coarse scoping means, concretely

A PAT scopes per repository and per permission. It does not scope per ref. So `auto` and `main`
in the same repository cannot be told apart by the credential, and releasing the credential for
an approved push to `auto` releases the ability to push `main` with it.

That is not a hole to be plugged here. It is the thing the forge's own rules are for, and the
next section is why that is the right place for it rather than a concession.

## Who enforces what, and why verse does not want the job

Splitting this out because it is the part that is easy to assume and expensive to get wrong.

| | enforced by |
|---|---|
| **whether** — may this happen at all | the vector, at the gate. Load-bearing: no approval, no credential. |
| **which secret** — what is released | the environment's capability → credential mapping. |
| **where** — which refs a credential may touch | the forge: token scope, and repository rules about who may push what. |
| **how** — fast-forward only | **the receiving end.** `receive.denyNonFastForwards`, or the forge's equivalent rule. |

**verse should not compete with git server configuration; it should work with whatever is
already there.** That is an overarching requirement rather than a preference about this feature,
and it settles the two rows at the bottom.

A repository's rules about its own history are not verse's to hold. Humans push to these
repositories too, and an invariant enforced by the receiver applies to all of them, while one
enforced by verse applies to the actors that went through verse — which is the smaller set and
the one that was never the worry. Duplicating the rule would give two places to configure it, two
places for them to disagree, and a verse approval that implies a safety it cannot actually
provide.

So `--ff-only` is a prompt-level instruction backed by `receive.denyNonFastForwards`, and a model
that ignores the instruction gets refused by the remote rather than merging some other way. That
the docket does not display that rule is correct: the docket shows a DECISION, and the receiving
end shows a RULE. Conflating them would be verse claiming to be the authority on somebody else's
repository.

One interaction to configure deliberately: `receive.denyDeletes` blocks the sweep. A destination
that exists to be retired needs a remote that permits deletes on that ref pattern.

## Where a secret already goes, and what actually contains it

Checked rather than assumed, because the design rests on it.

**Nothing crosses the network in the clear.** The egress proxy handles CONNECT by hijacking the
connection and piping bytes — *"CONNECT is the one that matters — everything the task does over
TLS"* — so the task's TLS session runs end to end to `api.portkey.ai` and the proxy sees a
hostname and a port. The parent's own push is go-git over HTTPS with `BasicAuth`. Both secrets
travel inside TLS, and a push credential taking the same route adds no plaintext hop.

**A secret already enters the sandbox, by IPC.** `childConfig.LLM` carries the Portkey key and
virtual key as VALUES over stdin, deliberately — *"the parent does the lookup because the parent
is where the environment is, and sending values means the child needs no inherited environment at
all"*. So "the token is never on this side" is true of the git credential specifically, not of
credentials generally.

**The line is not the sandbox wall; it is `buildEnv`.** `verse-fix` declares `env: [PATH, LANG]`,
and `buildEnv` is an allowlist of names, so the model's subprocesses inherit those two and
nothing else. The credential reaches `coderunner -task` — trusted Go that makes the model call
itself — and never reaches the agent's shell. **The invariant that holds is "no secret reaches the
MODEL", not "no secret enters the task".**

That is what makes taskproc the right place for a push credential: it is the side that already
holds one, and the model — the thing whose output is published to the read plane, the entity
store and a commit body — is on the other side of `buildEnv` from both.

### What that containment actually rests on

The agent's shell is a DESCENDANT of taskproc and runs as the SAME uid (`--unshare-user` maps the
caller to itself). So nothing about uids or namespaces stops it reading taskproc's memory. What
stops it is yama: `ptrace_scope=1` permits a process to trace its descendants and not its
ancestors, so a child cannot read its parent. On staging today:

```
ptrace_scope=1   uid=65532   (in a user namespace)
```

Two things follow, and both are worth stating rather than inheriting:

- **It is a host sysctl, and nothing here checks it.** `confine.go` asserts namespaces and mounts
  and says nothing about ptrace. A node that came up with `ptrace_scope=0` removes the barrier
  silently, and the launch would proceed — unlike a confiner that cannot unshare the network,
  which refuses.
- **It is already load-bearing.** The LLM credential depends on exactly this today. Putting a
  push credential beside it does not introduce the dependency; it raises the stakes on an
  assumption nobody has written down.

If that should not rest on a sysctl, the fix is not in this proposal: give the agent's
subprocesses a different uid, so same-uid access does not arise whatever yama is set to.

## What must not break

- the task cannot name a ref, only a declared destination;
- no approval, no credential;
- the credential reaches taskproc and never `buildEnv`'s allowlist, so the model never sees it;
- the channel stays an inherited connection rather than a path, so the agent's shell cannot reach
  it directly and ask without passing the gate;
- egress stays an allowlist. A destination the task pushes to is reached BY TASKPROC, which means
  `github.com` joins the allowlist — and that is the one widening this needs, admitting the forge
  to a network namespace the model's shell also lives in. What it does not admit is a credential
  to use there.
- the trailers stay the runner's to write.

## Open, and worth settling before anyone builds it

- What a credential can actually be scoped to on the forge in use: per-repo is ordinary,
  per-ref-pattern is a repository ruleset rather than a token property, and rulesets apply to
  everyone.
- How short a TTL is workable. It bounds the window in which possession exceeds what was
  approved, which is the whole residual risk of minting over parent-performs.

## What it unblocks

[merge-gate.tony](merge-gate.tony) and its [residual](merge-gate-residual.md), which is where
this was found: an agent's branch becomes a proposal, a gate decides it, and the stale ones
retire — none of which a task can ask for today.
