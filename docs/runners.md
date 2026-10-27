# Runners

A [runner](glossary.md#runner) is a place where verse runs a [task](glossary.md#task). You
install a runner as an entity. A [spawn](glossary.md#spawn) names a runner, and the task
records which runner it went to.

So a drive can deploy a place to run work that the verse deployment does not otherwise need to
know about. The drive can use that place for *some* of its work, and not for all of it.

**Selection for each spawn is the whole feature.** Deploying a runner is easy: the sample
runner is one program. The hard part is using a runner for *part* of the work.

To build a runner, start from [the sample runner](reference/examples/runner/main.go). The
sample is a real program that compiles with the tree. Comments mark the two functions that you
change.

## Installing one

A runner lives at `verse.runners.<name>`. It carries a URL: the base URL of
[task-control](task-control.md) that faces verse. The name of a runner is its address.

```sh
verse runner add gpu http://runner-gpu.verse.svc:9797
verse runner
verse runner check verse.runners.gpu
verse runner show verse.runners.gpu
```

`add` also reads a document. A deployment writes this form:

```tony
name: gpu
url:  "http://runner-gpu.verse.svc:9797"
```

There is no default runner entity, and no special case for local. `local` is what the
*absence* of a runner on a spawn means. So every installed runner is a place to reach, and
verse refuses a runner with no `url`.

**Adding a runner probes it.** The probe is `GET /tasks`, the cheapest question that has an
answer. The contract already serves this route, and it has no effect on the runner. The probe
has three outcomes, and only one of them refuses the install:

| Outcome | What happened | Result |
|---|---|---|
| *a runner* | the URL answered, and the body decoded as the task list that the contract promises | installed |
| *not a runner* | the URL answered *that it has no task list*: 404 or 501, or a body that does not decode | the install is **refused** |
| *unreachable* | nothing answered, or what answered was failing and did not say what it is | **installed, with a warning** |

**A runner that answers 500 is *unreachable*, not *not a runner***. So verse installs it with
a warning, and does not refuse it. Only 404 and 501, which mean no such route, and a task list
that does not decode are answers about what is at the URL.

### Who the runner is

Verse decides who a runner is at registration and nowhere else. A runner registered with a
`subject`, the subject of the workload token its runtime gives it, has to prove it is that
runner, twice over:

```sh
verse runner add verse-runner http://verse-runner:9802 -subject system:serviceaccount:verse:verse-runner
```

- **At the describe.** Verse presents a token it signs itself, as the issuer `auth.issuer`
  makes it, on every call it makes to a runner registered with a `subject`, and to no other,
  since a token presented before anything proved what answers is a token given away. The token
  is minted for that runner alone, its subject the audience, so a copy taken at one runner opens
  no other's door. A runner whose configuration names its verse (`verse:` in
  [runnerd.md](reference/runnerd.md#configuration)) reads verse's keys where it reaches verse,
  holds every bearer to verse's issuer name and its own audience, and refuses every other
  caller's launch. The
  description itself is public: anyone may ask what the runner offers. To the verse it
  verified, and to nobody else, the runner answers `GET /describe` with its own token, and
  verse checks the token's subject against the registration before it records what the runner
  offers. The registration
  then says `verified`, with when. A runner that answered as a runner and did not prove it is
  the one registered is refused at `add`, since waiting changes nothing; found later, by
  `verse runner check` or by a spawn that asks, it has `unverified` recorded in place of what it
  described, and takes no spawn until a check finds it proving itself. The describe is asked
  again before every launch to a runner registered with a subject, one request, since the launch
  is what hands whatever answers at the url the prompt, the repositories and the policy: a runner
  replaced behind the same url receives no launch, and `verified` is the last handshake's result,
  re-made at each.
- **At verse's door.** When the runner calls verse, its token makes it `runner:<identity>`
  ([runners at the door](reference/perms.md#runners-at-the-door)), and its name alone is refused.

The runner's first call is to say it is here. A runner whose configuration names its verse's
url announces itself at start, `POST /runners/hello` with its token, in a loop until verse
has described it, and verse describes it then and there, so a pod that comes up after its registration,
or comes back after a restart, is verified within seconds and nobody runs `verse runner check`.
The door is the acknowledgement: a token no registration names is refused before anything else,
and that refusal is the runner's cue to keep asking, with one log line saying why. The runner's
readiness, on `GET /ready`, is its server and nothing else. Whether verse has heard it, and
whether the describe that followed verified it, is the registration's to say, in `verified` or
`unverified`, where `verse runner` shows it.

A runner no runtime issues a workload token to, on a machine off Kubernetes, proves itself
with a static token instead: one it and its registrar hold, as an iod may. The registration
names the token by its hash, which becomes the subject, so verse never holds the token and the
registration stays everyone's to read:

```sh
verse runner add edge https://edge.example.com:9802 -token-file edge.token
```

The runner reads the same token at `VERSE_TOKEN_FILE`, answers it at the describe and presents
it at verse's door, and takes verse's tokens for the same hash as its audience; nothing else is
configured. A static token holds 32 bytes at least, since its hash is published. Each kind of
proof finds only its own kind of registration, so a workload token cannot claim a static
registration, nor a static token a workload one. No workload issuer is needed; verse still
issues its own tokens (`auth.issuer`), since the handshake runs both ways.

A runner registered without a `subject` is taken at its word, as every runner was: `verse
up`'s embedded runner, a runner on a laptop, a verse that issues no tokens of its own. The
record says which.

`-no-probe` installs the runner in every case. A runner installed without its offers takes no
spawn until it has described itself: verse asks it again when it starts, at the first spawn
that names it, and before every launch where the registration names a subject, and keeps what
it says on the registration; `verse runner check` asks on demand.
A spawn naming a runner that still has not answered is refused, with the reason.

A [worker](glossary.md#worker) is named on its runner's entity, or names itself on its first
running status. The [claim](glossary.md#claim-of-a-worker) is where verse assigns the task to
the worker:

- versed writes `read-write` and `control` for the worker on the task's record;
- the mirror writes every report from then on as the worker;
- verse lists the worker as a runner principal.

The grant reaches the task and nothing else. It does not reach the questions that the task
raises, and the worker may not answer them ([perms.md](perms.md)).

## Naming one on a spawn

A charter names a runner by name:

```tony
name: retrain-on-gpu
condition:
  subject:
    path: "demo.model.*"
    value:
      stage: ready
action:
  spawn:
    prompt:   "retrain the ranker on $subject"
    runner: verse.runners.gpu                             # ← where it runs
    agent: {name: verse}                                  # ← who does it
    environment: {systems: {network: {mode: open}, files: {}, commands: {}}}   # ← what it may reach
    autonomy:                                             # ← what it may do unasked
    - {match: {system: files}, then: allow}
    - {match: {system: commands}, then: allow}
    - {then: ask}
  onDone:
    patch: { stage: retrained }       # the latch: out of its own selection
```

An ad-hoc run names a runner too, in the spawn it runs:

```sh
echo '{prompt: "retrain the ranker", runner: verse.runners.gpu, environment: {systems: {network: {mode: open}}}}' |
  verse task run -f -
```

A spawn names a runner by **name**, not by URL. The name is an indirection, and the reason
for it is in [Why it is this way](#why-it-is-this-way).

**Verse refuses a spawn that names a runner that is not installed**, on both spawn paths. It
does not fall back to the default runner.

| Path | What verse does |
|---|---|
| ad-hoc run | refuses the run, with the name in the message. Nothing starts. |
| charter spawn | nobody is there to answer, so verse fails the spawn, and the reason goes onto the flow |

A spawn that names no runner goes to the default runner of this verse, which
`verse up -runner` sets.

### Not the agent

A spawn's `agent` says **who** does the work: which agent, under which model and credential (or a
`script`, which is its own). A runner is **where the work runs**. The two are independent: the
same agent may run in either of two places, and the same place may host either of two agents.

## Seeing where

```sh
verse ps                           # each task names its runner; the default reads `local`
verse ps -runner verse.runners.gpu # only that runner's work
verse runner                       # what is installed, and what each is carrying
verse runner -probe                # …and what each of them says right now
```

`verse runner` shows three columns:

| Column | What it counts |
|---|---|
| `running` | tasks on the runner now |
| `total` | tasks on the runner ever |
| `lastSpawn` | the last spawn to the runner |

So you can see a runner that nobody names. Verse **derives these columns from the store**.
`-probe` is the only thing that asks anybody.

**The store never records health.** Verse answers a probe live, prints it, and discards it.
So a column of reachability is always as of now. There is no polling in the background.

`verse ps -runner` selects on the name that verse **stamped at the spawn**, not on what is
installed now. So it keeps answering for a runner that somebody removed. An operator most
wants the answer then, and `verse runner` does not show that case.

`verse ps` reads the `verse.task.instance` mirror. It does not ask each installed runner. So
when one runner is down, you lose live detail and not rows.

## What a restart does

`verse.task.instance` records both the resolved **URL** and the **name** that the spawn used.

- A restart rebuilds the runner from the URL. So work outlives the verse that started it, and
  it *also* outlives the entity that named its runner. Removing a runner entity cannot orphan
  work by making it impossible to rebuild.
- A person reads the name, and `verse ps -runner` selects on it.

## Removing one

Verse cannot *move* a running task between runners. It can cancel the task, or leave it. The
choice is an intent, and verse cannot read it from a delete. So **verse refuses a removal
with work on the runner until you say which**:

```
$ verse runner remove verse.runners.gpu
refused: 3 task(es) are running on verse.runners.gpu, and removing it means one of two
  things this cannot tell apart:
    --cancel "<why>"   the place is going away; end them with a reason their charters see
    --leave            the place stays; they keep running on the url already stamped on them
$ verse runner remove verse.runners.gpu -cancel "gpu pool retired"
```

A removal with nothing running on the runner needs no flag.

`-cancel` **reports the tasks that it could not reach**. It does not count them as ended.
Those tasks are orphans, and the orphan path covers them. A runner that is already gone is the
reason for half of all removals, so this is the ordinary case.

With a flag, the removal itself is never refused. `-leave` always succeeds, because there is
no drain here. The visible cost of `-leave` is that `verse ps` shows tasks on a runner that
`verse runner` does not list. That is the true state.

Removing a resolver *hands on* what the resolver held, because a question has a layer above to
go to. A runner has no layer above: work in flight runs in a physical place. So the choices are
to leave the work or to end it. Neither choice hands the work on.

## What this does not do

**No selection beyond a name.** There is no `match`, no `capability` and no
`select: auto|ask|vote`. A drive that needs the gpu pool names the gpu pool.

**No standing runners.** A condition that matches a subject may attach a resolver. It may not
attach a runner. "All work about payments runs on the payments pool" is a reasonable thing to
want, and it is a different feature.

**No scheduling, no capacity, and no health that routes.** Verse hands a spawn to a runner,
and the runner takes it or does not. There is no queue, no retry on a second runner, and no
draining. The probes diagnose and never dispatch. Nothing consults a probe before a spawn, and
verse does not skip or demote an unreachable runner. A runner that is down fails its spawns.
That failure takes the path that every failed spawn takes.

**No authority for each runner.** Nothing decides which runners a charter may name. A charter
that may name any runner may run its work anywhere that the deployment can reach.

**A runner entity carries a URL and who it acts as, and nothing about what work may do
there.** A grant ceiling, a tool allowlist, a label: verse can express each of these, and each
is absent by design.

The narrowing lives *at the runner*. Whoever deploys the runner declares it. The shipped
runner, `runnerd verse`, is the worked example; its configuration names:

- which systems it holds for its tasks, and the ceiling over what any task may do with each, the forge its repositories come from among them;
- which agents it offers, and the settings each takes (which models among them);
- what its sandbox can build, which the runner says itself rather than its configuration.

The runner publishes that at `GET /describe`, and verse reads it: `verse runner add` is the
probe, and what the runner offers is kept on its registration (`described`, which `verse runner
show` prints); `verse runner check` reads it again, so a runner that offers more after a
restart says so, and a registration kept from an older runner or verse is asked again. Each
offer holds the network, with the modes its sandbox builds under `builds`, or verse does not
install the runner. A spawn says what its task may reach, in its `environment` (the
repositories and what may leave each, the network, the other systems), and what it does unasked,
in its `autonomy`, and verse checks the spawn against the offer of the runner it names where the
spawn is written: a charter is refused at install, and `verse task run` at the prompt, naming
an agent the runner does not offer, a setting it does not take, a system it does not hold, a
block beyond its ceiling, a network it does not build, or a step its agent can't run without
and the environment leaves out. The runner checks the launch again when it arrives, and
refuses what it cannot honour. So a charter says where work runs, and the deployment says what
running there permits. What comes back is an ordinary step, which the task's environment and
autonomy decide like any other, so nothing in the arrangement is private to the runner. The sample in [examples/runner](reference/examples/runner/main.go) is the one to
copy for a runner of your own.

**Running work cannot be moved.** Writing a different runner onto a `verse.task.instance`
does not move the task. That field records where the work went. It is not an instruction about
where the work should go.

## What holds by convention

Two cases are open:

- The contract does not say *how* a runner applies control. So verse assumes that anything it
  reaches over a URL applies control synchronously. Verse relies on a runner that queues
  control to refuse it.
- `local` is the meaning of *absent*. It is not a name that a charter can write.

## Why it is this way

### Why selection is for each spawn

A runner that every spawn must go to is a whole second verse, with its own store, engine and
entities. The drive's work is then somewhere else entirely. That is the opposite of running
the drive in the deployment it works on.

### Why the probe refuses only "not a runner"

The three outcomes differ on whether waiting could change the answer. They do not differ on
whether anything answered.

A URL that answers *that it has no task list* states what it is, and states it now. Something
is at that address, and it does not serve the runner contract. Verse catches this mistake at
install, not at the first spawn.

A URL that nothing answers is the ordinary state of a runner that is deployed with the entity
that names it. The manifest lands, the pod is pending, and the entity is already written. A
refusal there would make deployment depend on order, for a fact that fixes itself. A spawn
naming it waits on the same fact: refused, saying so, until the runner answers, and checked
against what it says from then on.

A URL that speaks HTTP and fails has said nothing about whether it serves the contract. It is
a runner with a temporary fault at least as often as it is the wrong address.

`-no-probe` exists because a check with no way past it is a trap. Verse is not the authority
on what is at the end of a URL that an operator chose.

### Why a spawn names a runner and not a URL

A URL on the spawn would spread the deployment through the charters. Every charter that wants
the gpu pool would carry its address, and moving the pool would mean editing all of them.

### Why an unknown runner is refused

A misspelled runner that ran the work in-process would be the same kind of bug as a
misspelled resolver that left work unsupervised. The deployment says one thing, and verse does
another.

### Why a runner is not the agent

A place and an agent are independent. One field that chose both would make one choice answer
two questions, and force one entry for each pair of agent and place.

### Why health is not recorded or polled

A stored health value is a cache. It can be stale in the direction that does harm: it tells an
operator that a runner is well because it was well when something last looked.

There is no polling in the background for the same reason, and for one more. A column that is
always fresh needs an interval, a backoff and a rule for flapping. Once verse holds a standing
opinion about whether a runner is well, the next request is that verse act on it.

### Why `verse ps` reads the mirror

A fan-out to every runner would let one dead runner remove the whole list. Runners come and
go, and that is the point of the feature.

### Why the task records both the URL and the name

The split is the one place where this design can fail without a sign. If verse stamped only
the name, a restart would depend on the runner entity still existing. Removing the entity
would then orphan work that is running correctly. A deletion would act at a distance.

### Why removal asks

Neither default is right for the use that motivates runners.

*Leave* looks safe, and it is not. Runners exist for a drive that deploys a runner, uses it,
and tears it down. So `remove` **is** teardown. If verse leaves the work, the task never
reports a terminal status when the place disappears, and a flow that waits on the task waits
forever.

*Cancel* is not safe either. When you change the URL of a runner, the running work still talks
to a place that exists. A rename is an add and a remove of the same physical runner. A cancel
there destroys live work for a change of bookkeeping.

So verse asks. The precedent is a spawn that names a runner that is not installed. Verse
refuses it and does not pick a default, because the author is present and a guess has
consequences. A removal with nothing running needs no flag, because there is nothing to be
wrong about.

### Why `-cancel` reports what it could not reach

Saying "cancelled 3" when three tasks are unreachable would be false. It is the same error as
recording a queued answer as delivered.

### Why `-leave` always succeeds

A refusal with no way to satisfy it would be a trap. The cost is that `verse ps` shows tasks
on a runner that `verse runner` does not list. That is the true state, and the alternatives
are worse.

### Why there is no selection beyond a name

A runner's offers say what it holds, so "any runner that holds what this spawn asks for" can be
computed when somebody wants it. Building that then, against a real need, will give a better
answer than guessing the vocabulary now.

### Why there are no standing runners

A standing runner makes the runner a property of the *subject*, not of the spawn. The two
would need a rule of layers to resolve them.

### Why no health routes a spawn

A check before the spawn looks free, and it is not. **The spawn is the better probe.** It
asks the only question that matters, and it gets an answer about *this work*, not about the
runner in general.

### Why authority over runners matters more than over resolvers

Nothing decides which runners a charter may name, and the effect of a mistake is larger than
for a resolver. A runner is a place where work executes. So naming a runner is closer to
choosing where work executes than to choosing an answerer.

The install probe adds a smaller part to the same question. Writing a runner entity makes
verse send a request to an address that the writer chose. Nothing has yet decided that the
writer may name runners at all.

### Why a runner entity says nothing about what work may do

A limit of the form "work here may only do this" is the same narrowing a runner's ceiling answers.
Answering it in two places, without deciding how the two compose, is how a ceiling comes to
mean two things. The runner's offer gives that composition, so verse does not invent a second
vocabulary for it.

### Why writing a runner onto a task does not move it

The entity model invites the assumption. Entities are level truth, so a different runner on
`verse.task.instance` looks as if it should move the task. The field is a record, and a record
does not move running work.
