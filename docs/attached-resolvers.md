# Attached resolvers

A gate that blocks must be answered by somebody. The mode `ask` means that a person answers.
The mode `auto` means that nobody is asked. Most flows want a third thing: *a service decides,
and passes up what is not its to decide.*

A **resolver** is the installed record that says who answers
([resolver](glossary.md#resolver)). The service is its **answerer**
([answerer](glossary.md#answerer)). Verse does not call the answerer. The answerer is attached:
it reads the run's own [event stream](glossary.md#event-stream) for as long as the run lasts, so
it has the context before the question arrives. It answers through the same route that
`verse gate approve` uses, with `by` naming the resolver instead of a person.

This page is about a service, `driver: url`. A resolver may name a person or a group instead.
A person or a group answers from the [docket](glossary.md#docket)
([reference/resolvers.md](reference/resolvers.md#the-resolver)).
[An automatic supervisor](auto-supervisor.md) is the counterpart at the level of a charter. An
automatic supervisor is an agent that decides by writing to the subject. A resolver decides by
answering the gate.

Six words describe the design:

| Word | What it is |
|---|---|
| **question** | what a gate poses ([question](glossary.md#question)). It carries the origin that raised it, the subject it is about, a mode, and the derived effects of the action that the gate guards. |
| **resolver** | the record that says who answers questions |
| **answerer** | the service, the person or the group that the resolver names. The answerer answers. |
| **cover** | one resolver on one gate stream ([cover](glossary.md#cover-of-a-resolver)). A **gate stream** is the stream in which the gates are raised: a run's event stream, or, for a rule's own gates, the subject's commit stream. |
| **position** | how far into its stream a cover has read |
| **verdict** | one answer. It names the question that it answers. |

A gate stream is not a stream made only of gates. A cover reads everything in the stream. This
is how [context arrives before the ask](#what-an-answerer-reads-and-writes).

## A resolver is an entity

You install a resolver. You do not declare it in a charter. The resolver holds all of who
answers, and does not point elsewhere for it. It holds what it supervises, how it answers, the
limits on the answering, and its two settings.

```tony
name:      security-review                       # verse.resolvers.security-review
condition:                                       # optional — the standing door
  subject:
    path: "change.pr.*"
    value:
      area: payments
byRef:     true                                  # optional — the named door
method:
  driver:  url
  url:     "https://review.internal/verse"
deadline:  { within: "4h", then: escalate }
exclusive: true
```

A resolver record has six fields: `name`, `condition`, `method`, `deadline`, `exclusive` and
`byRef`. Verse refuses a record with an unknown field.

`condition` selects *entities*, in the same words that a rule's `subject` uses. It does not
match rules. A gate of a task is reached by the same `subject`, because verse fills a question's
subject from the task's `flow` when it poses the question. So "the payments PRs" matches the
gates that a run raises about one of them. It also matches the firings that spawn the run.

`method` says how the resolver answers and what the answering may do. `vector`, `tools` and
`grant` limit whatever the driver runs. They have no effect for a `url`, because a `url` runs
nothing that verse can gate. The entity carries them anyway, so that it says the same thing for
every driver.

The deadline is a **duration**, because a resolver cannot know when a question will be posed.
When verse poses the question, it stamps `escalatesAt = now + within` on it. `escalatesAt` is an
absolute time, so a restart cannot move it, and verse does no arithmetic on a wall clock later.
The deadline is spelled `{within, then}`, and not `escalatesAfter`. The reason is in
[Why it is this way](#the-spelling-of-the-deadline).

`verse resolver` manages resolvers. With no verb it lists them. `add`, `show` and `remove` act
on one resolver.

A verb, and not a raw write, installs a resolver. A raw write has no place for the checks that
make a resolver able to answer.

## Two doors, and one of them must be open

```tony
name: validate
condition:
  subject:
    path: "change.pr.*"
    value:
      stage: proposed
gating:
  resolver: verse.resolvers.security-review   # answers this RULE's own gates
  gates:
  - name: ship
    mode: ask
action:
  spawn:
    driver:   llm
    prompt:     "Validate $subject and fix what is trivially wrong."
    grant:    no-delegate                        # what the WORK may do at all
    vector:   ask                                # …and how much of it asks
    resolver: verse.resolvers.security-review # answers this RUN's gates
  onDone:
    patch: { stage: validated }                  # the latch: out of its own selection
```

A resolver is attached through one of two doors:

| Door | How the resolver is attached |
|---|---|
| `condition` | The resolver is **standing** ([standing resolver](glossary.md#standing-resolver)). Nobody names it. It supervises the work about the entities that it selects. |
| `byRef` | The resolver is attached by being *named*: by `gating` for a rule's own gates, or by `resolver` on a spawn for a run's gates. |

Verse refuses at install a resolver with neither door, because nothing could reach it.

A resolver may have both doors. It can match the subjects that it always watches, and a
charter can still name it where the charter wants it.

Verse also refuses a spawn that names a resolver that is **not installed**. A misspelled
resolver would otherwise make a supervised subject unsupervised, and nothing would say so.

The question would belong to something that cannot answer it. It would also hide the question
from the standing resolver that matches the same subject. That standing resolver would never see
the question, and it would have no cover through which to reach it.

## Layers

```
the work  →  a named resolver  →  a standing resolver  →  ⊤
             (gating, or a         (whose condition       (auto, or the
              spawn's resolver)     matches the subject)    human docket)
```

Here `⊤` is the top layer: `auto`, or the human docket. Layer *n* answers the questions that
layer *n−1* raises while it decides. Verse skips an absent layer. A spawn with no `resolver`
sends the work's questions to the standing resolver. A subject with no standing resolver sends
them to `⊤`.

What declared a resolver decides which layer it occupies. Verse never ranks two matches against
each other. A standing resolver is not a wider claim on the work's questions. It answers a
different set of questions.

**Overlap is ordered, not refused.** Two standing conditions may both match a subject. Then the
first resolver *by name* owns the questions. Installing an overlapping resolver gives a
**notice** that names what it overlaps. The reason for ordering is in
[Why it is this way](#overlap-is-ordered).

Three properties follow from the layers:

1. **Escalation needs no depth counter**, and verse stores none. The
   [ladder](glossary.md#ladder) is the named resolver, then the standing resolvers that match the
   subject **in name order**, then `⊤`. A resolver cannot hand a question to itself or to
   anything before it. So every hop goes up, and the last hop lands at `⊤`.
2. **There is no sideways move.** Every hop goes to a layer that answers a smaller class of
   questions.
3. **An answerer's own doubt ends with a person.** `⊤` matches everything and escalates to
   nothing. This follows from the layers, and needs no rule of its own.

A place on the ladder may be a service, a person or a group
([reference/resolvers.md](reference/resolvers.md#the-resolver)). A person moves a question up the
ladder with `verse gate escalate`.

## What an answerer reads and writes

Verse does not call an answerer. The answerer runs, connected, for as long as the thing it
resolves lasts. So its input and output are two streams, and not a request and a reply.

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

The two streams are ones that verse already has. The input is the run's own `?since=N`. The
output is the control POST that verse itself uses to answer. An answering service is one more
client of the pair.

**Context arrives before the ask.** By frame 36 the answerer has read frames 35 and 34. Those
frames explain what the question means. Nothing has to be gathered and attached to a question,
because the answerer has read the frames already. For this reason the answerer is attached and is
not handed a question. For the same reason, verse creates its cover when the run starts, and not
when the first question is posed.

**Reading and deciding are separate.** A hundred frames may produce no verdict, and that is the
usual case. The position advances anyway. The position tells "has not decided" apart from "has
not caught up". For this reason the position is on the record, and not only in the reader.

**The position is where to resume, not a unit of work.** It lets a replacement continue after a
restart, a takeover or a compaction. Nothing hands an answerer a batch and waits for it.

**Escalation is another frame.** An answerer that escalates says so in its output. The question
then appears in the stream of the layer above. That layer is also a live reader, connected for as
long as the thing that *it* resolves. It sees the question, the events around it, and the verdict
of the layer below, because a verdict is an event on the same stream. There is no second route,
at `⊤` either. The human docket is this same contract, with a person who reads at their own
pace.

An `escalate` verdict **transfers** the question. The layer above owns it from that moment. Its
answer resolves the *original* question: the work proceeds or is denied, on the work's own
stream. Escalating changes who answers, and not what is answered. Transferring a question also
creates a cover for the new owner, because a transfer is a decision about ownership, and verse
creates covers at such decisions. So a layer that takes over a run can read the run in detail,
and not only the fact that it now owns something.

**Silence has the same effect, more slowly.** When `escalatesAt` passes, the question moves up
without anyone saying so. Verse checks the deadline when a **reader arrives**. It has no clock of
its own for this ([Why it is this way](#deadlines-are-checked-by-readers)). A question that is overdue while nothing reads stays where it is. It moves as
soon as anything reads. For `⊤`, this means the docket that a person opens is the docket as of
that moment.

When a question is transferred, verse **re-arms** its deadline from the new owner. The question
does not keep the last owner's deadline.

Without this, a question handed to a resolver with no deadline would keep the last one's
deadline. A question handed to a resolver with its own deadline would stay overdue against a
deadline that nobody set, and would move again on every read.

**Nothing waits.** An answerer that escalates keeps reading. It may answer other questions in the
next frame. Only the *work* is blocked, at its gate. The verdict arrives as a frame like any
other. So the answerer that escalated learns the decision by reading on. It can use that decision
the next time, so a precedent is visible in the stream and does not depend on memory.

**Notes are a handover, not a memory.** A live answerer remembers because it is running. `notes`
goes with the position report. It is for whoever continues from that position: a replacement
after a restart, or the same answerer with a fresh context.

## Principles

### Answering is control, not a write

[Deployment and steering](deployment-and-steering.md) lists answering a gate as control. It
forbids an intervention that is modelled as a write to the store. So a service answers outside
the store, and verse writes the record *from* the mirrored decision, afterwards. Nothing in
verse resolves a question because a verdict appears on the record. The reason is in
[Why it is this way](#answering-is-not-a-write).

Verse replays a pending question, routes it again, sends the POST again with the same idempotency
key, and removes duplicates by cursor. So an answerer that lives longer or shorter than the asker
still reaches the same result. Answering through the runner API makes this possible.

### Advisory, or exclusive

A resolver is **advisory** by default. The question stays on the human docket, and the first
answer wins. `exclusive` is a setting on the resolver
([exclusive resolver](glossary.md#exclusive-resolver)). With `exclusive: true`, only the
resolver's answer counts.

`exclusive` has three limits:

| It is | What that means |
|---|---|
| about **whose answer counts** | It never changes who may see the question. The docket is unchanged. A person may still read what an exclusive resolver owns, and argue about it. |
| **not a limit on somebody who holds the operator API** | An override stays possible, as a named act: `verse gate approve <id> -as me@example.com -override` records `overrode: <resolver>` beside `by: <who>`. The record names both parties. The refusal names the owner and says how to override. |
| **not a security boundary** | It decides who answers a question. It never decides what an answer may allow. The grant decides that, and the two are separate. |

An exclusive resolver **requires a deadline**. Verse checks this at install.

Without a deadline, an answerer that is down would make every question it owns a question that
nobody may answer.

### An answerer sees what an attached observer sees

The stream carries what the task reported: narration, tool calls and their outputs, boundaries,
children and status. A [boundary](glossary.md#boundary) is the point between two actions where
the task handles control before it proposes the next action. The boundary is on the stream,
because it says that the task stopped to check. Without it, the stream would look as if nothing
happened.

For a task, the answerer sees exactly what `verse task attach` shows. That is the whole
[observation plane](glossary.md#observation-plane), in order, and not a projection. There is no
separate view for a supervisor. The stream does not carry the driver's internal reasoning.
[External tasks](external-tasks.md) draws that line, between intercepting and attesting.

That line lets one answerer work the same way over a local loop, a k8s Job, and a person who
connects to a [broker](glossary.md#broker).

### Supervision is never a side channel

Verse writes each control act on the one ordered stream. So an observer reads the whole exchange
in order: the question, the decision with its `by`, the say, and the action that followed. No
supervised run looks unsupervised, and nobody can answer invisibly.

`verse task attach <run>` shows it:

```tony
{kind: pending,  id: "review-bff7…~d1", mode: ASK, tool: run_bash}
{kind: decision, id: "review-bff7…~d1", by: verse.resolvers.reviewer, proceed: true}
{kind: exec,     tool: run_bash, text: reviewing}
{kind: status,   text: done}
```

The `decision` frame has the same shape as a person's answer. Only the `by` differs.

The stream does not show what was *watched*, because a position that advances with no verdict
emits nothing. So the cover is a second subject, with a record of its own. **The task stream is
what was decided, and the cover is who was watching and on what evidence.**

For the same reason, verse writes a position on the cover, and not on the resolver. Who was
watching belongs to one run. If verse wrote it on the resolver, the second run that the resolver
covers would overwrite what the first run saw.

### Two planes, one spine

An answerer above one task reads two streams. Each stream has its own cursor, and the two have no
common order. Verse has no sequencer for them, on purpose. The answerer keeps a pair of cursors.

The answerer relies on a property that already holds. A task's spawn and its end are both commits
in the store, because `mirror` writes them. The store stream is the spine: ordered, durable, and
carrying the anchors. The task streams give the detail between the anchors.

### Where a cover lives

A cover lives as long as the thing it resolves, and its stream decides what that thing is:

| The cover is over | It lives for | It reads |
|---|---|---|
| a **run** | the run | the run's event stream |
| a **subject** | the subject | the store |

A firing has no event stream. So a cover over a firing reads the store's commit stream, narrowed
to what the rule reads. Its position is a [revision](glossary.md#revision), and not a cursor. Its
evidence is the subject's history, and not a run's.

A cover over a subject lives for the subject because a firing ends within a commit or two
([Why it is this way](#why-a-cover-over-a-subject-lives-for-the-subject)).

A cover is **machinery, not an audit trail**. The store is already the evidence, because a
position report and a note are commits when they are made. So a cover belongs to its resolver,
and not to the thing it watches. Removing the resolver removes its covers. Removal also passes on
what the resolver held: its pending questions go to the layer above with `reownedWhy: removed`.

A question that still names a removed resolver could never be answered. It would sit on a docket
as though somebody were coming to answer it.

## Walk it

```sh
./scripts/attached-resolver.sh              # five acts, one command each
./scripts/attached-resolver.sh run layered  # named → standing → ⊤, in two hops
```

[reference/resolvers.md](reference/resolvers.md) lists every field, every route and every verb.

## Why it is this way

### The spelling of the deadline

The deadline is spelled `{within, then}`, and not `escalatesAfter`. A provider selection and a
gate's deadline already call a duration `within`. A gate's deadline picks a winning *option*.
The deadline of a resolver picks a new *answerer*. So the outcome is a value, `then`, and not a
fact carried by the field's name.

### Overlap is ordered

Verse could instead require that the conditions of standing resolvers do not overlap. That is not
possible. Whether two predicates are disjoint is undecidable in general. So a refusal would have
to be cautious, and it would block pairs that are correct.

Ordering keeps exactly one answerer on each layer, and verse decides nothing at install. Ordering
also handles overlap that only appears at runtime, which a refusal at install could not see.

### Deadlines are checked by readers

The layer above is the only party that an expiry matters to, and that layer learns of it by
reading. A process that swept for expired questions would compute the same transfer slightly
earlier, for nobody.

### Answering is not a write

Suppose something in verse resolved a question because a verdict appeared on the record. Then
the command would be an entity again, dressed as a record. It would wait in line behind the
backlog of writes that it is meant to go before.

### Why a cover over a subject lives for the subject

A cover per firing would start, answer one question and end. That is the shape of one call per
question, and this design exists to avoid it. A cover per firing reaches the same shape from the
other end.
