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

**A second plane, not a different one.** A standing binding supervises the work ABOUT the
entities it selects, so it owns both a firing's gates and the gates of runs about those
subjects. A firing has no event stream, so that half reads the store's commit stream narrowed
to the subject, with a revision for a position; `GET /watch` already serves exactly that,
pattern-narrowed from `?since=<revision>` and subscribing before its catch-up snapshot.

It does not read the store INSTEAD of a run. The design settles this: the store stream is the
spine — ordered, durable, carrying the anchors, since a process's spawn and its terminal are
both store commits that `mirror` writes — and process streams hang off it as fine grain
between anchors. Two streams, independent cursors, no common order, and no sequencer.

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

## What an instance is for, and how long it lives

**The store is already the evidence.** A subject's whole history is commits, and so are the
resolver's own positions and notes, which are writes like any other. So the instance record
does not have to be the audit trail and is not one. It is machinery: a position so a reader
can resume, and a handover for whoever picks it up.

That settles a lifetime this plan would otherwise have left open. An instance belongs to its
BINDING, not to the thing it watches, and is reaped when the binding is removed — step 5 — and
at no other time.

Which in turn settles something the design cares about more. `sys:k:i` born at commit `C`,
marked vanishing at `C+n`, deleted at `C+n+m` and reborn at `C+n+m+o` is ONE story to anyone
watching that ref, and a resolver for `sys:k:i` sees all of it. Nothing about the entity's
death ends the watch, because a position addresses the STORE's revision line, and that does
not die when an entity does.

Two consequences, both visible in what this plan builds:

- **The spine is narrowed by REF, not by the binding's condition.** A predicate reads a
  payload and a deletion has none — `Event.To` is null on a delete — so a payload-narrowed
  stream hides exactly the end of a life it most needs to show. The condition decides which
  subjects get an instance; the instance's spine is then that subject's whole story,
  unfiltered. A subject that stops matching the condition does not stop being watched by an
  instance already minted, which is the same rule read from the other side.
- **Nothing reaps or resets an instance when its subject vanishes or is deleted**, and a
  rebirth continues the same one. That needs no code: the address is unchanged, so the
  do-nothing path is the correct path — which is the best kind of direction to leave, because
  it cannot rot.

What is deliberately NOT settled here is what a resolver should DO across the dead spot —
whether an instance whose subject does not currently exist is idle-but-watching or something
else, and what a question arising there would even mean. The behaviour above points at it
without depending on the answer.

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

### 3. An instance per origin, and a binding may have several

`resolvers:instance:<name>@<subject>` for the spine, minted when the first question about that
subject is posed and living for the subject after that — not per firing, which would be the
spawn-answer-die shape this design exists to avoid, reached from the other end.

And `resolvers:instance:<name>@<run>` for each run about that subject whose questions it owns,
which is the same record the first increment already mints and the same route already serves.
A binding is not an instance: "an instance lives for the duration of what it resolves, and the
ORIGIN decides what that is", so one binding watching a subject and three runs about it has
four instances, each living exactly as long as the thing it watches.


That is what carrying a cursor pair means here, and it is why nothing has to sequence the two
planes: the positions are two records rather than two fields, so there is no place for a
merged order to be invented and no code that could be tempted to invent one. It also settles
what the previous section left as a fork — the plane a stream serves follows from the
instance's origin, with nothing to disambiguate.

**Proves:** instances follow origins, not bindings. Test: three firings about one subject mint
one subject instance; a run about that subject whose gate the binding owns mints a run
instance beside it; the subject instance's position is a revision and the run instance's is a
cursor; and two subjects mint two spines.

### 4. The spine

`GET /resolvers/instance/{id}/events?since=<revision>` serves store commits narrowed to the
subject, for an instance whose origin is a subject — the same route that already serves
process frames for an instance whose origin is a run, deciding by the origin rather than by
anything the caller has to say twice.

The narrowing is to the subject's own REF — every commit touching it, and nothing else. Not
the binding's `condition`: a predicate reads a payload, a deletion has none, and a resolver
that cannot see its subject die is one whose evidence stops exactly where it matters.
`entity.Pattern` narrows by system and kind but carries no id, so this is a ref comparison at
the route rather than a pattern.

A resolver reading both planes follows the anchors: a spawn commit on the spine names a run,
and that run's instance is where its fine grain is. Which is the whole of the navigation — no
sequencer, and no merged order for anything to disagree about.

**Proves:** a standing resolver reads a subject's history and answers a firing's gate. Test:
an httptest resolver dials in, reads the commits that led to a trigger gate, answers it, and
the firing proceeds — the same test the first increment ran over a run, with the origin
changed and nothing else. And the anchor: a spawn about the subject appears on the spine, and
the run instance it names serves that run's frames.

And the lifecycle, which is where the direction becomes behaviour rather than intent: a
subject marked vanishing, then deleted, then written again under the same ref produces four
commits on one spine, in order, to a resolver that never stopped reading — and its instance is
the same record throughout, at the position it had reached.

### 5. Removing a binding re-routes what it owned

Ownership is stamped on a question when it is posed, so removing a binding leaves questions
carrying the ref of something that no longer exists — answerable by nobody, and listed on a
docket as though somebody were coming.

Ordering makes this sharper rather than softer: with overlap allowed, removing the first name
promotes the second, so removal is a change of owner rather than only a loss of one.

**Re-route rather than drain.** On removal, its open questions are re-owned by whatever the
layer rule gives them now — the next standing binding covering the subject, else `⊤` — and the
change is written on each question so the handover is readable rather than inferred.

Its instances go with it, and this is the ONLY thing that reaps one. They are machinery for a
reader that no longer exists; what they were evidence of is in the store, where a position
report and a note were commits when they were made and remain commits afterwards.

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

**Which plane a resolver reads first, and whether it must read both.** A binding owning a run's
gate has a run instance for it, so the fine grain is reachable — but nothing says it has to be
read before deciding, and a resolver that answers from the spine alone is answering on less
than it could have seen. That is a matter for the protocol's obligations rather than for
routing, and it is the same question an `llm` driver will make concrete.
