# Attached resolvers

A gate that blocks has to be answered by somebody. `ask` means a person; `auto` means nobody is
asked at all. Between them is the thing most flows want: *a service decides, and hands up what
is not its to decide.*

A **resolver** is that service. It is attached rather than called: it reads the run's own stream
for as long as the run lasts, so the context arrives before the ask. It answers through the same
path `verse gate approve` takes, with `by` naming the binding instead of a person.

`driver: url` is the only driver built, and the missing corners are named in
[the residual](working/attached-resolvers-residual.md).
[An automatic supervisor](auto-supervisor.md) is the charter-level counterpart: an agent that
decides by writing to the subject, where this decides by answering the gate.

Four words carry it. A **question** is what a gate poses, carrying the origin that raised it,
the subject it is about, a mode, and the derived effects of the action it gates. A **resolver**
answers questions. A **position** is how far into a stream a resolver has read. A **verdict** is
one answer, naming the question it answers.

## A resolver is an entity

It is installed rather than declared in a charter, and it carries the whole binding rather than
pointing at one: what it supervises, how it answers, under what envelope, and its two dials.

```tony
name:      security-review                       # verse.resolvers.security.security-review
kind:      security
condition:                                       # optional — the standing door
  subject: {path: [change, pr, *], value: { area: payments }}
byRef:     true                                  # optional — the named door
method:
  driver:  url
  url:     "https://review.internal/verse"
deadline:  { within: "4h", then: escalate }
exclusive: true
```

`condition` selects *entities*, in the vocabulary a trigger already uses. It does not match rules
and does not need to: a task gate is reachable by the same `subject`, because a question's subject
is filled from the task's `flow` when it is posed. So "the payments PRs" covers the gates a run
raises about one as well as the firings that spawn it.

`method` says how it answers and what the answering may do. `vector`, `tools` and `grant` bound
whatever the driver runs, and are inert for a `url` — which runs nothing verse can gate — but
are carried anyway so the entity says the same thing about every driver.

The deadline is a **duration**, because a binding cannot know when a question will be posed. The
question stamps `escalatesAt = now + within` when it is posed, absolute, so a restart cannot slide
it and no arithmetic happens on a wall clock later. It is spelled `{within, then}` rather than
`escalatesAfter` because a provider selection and a gate's deadline already call a duration
`within`, and because that deadline picks a winning *option* while this one picks a new *answerer*, so the
outcome is a value rather than a fact about the field's name.

`verse resolver` manages it: bare it lists, and `add`, `show` and `remove` act on one. A verb
rather than a raw write, because a write has nowhere to put the checks that make a binding
answerable at all.

## Two doors, and one of them must be open

```tony
name: validate
condition:
  subject: {path: [change, pr, *], value: { stage: proposed }}
gating:
  resolver: verse.resolvers.security.security-review   # answers this RULE's own gates
  gates:  [{ name: ship, mode: ask }]
action:
  spawn:
    driver:   llm
    prompt:     "Validate $subject and fix what is trivially wrong."
    grant:    no-delegate                        # what the WORK may do at all
    vector:   ask                                # …and how much of it asks
    resolver: verse.resolvers.security.security-review # answers this RUN's gates
  onDone:
    patch: { stage: validated }                  # the latch: out of its own selection
```

A `condition` makes a resolver **standing**: nobody names it, and it supervises the work about
the entities it selects. `byRef` is the other door, and it attaches by being *named* — `gating`
for a rule's own gates, `resolver` on a spawn for a run's. A binding with neither is refused at
install, because nothing could ever reach it.

The two are not exclusive. One remit can cover the subjects it always watches and still be
pulled in where a charter wants it.

A spawn naming a binding that is **not installed** is refused too. Left standing, the question
would be owned by something that cannot answer it, *and* it would shadow the standing binding
covering the same subject, which would never see the question and would have no instance through
which to reach it. A misspelled binding would turn a supervised subject into an unsupervised one,
silently.

## Layers

```
the work  →  a named resolver  →  a standing resolver  →  ⊤
             (gating, or a         (whose condition       (auto, or the
              spawn's resolver)     covers the subject)     human docket)
```

Layer *n* answers the questions layer *n−1* raises while deciding. Absent layers are skipped: a
spawn with no `resolver` sends the work's questions straight to the standing resolver, and a
subject with none sends them to ⊤.

Which layer a resolver occupies follows from what declared it, never from ranking two matches
against each other. A standing resolver is not a wider claim on the work's questions; it is the
answerer of a different set of questions.

**Overlap is ordered, not refused.** Two standing conditions may both cover a subject, and the
first binding *by name* owns its questions. Requiring disjointness instead is not available:
deciding whether two predicates are disjoint is undecidable in general, so the refusal would have
to be conservative and would block legitimate pairs. Ordering keeps exactly one answerer per layer
without deciding anything at install, and it covers the runtime case a refusal could not see.
Installing an overlapping binding is a **notice**, naming what it overlaps.

Three properties follow. Escalation needs no depth counter, and none is stored: the ladder is the
named binding, then the standing bindings covering the subject **in name order**, then ⊤, and a
binding cannot hand a question to itself or to anything before it, so every hop climbs and the
last lands at ⊤. Every hop is to a layer answering a smaller class of questions, so there is no
sideways move. And ⊤ matches everything while escalating to nothing, so "a resolver's own doubt
ends with a person" falls out rather than being a rule. A rung may be a service, a person or a
group ([reference/resolvers.md](reference/resolvers.md#the-binding)); a person climbs it with
`verse gate escalate`.

## What a resolver reads and writes

It is not called. It runs, connected, for as long as what it resolves lasts. So the I/O is two
streams rather than a request and a reply.

```tony
# in — frames in order, from `?since=<position>`
{ cursor: 33, kind: exec,    tool: run_tests, text: "2 failed" }
{ cursor: 34, kind: say,     text: "retrying with the fixture rebuilt" }
{ cursor: 35, kind: exec,    tool: write_file, path: "infra/deploy.tf" }
{ cursor: 36, kind: pending, id: "review-8f30@demo.pr.1~d3", gate: deploy, mode: ask,
  tool: run_bash, effects: { verbs: [exec], paths: ["infra/**"] },
  escalatesAt: 2026-08-02T13:12:44Z }

# out — a verdict when it decides, a position whenever it moves
{ id: "review-8f30@demo.pr.1~d3", verdict: deny, why: "prod path, and staging was asked for",
  say: "use the staging bucket" }
{ position: 36, notes: "denied the prod path at ~d3; staging is fine if it retries" }
```

Both streams already existed: this is the run's own `?since=N` and the control POST verse itself
uses to watch and answer. A resolver is another client of the pair.

**Context arrives before the ask.** By frame 36 the resolver has read 35 and 34, which are why the
ask means what it means. Nothing has to be gathered and attached to a question, because the resolver
was there. That is why a resolver is attached rather than handed a question, and why its instance is
minted when the run starts rather than when the first question is posed.

**Reading and deciding are separate.** A hundred frames may produce nothing, which is the
ordinary case, and the position advances anyway. That is what tells "has not decided" from "has
not caught up", and it is why the position is on the record rather than only in the reader.

**The position is where to resume, not how to slice.** It exists so a replacement can pick up
after a restart, a takeover, or a compaction. Nothing hands a resolver a batch and waits.

**Escalation is another frame.** A resolver that escalates says so, and the question appears in
the stream of the layer above — itself a live reader, connected for the duration of what *it*
resolves. That layer sees the ask, the events around it, and the inner verdict, because a
decision is an event on the same stream. There is no second path anywhere, ⊤ included: the human
docket is this contract with a person reading at their own pace.

An `escalate` verdict **transfers** the question rather than asking a favour. The layer above owns
it from that moment, and its answer resolves the *original* question: the work proceeds or is
denied, on the work's own stream, because escalating changes who answers and not what is being
answered. Re-owning also mints an instance for the new owner, since re-owning is an ownership
decision and that is where instances are minted, so a layer that inherits a run can read its fine
grain rather than only the fact that it now owns something.

**Silence does the same thing more slowly.** `escalatesAt` passes and the question moves up without
anyone saying so, evaluated when a **reader arrives** rather than by a clock of its own, because the
layer above is the only party an expiry is for and it learns by reading. A sweeper would compute the
same transfer slightly earlier, for nobody. So a question overdue while nothing is reading stays
where it is and moves the instant anything looks, which for ⊤ is exactly right: the docket a person
opens is the docket as of now.

Re-owning **re-arms** rather than inherits. A question handed to a binding with no deadline
would otherwise keep the last one's; one handed to a binding that has its own would stay overdue
against a deadline nobody set and move again on every read.

**Nothing waits.** A resolver that escalates keeps reading and may answer other questions in the
next frame; only the *work* is blocked, at the gate it always was. The verdict, when it comes,
arrives as a frame like any other, so the resolver that escalated learns what was decided by
reading on — and can lean on it next time, which is what makes a precedent visible instead of
remembered.

**Notes are a handover, not a memory.** A live resolver remembers by being alive; `notes` rides
the position report, for whoever picks that position up — a replacement after a restart, or
itself with a fresh context.

## Principles

### Answering is control, not a write

[Deployment and steering](deployment-and-steering.md) lists answering a gate as control and
forbids modelling an intervention as a store write. So a resolver answers out-of-band, and the
record is written *from* the mirrored decision, after the fact. Nothing reacts to a verdict on
the record by resolving; the moment something does, the command entity is back wearing a
record's clothes, queueing behind the backlog it was meant to outrank.

Routing through the runner API is what makes supervision survivable. Verse replays a pending
question, re-routes it, re-POSTs with the same idempotency key and dedups by cursor — so an
answerer whose lifetime is decoupled from the asker's still converges.

### Advisory, or exclusive

A binding is **advisory** by default: the question stays on the human docket and the first
answer wins. That is wrong for the case people bind for — a review a person may not
rubber-stamp — so `exclusive` is a dial on the resolver.

Three things it is, and is not:

- It governs **whose answer counts**, never who may see the question. Nothing changes about the
  docket, and a person may still read — and argue about — what an exclusive binding owns.
- It is **not real against somebody holding the operator API**, and pretending otherwise would make
  the dial a lie. So an override stays possible, as a named act: `verse gate approve <id> --as
  me@example.com --override` lands `overrode: <binding>` beside `by: <who>`, and the record names both parties. The
  refusal names the owner and says how, so the flag answers a question the operator has just been
  asked.
- It is **not a security boundary**. It decides who answers an ask, never what an answer may
  authorise; the grant does that, and the two are not the same axis.

Exclusive **requires a deadline**, or a resolver that is down turns every question it owns into
one nobody may answer. That is checked at install.

### A resolver sees what an attached observer sees

The stream carries what was reported: narration, tool calls and their outputs, boundaries, children,
status. A *boundary* is the point between actions where the loop services control before proposing
the next one, and it is on the stream because it says the task stopped to check rather than that
nothing happened.

For a task that is exactly what `verse task attach` shows: the read plane, whole and in
order, not a projection. There is no supervisor's view to design. What the stream does not carry
is the driver's internal reasoning, which is the line
[external tasks](external-tasks.md) already draws between intercepting and attesting —
and what makes one resolver work identically over a local loop, a k8s Job, and a human dialing
into a broker.

### Supervision is never a side channel

Control application is emitted on the one ordered stream, so an observer reads the whole
exchange in order: the ask, the decision with its `by`, the say, the action that followed. There
is no supervised run that reads as unsupervised, and no way to answer invisibly.

`verse task attach <run>` shows it, and it is worth seeing rather than being told:

```tony
{kind: pending,  id: "review-bff7…~d1", mode: ASK, tool: run_bash}
{kind: decision, id: "review-bff7…~d1", by: verse.resolvers.security.reviewer, proceed: true}
{kind: exec,     tool: run_bash, text: reviewing}
{kind: status,   text: done}
```

Nothing about that `decision` frame differs in shape from a person's answer — only the `by`.

What the stream does not show is what was *watched*, since a position advancing with no verdict
emits nothing. So the resolver is a second subject with a record of its own: **the task stream is
what was decided, the resolver's record is who was watching and on what evidence.** That is also
why a position is written on the instance rather than on the binding: who was watching is per run,
so writing it on the binding would be one resolver's second run overwriting what its first one
saw.

### Two planes, one spine

A resolver above one task reads two streams with independent cursors and no common order.
There is no sequencer for it, deliberately. It carries a cursor pair and leans on a property
that already holds: a task's spawn and its terminal are both store commits, because `mirror`
writes them. The store stream is the spine, ordered and durable, carrying the anchors; task
streams hang off it as fine grain between anchors.

### Where a resolver lives

An instance lives for the duration of what it resolves, and the origin decides what that is.
Over a **run** it lives for the run and reads the run. Over a **subject** it lives for the
subject and reads the store, because a firing is over in a commit or two and attaching per
firing would be spawn-answer-die — the per-question shape this design exists to avoid, arrived
at from the other end.

A firing has no event stream, so a resolver at that origin reads the store's commit stream,
narrowed to what the rule reads. Its position is a revision rather than a cursor, and its
evidence is the subject's history rather than a run's.

An instance is **machinery, not an audit trail**. The store is already the evidence, since a
position report and a note are commits when they are made. So an instance belongs to its binding
rather than to the thing it watches, and removing the binding is what reaps one. Removal hands on
what the binding held: its pending questions go to the layer above with `reownedWhy: removed`,
because a question left naming a binding that will not answer is one nobody can answer, listed on a
docket as though somebody were coming.

## Walk it

```sh
./scripts/attached-resolver.sh              # five acts, one command each
./scripts/attached-resolver.sh run layered  # named → standing → ⊤, in two hops
```

The surface — every field, every route, every verb — is
[reference/resolvers.md](reference/resolvers.md).
