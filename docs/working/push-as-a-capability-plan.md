# A push as a capability: implementation plan

Status: plan, nothing built. The design is
[push-as-a-capability.md](push-as-a-capability.md) — why one canned act is a button rather than a
capability, and why the parent should hand out credentials instead of performing operations. The
confinement half is [push-as-a-capability-residual.md](push-as-a-capability-residual.md). This
file is the order of work.

The change in one line: **the runner's one delivery becomes a named destination among several,
and approval releases a credential instead of triggering an operation somebody else performs.**

## Why this lands in steps, and the sibling plan did not

[loop-plan.md](loop-plan.md) argued its change had to land at once, because its intermediate
states were ones where the invariants did not hold. This is the opposite and it is worth saying
why rather than leaving the difference to look like inconsistency.

Every step below leaves a working delivery. The existing `deliver` keeps doing exactly what it
does now until the step that retires it, and the comment-drift drive on staging keeps running
throughout — which matters, because that drive is the only thing exercising this path at all.
Nothing here is interlocked: a destination table with no second destination is inert, a gated
push the parent performs is the current behaviour with a better decision in front of it, and the
credential moves last, on its own, when the thing it depends on has been stated.

## Order of work

### 1. Destinations, with delivery unchanged

`push:` is already a per-repo push target that `PushTarget(branch)` resolves. Generalise it to a
table of named destinations, with `auto` as the compatibility name for what exists:

```tony
repos:
- name: verse
  repo: "https://github.com/signadot/verse"
  base: main
  destinations:
  - name: auto
    ref: "refs/heads/verse/auto/$sha/$task"
```

`deliverFinisher` asks for `auto` by name. Nothing else changes: same op, same push, same ref.

**Proves:** the config shape round-trips and the expansion still agrees on both sides of the
process boundary — which is what `childConfig.Bases` exists for, so that `$sha` in a push target
means the same thing where the gate matches it and where the parent writes it. A test that the
resolved ref is byte-identical to today's for the same task is the whole of it.

### 2. Push as a proposed, GATED action — the parent still performs it

The task proposes an action naming a destination. Deriving stamps `Verb: Push` with
`Resource{KindGitRef, <resolved ref>}`. The vector gates it. The executor forwards
`{Op: "push", Destination: <name>}` and the parent does the work it does today.

**Proves:** the part the whole design is for, and it proves it without moving a secret. A charter
can say *"ask before anything touches main"* and have it mean something, because `OnPush` and
`branches:` finally match the ref that will actually be touched rather than a fixed one. Test: a
vector that ASKs on `branches: [main]` and AUTOs elsewhere poses a question for one destination
and not the other; a destination the environment does not declare is refused before the gate,
not after it.

This step is also where a merge becomes possible at all, so it is the one to stop at if the rest
is deferred.

#### The push failing is the ORDINARY case, and the design has it in the wrong place

Main moves constantly here, so a merge proposal is usually stale by the time anybody answers the
gate. "Could not fast-forward" is not the error branch of this feature; it is the common outcome,
and a design that routes it through failure will fill the record with alarm about the thing
working as intended.

**A non-fast-forward is a SEMANTIC outcome, not an infrastructure failure.** The task did its job:
it tried, correctly, and the answer is no. `task.Err`'s own doc draws that line — *"Err is
infrastructure failure; semantic failure is the charter's to check by naming what it expects"* —
so the merge task should complete `done` with an answer that says what happened, and the charter
should branch on the answer:

```tony
answer:
  merged: !irtype true
  reason: !irtype "not a fast-forward: main has moved since this branch was cut"
onDone:
  patch:
    state: !when [ $data.merged, merged, stale ]     # spelling to be checked against the grammar
```

That is a correction to [merge-gate.tony](merge-gate.tony), which currently writes `state: stale`
from `onFailed` — routing the expected outcome through the branch meant for the unexpected one,
and losing the distinction between "main moved" and "the credential expired".

The five paths, and which is which:

| what happened | shape | what the task does |
|---|---|---|
| not a fast-forward | the remote's rule refused it | complete, `merged: false`, reason on the record |
| protected-branch / ruleset rejection | the remote's rule refused it | same: a refusal by an authority outside verse is a DECISION, not a fault |
| auth: bad or expired credential | infrastructure | the action errors; for a closing act the task fails, because work that did not leave did not succeed |
| transient (5xx, DNS) | infrastructure | as above, after the client's own retry — nothing here adds one |
| some repos pushed, others did not | needs a decision | see below |

The first two want the loop's `Blocked` rather than `Err`. `StepResult.Blocked` exists to carry
*"a gate/scope refusal so the driver can re-plan rather than treating it as an error"*, and a
remote enforcing its own rules is exactly that: the authority [the design
note](push-as-a-capability.md) deliberately handed to the receiving end, exercising it. Modelling
it as `Blocked` also gets the closing-act case right for free — `machine.go` already treats a
denied closing action as *"a decision the task complied with"* and ends `done`, while an errored
one fails the task.

The classifier lives with whoever ran git, which is the parent in this step and taskproc after
step 4. Both have the git error; neither should be guessing from a string if the transport gives
something better.

**Partial success across repositories is the one with no obvious answer.** A workspace has several
repos and `deliverFinisher` already builds one effect per destination. If one push lands and the
next is refused, the task is neither done nor failed, and re-running it will re-push the first.
This plan does not settle it, and says so rather than discovering it: the minimum is that the
answer names which refs moved, so the record is true even when the outcome is mixed.

**And a cancel mid-push is at-most-once that nobody can offer.** The loop tears down on cancel and
the push may already have landed; a ff-only push of the same sha is at least idempotent, which is
the only reason this is survivable rather than a correctness problem.

### 3. Say what containment is being relied on, and check it

[The residual](push-as-a-capability-residual.md), item 1, and it comes BEFORE the credential
moves rather than after. `principal: runner` means the task process and the model's shell are the
same principal; what separates a secret in one from the other is `kernel.yama.ptrace_scope`,
which is a host sysctl outside the Point that nothing verifies.

A leaf an environment can ask for, and `bwrapConfiner.Provides` answering it by reading
`/proc/sys/kernel/yama/ptrace_scope` — absent or `0` refuses, in the same voice the network leaf
refuses with.

**Proves:** that step 4 is putting the credential somewhere with a stated property rather than a
hoped-for one. Test: a Point asking for it against a fake sysctl at `0` refuses the launch, at
`1` proceeds.

#### Deferred, on the threat model, with a trigger

**Not doing this yet.** The deployment is behind a VPN, open to a handful of people, none of whom
are expecting bulletproof security — and the limitation is already being swallowed for the LLM
credential, which sits behind the same assumption today. Adding a repository-scoped PAT beside it
does not change the shape of the exposure, only its content, and the people who could exploit it
are the people who already have the repository.

Deferring it means step 4 proceeds on an assumption rather than a stated property, and the
[residual](push-as-a-capability-residual.md) is the record of that. Following
[contributing.md](contributing.md)'s discipline for what it is deliberately not doing yet, the
trigger matters more than the decision — naming it is what stops "not yet" becoming "never":

- the deployment stops being VPN-only, or task spawning reaches anybody outside that handful;
- a credential lands in taskproc that is scoped wider than one repository, or whose compromise
  costs more than a `git revert` — a registry, a cloud account, anything with spend;
- `ptrace_scope` is found at `0` on a node, which today nothing would report.

The last one is the cheap half and could be taken on its own: not a Point leaf and not a refusal,
just the runner logging what it read at startup. That turns the assumption from unverified into
merely accepted, which is a different thing, and it costs one file read.

### 4. Approval releases the credential; taskproc performs

`{Op: "push", …}` becomes `{Op: "credential", Destination: <name>}`, answered with the secret the
destination names. taskproc runs git itself, with go-git and per-operation auth, exactly as the
parent does today. The credential never reaches `buildEnv`'s allowlist, so the model never sees
it.

**Proves:** the protocol stops growing. This is the step the design exists for and its argument
is easy to state one way and hard the other, so: step 2 leaves ONE op, which is not a menu — but
the parent must then implement every *operation* anyone wants through it. A merge is fetch,
merge, push. A retire is a delete. A tag is another. The op count stays at one while the
operation count grows inside the parent, which is the same multiplier wearing a different hat.
Releasing a credential is the point at which the parent stops needing to know what git operation
is being done.

Test: a task that is refused at the gate gets no credential; an approved one gets exactly the
credential its destination names and no other.

### 5. Retire `deliver`

The op goes. `deliverFinisher` proposes a push to `auto` like anything else, and
`finish.go`'s argument — the vector gates it, the read plane narrates it, it lands before
terminal — is unchanged, because it was always about the action rather than about the transport.

**Proves:** that `deliver` was one destination all along. Nothing observable moves: the same ref,
from the same task, at the same point in the loop.

### 6. The operational half

Not code, and not optional.

- `github.com` joins the egress allowlist of any environment with destinations. It is reachable
  from the model's shell, because the allowlist is per network namespace and the shell shares it
  — and it is not usable there, because the credential is on the other side of `buildEnv`. That
  is the one place "reachable" and "authorised" come apart in this design and it should be
  written into the environment's comment where somebody configuring it will read it.
- `receive.denyNonFastForwards` on the remote, or the forge's equivalent rule. This is what
  enforces `--ff-only`; the prompt only asks for it.
- `receive.denyDeletes` must NOT be set on the ref pattern a retiring destination targets, or the
  sweep silently cannot do its job.

**Proves:** nothing mechanical, and that is why it is a step rather than a footnote. Each of
these is a thing that fails open — a missing rule does not error, it permits — so the only
protection is that somebody wrote them down where they are configured.

## Verification, and what each part cannot show

- **Steps 1, 2, 5** are ordinary tests: the resolved ref, the gate posed against the right one,
  the refusal of an undeclared destination.
- **Step 3** is testable against a fake sysctl and says nothing about the real node. What it
  buys is a refusal where there is currently silence.
- **Step 4's** claim is about growth rather than behaviour, so no test shows it. What a test can
  show is the negative — refused means no credential — and the positive is an argument about the
  next five flows, which is exactly the kind of claim [the design note](push-as-a-capability.md)
  should be disagreed with over rather than measured.
- **None of it** shows the credential is safe where it lands. Step 3 states the requirement,
  `principal: distinct` would remove it, and neither is a proof — the model's output is published
  by design, and what keeps a secret out of it is that the secret is on the other side of an
  allowlist somebody has to keep correct.

## Not in this plan

- **The confinement check itself.** Step 3 is deferred on the threat model above; the residual
  stays as the record and the triggers say when it stops being a reasonable trade.
- **`principal: distinct`.** Residual item 2. It would make step 3 unnecessary rather than
  satisfied, and it costs a checkout owned by one uid and read by another — a change to the
  workspace and delivery path rather than a flag.
- **Minting credentials.** Out on an operational constraint, not a technical one: these are
  hand-made PATs, and a system that can create credentials is a system whose compromise creates
  credentials. If the forge ever makes it cheap it narrows step 4's window and changes nothing
  else.
- **Per-ref credential scoping.** A PAT scopes per repository, not per ref, so `auto` and `main`
  in one repository cannot be told apart by the credential. That is the forge's rules to hold,
  which is [the design note](push-as-a-capability.md)'s position on where such rules belong.
- **The merge-gate charter itself.** [merge-gate.tony](merge-gate.tony) is what this unblocks,
  and its own [residual](merge-gate-residual.md) has the part that is still not expressible after
  all of the above: nothing withdraws a standing gate when its subject stops qualifying.

## Risks worth watching

- **Step 2 makes `OnPush` gates fire where nothing fired before.** A charter written against
  today's behaviour, where the only push was ungated by construction, may start posing questions
  nobody expects. Worth a look at what is installed before it lands.
- **Step 4 changes who git is run by**, from go-git in the parent to go-git in taskproc, under
  bwrap with an unshared network. Whatever the parent's environment was giving it — proxy
  variables, CA bundle, DNS — is a thing to check rather than assume, and the failure looks like
  a push that worked yesterday.
