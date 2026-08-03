# Per-process runners: a plan

Status: plan, nothing built. It answers git-issue `zwrnysjrh12ksrgye1n0`, whose ask is one
sentence and whose motivation is the thing that decides the design:

> to facilitate dogfooding in the deployment, verse should have per-process runners. This will
> enable deploying arbitrary runners in the verse namespace, including whatever is needed for a
> drive that verse itself doesn't need.

The last clause is the requirement. A drive needs a place to run work that the verse deployment
does not otherwise need to know about — a runner with a toolchain, a credential, a network
position, an image — and it must be able to add one without changing what verse runs for
everybody else.

## Where we are

**One runner per verse, chosen at startup.** `Server.runner` is a single field, set once:

```go
s.runner = localRunner{procd}          // the default: in-process
srv.SetRunner(runner.HTTP(cfg.Runner)) // verse up --runner <url>
srv.UseBroker(face)                    // verse up --broker
```

`verse up` refuses `--runner` with `--broker` — *"(one runner)"* — which is the premise this
plan changes. Every spawn goes wherever that field points.

What that costs is worth stating exactly, because it is easy to overstate. A drive *can* deploy
its own runner today, including a broker its workers dial into: `broker.New()` and
`WorkerHandler()` are exported, and a second `verse up --broker` is one command. What it cannot
do is use that runner for *some* of the work. A second runner means a second verse — its own
store, its own engine, its own entities — and the drive's work is then somewhere else entirely,
which is the opposite of dogfooding in the deployment.

So the missing thing is not the ability to run a runner. It is per-spawn selection.

**The seam is already right.** `process.Runner` is three methods — `Spawn`, `Attach`, `Ref` —
and `Handle` already hides whether a process runs in-process, behind a URL, or on a worker that
dials in. Nothing about running work in several places is blocked by the interface. What is
missing is *selection*: a way to say which one, per spawn, and a way for a restart to find it
again.

**A restart already depends on the answer.** `mirror.Meta.Runner` stamps `Ref()` on
`process:instance` precisely so a restart can reconstruct the runner and decide resume-vs-orphan
— `"local"` orphans, a base URL re-attaches. Whatever names a runner has to keep working after
the process that chose it is gone and the verse that spawned it has restarted.

## The shape

**A runner is an entity**, `runners:<kind>:<name>`, and a spawn names one. Both blocks below are
fenced `proposed` — none of this syntax exists yet, so the docs test counts them as skipped
rather than silently passing over them.

```tony proposed
name: gpu
kind: pool
url:  "http://runner-gpu.verse.svc:9797/broker/local"
```

```tony proposed
action:
  spawn:
    task:   "retrain the ranker on $subject"
    runner: runners:pool:gpu          # ← where it runs
    driver: llm                       # ← what runs it
    grant:  no-delegate               # ← what it may do
```

Three reasons for an entity rather than a startup flag or a bare URL on the spawn.

*It is the only form the ask admits.* "Deploying arbitrary runners in the verse namespace"
means adding one is a deployment act — write an entity — not an edit to how verse itself is
started. A drive adds a runner the way it adds a doer or a resolver: by writing, not by
restarting the thing everybody else is using.

*A bare URL on the spawn spreads the deployment through the charters.* Every charter that
wanted the gpu pool would carry its address, and moving the pool would mean editing all of
them. The name is the indirection, and it is the same indirection `provider:` and
`resolvers:` already are.

*It gives the restart something to read.* `Ref()` can stay a URL — the thing a restart can
actually reconstruct from — while the charter names something stable. That split is discussed
below, because it is the one place this design can go quietly wrong.

### Not a provider

The nearest existing thing is [provider selection](../provider-selection.md), and it is worth
saying plainly why this is not that. A provider picks the **doer**: which driver, under which
frame, with which tools and grant. A runner is **where the work runs**. The two are orthogonal
— the same doer may run in either of two places, and the same place may host either of two
doers — so folding runners into `provider:` would make one selection answer two questions and
force a doer entity per (doer, place) pair.

What this *should* borrow from providers is the vocabulary if selection ever grows past a name:
`runner: { capability: gpu, match: {…}, select: auto }` is the shape, and it is deliberately not
in this plan (see the last section).

## Order of work

Four steps. Each leaves the tree green and each is visible from the CLI, for the reason the
resolver plans gave: a runner that cannot be seen installed, seen chosen, or seen running
something is one nobody can debug.

### 1. A runner is an entity, and a verb manages it

`runners:<kind>:<name>` carrying `url` and nothing else this increment reads.
`verse runner add|show|remove`, `verse runners`.

A verb rather than a raw write, for the reason `verse resolver add` is one: a write has nowhere
to put the checks. Here they are real from the first commit — a `url` that does not parse, and a
name containing the characters an id cannot survive.

Refused: a runner with no `url`. There is no default and no local special case at this layer;
`local` is what the *absence* of a runner means, which keeps the entity honest — every runner
entity is somewhere to reach.

**Removal does nothing to work already running on it**, and that has to be said here rather than
left to step 3, because `remove` is introduced here.

Nothing, because there is nothing honest it could do. verse cannot move a running process
between runners, and it will not kill work to tidy up a name. The entity was how a spawn *found*
that place; the process already went there, and step 3 stamps the resolved URL on it so verse
keeps reaching it — through the removal, and through a restart after it.

Worth naming the asymmetry with the thing this most resembles. Removing a resolver binding
*hands on* what it held, because a question has a layer above to go to. A runner has no layer
above: work in flight is somewhere physical, and "somewhere else" is not a thing verse can offer
it. So removal means only *stop choosing this*, and the plan should not dress it up as more.

But it is not silent. Removal **warns**, naming how many processes are still on it — the same
notice-rather-than-refusal move an overlapping resolver condition makes, and computable from the
counts step 4 adds:

```
$ verse runner remove runners:pool:gpu
removed runners:pool:gpu
note: 3 processes are still running on it; they keep running and stay reachable, and no
      new spawn may name it
```

Not a refusal, because there is no drain — this plan builds no way to move or wait out that
work, so refusing would mean a runner with one long-lived process on it can never be removed.

The visible cost, stated rather than discovered: `verse ps` will show processes on a runner that
`verse runners` no longer lists. That is the true state and the alternative is worse — either
lying about where work is, or refusing a removal forever.

**Proves:** an operator can install, list and remove one; a runner that cannot be reached is
refused rather than accepted; and removing one does not disturb what runs on it. Test: add, list,
remove over the CLI; a malformed url is refused with the url quoted; a removal while a process
is running on it warns, names the count, and leaves the process running and reachable.

### 2. A spawn names one

`launch.Spec.Runner` and `api.Spawn.Runner`, plus `verse process run --runner`. The server
resolves the name at spawn time and hands `runner.HTTP(url)` to that one spawn.

The resolution is where this earns its first refusal, and it is the one the resolver work
already learned: **a spawn naming a runner that is not installed is refused**, on both spawn
paths, rather than silently falling back to the default. A misspelled runner that quietly ran
the work in-process would be the same class of bug as a misspelled resolver that quietly left
work unsupervised — the deployment says one thing and the verse does another.

`Server.runner` stays, as the default for a spawn that names none. `--runner`/`--broker` at
startup keep meaning what they mean: they set that default. They stop being mutually exclusive
with *each other* only if somebody wants that; this plan does not need it.

**Prerequisite**, and it is not optional: `httpRunner.Spawn` discards the response status, so a
runner REFUSING a spawn is treated as having accepted it (git-issue `6mnmxj6mh12krz3ae1n0`). A
refusal has to read as a refusal before a spawn may name an arbitrary runner — otherwise the
first thing a drive meets when it points work somewhere that will not take it is a process that
hangs. See the residual, which is also where the queued-vs-delivered gap this closes is
argued.

**Proves:** two processes in one verse run in two places. Test: a spawn naming an installed
runner reaches it (an `httptest` runner records the call) while a spawn naming none goes to the
default; a spawn naming an uninstalled one is refused, naming it.

### 3. The restart finds it again

This is the step that decides whether the feature is safe, and it is worth doing third rather
than last because steps 1 and 2 are cheap to change and this is not.

`process:instance` stamps `runner: <Ref()>` today, and a restart reads it to decide
resume-vs-orphan. With per-process runners the question becomes: **what is stamped, the name or
the URL?**

Stamp the **URL** — `Ref()` unchanged, the resolved address — and additionally stamp
`runnerName` when the spawn named one. The URL is what a restart can act on with nothing else
present; the name is what a person reads and what a later change can re-resolve. Stamping only
the name would make a restart depend on the runner entity still existing, so removing a runner
entity would orphan running work by making it unreconstructible — a deletion with an action at a
distance, which is exactly the shape [the resolver work](../attached-resolvers.md) refused when
it made removal *hand on* what it held rather than strand it.

This is also what makes step 1's answer about removal true. Removal leaves running work alone
*and reachable* only because the URL is on the process rather than a name that has just been
deleted — the two decisions are one decision, and getting this one wrong would turn a tidy-up
into an outage.

**Proves:** work outlives the verse that started it, and outlives the entity that named its
runner. Test: spawn on a named runner, restart the server, and the process is re-attached rather
than orphaned; remove the runner entity while the process runs and the restart still re-attaches
it; a spawn naming the removed runner is refused.

### 4. An operator can see where

Not the docket — `verse docket` is motions, and this has nothing to do with them. This is the
process surface:

```sh
verse ps                      # each process names the runner it is on; the default reads `local`
verse ps --runner gpu         # …and only that runner's, which is the question a drive asks
verse runners                 # what is installed, and what each is carrying
```

`verse runners` earns its keep by showing more than the list it could have shown in step 1:

| | |
|---|---|
| `running` | processes on it now — `process:instance` with this runner and a live status |
| `total` | how many it has been given, ever |
| `lastSpawn` | when it was last used, so a runner nobody names is visible as such |

`verse ps --runner <name>` reads the `runnerName` step 3 stamps, so it keeps working for a runner
that has since been removed — which is when an operator most wants it, since that is the case
`verse runners` can no longer show.

All three are **derived from the store**, not probed. That is the line this plan holds
everywhere: verse reports what it has recorded, and does not ask a runner how it feels. A
reachability check is a health check wearing a smaller hat, and health is named below as
something this does not build — a runner that is down fails its spawns, and that failure travels
the path a failed spawn already travels.

Small, and last on purpose: it is the step that makes the previous three legible, and it is the
one that would otherwise be skipped.

**Proves:** an operator can answer "where is this running", and "what is this runner carrying",
without reading a charter. Test: `verse ps` names the runner per process and the default reads
as `local` rather than blank; `--runner <name>` selects; `verse runners` counts a spawn onto a
named runner and does not count one onto the default.


## What this plan does not build

**No selection beyond a name.** No `match`, no `capability`, no `select: auto|ask|vote`. A drive
that needs the gpu pool names the gpu pool. The provider machinery is there to copy when
somebody wants "any runner with a gpu" — and copying it then, against a real want, will produce
a better answer than guessing at the vocabulary now.

**No standing runners.** A resolver may be attached by a condition covering a subject; a runner
may not. "All work about payments runs on the payments pool" is a coherent thing to want and a
different feature: it makes the runner a property of the *subject* rather than of the spawn, and
the two would need a layer rule to resolve. Not until somebody asks.

**No scheduling, no capacity, no health.** verse hands a spawn to a runner and the runner either
takes it or does not. There is no queue, no retry onto a second runner, no draining. A runner
that is down fails its spawns, and that failure travels the path a failed spawn already travels.

**No per-runner authority.** Which runners a charter may name is the same open question as who
may install a resolver, and it belongs to [authority](../roadmap/authority.md). Worth stating
sharply here because the blast radius is larger: a runner is a place work executes, so naming
one is closer to choosing an execution environment than to choosing an answerer. A charter that
may name any runner may run its work anywhere the deployment can reach.

## The residual to expect

**Whether `local` should be nameable.** This plan makes `local` the meaning of *absent*, which
keeps every runner entity a reachable address. The cost is that a charter cannot say "explicitly
in-process" — it can only decline to say anything. If a deployment ever has a default that is
not local, that distinction starts to matter and `runners:local:local` (or a reserved kind, as
`resolvers:instance` is) becomes worth having.

**The runner contract does not say HOW control is applied, and this plan makes that reachable.**

An answer accepted for a brokered process is *queued* for a worker that must dial in and pull
it, and the question record says so — `delivered: false` until the acknowledgement clears it.
Recording that as done is what let an answer evaporate with a worker that never came back while
every surface said the question was settled (`an accepted answer is not a delivered one`).

verse learns which case it is by asking the handle, in Go:

```go
r, ok := h.(interface{ RelaysControl() bool })
return ok && r.RelaysControl()
```

`broker.handle` returns true. **`runner.HTTP`'s handle does not implement it at all**, and the
process-control wire has no representation of it — so anything reached over a URL is treated as
applying control synchronously.

That is *correct today*, because the only runner URL anyone binds is `/broker/local`, which
serves the local supervisor and does apply control synchronously. It is correct by
configuration, not by construction: nothing detects a runner that queues. And this plan is
exactly what turns "the person deploying verse chose one runner URL" into "anyone who can write
an entity chooses a runner URL per spawn".

So: *should brokered processes go through external runners?*

Not a blanket no, but the reason is narrower than "a drive needs its own broker" — a drive can
have one today. `broker.New()` and `WorkerHandler()` are exported, and a second `verse up
--broker` is a broker anybody can stand up. What it cannot be is **one runner among several**:
`UseBroker` calls `SetRunner`, `--broker` and `--runner` are mutually exclusive, and `HostBroker`
is a Go API rather than something reachable over HTTP. So a drive's broker today costs a whole
second verse — its own store, its own engine, its own entities — and then *everything* that
verse spawns goes there.

That all-or-nothing is what this feature removes, and it is what a ban would put back: a runner
entity that may not point at a queuing runner means a drive's broker can only be reached by
making it the whole verse's runner again. Not "you cannot deploy a broker" — "you cannot have
one for some of the work".

**The runner refuses, and that is the default.** A real runner knows what it is and what it can
host; verse does not, and cannot be made to without either a contract change or a probe. So the
runner handed a launch it cannot host with the semantics verse expects returns an error, and the
onus sits with whoever deployed it — which is the drive, which is the party that knows. For a
broker, refusing to be driven as somebody else's external runner is the right default, because
the queued/delivered distinction has nowhere to go across that hop.

This needs nothing new. A refused spawn already has a path — `process.FailedHandle` carries the
reason onto the flow, the way an envelope this build does not have already does.

**Except that verse ignores refusals today** (git-issue `6mnmxj6mh12krz3ae1n0`).
`httpRunner.Spawn` checks the transport error and discards the HTTP status, so a runner
answering 400 or 501 is treated as having accepted the work: a remote handle comes back, a
`process:instance` is mirrored, and the flow waits on a process that was never started. The
refusal reads as a hang. That has to be fixed for this option to mean anything, and it is worth
fixing regardless — with per-process runners a refusal stops being a deploy-time
misconfiguration and becomes a normal outcome, a drive pointing work at a runner that will not
take it.

Two further ways, neither needed first:

- **the contract carries it** — process-control exposes how control is applied (a capability on
  the status response), and `runner.HTTP`'s handle reports what it was told. Where this should
  end up: it makes the guarantee structural rather than a convention, and it lets a broker stop
  refusing.
- **verse constrains the URL** — a runner entity must be a runner face, checked at install. The
  weakest of the three: verse cannot classify a URL without probing it, and a probe is the health
  check this plan says it does not do.

The residual under either: several workers stand behind one broker name, so "which worker took
it" is a question the runner name cannot answer, and a drive debugging a stuck process will ask
it.

**Whether a runner entity should carry more than a url.** A grant ceiling, a tool allowlist, a
label — anything that says "work here may only do this" — is expressible and deliberately not
here. It is the same narrowing question a doer answers, and answering it in two places without
deciding how they compose is how a ceiling comes to mean two things.

**Moving running work.** Not possible, not planned, and named because the entity model invites
the assumption: entities are level truth, so writing a different runner onto a `process:instance`
looks like it should move the process. It does not; that field is a record of where the work
went, not an instruction about where it should be. The steering invariant says the same thing in
general terms — a reaction wired to that field would be the command entity wearing a record's
clothes.
