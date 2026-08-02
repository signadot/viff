# Escalation and exclusive: implementation plan

Status: plan, nothing built. It follows [the standing resolver](3-standing-resolver-plan.md),
which runs — bindings by name and by condition, ownership by layer, an instance per origin, a
spine, and removal that hands on.

The two arrive together because the design couples them: *exclusive requires a deadline, or a
resolver that is down becomes a question nobody may answer.* The deadline is `escalatesAfter`,
which is escalation's own machinery, so building `exclusive` alone would mean building the
half of escalation that makes it safe and stopping there.

## The slice

`escalate` as a verdict, a deadline that does the same thing more slowly, and `exclusive` as
the dial that says whose answer counts.

The deadline is spelled `within`, which is what a provider selection and a motion already call
one — see the section below, which is also why it is not spelled `escalatesAfter`.

That completes the layer story: until now every layer answered what nobody above it had
claimed. Escalation is what makes layer *n* answer the questions layer *n−1* RAISES, which is
the sentence the design defines a layer with.

## Where this sits relative to motions, which matters more than it looks

Verse already has a unified n-choice question, and it is not the one resolvers own.
`trigger/motion.go`: *"A governed motion is ONE primitive with two faces. A whether-gate is a
motion over the options `{proceed, deny}`; a who-select is a motion over `{doerA, doerB, …}`."*

So there are two representations of a posed question, and they differ on two independent axes:

| | arity | plurality | resolvers reach it |
|---|---|---|---|
| `process.GateQuestion` — a gate with `mode: ask`, no formula | 2 | one answerer | yes |
| `motion` — a gate with a `Formula`, or any `provider:` selection | n | a threshold over a vote log | no |

Everything this design has built is on the plurality-of-one side and is INDIFFERENT to arity:
ownership by layer, instances per origin, the spine and its fine grain, position and notes,
escalation, removal that re-routes. None of it asks how many options a question has.

Two things follow, and the first one changes this plan.

**The deadline already has a shape, and inventing a second one would be the mistake.**
`motionDeadline{within, defaultOption}` is exactly the `deadline: {after, then}` this plan was
about to describe as future work. Motions already express the `proceed` and `deny` rows of the
table below — a default option that wins when the clock fires — and the "no pick, so no spawn"
row besides. So `escalatesAfter` would be a second vocabulary for a thing verse can already
say, and the word for the duration is `within` in both `launch.Provider` and `motion`.

What motions CANNOT express is `escalate`, and the reason is worth stating: a motion's deadline
picks a winning OPTION, while escalation picks a new ANSWERER. Those are different kinds of
outcome, not two values of one. Hence `then:` naming the outcome explicitly rather than a field
name that bakes one in — so the option-winner and the answerer-transfer are visibly different
things sharing a clock.

**Selections are the obvious next application, and this plan should not foreclose it.**
`select: ask` is documented as "one party picks; a threshold of 1", which is exactly one owner
— so an `ask` selection is resolver-shaped today in everything but the field the answer arrives
in. What conflicts is `select: vote`: a threshold over an electorate is a different plurality
from one owner, and a resolver could be *a voter* only by giving up what `exclusive` and
`owner` mean here. That is the seam to think about before building, not after.

## What is new, and what only looks new

**A question can change hands twice and no more.** The ladder is: a named binding, then the
standing binding covering the subject, then `⊤`. Escalation reaches `⊤` in at most two hops, so
nothing needs a depth counter, and every hop is to a layer answering a smaller class of
questions, so there is no sideways move. Both properties already hold from how layers are
declared; escalation is what exercises them.

**Transfer, not request.** An `escalate` verdict transfers the question — the layer above owns
it from that moment, and its answer resolves the ORIGINAL question, because escalating changes
who answers and not what is being answered. Nothing waits: the escalating resolver keeps
reading and may answer other questions in the next frame, and only the work is blocked, at the
gate it always was. The verdict, when it comes, arrives as a frame like any other, so the
resolver that escalated learns what was decided by reading on.

**Re-owning already exists.** Removal re-routes a question to the next layer and writes the
handover on it. Escalation is that mechanism with a different trigger and one more field, which
is worth noticing before building a second one beside it.

## Order of work

### 1. The two dials, and the rule that binds them

```tony
deadline:  {within: "4h", then: escalate}
exclusive: true
```

`within` because that is what a selection and a motion already call a duration, and `then`
because the outcome is a choice rather than a fact about the field's name. `escalate` is its
only value here; the others motions already say their own way.

`exclusive` without a deadline is refused, and the message says why rather than citing a rule:
an exclusive binding that is down is a question nobody may answer, and a deadline is what makes
that a delay instead of a deadlock.

**Proves:** the coupling is enforced where it is cheap. Test: `exclusive` alone is refused
naming the deadline; the two together install; a deadline alone installs, since one without
exclusivity is just a slow hand-off; and `then:` with an outcome this build does not have is
refused naming what does.

### 2. The ladder

One function: given a question and its current owner, who is next. Named → the standing binding
covering its subject → nothing, which is `⊤`.

It is the same computation removal already does, and should be the same code: removal asks "who
owns this now that I am gone", escalation asks "who owns this now that I have declined". The
difference is only whether the current owner still exists.

**Proves:** at most two hops, and every hop narrows. Test: a question owned by a named binding
escalates to the standing one covering its subject; escalating again reaches `⊤`; a question
already at `⊤` cannot escalate; and the question id does not change, because the thing being
answered did not.

### 3. `escalate` as a verdict

The third verdict, accepted where `proceed` and `deny` are. It re-owns the question, writes the
handover the way removal does, and returns — the escalating resolver is not blocked and gets no
answer back.

**And the question must ARRIVE for the layer above.** This is the part with a hole in it today,
and it is the same hole the spine had before step 4: a `gate:question:*` entity is neither the
subject's ref nor a run about it, so an escalated question would appear on no stream and the
layer above would learn of it only by polling its docket. The design says it "appears in the
stream of the layer above" and that there is no second path anywhere, `⊤` included. So the
spine carries the questions about its subject, which is a third narrowing clause and the same
shape as the anchor one.

**Proves:** a transfer is visible to both parties. Test: an httptest resolver escalates and
keeps reading; the question appears on the standing binding's spine; that binding answers it;
the run proceeds on the original question; and the escalating resolver sees the decision on its
own stream, which is what makes a precedent visible instead of remembered.

### 4. What a deadline MEANS, and then what makes it happen

`escalatesAt = posedAt + within`, stamped on the question when it is posed and ABSOLUTE
so a restart cannot slide it. That much is settled. What is not is what the expiry does, and
that question is bigger than the mechanism it is usually mistaken for.

**Four readings, and they are not variants of one another:**

| on expiry | what it says | already sayable |
|---|---|---|
| **escalate** | the ask moves up; nobody has yet answered | no — this is what the plan adds |
| **proceed** | give folks a chance to weigh in, but progress is assumed without input | yes, as a motion's `defaultOption` |
| **deny** | the same shape, defaulting the other way | yes, the same way |
| **revert** | the escalated ask lapses and the ask it was raised from stands again | no, and not additively — see below |

The first three are one axis — an escalated ASK versus an escalated ANSWER — and the fourth is
not on it at all.

**This increment proves `escalate`, and the reason is forced rather than chosen.** The whole
point of pairing the deadline with `exclusive` is that an exclusive binding which is down must
not become a question nobody may answer. If expiry meant `proceed`, a down exclusive resolver
would silently approve every gate it exists to hold — the failure it was installed to prevent,
arriving by the mechanism installed to make it safe. `escalate` is the only outcome that
resolves the deadlock by finding somebody who *can* answer, and `⊤` is a person.

**`proceed` and `deny` are already sayable, elsewhere.** A motion's deadline names a
`defaultOption` that wins when the clock fires, which is exactly those two rows for a
whether-motion. So they are not future work here so much as an argument for the field shape
above: `then:` is a value, and a binding that wanted "proceed on silence" is asking for a
motion's default rather than for something new.

**`revert` is on neither list, which is the finding worth writing down.** It requires escalation
to be a request that can lapse, and the design says a transfer: "the layer above owns it from
that moment", "it does not ask a favour". A deadline that hands the question back is a
different protocol wearing the same word — layer *n* would have to remain able to answer what
it has already declined, and "every hop is to a layer answering a smaller class of questions"
stops holding. Worth wanting, and worth building as its own thing rather than as a fourth value
of this field.

### 4b. And then the mechanism, which is smaller than it looked

Tracing who CARES that a deadline passed answers it. The layer above is the only party the
expiry is for, and it learns by reading — its spine, or its docket if it is `⊤` and a person.
So the expiry can be evaluated when that reader arrives, which is the shape the claim lease
already uses: "expiry happens when somebody next asks for the work, which is the only moment
it matters".

Which corrects the thing this plan said first: that a lazy deadline "will not do here, because
the layer above must SEE the question arrive". It must — but the reader connecting is the
moment to make it appear, not a moment too late. A sweeper would compute the same transfer
slightly earlier, for nobody.

The cost is stated rather than hidden: a question overdue while nothing is reading stays where
it is, and moves the instant anything looks. For `⊤` that is exactly right — the docket a
person opens is the docket as of now. For an offline layer above, the work waits, which it was
going to do anyway.

**Proves:** a question moves without anyone saying so, and moves once. Test: a binding with a
short `escalatesAfter` stays silent; the layer above reads its spine and the question is there,
re-owned, with the handover and an unchanged id; a question inside its deadline is untouched;
reading twice does not move it twice; and a question whose owner answers before the deadline
never escalates at all.

### 5. `exclusive`: whose answer counts

The resolve path already knows the question's owner. Exclusive makes a `by` that is not that
owner a refusal.

Three things the design is explicit about, and each is a line of code or a line of prose:

- It governs whose answer COUNTS, never who may see the question. Nothing changes about the
  docket, and a person may still read what an exclusive binding owns.
- It is not real against somebody holding the operator API, and pretending otherwise would make
  the dial a lie. So an override stays possible and lands as an event naming both parties —
  who overrode, and whom.
- It is not a security boundary. It decides who answers an ask, never what an answer may
  authorise; the grant does that, and the two are not the same axis.

**Proves:** the dial is real where it is claimed to be real, and honest where it is not. Test:
a person answering an exclusively-owned question is refused naming the owner; the owner's own
verdict is accepted; an override is accepted and lands as an event naming both parties; and an
advisory binding is unchanged — the first answer still wins, which is what advisory means.

## What this plan does not build

No `llm` or `script` driver. No resolver over a scope other than baseline. No third door: a
binding is still named or standing, and the ladder is still two hops.

And no resolver over a MOTION, which is the increment after this one rather than a gap in it.
An `ask` selection is a one-answerer n-choice question and needs three things: the verdict
carrying a chosen option rather than proceed/deny, the question record carrying the options,
and a decision about what `exclusive` means when the options are doers. That last one is not
plumbing — motion.go notes that once a doer brings its own grant and tool set, "picking between
two names is picking between two authorities, and a ballot that shows only names asks the group
to authorise a capability it was never shown". A resolver answering a who-question is doing
something stronger than proceeding an action, and `exclusive` there decides who may hand out an
authority rather than who may allow a step.

## The residual to expect

**Whether lazy expiry is enough once something depends on the timing.** Evaluating at the
reader is right while the only consumer of an escalation is a reader. A rule that fired on an
overdue question, or a report that counted them, would want the transfer to have happened —
and would be the evidence that a sweep is worth its interval. Nothing wants that yet.

**Whether a resolver should ever be one voter among several.** Everything here assumes the
plurality-of-one side: exactly one owner per layer, which is what makes ownership a rule rather
than a tally. A `vote` selection is the other side, and a resolver casting a ballot alongside
people is a coherent thing to want — but `exclusive` and `owner` would both stop meaning what
they mean here, so it is a different mechanism sharing a word rather than an extension of this
one.

**Whether an override should be answerable by anyone.** It lands as an event naming both
parties, which is the design's answer, and that answer is about VISIBILITY rather than
permission — it says who did it, not who may. The gap `authority.md` names is the same one, and
this increment does not close it.

**The clock, in a footnote where it belongs.** `escalatesAt` is absolute and computed once, so
a restart cannot slide it and no arithmetic happens on a wall clock later. Two readings of one
machine's clock, as the claim lease already does.

**Whether escalation should carry the escalating resolver's notes.** A transfer that arrives
bare makes the layer above read the history itself, which it can; a transfer carrying what the
inner resolver had concluded is a handover in the sense `notes` already means. The design says
notes are for whoever picks up a POSITION, not a question, so this would be a second meaning
for the word — worth resisting until somebody wants it.
