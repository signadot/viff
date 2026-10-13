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

`-no-probe` installs the runner in every case.

A [worker](glossary.md#worker) is named on its runner's entity, or names itself on its first
running status. The [claim](glossary.md#claim-of-a-worker) is where verse assigns the task to
the worker:

- versed writes `read-write` and `control` for the worker on the task's record;
- the mirror writes every report from then on as the worker;
- verse lists the worker as a runner principal.

The grant reaches the task and nothing else. It does not reach the questions that the task
raises, and the worker may not answer them ([perms.md](perms.md)).

## Naming one on a spawn

A charter names a runner in the same way that it names a provider:

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
    runner: verse.runners.gpu          # ← where it runs
    driver: scripted                  # ← what runs it
    vector: auto                      # ← how much it may decide alone
    grant:  no-delegate               # ← what it may do
  onDone:
    patch: { stage: retrained }       # the latch: out of its own selection
```

An ad-hoc run names a runner too:

```sh
verse task run -runner verse.runners.gpu "retrain the ranker"
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

### Not a provider

[Provider selection](provider-selection.md) picks the **doer**: which driver, under which
frame, with which tools and which grant. A runner is **where the work runs**.

The two are independent. The same doer may run in either of two places, and the same place
may host either of two doers.

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

The narrowing lives *at the runner*. Whoever deploys the runner declares it. The
[coding runner](reference/examples/README.md#running-the-coderunner) is the worked example. An
[environment](glossary.md#environment) there names:

- which repositories exist;
- which tools a task has at all;
- what the task may reach over the network;
- where the task may push its work.

A spawn names that environment and can only narrow it. So a charter says where work runs, and
the deployment says what running there permits. What comes back is an ordinary action with a
typed effect. The vector gates it like any other action, so nothing in the arrangement is
private to the runner.

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
refusal there would make deployment depend on order, for a fact that fixes itself.

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

### Why a runner is not a provider

A place and a doer are independent. Folding runners into `provider:` would make one selection
answer two questions. It would also force one doer entity for each pair of doer and place.

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

So verse asks. The precedent is a spawn that carries `provider:` with no `grant:`. Verse
refuses it at install and does not pick a default, because the author is present and a guess
has consequences. A removal with nothing running needs no flag, because there is nothing to be
wrong about.

### Why `-cancel` reports what it could not reach

Saying "cancelled 3" when three tasks are unreachable would be false. It is the same error as
recording a queued answer as delivered.

### Why `-leave` always succeeds

A refusal with no way to satisfy it would be a trap. The cost is that `verse ps` shows tasks
on a runner that `verse runner` does not list. That is the true state, and the alternatives
are worse.

### Why there is no selection beyond a name

The provider machinery is there to copy when somebody wants "any runner with a gpu". Copying
it then, against a real need, will give a better answer than guessing the vocabulary now.

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
choosing an execution environment than to choosing an answerer.

The install probe adds a smaller part to the same question. Writing a runner entity makes
verse send a request to an address that the writer chose. Nothing has yet decided that the
writer may name runners at all.

### Why a runner entity says nothing about what work may do

A limit of the form "work here may only do this" is the same narrowing that a doer answers.
Answering it in two places, without deciding how the two compose, is how a ceiling comes to
mean two things. The environment at the runner gives that composition, so verse does not
invent a second vocabulary for it.

### Why writing a runner onto a task does not move it

The entity model invites the assumption. Entities are level truth, so a different runner on
`verse.task.instance` looks as if it should move the task. The field is a record, and a record
does not move running work.
