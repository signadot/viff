# An occurrence has a name: implementation plan

Status: plan, nothing built. The argument is git-issue `gjv063rrh12ksvgnfsn0`, recorded first
as finding 19 of [verse-dev-comment-drift.md](verse-dev-comment-drift.md); this file is the
order of work and what each step proves.

The change in one line: **an id goes on naming the work, and a RUN gets a discriminator its
records can carry.**

## The constraint that decides the shape

Before any of it: the engine's own linkage is **deliberately commit-free**, and
`trigger/stratum.go` says why in the comment above `deriveMayReference`. A derive that records
its causing commit reintroduces the cycle the strata exist to rule out —

    L (level 0, subject S, reads D)   derive: { at: $subject.commit }
    H (level 10, subject D)           patch:  { seen: true }

    S changes → L derives D{at: 100} → H patches D → L wakes at 102 → D{at: 102} → …

unbounded, across a level, paced only by the throttle. So the cheerful version of this
plan — "stamp the causing commit on things" — is already ruled out for the one place it would
be most convenient. **An occurrence may live on a TASK RECORD, which changes once per run, and
must not become a value a derive can reference.**

That constraint splits the issue's three consumers, and they are not as alike as the issue
claims:

- **The task's own record** — this fix. `task:instance:<id>` is one entity, so a second run
  overwrites the first.
- **The branch a fix lands on** — already worked around with `$sha/$task`, and worth noticing
  WHY that worked: the sha is DATA on the subject, not a commit, so it costs nothing and
  cycles nothing.
- **The `at:` on every finding** — not this fix at all, and probably not a defect.
  `deriveMayReference` admits `$subject.value.*`, so a charter can derive
  `at: $subject.value.sha` and stop asking the answer for it. That is a one-line charter
  change available today. The issue read this as a third instance of one gap; it is the same
  fact (the sha) reached the long way round, by a prompt, when a reference would have done.

So the plan is smaller than the issue implies, and step 4 is where that gets said out loud
rather than quietly dropped.

## What an occurrence IS

**The store revision the run is about.** For a firing, the commit that woke it — the engine has
it (`oc.commit.Rev`) and already carries it into the writes a firing makes. For an ad-hoc
`verse task run`, `Store.Rev()` at spawn. One field, one meaning — *as of what the verse knew*
— from two sources, which is the same shape `AsOf`/`Since` already give every other question
about when.

Rejected, and why, since each looks reasonable:

- **A unique id.** Breaks the dedupe determinism buys: the engine dedups a spawn against a live
  task of the same id, a completion hook is scoped to the run that raised it, and
  `verse flow demo:pr:1` addresses it. The name is right; it is the record that is missing one.
- **A sequence number per id.** Needs state nobody holds, and answers "the third run" — a
  question no one asks — where the revision answers "the run about commit 907", which is what
  an operator is actually holding when they ask.
- **A timestamp.** Two runs a millisecond apart on one subject are exactly the case this is
  for, and wall-clock is the one axis verse has been careful not to reason with.

## Order of work

Four steps and a drive. Each leaves the tree green.

### 1. The record says which run it is

`task:instance:<id>` gains `occurrence`, stamped by the mirror from `mirror.Meta`, filled by
the spawner (the firing's commit) and by the ad-hoc path (`Rev()`). Additive: no address
moves, nothing that reads the record today reads differently.

**Proves:** `verse ps` can say which run a record describes, and a reader can tell a stale
record from a live one — which is the question that could not be asked at all when a deploy
interrupted a review and left `status: running` beside an error from the run before it.

The test worth writing is the one that fails today: run twice over one subject, and assert the
record's `occurrence` moved. It will pass trivially once the field exists, which is the point —
it is the second step that has to work harder.

### 2. A run leaves a trace

The occurrence record: `task:run:<id>@<occurrence>`, a new kind, written by the mirror beside
the index. `task:instance:<id>` stays exactly what it is — the latest run's view — so every
reader, charter and CLI path keeps working.

**Proves:** a failed run followed by a successful one leaves BOTH, which is the thing the issue
was filed about and the thing step 1 does not fix. Test: fail a run, succeed the next, and
assert two records with two occurrences and two outcomes.

Two questions to settle here rather than discover:

- **Who removes them.** A run record per occurrence grows without bound on a hot subject. The
  honest options are a retention count per id, a vanishing stamp (`verse.meta.vanishing`
  already exists and is the house spelling), or nothing-and-say-so. I would take the vanishing
  stamp: it is the mechanism verse already has for "this was worth keeping for a while".
- **Whether the mirror writes two records or one plus a pointer.** Two writes per transition is
  the simple version and doubles the write rate of the busiest writer in the system. One write
  to the occurrence record with the index rewritten only on status changes is better and needs
  a sentence saying what the index promises.

### 3. `verse ps` shows a run's history

`verse ps <id> --history`, or plain `verse entity --system task --kind run --if {…}` if the
listing does the job without a flag. Decide by writing the command out and seeing whether it
reads like a question somebody asks.

**Proves:** the records are reachable by the person who wanted them. A trace nobody can list is
a trace nobody has.

### 4. Say what did NOT need fixing

The `at:` on comment-drift's findings moves from the answer to `$subject.value.sha`, and the
drive note's finding 19 gets a correction: the third consumer was a charter reaching for a fact
by the long route, not a third instance of the missing occurrence. The push target keeps
`$sha/$task` unchanged, because it was right for a reason this plan now states.

**Proves:** nothing, mechanically — it is what stops the issue from being remembered as bigger
than it was, and it removes one prompt-carried value from a charter, which is one fewer thing a
model can get wrong.

### 5. Drive it

Two reviews of one subject on staging, the first made to fail. Both visible, each naming its
commit, and `verse ps` showing the live one.

## What this does not do

- **It does not make ids unique.** See above; the dedupe is load-bearing.
- **It does not expose the occurrence to a derive.** That is the cycle, and the guard that
  refuses it (`deriveMayReference`) stays exactly as it is. A charter that wants the commit its
  review was about wants `$subject.value.sha` — a fact about the subject, not about the engine.
- **It does not backfill.** Records written before this have no occurrence, and a reader
  should see an absent field rather than a guessed one.
