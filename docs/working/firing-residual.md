# Firing semantics: what is left

Status: residual of a landed feature. The firing rule itself runs — one rule, per commit, per
subject — and it is documented in [the trigger model](../trigger-model.md) with the argument
that a charter settles in [convergence](../convergence.md). What is here is what those two docs
name as missing, gathered in one place so the next person does not have to reconstruct why each
was left.

Nothing here is a design owed to anything else here.

## 1. Time as a cause: the clock a ticker would be

**The cycle check that used to head this list is built**, and not as the graph proposed here. It
runs at runtime, on causal depth: every write an engine makes during a firing carries how far it
stands from the external write that started the chain, and a chain deeper than the installed
trigger count has necessarily repeated a rule. See [the cycle throttle](../cycle-throttle.md).

What that settles is worth stating, because it decides the shape of what is left. The rule the
graph was reaching for — *a cycle is not wrong; an UNPACED cycle is*, a combinational loop being
an error where a cycle through a register is a state machine — survives intact and is what the
throttle implements. But pacing turns out to be a property of how a chain RUNS, not of how it is
written: a chain breaks wherever real time enters, through a spawned process, through the world
via the mirror, through a decision somebody answers. So the check needed neither the graph nor
the ticker, and a static analysis over templated patches — always a warning rather than a proof —
is not worth building now that a dynamic one is exact.

**A ticker is still wanted, for the other two faces of time.** "Time as a cause" was one question
with three: a cycle check, a grace period, and a temporal condition. The first is answered; the
other two are not, and both want the same missing thing.

The wrinkle, not visible until you try to write a ticker: **a ticker needs to say "changed", and
`on` can only say "became true".** A monotonic counter rises once and stays risen; one entity per
tick leaves "a tick exists" true forever. So a ticker has to produce a rise AND a fall, which
means either

- the tick is a fact that gets CONSUMED — it appears, the rule fires, it is removed — which is
  the [vanishing marker](../trigger-model.md#lifecycle-how-an-entity-leaves) doing double duty,
  and is the answer most in the grain; or
- `on` grows a form meaning "this changed" rather than "this became true", which is precisely
  the change-edge the re-modelling removed, re-admitted in a narrow place.

The first is worth trying before the second. Its customer is the reaper's grace period, §4 below.

Adjacent, and probably the same work: a temporal `on` or `when` is refused at install today.
`ReqDeadline` exists as a formula leaf but `Requirement.eval` falls through to `default` and
BLOCKS (`formula.go`), so a deadline in a condition never holds; it is resolved by the motion
monitor, in a gate, which is a different path entirely.

## 2. The read per commit: carry the document, do not re-read it

Why it costs what it does:
[convergence §the cost of asking](../convergence.md#the-cost-of-asking-a-read-per-commit). A
snapshot read costs the log replay to that commit, the path asked for makes no difference, and
the commit skip is what keeps this affordable in practice.

The fix is to carry the rolled-forward document rather than re-read it: a floor plus a bounded
window of deltas, advanced as commits are consumed in order. Then `store@C` is the last snapshot
plus one delta, and `store@(C-1)` — which an `on` edge needs — is the one already in hand, so an
edge costs nothing at all. A cursor older than the floor falls back to the read path, which is
the low-watermark discipline `Since` already has.

The alternative is already in the tree and this is the argument for generalizing it rather than
inventing anything: `Since` reads the document ONCE at its floor and rolls it forward with the
deltas it is replaying, precisely so a catch-up is one read rather than one per event — then
throws everything but the per-entity payloads away, and the evaluator re-derives the same
documents with a read apiece.

It closes a second thing, which is the argument for doing it rather than merely wanting it: an
action's `$references` read the subject from the store as it stands when the action runs, not from
the snapshot the rule was evaluated against (`Engine.refPayload`). A firing that CARRIES its
snapshot reads the value the condition was true of. Doing it any other way today would mean a
whole-verse read per subject, which is why the looseness is documented rather than patched.

`AsOf`'s `within` parameter is kept for this: it bounds what crosses the wire, which is the shape
a per-subtree roll-forward will want. If this never lands, `within` is a candidate for removal,
since measurement says bounding the path is all it does.

## 3. Two triggers, one field

Still not built, and the reason has changed. It used to be that the cycle graph would subsume it,
so building the weaker check first meant building it twice. The cycle check went the other way —
dynamic, on causal depth — and does NOT subsume this: two rules writing one field with different
values settle wherever the last write lands, without any chain growing, so nothing trips. It is
now an independent piece of work.

It is also the one source with no principled fix once detected: a charter that means two things
is one only its author can settle. The shape and its decidability are in
[convergence §two triggers, one field](../convergence.md#two-triggers-one-field).

## 4. What `verse entity rm` should mean

The lifecycle mechanism runs — intent written before removal, a vanishing entity is not a
subject, the reaper is an ordinary trigger
([trigger model §Lifecycle](../trigger-model.md#lifecycle-how-an-entity-leaves)). What is NOT
settled is what a DELETE does: today nothing enters the sink state except a charter.

**Delete means mark; removal is the reaper's.** The second phase cannot be in the same call, and
not because it might fail. Mark and remove back to back leaves the departure *observable* but not
*actionable*: a rule fires at commit C, the entity is gone by C+1, and bindings read the subject
— so "on departure, archive the payload" cannot read the payload. The marker degenerates into a
signal that something left with no way to say what. Deferring is what keeps the content available
to whatever wants it. If no reaper is installed the entity lingers *visibly*, which is the
failure mode to want over a silent half-delete.

The cost is operator-facing, which is why this is a question and not a patch. `verse entity rm`
stops meaning "it is gone" and starts meaning "it is marked, and it goes when the reaper says".
`verse entities` still lists it, deliberately. Two coherent answers:

1. **`rm` marks**, and a default reaper charter ships with a short grace. Uniform: every
   departure is observable, including an operator's. Costs an operator the surprise, once.
2. **`rm` keeps hard-deleting**, and the marker stays for charter-initiated departures only. No
   surprise, but a rule cannot react to an operator removing something — which is most of the
   departures a verse will actually see.

Only (1) makes the mechanism worth having. Only (2) leaves `rm` meaning what it says.

Whichever wins, **machinery deletes should stay one-phase.** `process:instance`,
`motion:channel`, `trigger:rule`, a scope teardown: nothing reacts to their departure, and
two-phasing them buys lingering entities and a second commit for no reader. `connect`'s teardown
(one delete per federated entity) is the case to think about — N markers and a reaper sweep,
against N removals.

### Gap: a reaper cannot express a grace period

This blocks answer (1) as stated. The marker carries a timestamp, and a reaper wants "reap what
was marked more than an hour ago". That is a comparison between a stored value and *now*, and
nothing in the grammar can say it: a subject's payload predicate is a tony match (`!glob`, `!or`,
`!not`, equality) with no temporal comparison and no access to the clock, and a `when` is a store
predicate asked at a revision, with the temporal leaf refused at install (§1). So a reaper today
reaps *immediately* — which is exactly the same-call two-phase this note argues against, wearing
a charter.

This is the ticker again, and a tick that is a fact which gets CONSUMED answers both: a reaper
woken by the tick, selecting what was marked before it.

An interim that needs no new machinery: the reaper's action spawns a process that sleeps and then
writes the tombstone. Ugly, but it puts the grace in the charter where it belongs, and it is what
`scripted` is for.

### Gap: a tombstone and a never-written entity read alike

`Store.Get` reports both as `ok=false`. The distinction exists at the precondition level —
`entity.Absent()` is `!irtype null`, and `IsAbsent` is structural precisely because the two
substrates spell absence differently — but a reader cannot ask "was this removed, or was it never
here".

It matters for the resurrection hazard the lifecycle fix closed. That fix works by refusing a
VANISHING subject, which covers the ordered case (mark, then remove). It does not cover a rule
racing a removal with no marker at all — a hard delete from `rm`, or machinery. There, a patch
still creates, and the entity comes back stripped to whatever the rule wrote.

The stronger rule would be: **a patch creates over a name that has never existed, and refuses
over a tombstone** — "a name you may bring into being" against "a name that has been retired".
That needs `Get` (or a sibling) to stop collapsing the two, which is store work, and it is worth
doing only if answer (2) wins. Under (1) nothing hard-deletes and the ordered case is the only
case.

## 5. Does the grammar grow ∄?

The model is existence-positive throughout: a match asks what is there, so *"no entity matching
M exists"* — "ship when there is no open blocker" — has no spelling. A leaf's `!not` negates a
payload, so "an entity exists whose payload is not X" is sayable and "nothing matches" is not.

Deletion took the other road and it worked: make the absence a positive fact somebody writes.
The same discipline answers a guard about nothing existing — a count, a summary entity — but it
is a discipline rather than a grammar, and it is the kind of thing better decided than
discovered at the first guard that wants ∄.

## 6. Bulk gate answers

A firing is about one entity, so fifty reviewed PRs are fifty questions where a summary used to
stand in for the set. That is the honest arity, and it is the right change; it costs an operator
fifty answers until `verse gate approve` grows a bulk form. A surface question, not a model one.
