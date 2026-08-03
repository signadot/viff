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
plan changes. Every spawn goes wherever that field points, so a drive that needs a different
runner needs a different verse.

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

**Proves:** an operator can install, list and remove one, and a runner that cannot be reached is
refused rather than accepted. Test: add, list, remove over the CLI; a malformed url is refused
with the url quoted.

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

Which raises the same question one level down: **what should removing a runner entity do to
processes running on it?** The honest answer for this increment: nothing, and say so. The work
is already running somewhere; the entity was the way to *find* that place, and the stamped URL
still finds it. Removal stops it being *chosen* again. That is a smaller promise than the
resolver's hand-on, and it is the true one — verse cannot move running work between runners, and
pretending removal does something to it would be worse than the gap.

**Proves:** work outlives the verse that started it, and outlives the entity that named its
runner. Test: spawn on a named runner, restart the server, and the process is re-attached rather
than orphaned; remove the runner entity while the process runs and the restart still re-attaches
it; a spawn naming the removed runner is refused.

### 4. The docket says where

`verse ps` and `process:instance` show the runner a process is on, and `verse runners` shows
what is installed. One `verse entities --system runners` shows both what is installed and, once
step 3 stamps it, what is running where.

Small, and last on purpose: it is the step that makes the previous three legible, and it is the
one that would otherwise be skipped.

**Proves:** an operator can answer "where is this running" without reading a charter. Test:
`verse ps` names the runner per process; the default reads as `local` rather than as blank.

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

**What a broker runner's name means.** A broker is a runner workers dial *into*, so its URL is a
face rather than a destination, and several workers stand behind one name. Nothing in this plan
breaks on that — the URL is still the address verse posts to — but "which worker took it" is a
question the name cannot answer, and a drive debugging a stuck process will ask it.

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
