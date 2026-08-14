# The coding runner: what is left

Status: residual of a landed thing. The coderunner runs and is documented in
[reference/examples/README.md](../reference/examples/README.md#running-the-coderunner); the
program is [docs/reference/examples/coderunner](../reference/examples/coderunner/main.go), and
[verse-dev-coderunner.md](verse-dev-coderunner.md) is the drive that exercised it. This is what the
design note named and the landing did not build.

It replaced a 900-line design note. The reasoning that survived the landing is in the program's own
comments, where it is read by whoever changes the thing it is about; what is here is the part with
nothing to attach to yet.

## Never deployed, and three assumptions ride on it

The image and the manifests (`deploy/coderunner/coderunner.yaml`, outside `docs/`) are
built and read and have never touched a cluster. Three things are assumed and cannot be checked
anywhere else:

- **bwrap passes inherited descriptors** (3 the control listener, 4 the lifeline, 5 the delivery
  channel) through to the program it execs. Expected — bwrap's own `--args FD` proves fds reach it
  — but unverified. The diagnosis is in place: the child fails with *"fd 3 is not the listener this
  process serves on"* and the parent reports it.
- **bwrap brings `lo` up under `--unshare-net`.** The egress forwarder listens on the sandbox's own
  loopback, and a fresh network namespace has `lo` DOWN until something raises it.
- **the egress socket bind** lands where `EgressAt` says it does.

Any of them wrong is a failed spawn with a message, not an escape. That is the point of the
refusals, but a message nobody has seen is still a message nobody has seen.

## `network.egress: restricted` is untestable on the machine it was written on

The proxy, the allowlist, the CONNECT tunnel and the loopback forwarder are all exercised against
real servers. The **combination** that makes them a floor — an unshared network plus one bound
socket — needs bwrap, so `hostConfiner` refuses it and the end-to-end path has never run.

Refusing is correct: under a confiner that cannot unshare, the proxy works and is *advice*, because
nothing stops the task opening its own connection. A floor that can be stepped around is not one.
The cost is that the first real exercise of the rung will be a deployment.

What the allowlist does not do, and no amount of deploying will fix: it bounds **where**, never
**what**. `api.anthropic.com` will accept anything you care to send it, so this bounds a secret
against a task that wanders and not against one that is trying.

## `principal: runner` says the quiet part, and nothing refuses on it

**This is the one that carries a secret, and it is the largest thing here.**

`principal: runner` is the strongest rung any confiner reaches, and it is being read as stronger
than it is. It means the task process and the AGENT'S SUBPROCESSES are the same principal:
`--unshare-user` maps the caller to itself, so the model's shell runs as the same uid as
`coderunner -task`. Nothing about uids or namespaces separates a credential in the one from the
other.

What separates them is yama. `ptrace_scope >= 1` permits tracing descendants and not ancestors,
and the shell is a descendant; at `0`, or with no yama at all, any process of that uid may attach
to any other and read what it holds. That is a **host sysctl, outside the Point**, and it is
already load-bearing — the model key has always been in the task process — with a push credential
now behind the same assumption.

Half of it is done, and it is the cheap half: the runner **reads** the sysctl at startup and says
what it found, in a sentence that names what is at stake rather than reporting a kernel setting.
That turns the assumption from unverified into merely accepted, which is a different thing, and it
costs one file read.

What is not done is the refusal, and the awkward part is what it should refuse *against*. Nothing
declares "I hold a secret this process must contain", so a bare check either refuses launches that
never needed it or refuses nothing. The honest shape is a leaf — something like
`process.peek: {allowed, denied}` — that an environment holding credentials asks for and bwrap
answers by reading the sysctl, in the same voice the network leaf refuses with: *a confiner that
cannot deliver what was asked for REFUSES the launch rather than running the task with a barrier
it cannot see.*

It is deferred on the threat model rather than forgotten, and the trigger matters more than the
decision. The deployment is behind a VPN, open to a handful of people, and the limitation is
already being swallowed for the model key; a repository-scoped token beside it changes the content
of the exposure and not its shape, and the people who could exploit it are the people who already
have the repository. What would change the answer:

- the deployment stops being VPN-only, or task spawning reaches anybody outside that handful;
- a credential lands in the task process scoped wider than one repository, or whose compromise
  costs more than a `git revert` — a registry, a cloud account, anything with spend;
- `ptrace_scope` is found at `0` on a node, which the startup line would now say.

## `principal: distinct` is the rung nothing reaches, and it is the fix for the above

A per-task uid from a pool, and orthogonal to everything else: bwrap and rootless runc both reach a
private filesystem with the caller mapped to itself, so the task runs as the runner however
contained it is. Every other leaf has an implementation and this one has none, which makes it the
honest test of whether the tree's leaves really are independent.

It is also what makes the yama question not arise: a different uid for the agent's subprocesses
means same-uid ptrace is not available whatever the sysctl says. bwrap can map a uid, so it is not
out of reach in principle; what it costs is that the workspace, the mirrors and every file the
agent writes are owned by one uid and read by another, which is a change to the checkout and
delivery path rather than a flag.

Worth noting which way round the two go: the leaf above makes the current arrangement honest, and
this makes it unnecessary. Doing the first is right anyway, because it turns an assumption into a
stated requirement that this would then satisfy properly.

## A destination cannot express a DELETE

A destination resolves to a ref and a push writes it, so retiring a branch — which
[merge-gate.tony](merge-gate.tony)'s sweep needs — has no spelling. Nothing in the config says "and
this one may be deleted", and nothing in the protocol distinguishes the two.

The shape it wants is a destination that permits one, and the operational half that goes with it:
`receive.denyDeletes` must not be set on the ref pattern such a destination targets, or the sweep
silently cannot do its job. Until then a stale `verse/auto/*` branch is retired by hand.

## The descriptor's contract half

`GET /runner` serves something real — drivers, the containment this deployment can build per leaf,
environments, and what work is undelivered — and **nothing consumes it**. `verse runner add` still
probes with `GET /tasks`. An environment naming a containment `profile` is therefore refused
whatever it names, because the runner publishes no catalogue to name from, and the point per
environment is derived (an `egress:` list means `restricted`) rather than declared.

The unbuilt design is [runner-descriptor.md](runner-descriptor.md), which is a sketch and says so.
What the landing adds to it is one measured fact: a **per-leaf ceiling is not a catalogue of
buildable points**. bwrap's network is one switch, so it builds `(full, full)`, `(none, none)` and —
with the proxy — `(none, restricted)`, and nothing else; a per-leaf reading of the same numbers
admits `(none, full)`, which cannot be built. The descriptor names the field `perLeaf` for that
reason and `Provides` stays the authority.

## Staged work is append-only

`refs/pending/<task>/<repo>/<target>` is self-limiting: the sweep pushes it again, which is a no-op
if the far side already has the commit, and the ref is dropped. `refs/held/<task>/<repo>` — work a
gate refused, a remote refused, or a task that died before asking — is **never swept**. Nothing
deletes it, nothing ages it out, and the only reason that is tolerable is that it is small and
`GET /runner` lists it.

The two namespaces exist so a retry cannot push work somebody declined, and `held` gained a second
population when the remote became able to refuse: a push that will never be accepted moves there
rather than being retried on every spawn forever. That makes the missing sweep slightly more
pressing than it was, and it still needs a policy nobody has stated: how long a refusal stays
reviewable, and whether deleting it is an operator's act or a runner's.

Same shape one level up: nothing cleans workspaces left by a runner that predates staging.

## `verse task get` cannot say where the work went

The delivery action's output names the branch on the read plane, and the runner's log names the
staged ref. The **entity** carries neither. So "where did this task's work go" is a question you
answer by reading a stream rather than by querying a store, which is the half of finding 7 that
survived — and it is the same want as recording a run's envelope (finding 12), since both are
facts about a run that live nowhere durable.

## A wrong turn whose reasoning still teaches

The note predicted the seam that moves the push inside the loop would be **`harness.BuildParts`** —
hand back the driver and executor so a caller can wrap them. That was the right diagnosis and the
wrong home. The seam is about drivers and executors, which belong to `task`, and this runner's
*command* driver needs it exactly as much as the LLM one does; a harness-shaped answer would have
served one of the two. `task.Finisher` is what landed, with `harness.Config.Finish` passing one
through.

Worth keeping because the mistake is a common one: the blocked caller was in the harness's client,
so the missing thing looked like it was the harness's to offer.

## Open questions the landing did not answer

- **Where a per-spawn command opt-in is written.** It is request-specific, so not the environment;
  `grant:` could carry it and is the wrong mechanism. A field beside `environment:` is the obvious
  third answer and adds charter grammar this design otherwise avoided.
- **Whether `$task` in a push target is a template the runner expands.** `launch.Spec` already has a
  `$var` pass on the charter side against the matched event. A second expansion, runner-side,
  against a different vocabulary, is two template languages that look alike.
- **What a task's result should be.** The agent's final message, or the branch it pushed, or both.
  `$result.branch` is what a charter wants to deref, which argues for a contract the runner
  *imposes* — and a runner imposing a contract on the work is a new kind of thing.
- **Credential form.** A token in the runner's environment, a credential helper, or an SSH agent
  socket, differing in what a compromised runner leaks. It stopped being identical from the task's
  side when approval started releasing one
  (see the doc this is a residual of): a token is a value that can travel over
  the channel, and a helper or an agent socket is not, so choosing either of those is choosing to
  put the push back in the parent.
- **Whether workspaces should be pooled.** The measured case is the build cache rather than the
  checkout — which is also what makes it a cross-task channel rather than a cache.
- **When a clone should become alternates.** The trigger is repository size and the switch is one
  function; worth doing against a real monorepo rather than an argument, and worth noting that the
  mirror can then never be GC'd under a running task.
- **Whether a repository can be added to a running workspace.** Probably not — a set declared rather
  than accumulated — but untested against real work.
- **What a `paths:` predicate means across repositories.** Changed files are workspace-relative
  (`verse/task/loop.go`), so a charter's path globs need a repository segment they did not need
  before, and nothing warns about one written for the single-repo shape.
- **Whether anything should reserve the `Verse-*` trailer prefix**, given a repository is free to
  write those trailers itself.

## What it deliberately does not do

Recorded because several absences above are consequences of one bet: **the runner's job ends at the
edge, and everything interesting is downstream of it.**

No scheduling, no queue, no capacity — a workspace per task is disk, and the bound is a flag; over
it the spawn is refused. No merge, no PR, no review: the runner pushes a branch and what happens to
it is a charter's business, because a runner that opened pull requests would be a runner with
opinions about a workflow. No conflict handling — two tasks on one base make two branches. No
incremental workspaces — each task starts from `base`, and chaining is a second spawn naming a
different one.

Which is exactly why the push had to become disciplined: it is the one event the whole downstream
depends on, so it happens once, at a defined moment, to a target the environment named, caused by a
party verse knows and gated by a vector that can decline it.
