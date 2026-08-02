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

**Two bindings can now claim the same work.** Named and standing are different layers rather
than competitors, but nothing stops two STANDING conditions from both covering one subject.
The answer is a total order rather than a prohibition — see step 1.

## Order of work

### 1. A standing binding, and a total order over the ones that match

`condition` stops being refused. It selects entities in the vocabulary a trigger's `subject`
already uses, so nothing new is parsed.

**Overlap is ordered, not forbidden.** When several standing bindings match a subject, the
first BY NAME owns its questions. That is the whole rule: total, deterministic, stable under
installs, and it needs nothing to be decided at add time.

The design says standing conditions must be disjoint so that a question has exactly one
answerer. It still has exactly one — by construction now, rather than by refusal — so the
three properties that follow from it are untouched: escalation still reaches `⊤` in at most
two hops, every hop still narrows, and `⊤` still matches everything.

*Why not refuse instead.* Disjointness of two predicates is undecidable in general, so a
refusal has to be conservative, and a conservative refusal blocks legitimate installs — the
operator with `{area: payments}` and `{area: infra}` is told no for a reason that is not true
of their case.

*And not with the schema SAT check, which was measured rather than assumed.* `go-tony`'s
`schema.CheckAcceptSatisfiability` will answer "is `!and [A, B]` satisfiable", and it works:
`!irtype "s"` against `!irtype 1` comes back unsatisfiable. But it reasons about IR TYPES,
not values — `{area: payments}` against `{area: infra}` is satisfiable to it, as is
`{stage: open}` against `{stage: closed}`. Used as the oracle here it would refuse every pair
over one kind, which is worse than the hand-rolled rule it would replace. It is not a weaker
tool; it answers a different question. (`scratchpad/sat` is the probe.)

*What ordering costs, and what pays it back.* A binding installed second can be shadowed for
the subjects an earlier name already covers, and a resolver that is silently idle is the
failure class this whole increment is arranged around. Two things answer that, and neither
needs the check to be sound:

- The owner is already ON the question. `verse gates` and any predicate over
  `gate:question:*` say which binding owns each one, so "why is mine idle" is a selection
  rather than an inference.
- Installing REPORTS the bindings it shares a `(system, kind)` with, without refusing. A
  cheap syntactic check is exactly right for a notice, because a false positive costs a
  sentence rather than an install.

**Proves:** several matching bindings resolve to one owner, predictably. Test: two standing
bindings covering one subject — the first by name owns its questions and the second owns
none; installing the second reports the first; renaming so the other sorts first moves
ownership; and a binding over a different kind is unaffected.

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

### 5. Removing a binding re-routes what it owned

Ownership is stamped on a question when it is posed, so removing a binding leaves questions
carrying the ref of something that no longer exists — answerable by nobody, and listed on a
docket as though somebody were coming.

Ordering makes this sharper rather than softer: with overlap allowed, removing the first name
promotes the second, so removal is a change of owner rather than only a loss of one.

**Re-route rather than drain.** On removal, its open questions are re-owned by whatever the
layer rule gives them now — the next standing binding covering the subject, else `⊤` — and the
change is written on each question so the handover is readable rather than inferred.

*Why not drain.* Draining means the removed binding keeps answering what it already owns until
those settle. It sounds gentler and is worse in the case that matters: an operator removing a
resolver usually wants it to STOP, and its service is often being shut down in the same
breath — so the questions it still owns are stuck until something re-routes them anyway, and
draining is a state machine that postpones the same work. Re-routing also has the property
that `⊤` exists precisely for: a question nobody is watching belongs to a person.

A verdict arriving from the removed binding after that loses on the ownership check it already
has to pass, which is the right answer and needs no new rule.

**Proves:** removal moves a question rather than stranding it. Test: a standing binding owning
an open question is removed; the question is re-owned by the next covering binding, or its
field goes absent when there is none and `verse gates` still lists it; a verdict from the
removed binding is refused; and an instance of it is left where it is, since a record of what
was watched outlives the watching.

## What this plan does not build

Escalation, and therefore the second layer as a live thing: a standing binding here answers
what nobody named, not what a named one escalated. `escalatesAfter`, `exclusive`, the `llm`
and `script` drivers. And no resolver over a scope other than baseline.

## The residual to expect

**Whether shadowing should be visible on the binding itself.** The owner is on every question,
and installing reports what a new binding overlaps — but a binding that has been shadowed
since some LATER install has been told nothing. A field on the binding would say it, and would
be a derived fact stored, which is the thing this codebase keeps refusing to do. Left open
because the answer probably arrives with somebody noticing an idle resolver.

**When a subject instance is reaped.** A run ends; a subject may not. The first increment left
this open for run instances and this one makes it sharper rather than answering it.

**Whether a standing binding should read a run's stream at all.** It covers the subject, and a
run about that subject is part of the subject's story — but its position is a revision and a
run's frames are not commits. Escalation is where this gets decided, because a question
transferred up from a run's resolver arrives at a binding whose position cannot address it.
