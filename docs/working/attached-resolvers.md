# Attached resolvers

Status: nothing here runs. It builds on [supervising process
gates](supervising-process-gates.md) and refines one move in it: that
note answers a gate by writing, and the steering invariant forbids
exactly that. The charter-level counterpart that does run is [an automatic
supervisor](../auto-supervisor.md).

The ask is to say a supervisor of process gates in a charter, apply it to the work that charter
spawns, and later point the same supervisor at trigger gates. One mechanism serves both, which
is the only reason the second half is cheap.

Four words carry the design. A _question_ is what a gate poses — a `process.GateQuestion`,
carrying the `Origin` that raised it, the `Subject` it is about, a mode, and the derived effects
of the action it gates. A _resolver_ answers questions. A _position_ is how far into a stream a
resolver has read. A _verdict_ is one answer, naming the question it answers.

## A resolver is an entity

It is installed, not declared in a charter, and it carries the whole binding rather than
pointing at one: what it supervises, how it answers, under what envelope, and its two dials.

```tony
name:      security-review                       # resolvers:security:security-review
condition:                                       # optional
  subject: { system: change, kind: pr, value: { area: payments } }
  when:    …                                     # the charter's own condition vocabulary
byRef:     true                                  # may be named
method:
  driver:  url                                   # llm | script | url
  vector:  ask                                   # auto | ask | park
  tools:   [read_entity]
  grant:   read-only
deadline: {within: "4h", then: escalate}   # was `escalatesAfter: 4h`
exclusive:      true
```

`condition` selects _entities_, in the vocabulary a trigger already uses. It does not match
rules and does not need to.

A process gate is reachable by the same `subject`, which it was not when this was written: a
question's subject is filled from the process's `flow` when it is posed, so "the payments PRs"
covers the gates a run raises about one as well as the firings that spawn it.

`method` says how it answers and what the answering may do. `driver` chooses between an `llm`
deciding, a `script` deciding, and a `url` verse hands the stream to. The rest is the envelope
of whatever that driver runs — its _acting_ grant. Those three are inert for a `url`, which
runs nothing verse can gate.

The deadline is a duration because a binding cannot know when a question will be posed. The
question stamps `escalatesAt = posedAt + within` when it is, absolute so a restart cannot slide
it. It is spelled `{within, then}` rather than `escalatesAfter` because a selection and a
motion already call a duration `within`, and because a motion's deadline picks a winning
OPTION while this one picks a new ANSWERER — so the outcome is a value rather than a fact about
the field's name. See [the plan](attached-resolvers/4-escalation-and-exclusive-plan.md).

`verse resolver add|remove` and `verse resolvers` manage it. A verb rather than a raw write,
because a write has nowhere to put the check that two conditions do not overlap.

## Two doors, and one of them must be open

```tony proposed
name: validate
condition:
  subject: { system: change, kind: pr, value: { stage: proposed } }
gating:
  resolver: resolvers:security:security-review   # answers this rule's own gates
  gates:  [{ name: ship, mode: ask }]
action:
  spawn:
    driver:   llm
    task:     "Validate $subject and fix what is trivially wrong."
    grant:    no-delegate                        # what the WORK may do at all
    vector:   ask                                # …and how much of it asks
    resolver: resolvers:security:security-review # answers this run's gates
```

A `condition` makes a resolver standing: nobody names it, and it supervises the work about the
entities it selects. `byRef` is the other door, and it attaches by being named — `gating` for a
rule's own gates, `resolver` on a spawn for a run's. A resolver with neither applies to nothing.

The two are not exclusive. One remit can cover the subjects it always watches and still be
pulled in where a charter wants it.

## Layers

```
the work  →  a named resolver  →  a standing resolver  →  ⊤
             (gating, or a         (whose condition       (auto, or the
              spawn's resolver)     covers the subject)     human docket)
```

Layer _n_ answers the questions layer _n−1_ raises while deciding. Absent layers are skipped: a
spawn with no `resolver` sends the work's questions straight to the standing resolver, and a
subject with none sends them to `⊤`.

Which layer a resolver occupies follows from what declared it, never from ranking two matches
against each other. A standing resolver is not a wider claim on the work's questions; it is the
answerer of a different set of questions.

Ownership needs no tie-break. A spawn names at most one resolver, a rule names at most one, and
standing conditions must be disjoint — so a question has exactly one answerer at its layer. The
_owner_ of a question is that answerer, and `⊤` always exists, so every question has one.

Three properties follow. Escalation reaches `⊤` in at most two hops, so no depth counter is
needed. Every hop is to a layer answering a smaller class of questions, so there is no sideways
move. And `⊤` matches everything while escalating to nothing, so "a resolver's own doubt ends
with a person" falls out rather than being a rule.

## What a resolver reads and writes

It is not called. It runs, connected, for as long as what it resolves lasts. So the I/O is two
streams rather than a request and a reply.

```tony
# in — frames in order, from `?since=<position>`
{ cursor: 33, kind: exec,    tool: run_tests, text: "2 failed" }
{ cursor: 34, kind: say,     text: "retrying with the fixture rebuilt" }
{ cursor: 35, kind: exec,    tool: write_file, path: "infra/deploy.tf" }
{ cursor: 36, kind: pending, id: "review@demo:pr:1~d3", gate: deploy, mode: ask,
  tool: run_bash, effects: { verbs: [exec], paths: ["infra/**"] },
  escalatesAt: 2026-08-02T13:12:44Z }

# out — a verdict when it decides, a position whenever it moves
{ id: "review@demo:pr:1~d3", verdict: deny, why: "prod path, and staging was asked for",
  say: "use the staging bucket" }
{ position: 36, notes: "denied the prod path at ~d3; staging is fine if it retries" }
```

Both streams already exist: this is `GET /events?since=N` and the control POST verse itself
uses to watch and answer. A resolver is another client of the pair.

They did not yet mean what this design assumed, which a census of the seams found and
[repairing them](seam-repair.md) fixed. `?since=` was read only for a local process, so a
resolver on a brokered or remote one was replayed events it had already acted on. A resume
below the retained window announced that something was missed and not WHAT, so a reconnecting
resolver could not see the open question it exists to answer. A stream that died mid-process
was indistinguishable from one that ended, so nothing reconnected. And an answer was recorded
as delivered the moment it was queued. All four are closed; a resolver can be written against
the pair as described.

Context arrives _before_ the ask. By frame 36 the resolver has read 35 and 34, which are why the
ask means what it means. Nothing has to be gathered and attached to a question, because the
resolver was there.

Reading and deciding are separate. A hundred frames may produce nothing, which is the ordinary
case, and the position advances anyway. That is what tells "has not decided" from "has not
caught up", and it is why the position is on the record rather than only in the reader.

The position is where to resume, not how to slice. It exists so a replacement can pick up after
a restart, a takeover, or a compaction. Nothing hands a resolver a batch and waits.

Escalation is another frame. A resolver that escalates says so, and the question appears in the
stream of the layer above — itself a live reader, connected for the duration of what _it_
resolves. That layer sees the ask, the events around it, and the inner verdict, because a
decision is an event on the same stream. There is no second path anywhere in the design, `⊤`
included: the human docket is this contract with a person reading at their own pace.

An `escalate` verdict transfers the question; it does not ask a favour. The layer above owns it
from that moment, and its answer resolves the _original_ question — the work proceeds or is
denied, on the work's own stream, because escalating changes who answers and not what is being
answered. Silence does the same thing more slowly: `escalatesAt` passes and the question moves
up without anyone saying so.

Nothing waits. A resolver that escalates keeps reading and may answer other questions in the
next frame; only the _work_ is blocked, at the gate it always was. The verdict, when it comes,
arrives as a frame like any other, so the resolver that escalated learns what was decided by
reading on — and can lean on it next time, which is what makes a precedent visible instead of
remembered.

Notes are a handover, not a memory. A live resolver remembers by being alive; `notes` rides the
position report above, for whoever picks that position up — a replacement after a restart, or
itself with a fresh context.

## A url resolver

```
GET  <verse>/resolvers/<ref>/events?since=<position>     replay, then live
POST <verse>/resolvers/<ref>/verdicts                    { id, verdict, why, say? }
POST <verse>/resolvers/<ref>/position                    { position, notes? }
```

`method.url` says where the service is, so verse can hand it a capability and an operator can
see what is bound. It is not an endpoint verse posts questions to. The service dials out for
both streams, so one behind NAT needs no special case.

Its obligations are the runner contract's, with the nouns changed: consume in order from your
position; answer only questions still open, since one may have lapsed upward while you read;
tolerate replay, because a question re-posed after a restart carries the same id; and report
your position, which is what lets verse resume you and tells an outer supervisor what you had
seen when you decided.

Attribution is verse's. `by` is the resolver's ref, stamped when the verdict is applied — a
service says what it decided, never who it is.

## An llm resolver

`driver: llm` earns its place only if it does something a script calling the same API cannot.
Otherwise it is sugar over a chat call, and thirty lines of script would serve better.

```tony
method:
  driver: llm
  grant:  read-only
  vector: auto
  tools:  [read_journal, read_entity]
  frame: |
    You are the reviewer for this run. What follows is everything that has happened since you
    last looked, including any asks. Answer the ones that are yours, escalate the ones a person
    should decide, and say nothing about the rest — advancing without deciding is a normal
    reply. Your notes are what the next reviewer of this run will know.
  answer:
    verdicts:
    - id:      !irtype string
      verdict: !or [proceed, deny, escalate]
      why:     !irtype string
      say:     !irtype string
    notes:     !irtype string
```

It earns its place on one thing: the conversation survives. An llm resolver is a long-lived
process, so frames are appended as they arrive and nothing is re-sent. A script is invoked per
wake and has nowhere to keep a conversation; one that kept a conversation would be a daemon,
which is what the process plane already is.

The contract is a _list_ because the asks arrive together, in the order they happened, beside
the events that explain them. A single verdict would force one invocation per ask and throw
that away.

The frame carries the remit and the protocol's three surprises: that saying nothing is a normal
reply, that `escalate` is an answer rather than a failure, and that notes are a handover. A
model told none of those answers every ask to be helpful, which is the supervisor that clears
everything.

Verse enforces the rest, so the frame does not have to: the answer shape and its repair budget,
addressing a verdict to a question still open, attribution, replay, and the envelope. A run that
cannot produce the shape escalates what is open — a resolver that could not answer has
escalated.

Two things end a conversation early: a restart, and a context grown past what the model holds.
Both are one repair — rebuild from the position, the notes, and a bounded tail of the stream.
Restart, takeover and compaction being one path is what keeps it exercised.

Write a `script` instead when the decision is a function of the question alone. Most remits are
predicates, and a predicate that spawns a model pays for judgment it does not use. The llm
driver earns its keep when the _history_ is the evidence — when what makes an action wrong is
that it contradicts something said twenty events ago, which is also when re-sending that history
per call costs the most.

# Principles

## Answering is control, not a write

[deployment-and-steering](../deployment-and-steering.md) lists answering a gate as control and
forbids modelling an intervention as a store write. So a resolver answers out-of-band, and the
record is written _from_ the mirrored decision, after the fact. Nothing reacts to a verdict on
the record by resolving; the moment something does, the command entity is back wearing a
record's clothes, queueing behind the backlog it was meant to outrank.

Routing through the runner API is what makes supervision survivable. Verse replays a pending
question, re-routes it, re-POSTs with the same idempotency key and dedups by cursor — so an
answerer whose lifetime is decoupled from the asker's still converges.

## Advisory, or exclusive

A binding is advisory by default: the question stays on the human docket and the first answer
wins. That is wrong for the case people bind for — a review a person may not rubber-stamp — so
`exclusive` is a dial on the resolver, and nothing that names it may weaken it.

Exclusivity governs whose answer counts, never who may see the question. Verse can enforce it
where it already enforces the id: the resolve endpoint knows the binding and can refuse a `by`
that is not the owner. That is real against somebody working down a docket without noticing;
it is not real against somebody holding the operator API, and pretending otherwise would make
the dial a lie. An override stays possible and lands as an event naming both parties.

Two things travel with it. Exclusive requires a deadline, or a resolver that is down becomes a
question nobody may answer. And exclusivity is not a security boundary: it decides who answers
an ask, never what an answer may authorise.

## A resolver sees what an attached observer sees

The stream carries what was reported — narration, tool calls and their outputs, boundaries,
children, status. A _boundary_ is the point between actions where the loop services control
before proposing the next one, so it is on the stream because it says the process stopped to
check rather than that nothing happened.

For a process that is exactly what `verse process attach` shows: the read plane, whole and in
order, not a projection. There is no supervisor's view to design. What the stream does not carry
is the driver's internal reasoning, which is the line
[external processes](../external-processes.md) already draws between intercepting and attesting
— and what makes one resolver work identically over a local loop, a k8s Job, and a human dialing
into a broker.

## Supervision is never a side channel

Control application is emitted on the one ordered stream, so an observer reads the whole
exchange in order: the ask, the decision with its `by`, the say, the action that followed. There
is no supervised run that reads as unsupervised, and no way to answer invisibly.

What the stream does not show is what was _watched_ — a position advancing with no verdict emits
nothing. So the resolver is a second subject with a record of its own, and the rule of thumb is
worth keeping: the process stream is what was decided, the resolver's record is who was watching
and on what evidence.

## The journal needs an address

A process's events are served from a bounded ring, so a consumer that falls behind gets
resync-to-snapshot rather than the frames it missed. For verse that is benign, because the
entity is level truth. For a resolver it is fatal in the wrong place: current state is exact,
and the run that explains it is gone.

Give the journal an address and leave it with its owner — `process:journal:<id>`, served on
demand, the split [content federation](../content-federation.md) already makes for files. The
served range reads a durable log rather than the live ring, which is the half that makes the
address worth having; without it a resolver asking for events 12–40 gets the same
resync-to-snapshot as everyone else.

It costs a retention policy with a cap, and a served range needs three answers rather than two:
here it is, that range is not retained, no such process. Collapsing the middle into an error is
how a supervisor comes to judge on less than it asked for without knowing.

## Two planes, one spine

A resolver above one process reads two streams with independent cursors and no common order.
Do not build a sequencer for it. Carry a cursor pair and lean on a property that already holds:
a process's spawn and its terminal are both store commits, because `mirror` writes them. The
store stream is the spine, ordered and durable, carrying the anchors; process streams hang off
it as fine grain between anchors.

## Where a resolver lives

An instance lives for the duration of what it resolves, and the origin decides what that is. Over
a run it lives for the run and reads the run. Over a subject it lives for the subject and reads
the store, because a firing is over in a commit or two and attaching per firing would be
spawn-answer-die — the per-question shape this design exists to avoid, arrived at from the other
end.

A firing has no event stream, so a resolver at that origin reads the store's commit stream,
narrowed to what the rule reads. Its position is a revision rather than a cursor, and its
evidence is the subject's history rather than a run's.

# What is open

- _Escalate on conflict._ Answered differently than this asked. Overlapping conditions are no
  longer refused at add — disjointness of two predicates is undecidable, so the refusal had to
  be conservative and would have blocked legitimate pairs. They are ORDERED instead: the first
  binding by name owns, which keeps exactly one answerer per layer without deciding anything at
  install. Promoting on conflict is then unnecessary rather than deferred, and the runtime case
  refusal could not see is covered by the same rule.
- _Who may write a resolver._ A standing binding is exactly as strong as the ability to add and
  remove it, and the store has no per-slice write authority — the same gap
  [authority](../roadmap/authority.md) has about who may declare a resolver that sponsors.
- _Whether a long conversation holds the protocol._ Does it stay silent on frames that do not
  concern it, keep `escalate` from drifting into approval as context fills, and not re-answer
  what it has answered? Measurable rather than arguable; `scratchpad/resolverio` runs both shapes
  over one stream to find out.
