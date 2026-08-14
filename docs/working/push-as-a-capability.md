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

**The parent hands out CREDENTIALS, never performs OPERATIONS — and mints them when the gate
approves, scoped to what was approved.**

The environment declares destinations. The task asks for one by NAME, never by ref:

```tony
environment: verse-fix
destinations:
- name: auto            # what `deliver` means today
  ref: "refs/heads/verse/auto/$task"
- name: main
  ref: "refs/heads/main"
```

1. The task proposes an action naming a destination. "Where the work goes is the environment's to
   say" survives intact: a name the environment does not declare is refused before anything is
   gated.
2. Deriving stamps it `Verb: Push` with `Resource{KindGitRef, <resolved ref>}`, so the vector
   gates it on the ref it will actually touch — which is what makes `OnPush` and `branches:` mean
   something for the first time.
3. **On approval, and only on approval, the parent mints a short-lived credential scoped to that
   destination** and hands it over. The gate is not advisory: the credential does not exist until
   the vector says yes, so approval is the act that creates the capability.
4. The task runs git itself. There is no operation-shaped RPC, so the protocol never grows: a new
   flow is a destination in config, and the marginal protocol cost of every future flow is zero.

`deliver` stops being special — it is the finisher asking for `auto`. A merge is the same action
with a different destination. Retiring a branch is a delete against a destination that allows one.

### Why not have the parent perform the push

It was the first proposal here and it is not obviously worse — it binds an approval to exactly one
operation, where minting binds it to a scope and a window. The reason to prefer minting is the
growth rate: parent-performs needs the parent to understand every operation anyone will ever want,
and that understanding is where the ops multiply. Handing out a scoped capability needs the parent
to understand credentials, once.

## Who enforces what

Splitting this out because it is the part that is easy to assume and expensive to get wrong.

| | enforced by |
|---|---|
| **where** — which ref may be touched | the credential's scope, plus the environment's destination list. The task cannot name a ref. |
| **whether** — may this happen at all | the vector, at the gate. Load-bearing: no approval, no credential. |
| **how** — fast-forward only | **the receiving end.** `receive.denyNonFastForwards` on the server, or the forge's equivalent ruleset. |

**`--ff-only` is a prompt-level instruction and a server-side rule, and deliberately not lifted
into credential scoping.** `where` scopes onto a token; `how` does not scope at all, and
contorting a credential to express it would be inventing a mechanism where git already has one.
`receive.denyNonFastForwards` applies to every actor including humans, which is the right
property for a rule about what a branch's history may look like — it is not about who is asking.

The cost is that this invariant lives outside verse, so the docket cannot show it and a
misconfigured remote fails open. Accepted knowingly: an invariant enforced by the receiver is
stronger than one enforced by the caller, and the alternative on offer was a sentence in a prompt.

Note the interaction with retiring a branch: `receive.denyDeletes` would block the sweep. A
destination that exists to be deleted needs a remote that permits deletes on that ref pattern.

## What it costs, and what it must not break

Two exposures are real and are not solved by any of the above:

- **The credential is in the sandbox, and the sandbox publishes.** An agent's output is on the
  read plane, in the entity store, and in a commit body a charter may deref. A short-lived scoped
  token limits the blast radius; it does not stop the token appearing somewhere durable. Today
  that is impossible because the token is not in the address space, and that property is being
  traded away on purpose.
- **Egress must widen with it.** The allowlist is per environment and "by construction rather
  than by configuration the task could change", and `verse-fix` has no reason to reach the forge
  today — the parent prepares the workspace. Admitting the forge admits its whole API for
  whatever the token can see, not just the push.

The properties that must survive, and none of them is what changes:

- the task cannot name a ref, only a declared destination;
- no approval, no credential;
- the credential is scoped and short-lived, so possession is not the same as authority;
- the channel stays an inherited connection rather than a path, so the agent's shell cannot reach
  it directly and mint without passing the gate;
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
