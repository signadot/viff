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
`1` proceeds. It is worth doing on its own account either way — the LLM credential is behind the
same assumption today, which is the argument for this step existing whether or not step 4 ever
happens.

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
