# The standing resolver: implementation plan

Status: plan, nothing built. It follows [the first resolver](2-first-resolver-plan.md), which
runs: a binding named by a spawn, reading that run's stream and answering its gates. This is
the other door — a binding nobody names, covering the work about the entities its `condition`
selects — and it is where the design's second origin arrives, because a trigger gate has no
run to be attached to.

## The slice

`condition` accepted, the layer rule that decides which binding owns a question, and a
resolver whose origin is a SUBJECT rather than a run.

Still no escalation, no `escalatesAfter`, no `exclusive`, no `llm` or `script` driver. Those
were deferred in the first increment and nothing here needs them.

## What actually differs, and what only looks like it does

Three things are genuinely new. Everything else is the first increment with a different origin.

**The origin is a subject, so the stream is the store.** A firing has no event stream — it is
over in a commit or two — so a resolver at that origin reads the store's commit stream narrowed
to the subject, and its position is a revision rather than a cursor. `GET /watch` already
serves exactly that: a pattern-narrowed commit stream from `?since=<revision>`, subscribing
before its catch-up snapshot so nothing falls between the two. The subject form is that
capability wearing the resolver's address.

**It may attach late, and the run form may not.** The first increment mints an instance at
spawn because a run's stream is bounded and ephemeral: miss the beginning and it is gone. A
subject's history is neither — the store keeps its commits and serves them from any revision —
so a subject instance can be minted when there is first a question about that subject, and
still read everything that led to it. That is not a convenience. It is the reason the two forms
can differ here without the design meaning two different things by "context arrives before the
ask".

**Two bindings can now claim the same work, and must not.** Named and standing are different
layers rather than competitors, but nothing yet enforces that two STANDING conditions do not
both cover one subject.

## Order of work

### 1. A standing binding, and the check that they do not overlap

`condition` stops being refused. It selects entities in the vocabulary a trigger's `subject`
already uses, so nothing new is parsed.

Two standing bindings whose conditions can both select one entity are refused at add — which
is why installing is a verb. Disjointness is undecidable in general, so the check is
conservative and its rule is one sentence: two standing bindings over the same `(system,
kind)` are refused unless their predicates name a common field with different constant values.
`{area: payments}` and `{area: infra}` pass; `{area: payments}` and `{urgent: true}` do not,
and a resolver whose author knows they are disjoint says so by naming the field.

Refusing more than it must is the right direction here. A pair wrongly refused is an error
message at install; a pair wrongly allowed is two supervisors answering one question, which is
the thing ownership exists to prevent.

**Proves:** a standing binding installs, and an overlapping one does not. Test: a second
binding over the same kind with an unrelated predicate is refused naming both; one told apart
by a field is accepted; a different kind is accepted.

### 2. The layer rule decides who owns a question

Today a question carries the binding its spawn named, or nothing. It should carry the owner
its LAYER gives it:

```
a run's question    → the spawn's resolver, else the standing binding covering its flow, else ⊤
a firing's question → the standing binding covering its subject, else ⊤
```

Absent layers are skipped, which is what makes this a rule about layers and not a ranking. A
standing binding is not a wider claim on a run's questions; it answers a different set — and
without escalation, a run whose spawn names a resolver leaves its standing binding nothing to
do, which is correct rather than a gap.

The subject a run's question is about is already on the question (filled from the process's
`flow` when it is posed), so this is a lookup rather than a plumbing exercise.

**Proves:** ownership follows from what declared it. Test: a run with a named resolver and a
covering standing binding is owned by the named one; the same run without the name is owned by
the standing one; a firing's gate over a covered subject is owned by the standing one; and an
uncovered subject leaves the field absent, which still means ⊤.

### 3. A subject instance

`resolvers:instance:<name>@<subject>`, minted when the first question about that subject is
posed and living for the subject after that — not per firing, which would be the
spawn-answer-die shape this design exists to avoid, reached from the other end.

Its `origin` is the subject's own ref rather than a `process:instance:`, which is what tells
the two forms apart at read time without a second field saying which kind it is.

**Proves:** one instance per subject, not per firing or per run. Test: three firings about one
subject mint one instance; two subjects mint two; and its position field is a revision, which
the next step is what actually moves.

### 4. The subject stream

`GET /resolvers/instance/{id}/events?since=<revision>` serves store commits narrowed to the
subject, for an instance whose origin is a subject. The same route already serves process
frames for an instance whose origin is a run.

That fork is the one thing here worth deciding rather than discovering: **one route serving two
payload shapes**, or two routes. One route is what the design's protocol section describes — a
resolver's obligations are stated once, in the nouns of positions and verdicts, not per origin
— and the shape is discoverable from the instance the caller already named. Two routes would
make a client's code branch on something it can already see, and would let a client read the
wrong stream by asking the wrong route rather than by being told.

So: one route, and the frame carries what it is. The narrowing is to the subject's own
commits, which is what `queryPattern` builds from a `?system/?kind/?if` today and what the
binding's `condition` is already written in.

**Proves:** a standing resolver reads a subject's history and answers a firing's gate. Test:
an httptest resolver dials in, reads the commits that led to a trigger gate, answers it, and
the firing proceeds — the same test the first increment ran over a run, with the origin
changed and nothing else.

## What this plan does not build

Escalation, and therefore the second layer as a live thing: a standing binding here answers
what nobody named, not what a named one escalated. `escalatesAfter`, `exclusive`, the `llm`
and `script` drivers. And no resolver over a scope other than baseline.

## The residual to expect

**Overlap that refusal cannot see.** Two conditions disjoint as written can select one entity
at runtime — `{stage: open}` and `{urgent: true}` on the same PR. The design already names
this and prefers promoting the question to the layer above over refusing at add, which needs
escalation. Until then a question with two claimants is a case the check is built to make
unreachable, and worth an assertion at post time rather than a belief.

**When a subject instance is reaped.** A run ends; a subject may not. The first increment left
this open for run instances and this one makes it sharper rather than answering it.

**Whether a standing binding should read a run's stream at all.** It covers the subject, and a
run about that subject is part of the subject's story — but its position is a revision and a
run's frames are not commits. Escalation is where this gets decided, because a question
transferred up from a run's resolver arrives at a binding whose position cannot address it.
