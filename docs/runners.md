# Runners

A runner is a place verse runs a task. One is installed as an entity, a spawn names one, and the
task records which it went to. So a drive can deploy somewhere to run work the verse deployment
does not otherwise need to know about, and use it for *some* of the work rather than all of it.

Per-spawn selection is the whole of it. Deploying a runner is not the hard part: `broker.New()`
and `WorkerHandler()` are exported, and a second `verse up --broker` is one command. Using one for
*part* of the work is the hard part. A runner that every spawn must go to is a whole second verse,
with its own store, engine and entities, and the drive's work is then somewhere else entirely,
which is the opposite of dogfooding in the deployment.

To build one, start from [the sample runner](reference/examples/runner/main.go). It is a real
program that compiles with the tree, and the two functions you change are marked.

## Installing one

A runner lives at `verse.runners.<kind>:<name>` and carries a url, the verse-facing
[task-control](task-control.md) base. `kind` is the operator's own category: what this runner *is*,
the way a resolver's kind is what it is *for*.

```sh
verse runner add pool.gpu http://runner-gpu.verse.svc:9797
verse runner
verse runner check verse.runners.pool.gpu
verse runner show verse.runners.pool.gpu
```

`add` also reads a document, which is what a deployment writes:

```tony
name: gpu
kind: pool
url:  "http://runner-gpu.verse.svc:9797"
```

There is no default and no local special case. `local` is what the *absence* of a runner on a spawn
means, so every installed runner is somewhere to reach, which is why a runner with no `url` is
refused.

**Adding one probes it**, with the cheapest question that has an answer: `GET /tasks`, which
the contract already serves and which has no effect on the runner. Three outcomes, and only one
of them refuses the install:

| | |
|---|---|
| *a runner* | it answered, and the body decoded as the task list the contract promises |
| *not a runner* | it answered *that it has no task list* — 404 or 501, or a body that does not decode — the install is **refused** |
| *unreachable* | nothing answered, or what answered was failing rather than saying what it is — **installed, with a warning** |

They differ on whether waiting could change the answer, not on whether anything answered. A url
that answers *that it has no task list* is stating what it is, and stating it now: something is at
that address and it is not a runner face. A broker's worker leg is exactly this, serving claim,
inbox and report and no `GET /tasks`, and it is the confusion worth catching at install rather than
at the first spawn. A url nothing answers is the ordinary state of a runner deployed alongside the
entity that names it: the manifest lands, the pod is pending, the entity is already written.
Refusing that would make deployment order-dependent for a fact that fixes itself.

**A runner answering 500 is *unreachable*, not *not a runner***, so it is installed with a warning
rather than refused. A url speaking HTTP and failing has said nothing about whether it serves the
contract, and is a runner having a bad day at least as often as it is the wrong address. Only 404
and 501, meaning no such face, and an undecodable task list are answers about what is there.

`--no-probe` installs either way. A check with no way past it is a trap, and verse is not the
authority on what is at the end of a url an operator chose.

## Naming one on a spawn

A charter names a runner the way it names a provider or a resolver:

```tony
name: retrain-on-gpu
condition:
  subject: {path: [demo, model, *], value: { stage: ready }}
action:
  spawn:
    prompt:   "retrain the ranker on $subject"
    runner: verse.runners.pool.gpu          # ← where it runs
    driver: scripted                  # ← what runs it
    vector: auto                      # ← how much it may decide alone
    grant:  no-delegate               # ← what it may do
  onDone:
    patch: { stage: retrained }       # the latch: out of its own selection
```

and so does an ad-hoc run:

```sh
verse task run --runner verse.runners.pool.gpu "retrain the ranker"
```

A **name**, not a url, and the indirection is the point. A url on the spawn would spread the
deployment through the charters, so every charter wanting the gpu pool would carry its address, and
moving the pool would mean editing all of them.

**A spawn naming a runner that is not installed is refused**, on both spawn paths, rather than
quietly falling back to the default. A misspelled runner that ran the work in-process would be the
same class of bug as a misspelled resolver that quietly left work unsupervised: the deployment says
one thing and the verse does another. An ad-hoc run is refused with the name in the message and
nothing is started. A charter spawn has nobody to answer, so it fails the spawn and the reason
travels onto the flow.

A spawn that names none goes to the verse's own default runner, which `verse up --runner` and
`--broker` set.

### Not a provider

[Provider selection](provider-selection.md) picks the **doer**: which driver, under which frame,
with which tools and grant. A runner is **where the work runs**. The two are orthogonal, since the
same doer may run in either of two places and the same place may host either of two doers, so
folding runners into `provider:` would make one selection answer two questions and force a doer
entity per (doer, place) pair.

## Seeing where

```sh
verse ps                           # each task names its runner; the default reads `local`
verse ps --runner verse.runners.pool.gpu # only that runner's work
verse runner                       # what is installed, and what each is carrying
verse runner --probe               # …and what each of them says right now
```

`verse runner` shows `running` (now), `total` (ever) and `lastSpawn`, so a runner nobody names
is visible as such. Those columns are **derived from the store**, and `--probe` is the only thing
that asks anybody.

**The store never records health.** A probe is answered live, printed, and discarded, so a
reachability column is always as of now rather than a cache that can be stale in the direction that
costs: telling an operator a runner is well because it was well when something last looked. There
is no background polling, for the same reason and one more. A column that is always fresh needs an
interval, a backoff and a flap rule, and once verse holds a standing opinion about whether a runner
is well, the next ask is that it act on one.

`verse ps --runner` selects on the name **stamped at spawn** rather than on what is installed now,
so it keeps answering for a runner that has been removed, which is when an operator most wants it
and is exactly the case `verse runner` does not show.

`verse ps` reads the `verse.task.instance` mirror rather than asking each installed runner, so one
runner being down costs live detail and not rows. A fan-out would let a single dead runner take out
the whole list, in a feature whose point is that runners come and go.

## What a restart does

`verse.task.instance` records both the resolved **url** and the **name** the spawn used. The
url is what a restart reconstructs the runner from, so work outlives the verse that started it
*and* outlives the entity that named its runner: removing a runner entity cannot orphan work by
making it unreconstructible. The name is what a person reads and what `verse ps --runner`
selects on.

That split is the one place this design can go quietly wrong. Stamping only the name would make
a restart depend on the runner entity still existing, so removing one would orphan work that is
running perfectly well — a deletion with an action at a distance.

## Removing one

Verse cannot *move* a running task between runners. It can cancel one, or leave it, and the
difference is an intent it cannot read from a delete. So **removal with work on it is refused until
told which**:

```
$ verse runner remove verse.runners.pool.gpu
refused: 3 task(es) are running on verse.runners.pool.gpu, and removing it means one of two
  things this cannot tell apart:
    --cancel "<why>"   the place is going away; end them with a reason their charters see
    --leave            the place stays; they keep running on the url already stamped on them
$ verse runner remove verse.runners.pool.gpu --cancel "gpu pool retired"
```

Neither default survives the motivating use. *Leave* looks safe and is not: the lifecycle this
feature exists for is a drive deploying a runner, using it, and tearing it down, so `remove` **is**
teardown, and leaving the work means the task never reports a terminal when the place disappears,
so a flow waiting on it waits forever. *Cancel* is not safe either: retargeting a url leaves running
work talking to a place that still exists, and a rename is an add and a remove of the same physical
runner. Cancelling there destroys live work for a bookkeeping change.

So verse asks. The precedent is a spawn carrying `provider:` with no `grant:`, refused at install
rather than defaulted, on the reasoning that the author is present and a guess has consequences. A
removal with nothing running on it needs no flag, since there is nothing to be wrong about.

`--cancel` **reports what it could not reach** rather than counting it ended. An already-gone
runner is why an operator removes the entity half the time, so that is the ordinary case; saying
"cancelled 3" when three are unreachable would be the same lie as recording a queued answer as
delivered. Those are orphans, and the orphan path covers them.

The removal itself is never refused. `--leave` always succeeds, because there is no drain here and
a refusal with no way to satisfy it would be a trap. Its visible cost: `verse ps` shows tasks on a
runner that `verse runner` does not list. That is the true state, and the alternatives are worse.

Removing a resolver binding *hands on* what it held, because a question has a layer above to go
to. A runner has no layer above — work in flight is somewhere physical — so the choices are
leave it or end it, and neither is a hand-on.

## What this does not do

**No selection beyond a name.** No `match`, no `capability`, no `select: auto|ask|vote`. A drive
that needs the gpu pool names the gpu pool. The provider machinery is there to copy when somebody
wants "any runner with a gpu", and copying it then, against a real want, will produce a better
answer than guessing at the vocabulary now.

**No standing runners.** A resolver may be attached by a condition covering a subject; a runner may
not. "All work about payments runs on the payments pool" is a coherent thing to want and a different
feature. It makes the runner a property of the *subject* rather than of the spawn, and the two would
need a layer rule to resolve.

**No scheduling, no capacity, and no health that routes.** Verse hands a spawn to a runner and the
runner either takes it or does not. There is no queue, no retry onto a second runner, no draining.
The probes are diagnosis and never dispatch: nothing consults one before a spawn, and an unreachable
runner is not skipped or demoted. That is sharpest at the spawn path, where a pre-flight check looks
free and is not. **The spawn is the better probe**, since it asks the only question that matters and
gets an answer about *this work* rather than about the runner in general. A runner that is down
fails its spawns, and that failure travels the path a failed spawn already travels.

**No per-runner authority.** Which runners a charter may name belongs to
[authority](roadmap/authority.md), and the blast radius is larger than for a resolver: a runner is
a place work executes, so naming one is closer to choosing an execution environment than to
choosing an answerer. A charter that may name any runner may run its work anywhere the deployment
can reach. The install probe adds a smaller edge to the same question — writing a runner entity
makes verse issue a request to an address the writer chose, before anything has decided the
writer may name runners at all.

**A runner entity carries a url and nothing else.** A grant ceiling, a tool allowlist, a label —
anything saying "work here may only do this" — is expressible and deliberately absent. It is the
same narrowing a doer answers, and answering it in two places without deciding how they compose is
how a ceiling comes to mean two things.

The narrowing still has somewhere to live. It lives *at the runner*, declared by whoever deployed
it, and the [coding runner](reference/examples/README.md#running-the-coderunner) is the worked
example. An environment there names which repositories exist, which tools a task has at all, what
it may reach over the network, and where its work may be pushed, and a spawn names that environment
and can only narrow it. So a charter says where work runs and the deployment says what running
there permits, which is the composition verse declined to invent a second vocabulary for. What
crosses back is an ordinary action with a typed effect, gated by the vector like anything else, so
nothing about the arrangement is private to the runner.

**Running work cannot be moved.** The entity model invites the assumption: entities are level
truth, so writing a different runner onto a `verse.task.instance` looks like it should move the
task. It does not. That field is a record of where the work went, not an instruction about where it
should go.

## What holds by convention

Two corners are open, and both are recorded in
[what is left](working/runners-residual.md). The contract does not say *how* a runner applies
control, so anything reached over a url is taken to apply it synchronously and a runner that queues
is relied on to refuse. And `local` is the meaning of *absent* rather than a name a charter can
write.
