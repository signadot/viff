# Escalation and exclusive: implementation plan

Status: plan, nothing built. It follows [the standing resolver](3-standing-resolver-plan.md),
which runs — bindings by name and by condition, ownership by layer, an instance per origin, a
spine, and removal that hands on.

The two arrive together because the design couples them: *exclusive requires a deadline, or a
resolver that is down becomes a question nobody may answer.* The deadline is `escalatesAfter`,
which is escalation's own machinery, so building `exclusive` alone would mean building the
half of escalation that makes it safe and stopping there.

## The slice

`escalate` as a verdict, `escalatesAfter` as the deadline that does the same thing more slowly,
and `exclusive` as the dial that says whose answer counts.

That completes the layer story: until now every layer answered what nobody above it had
claimed. Escalation is what makes layer *n* answer the questions layer *n−1* RAISES, which is
the sentence the design defines a layer with.

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

`escalatesAfter` (a duration) and `exclusive` (a bool) stop being refused.

`exclusive` without `escalatesAfter` is refused, and the message says why rather than citing a
rule: an exclusive binding that is down is a question nobody may answer, and a deadline is what
makes that a delay instead of a deadlock.

**Proves:** the coupling is enforced where it is cheap. Test: `exclusive` alone is refused
naming the deadline; the two together install; `escalatesAfter` alone installs, since a
deadline without exclusivity is just a slow hand-off.

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

### 4. Silence does the same thing more slowly

`escalatesAt = posedAt + escalatesAfter`, stamped on the question when it is posed and ABSOLUTE
so a restart cannot slide it.

**The mechanism is the one real decision here.** The claim lease expires lazily — "when
somebody next asks for the work, which is the only moment it matters" — and that will not do
here, because the layer above must SEE the question arrive, and nothing is asking on its
behalf. A question whose deadline passed while nobody looked has not escalated in any sense the
design means.

So this is the first thing in verse that needs a clock to make something happen rather than to
answer a question when asked. It should be as small as that admission: one sweep, over pending
questions carrying an `escalatesAt` in the past, doing exactly what an `escalate` verdict does.
Not a scheduler, and not a timer per question — the docket is already a selection, and "which
questions are overdue" is a predicate over it.

**Proves:** a question moves without anyone saying so. Test: a binding with a short
`escalatesAfter` is given a question and stays silent; the question is re-owned by the layer
above and carries the handover; its id is unchanged; and a question inside its deadline is left
alone.

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

## The residual to expect

**What an overdue sweep costs when nothing is overdue.** The sweep is a predicate over pending
questions, which is a selection over a slice that is small by construction — a docket with a
thousand open questions is a verse in trouble for other reasons. But it runs whether or not
anything is waiting, and its interval is a number nobody has evidence for. The lease chose
laziness to avoid exactly this and could not be followed here.

**Whether an override should be answerable by anyone.** It lands as an event naming both
parties, which is the design's answer, and that answer is about VISIBILITY rather than
permission — it says who did it, not who may. The gap `authority.md` names is the same one, and
this increment does not close it.

**Whether escalation should carry the escalating resolver's notes.** A transfer that arrives
bare makes the layer above read the history itself, which it can; a transfer carrying what the
inner resolver had concluded is a handover in the sense `notes` already means. The design says
notes are for whoever picks up a POSITION, not a question, so this would be a second meaning
for the word — worth resisting until somebody wants it.
