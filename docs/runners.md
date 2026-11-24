# Runners

A [runner](glossary.md#runner) is a place where viff runs a [task](glossary.md#task). You
install a runner as data, its registration, and the runner says what it offers: which agents,
whether scripts, and how far each may reach. A [spawn](glossary.md#spawn) names its doer and what
it may reach, never a runner; viff finds a runner that offers the doer and holds the
environment, and the task records which runner it went to.

So a drive can deploy a place to run work that the viff deployment does not otherwise need to
know about. The drive's work goes there when it is the work that place offers, and not
otherwise.

**Routing each spawn is the whole feature.** Deploying a runner is easy: the sample runner is one
program. The hard part is sending it *part* of the work, and only the part it can do.

To build a runner, start from [the sample runner](reference/examples/runner/main.go). The
sample is a real program that compiles with the tree. Comments mark the two functions that you
change.

## Installing one

A runner's registration is the data at `mirror.runners.<name>`. It carries a URL: the base URL
of [task-control](task-control.md) that faces viff. The name of a runner is its address. A
runner that proves who it is, as `runnerd` does, is registered with the subject of its token
([who the runner is](#who-the-runner-is)):

```sh
viff runner add gpu http://runner-gpu.viff.svc:9802 -subject system:serviceaccount:viff:runner-gpu
viff runner
viff runner check mirror.runners.gpu
viff runner show mirror.runners.gpu
```

`add` also reads a document. A deployment writes this form:

```tony
name: gpu
url:  "http://runner-gpu.viff.svc:9802"
subject: "system:serviceaccount:viff:runner-gpu"
```

Every installed runner is a place to reach, and viff refuses a runner with no `url`. `viff up`'s
own runner is one of them: a `runnerd` it starts beside itself and registers as
`mirror.runners.local`, proved by a static token in its data dir, unless the config says
`localRunner: false` ([the local runner](reference/up.md#the-local-runner)). The name `local` is
`viff up`'s, and refused to anybody else.

**Adding a runner probes it.** The probe is `GET /describe`, which every runner serves: its
answer is what the runner offers, and asking has no effect on the runner. The probe has three
outcomes, and only one of them refuses the install:

| Outcome | What happened | Result |
|---|---|---|
| *a runner* | the URL answered `200`, with a description that decodes | installed |
| *not a runner* | the URL answered *that it does not describe itself*: a status other than those below, such as `404`, or a description that does not decode | the install is **refused** |
| *unreachable* | nothing answered; or the URL answered `401` or `403`, refusing this viff; or `500`, `502`, `503` or `504`, failing without saying what it is | **installed, with a warning** |

**A runner that answers 500 is *unreachable*, not *not a runner***. So viff installs it with
a warning, and does not refuse it. A runner that refuses this viff's token is a runner that
serves another viff, which the deployment fixes. Only an answer that the route is not there, or
a description that does not decode, is an answer about what is at the URL.

### Who the runner is

Viff decides who a runner is at registration and nowhere else, and a runner is never taken at
its word. Every registration names a `subject`: the subject of the workload token the runner's
runtime gives it, or a static token's hash (below). The runner has to prove it is that runner,
twice over:

```sh
viff runner add viff-runner http://viff-runner:9802 -subject system:serviceaccount:viff:viff-runner
```

- **At the describe.** Viff presents a token it signs itself, as the issuer `auth.issuer`
  makes it, on every call it makes to a registered runner. The token
  is minted for that runner alone, its subject the audience, so a copy taken at one runner opens
  no other's door. A runner whose configuration names its viff (`viff:` in
  [runnerd.md](reference/runnerd.md#configuration)) reads viff's keys where it reaches viff,
  holds every bearer to viff's issuer name, its own audience and the subject `viff`, and
  refuses every other caller's launch. The
  description itself is public: anyone may ask what the runner offers. To the viff it
  verified, and to nobody else, the runner answers `GET /describe` with its own token, and
  viff checks the token's subject against the registration before it records what the runner
  offers. A registration is described or it is not: a description is one the runner proved.
  A runner that answered as a runner and did not prove it is the one registered is refused at
  `add`, since waiting changes nothing; found later, by `viff runner check` or by a spawn that
  asks, it has what it described removed, and why said to whoever asked, and takes no spawn until
  a check finds it proving itself. The describe is asked again before every launch, one request,
  since the launch is what hands whatever answers at the url the prompt, the repositories and
  the policy: a runner replaced behind the same url receives no launch.
- **At viff's door.** When the runner calls viff, its token makes it `runner:<identity>`
  ([runners at the door](reference/perms.md#runners-at-the-door)), and its name alone is refused.

The runner's first call is to say it is here. A runner whose configuration names its viff's
url announces itself at start, `POST /runners/hello` with its token, in a loop until viff
has described it, and viff describes it then and there, so a pod that comes up after its registration,
or comes back after a restart, is verified within seconds and nobody runs `viff runner check`.
The door is the acknowledgement: a token no registration names is refused before anything else,
and that refusal is the runner's cue to keep asking, with one log line saying why. The runner's
readiness, on `GET /ready`, is its server and nothing else. Whether viff has heard it, and
whether the describe that followed verified it, is the registration's to say, in `described`,
where `viff runner` shows it.

A runner no runtime issues a workload token to, on a machine off Kubernetes, proves itself
with a static token instead: one it and its registrar hold, as an iod may. The registration
names the token by its hash, which becomes the subject, so viff never holds the token and the
registration stays everyone's to read:

```sh
viff runner add edge https://edge.example.com:9802 -token-file edge.token
```

The runner reads the same token at `VIFF_TOKEN_FILE`, answers it at the describe and presents
it at viff's door, and takes viff's tokens for the same hash as its audience; nothing else is
configured. A static token holds 32 bytes at least, since its hash is published, and is not
shaped like a JWT: one with exactly two `.` reads as a JWT on both sides and is refused at `add`
(`openssl rand -hex 32` makes one that is neither too short nor dotted). Each kind of
proof finds only its own kind of registration, so a workload token cannot claim a static
registration, nor a static token a workload one. No workload issuer is needed; viff still
issues its own tokens (`auth.issuer`), since the handshake runs both ways. This is how a runner
on a laptop is registered too.

`viff runner add` without `-subject` or `-token-file` is refused. `viff up` registers its local
runner the same way, by its static token's hash, as the system.

`-no-probe` installs the runner in every case. A runner installed without its offers takes no
spawn until it has described itself: at its hello, when viff starts, or at `viff runner
check`, which asks on demand; viff keeps what it says on the registration, and asks again
before every launch to it. Until then routing passes it over, and a spawn that only it might
take is refused naming it.

A [worker](glossary.md#worker) is named on its runner's registration, or names itself on its first
running status. The [claim](glossary.md#claim-of-a-worker) is where viff assigns the task to
the worker:

- viffd writes `read-write` and `control` for the worker on the task's record;
- the mirror writes every report from then on as the worker;
- viff lists the worker as a runner principal.

The grant reaches the task and nothing else. It does not reach the questions that the task
raises, and the worker may not answer them ([perms.md](perms.md)).

## Where a spawn runs

A charter names its doer and what it may reach, and nothing about where:

```tony
name: retrain-on-gpu
condition:
  subject:
    path: "demo.model.*"
    value:
      stage: ready
action:
  spawn:
    prompt:   "retrain the ranker on $[subject.ref]"
    agent: {name: viff}                                  # ← who does it
    environment: {systems: {network: {mode: open}}}       # ← what it may reach
    autonomy: [{then: ask}]                               # ← what it may do unasked
  onDone:
    patch: { stage: retrained }       # the latch: out of its own selection
```

An ad-hoc run is the same spawn:

```sh
echo '{prompt: "retrain the ranker", agent: {name: viff},
  environment: {systems: {network: {mode: open}}}, autonomy: [{then: ask}]}' |
  viff task run -f -
```

Viff routes each spawn when it launches, in its own view of the registrations:

1. **The candidates** are the runners that offer the doer, the agent the spawn names or scripts,
   as each last described itself, the local runner `viff up` starts among them. A
   registration with no description is passed over: the runner's hello, the probe when viff
   starts and `viff runner check` describe a runner, and a spawn does not.
2. **Fit**: of those, the runners whose offer takes the spawn: the agent's settings, every system
   the environment gives within the offer's ceilings, the network a mode it builds.
3. **Load**: the one carrying fewest tasks that have not ended, as viff has recorded them, then
   the first by name.
4. **The handshake**: viff asks that runner what it is now, as it does before every launch. One
   that does not prove it is the runner registered, or does not answer, is passed over for the
   next; one that proves itself and still takes the spawn is where it runs.

A prompt that names no agent is for viff's default agent, `viff` unless the config's
`defaultAgent` says another, and the task's record names the agent that ran it.

**Viff refuses a spawn that no runner takes**, on both spawn paths, and does not run it
somewhere that does not offer what it asks. The refusal says why: no runner offers the doer;
each runner that offers it and why it would not take it; the registrations passed over, not
described or unreadable, and how to find out why; or each runner that failed the handshake.

| Path | What viff does |
|---|---|
| ad-hoc run | refuses the run, with the reason. Nothing starts. |
| charter spawn | refused at install where no runner offers the doer or takes the spawn as written; at a firing, nobody is there to answer, so viff fails the spawn, and the reason goes onto the flow |

A spawn that writes `runner:` is refused for the field: where work runs is viff's to say.

What follows a task's ask, the rows of its `on`, runs on the task's runner, as it was checked when the
task raised the ask ([the viff system](systems/viff.md)).

### Not the agent

A spawn's `agent` says **who** does the work: which agent, under which model and credential (or a
`script`, which is its own). A runner is **where the work runs**. The spawn names the first, and
viff picks the second among the runners that offer it: the same agent may run in either of two
places, and the same place may host either of two agents.

## Seeing where

```sh
viff ps                           # each task names its runner, `mirror.runners.local` among them
viff ps -runner mirror.runners.gpu # only that runner's work
viff runner                       # what is installed, and what each is carrying
viff runner -probe                # …and what each of them says right now
```

`viff runner` shows three columns:

| Column | What it counts |
|---|---|
| `running` | tasks on the runner now |
| `total` | tasks on the runner ever |
| `lastSpawn` | the last spawn to the runner |

So you can see a runner that nothing is routed to. Viff **derives these columns from the store**.
`-probe` is the only thing that asks anybody.

**The store never records health.** Viff answers a probe live, prints it, and discards it.
So a column of reachability is always as of now. There is no polling in the background.

`viff ps -runner` selects on the name that viff **stamped at the spawn**, not on what is
installed now. So it keeps answering for a runner that somebody removed. An operator most
wants the answer then, and `viff runner` does not show that case.

`viff ps` reads the `mirror.task.instance` mirror. It does not ask each installed runner. So
when one runner is down, you lose live detail and not rows.

## What a restart does

`mirror.task.instance` records both the resolved **URL** and the **name** of the registration the
spawn went to.

- A restart rebuilds the runner from the URL. So work outlives the viff that started it, and
  it *also* outlives the registration that named its runner. Removing a runner's registration cannot orphan
  work by making it impossible to rebuild.
- A person reads the name, and `viff ps -runner` selects on it.

## A task's lease

A task runs only while viff can reach its runner, give or take its **lease**. The lease is the
launch's `lease`, a duration of `1m` or more, and `4m` where the launch names none
([a spawn's fields](reference/action.md)).

This is the runner's part of the contract:

- **Each subscribe the runner accepts renews the lease.** The lease counts from the launch, and
  again from each `GET /tasks/{id}/events` the runner answers for the task. A subscribe it
  does not answer renews nothing, and neither does an open stream after it is answered.
- **A task whose lease runs out is ended by its runner.** The runner stops the task and ends it
  `failed`, with `orphaned: true` and `err: no reader for 4m` on its snapshot and on the
  terminal frame a subscribe is answered with.
- **A subscribe is answered at once**, whatever state the task is in. A task whose repositories
  are still being cloned has a stream that is open and has no frames yet.
- **A subscribe that comes after the lease ran out renews nothing.** It is answered with the
  task's end, and so is a stream still open when the lease runs out. A launch of the same id
  after that starts the work again.
- **A launch of a task that is running joins it**, and renews its lease: from then the lease is
  the one the joining launch names, which is the one viff waits by.
- **Every answer the runner itself gives carries `Viff-Runner`**, refusals included; a door in
  front of it, such as its auth, need not. Anything can answer HTTP between viff and a runner:
  an ingress's default backend, a route not yet set up, a login page. Viff takes a subscribe as
  accepted only where the answer carries the header. It takes a `404` as the runner having no
  such task, which ends the task, only where the stream's `404` and the task's both carry it. An
  answer without it is taken as no answer.

Viff's part is to ask. It subscribes to each running task again four times within the lease,
waits a quarter of the lease at most for each answer, and stops waiting a minute after the lease
has gone by with no subscribe the runner accepted. It then
ends the task on its record, also as `failed` with `orphaned: true`. The lease runs out first, so
by the time viff ends a task for a runner it cannot reach, the runner has ended it too.

So a task rides out a viff restart or a partition shorter than its lease, and not a longer
one. A longer lease rides out a longer outage, and holds the task's subject that much longer
when the runner is really gone.

A runner that does not keep the lease leaves a task running that viff has given up on: its
rule can fire again and start a second run beside it.

## Removing one

Viff cannot *move* a running task between runners. It can cancel the task, or leave it. The
choice is an intent, and viff cannot read it from a delete. So **viff refuses a removal
with work on the runner until you say which**:

```
$ viff runner remove mirror.runners.gpu
refused: 3 task(es) are running on mirror.runners.gpu, and removing it means one of two
  things this cannot tell apart:
    --cancel "<why>"   the place is going away; end them with a reason their charters see
    --leave            the place stays; they keep running on the url already stamped on them
$ viff runner remove mirror.runners.gpu -cancel "gpu pool retired"
```

A removal with nothing running on the runner needs no flag.

`-cancel` **reports the tasks that it could not reach**. It does not count them as ended.
Those tasks are orphans, and the orphan path covers them. A runner that is already gone is the
reason for half of all removals, so this is the ordinary case.

With a flag, the removal itself is never refused. `-leave` always succeeds, because there is
no drain here. The visible cost of `-leave` is that `viff ps` shows tasks on a runner that
`viff runner` does not list. That is the true state.

Removing a resolver *hands on* what the resolver held, because a question has a layer above to
go to. A runner has no layer above: work in flight runs in a physical place. So the choices are
to leave the work or to end it. Neither choice hands the work on.

## What this does not do

**No routing beyond fit and load.** There is no `match`, no `capability` and no
`select: auto|ask|vote`. A drive that needs the gpu pool deploys a runner that offers what only
the gpu pool should do, and spawns that.

**No standing runners.** A condition that matches a subject may attach a resolver. It may not
attach a runner. "All work about payments runs on the payments pool" is a reasonable thing to
want, and it is a different feature.

**No scheduling, no capacity, and no health that routes.** Viff hands a spawn to a runner, and
the runner takes it or does not. There is no queue and no draining. The only second try is at
the launch: a runner that fails the handshake is passed over for the next that takes the spawn.
The probes diagnose and never dispatch, and nothing polls a registered runner; the one poll is
`viff up`'s of a local runner a crashed run left ([the local runner](reference/up.md#the-local-runner)). A runner that is down fails the
handshake of each spawn routed to it, and is tried again by the next; one that took a spawn and
then failed it fails the task, which takes the path every failed task takes.

**No authority for each runner.** Nothing decides which runners a spawner's work may go to beyond
what the runners offer. A person's permissions on runners govern installing, removing and
seeing them, not where their work runs.

**A runner's registration carries a URL and who it acts as, and nothing about what work may do
there.** A grant ceiling, a tool allowlist, a label: viff can express each of these, and each
is absent by design.

The narrowing lives *at the runner*. Whoever deploys the runner declares it. The shipped
runner, `runnerd`, is the worked example; its configuration names:

- which agents it offers, and the settings each takes (which models among them), and whether it offers scripts;
- for each of those doers, the ceiling over what its tasks may do with each system, and what of the runner's environment its shell sees; and the forge its repositories come from;
- what its sandbox can build, which the runner says itself rather than its configuration.

The runner publishes that at `GET /describe`, and viff reads it: `viff runner add` is the
probe, and what the runner offers is kept on its registration (`described`, which `viff runner
show` prints); `viff runner check` reads it again, so a runner that offers more after a
restart says so, and a registration kept from an older runner or viff is asked again. Each
offer holds the network, with the modes its sandbox builds under `builds`, or viff does not
install the runner. A spawn says what its task may reach, in its `environment` (the
repositories and what may leave each, the network, the other systems), and what it does unasked,
in its `autonomy`, and viff checks the spawn against what the runners offer where the spawn is
written: a charter is refused at install, and `viff task run` at the prompt, where no runner
offers its agent, or every runner that does refuses a setting it does not take, a system it does
not hold, a block beyond its ceiling, a network it does not build, or a step its agent can't run
without and the environment leaves out. The runner checks the launch again when it arrives, and
refuses what it cannot honour. So a charter says what its work needs, viff picks where it runs,
and the deployment says what running there permits. What comes back is an ordinary step, which the task's environment and
autonomy decide like any other, so nothing in the arrangement is private to the runner. The sample in [examples/runner](reference/examples/runner/main.go) is the one to
copy for a runner of your own.

**Running work cannot be moved.** Writing a different runner onto a `mirror.task.instance`
does not move the task. That field records where the work went. It is not an instruction about
where the work should go.

## What holds by convention

Two cases are open:

- The contract does not say *how* a runner applies control. So viff assumes that anything it
  reaches over a URL applies control synchronously. Viff relies on a runner that queues
  control to refuse it.
- `local` is the local runner's name, `viff up`'s to register and nobody else's.

## Why it is this way

### Why viff routes each spawn

A runner that every spawn must go to is a whole second viff, with its own store, engine and
data. The drive's work is then somewhere else entirely. That is the opposite of running
the drive in the deployment it works on. So each spawn goes where its doer is offered, and a
deployment says which doers run where by what each runner offers.

### Why the probe refuses only "not a runner"

The three outcomes differ on whether waiting could change the answer. They do not differ on
whether anything answered.

A URL that answers *that it does not describe itself* states what it is, and states it now. Something
is at that address, and it does not serve the runner contract. Viff catches this mistake at
install, not at the first spawn.

A URL that nothing answers is the ordinary state of a runner that is deployed with the registration
that names it. The manifest lands, the pod is pending, and the registration is already written. A
refusal there would make deployment depend on order, for a fact that fixes itself. Routing waits
on the same fact: the runner is passed over, and named in a refusal, until it answers, and
checked against what it says from then on.

A URL that speaks HTTP and fails has said nothing about whether it serves the contract. It is
a runner with a temporary fault at least as often as it is the wrong address.

`-no-probe` exists because a check with no way past it is a trap. Viff is not the authority
on what is at the end of a URL that an operator chose.

### Why a spawn names no runner

A URL on the spawn would spread the deployment through the charters: every charter that wants
the gpu pool would carry its address, and moving the pool would mean editing all of them. A
runner's name does the same, one step removed: a charter naming `mirror.runners.gpu` carries the
deployment's layout, and splitting the pool, or moving an agent from one runner to another, means
editing every charter that spawns it. What a charter knows is what its work needs. Which runner
has that is the deployment's to say, and it says it in what each runner offers.

### Why a spawn no runner takes is refused

Work run where its doer is not offered would be the same kind of bug as a misspelled resolver
that left work unsupervised. The spawn says one thing, and viff does another.

### Why a runner is not the agent

A place and an agent are independent. One field that chose both would make one choice answer
two questions, and force one entry for each pair of agent and place.

### Why health is not recorded or polled

A stored health value is a cache. It can be stale in the direction that does harm: it tells an
operator that a runner is well because it was well when something last looked.

There is no polling in the background for the same reason, and for one more. A column that is
always fresh needs an interval, a backoff and a rule for flapping. Once viff holds a standing
opinion about whether a runner is well, the next request is that viff act on it.

### Why `viff ps` reads the mirror

A fan-out to every runner would let one dead runner remove the whole list. Runners come and
go, and that is the point of the feature.

### Why the task records both the URL and the name

The split is the one place where this design can fail without a sign. If viff stamped only
the name, a restart would depend on the runner's registration still existing. Removing the registration
would then orphan work that is running correctly. A deletion would act at a distance.

### Why removal asks

Neither default is right for the use that motivates runners.

*Leave* looks safe, and it is not. Runners exist for a drive that deploys a runner, uses it,
and tears it down. So `remove` **is** teardown. If viff leaves the work, the task never
reports a terminal status when the place disappears, and a flow that waits on the task waits
forever.

*Cancel* is not safe either. When you change the URL of a runner, the running work still talks
to a place that exists. A rename is an add and a remove of the same physical runner. A cancel
there destroys live work for a change of bookkeeping.

So viff asks. The precedent is a spawn that no runner takes. Viff refuses it and does not
run it somewhere else, because the author is present and a guess has consequences. A removal with nothing running needs no flag, because there is nothing to be
wrong about.

### Why `-cancel` reports what it could not reach

Saying "cancelled 3" when three tasks are unreachable would be false. It is the same error as
recording a queued answer as delivered.

### Why `-leave` always succeeds

A refusal with no way to satisfy it would be a trap. The cost is that `viff ps` shows tasks
on a runner that `viff runner` does not list. That is the true state, and the alternatives
are worse.

### Why routing is by fit and load only

A runner's offers say what it holds, so "a runner that holds what this spawn asks for" is
computed from them, and nothing else is needed for it. Choosing among agents, by capability or
cost, is a different construct, and building it against a real need will give a better answer
than guessing the vocabulary now. The order among runners that fit, fewest tasks then name, is a
first choice, not a design.

### Why there are no standing runners

A standing runner makes the runner a property of the *subject*, not of the spawn. The two
would need a rule of layers to resolve them.

### Why no health routes a spawn

A check before the spawn looks free, and it is not. **The handshake is the better probe.** It
asks the one runner the spawn is about to go to, right before it goes, and its answer is about
*this launch*, not about the runner in general. A runner with no description is passed over
rather than asked, since asking every such runner at every spawn would be the polling this
avoids; describing it is the runner's hello, viff's probe at start, and `viff runner check`.

### Why authority over runners matters more than over resolvers

A runner is a place where work executes, so registering one is closer to choosing where work
executes than to choosing an answerer: the runner receives whatever work it offers to take, from
every spawner. That is what a person's permissions on `mirror.runners` govern: installing, removing
and seeing runners. They do not govern where a spawner's work runs, which viff decides, in its
own view of the registrations, from what each runner offers.

The install probe adds a smaller part to the same question. Writing a runner's registration makes
viff send a request to an address that the writer chose.

### Why a runner's registration says nothing about what work may do

A limit of the form "work here may only do this" is the same narrowing a runner's ceiling answers.
Answering it in two places, without deciding how the two compose, is how a ceiling comes to
mean two things. The runner's offer gives that composition, so viff does not invent a second
vocabulary for it.

### Why writing a runner onto a task does not move it

The data model invites the assumption. Data is level truth, so a different runner on
`mirror.task.instance` looks as if it should move the task. The field is a record, and a record
does not move running work.
