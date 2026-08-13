# The store session is a resource to reconcile, not a connection to establish

Status: proposal, nothing here runs.

verse's own law, turned on verse: **a hint is a hint, reconcile is the truth**
([sources.md](../sources.md)). Every external system verse touches is treated that way, and a
watch is level-triggered at its open for the same reason. The one connection verse does not
treat that way is its own, to the store — and a deployment found it.

## What happened

`make deploy` rolls the stateful tier and the engine together. docd's new pod could not start
until its volume detached from the old node (`FailedAttachVolume — Volume is already exclusively
attached to one node`), so it began listening at 14:26:14; versed had started at ~14:25:50. From
then on, every write versed made timed out:

```
verse: source sources:git:cluster: recording error: write sources:git:cluster.lastError: context deadline exceeded
```

The pod sat at `0/1` for five and a half minutes and would have sat there indefinitely.
`kubectl rollout restart deploy/versed` fixed it instantly, which is the tell: nothing was
broken except a session, and nothing was going to fix it except a human.

## The asymmetry, which is the actual bug

One process, one session, two opposite beliefs about the same event.

**The read plane recovers.** `LogdStore.WatchSubtree` says so in as many words: a dropped
connection is *"NOT a terminal condition. We re-establish below rather than returning… every
ending upstream defines is re-establishable, and re-watching is the whole response."*

**The write plane does not.** A write against a dead session returns a deadline, the caller
logs it, and the next write does the same thing forever. There is no redial anywhere on that
path.

**And readiness is right by accident.** `/status` reports `Store.Rev()`, which with no live root
watch goes through `ensureSynced` and opens a watch — so the probe *is* a store round trip, and
that is why the pod correctly refused to be ready. Nothing about `/status` says it means to be;
a future refactor that made `Rev()` read a cached field would silently turn the readiness probe
into a test that the HTTP server is listening, and this outage would have presented as a healthy
verse writing nothing.

## What to build

Three pieces, and the point is that they are one thing: **the connection is reconciled, and
readiness is a fact about it.**

**1. A session that redials.** An operation that fails because the store could not be reached
re-establishes and retries; one that fails because the store *answered* — a CAS precondition, a
refused replay, a malformed patch — does not. That distinction already exists in the errors and
is the whole of the design: `task.ErrUnreachable` is the same cut on the runner side.

The watches ride the same session and already know how to come back; what they need from a
reconnect is a cursor that survives it, which is exactly what `Since` now answers after
`88651aac` removed the per-instance watermark. A re-established watch replays `FromCommit` and
the consumer cannot tell. That was not true a day ago, which is worth noticing: this proposal
became implementable because a different bug got fixed.

**2. Readiness that means "the store answered, now".** Deliberately, with its own cheap round
trip, rather than through whatever `Rev()` happens to do this month. Then kubernetes routes away
from a verse that cannot serve and back when it can — which is what makes deploy ordering
unnecessary rather than merely tolerable.

**3. Boot is the steady state's first iteration.** `cmd/verse/up.go` has a patient wait-for-docd
loop with a deadline and its own log line. Delete it. An unreachable store at second zero is the
same condition as one that vanished at 14:26, and a system that handles the second needs no
special case for the first. That is the "restarts included" part: node eviction, a docd rollout,
a partition and a cold boot stop being four situations.

## What is genuinely hard, and must be decided rather than discovered

**Which writes may be retried.** A deadline is not a failure — it is an absence of an answer,
and the write may have landed. Verse is well placed here and should say so explicitly rather
than relying on it:

- `Modify` and `CommitIf` carry a precondition, so a retry after an uncertain deadline either
  applies once or fails its CAS and re-reads. Safe.
- `ReplaceFrom`/`ReplaceAt` are the single-writer path with no precondition, and they write a
  whole value. Re-writing the same value is inert. Safe.
- A bare `Write` with a caller-supplied value is idempotent only if the caller says so. This is
  the one that needs a decision: either every write in the tree is one of the two above (worth
  checking; it nearly is), or the retry is opt-in per call.

**What a redial owes a caller mid-flight.** A `Modify` retry loop that redials must not spin
forever against a store that is down: bounded attempts with backoff, and then the error, which
callers already handle. The engine's `patchEntity` logs "the firing's state transition was lost"
— that message stays true and stays rare.

## Why not deploy ordering

It was the first thing I reached for: have `make ship` wait for docd's rollout before versed's.
It fixes the case somebody foresaw and none of the others — docd restarting at 3am, a node
eviction, a partition, an OOM — and it puts knowledge about the store's lifecycle into a
Makefile, which is the wrong place for it and the place nobody looks when the pod is unready.

The ordering question dissolves once the engine converges: versed may start first, be unready
while docd is absent, and become ready when it arrives. That is what kubernetes' model expects
of a component, and verse asks exactly this of everything it integrates with.

## The pattern worth naming

This is the third time in one day that a property of the PROCESS leaked into behaviour that
should have been about the store. `Since` refused any cursor below the revision this instance
opened at, so every restart truncated readable history. A task record is one entity per task
NAME, so a second run overwrites the first. And a session established once at boot makes an
outage permanent.

Each was locally reasonable and each is the same mistake: the store is a thing out there with
its own life, and the adapter keeps treating this process's view of it as the fact.
