# Delete intent: what runs, what is broken, and what `rm` should mean

Status: one fix landed, one question open, two gaps recorded. The mechanism described in
[fire-semantics.md](fire-semantics.md) §3 and `docs/trigger-model.md` §Lifecycle runs. What is
NOT settled is what a delete does — today nothing enters the sink state except a charter — and
two things a charter needs before the answer can be "the reaper's business".

## What runs

An entity's intent to leave is written before it goes, as `verse.meta.vanishing` carrying the
instant it was recorded. It exists so a DISAPPEARANCE is observable: a cause is the rising edge
of a store predicate — *something matching this now exists* — and there is no way to say the
negative, so the intent has to be a positive fact. `on: {…, value: {verse: {meta: {vanishing:
null}}}}` is then an ordinary edge, the removal itself is inert (a falling edge fires nothing),
and the reaper is an ordinary trigger: select what is leaving, `patch: null` at the root.

## The fix: what is leaving is not a subject

Found while probing the two-phase question, and it did not need two-phase to reach.

A rule reacting to a departure could act on the DEPARTING entity — a named subject is a set of
one whether or not it exists — and a patch over an absent entity CREATES. So a removal landing
first meant the rule resurrected what it was writing about:

```
after mark+remove: exists=true payload=map[farewell:noted]
```

Back as an ordinary live entity, stripped to whatever the rule wrote, with no marker on it.
Worse than a race: the resurrected thing does not look like something that was ever leaving, so
nothing downstream can tell.

A named subject is now skipped when it is vanishing. This is the one place naming does not beat
selecting — you may name something that does not exist yet, you may not name something on its
way out — and the create amendment is untouched, since a never-written name is still a set of
one. A pattern subject was already safe.

**The rule a charter author needs from this: react to a departure by writing somewhere ELSE.**
An audit record, an archive, a counter. The thing that is going is not a subject.

## The question: what should `rm` mean

Storage delete could write the marker and then remove — two phases, with the second best-effort,
since the marker is the commitment and the removal is completion.

**The second phase cannot be in the same call.** Not because it might fail, but because of what
the deferral is FOR. Mark and remove back to back leaves the departure *observable* but not
*actionable*: a rule fires at commit C, the entity is gone by C+1, and bindings read the
subject — so "on departure, archive the payload" cannot read the payload. The marker degenerates
into a signal that something left, with no way to say what. Deferring is what keeps the content
available to whatever wants it.

So the shape is **delete means mark; removal is the reaper's**, and "accept failure on the
second phase" becomes stronger than intended: there is no second phase to fail. If no reaper is
installed the entity lingers *visibly*, which is the failure mode to want over a silent
half-delete.

The cost is operator-facing, which is why it is a question and not a patch. `verse entity rm`
stops meaning "it is gone" and starts meaning "it is marked, and it goes when the reaper says".
`verse entities` still lists it, deliberately. Two coherent answers:

1. **`rm` marks**, and a default reaper charter ships with a short grace. Uniform: every
   departure is observable, including an operator's. Costs an operator the surprise, once.
2. **`rm` keeps hard-deleting**, and the marker stays for charter-initiated departures only.
   No surprise, but a rule cannot react to an operator removing something — which is most of
   the departures a verse will actually see.

Only (1) makes the mechanism worth having. Only (2) leaves `rm` meaning what it says.

Whichever, **machinery deletes should stay one-phase.** `process:instance`, `motion:channel`,
`trigger:rule`, a scope teardown: nothing reacts to their departure, and two-phasing them buys
lingering entities and a second commit for no reader. `connect`'s teardown (`connect.go:366`,
one delete per federated entity) is the case to think about — N markers and a reaper sweep,
against N removals.

## Gap 1: a reaper cannot express a grace period

This is the one that blocks answer (1) as stated, and it was not obvious.

The marker carries a timestamp, and a reaper wants "reap what was marked more than an hour ago".
That is a comparison between a stored value and *now*, and nothing in the grammar can say it:

- a subject's payload predicate is a tony match — `!glob`, `!or`, `!not`, equality — with no
  temporal comparison and no access to the clock;
- a `when` is a store predicate asked at a revision, and a temporal one is refused at install;
- `ReqDeadline` exists as a formula leaf but `Requirement.eval` falls through to `default` and
  BLOCKS (`formula.go:281`), so a deadline in a condition never holds. It is resolved by the
  motion monitor, in a gate, which is a different path entirely.

So a reaper today reaps *immediately* — which is exactly the same-call two-phase this note
argues against, wearing a charter. The grace period has nowhere to live.

The connection worth noticing: this is the ticker again (fire-semantics.md §5.3, "a cycle is
not wrong; an unpaced cycle is"). Both want time as a cause, and a tick that is a fact that
gets CONSUMED answers both — a reaper woken by the tick, selecting what was marked before it.
Whoever builds the ticker should know it has two customers.

An interim that needs no new machinery: the reaper's action spawns a process that sleeps and
then writes the tombstone. Ugly, but it puts the grace in the charter where it belongs, and it
is what `scripted` is for.

## Gap 2: a tombstone and a never-written entity read alike

`Store.Get` reports both as `ok=false`. The distinction exists at the precondition level —
`entity.Absent()` is `!irtype null`, and `IsAbsent` is structural precisely because the two
substrates spell absence differently — but a reader cannot ask "was this removed, or was it
never here".

It matters for the resurrection hazard fixed above. The fix works by refusing a VANISHING
subject, which covers the ordered case (mark, then remove). It does not cover a rule that races
a removal with no marker at all — a hard delete from `rm`, or machinery. There, a patch still
creates, and the entity comes back.

The stronger rule would be: **a patch creates over a name that has never existed, and refuses
over a tombstone.** "A name you may bring into being" against "a name that has been retired".
That needs `Get` (or a sibling) to stop collapsing the two, which is store work, and it is worth
doing only if answer (2) wins — under (1), nothing hard-deletes and the ordered case is the only
case.
