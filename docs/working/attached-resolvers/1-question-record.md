# The question record

Status: proposal, nothing built. This is the shape of the thing
[0-prerequisites](0-prerequisites.md) calls "a question published as a record", worked out far
enough to build — it details that step rather than reaching past it. The design it serves is
[attached resolvers](../attached-resolvers.md).

**The claim.** A question is an entity. Not a field on the process that raised it, not a row in
a registry, and not a projection of either: `gate:question:<id>`, written when the question is
posed and closed when it is answered. Everything the rest of the design wants — routing by
predicate, a durable docket, an evidence trail, parity between process gates and trigger gates —
is a consequence of that one move. The alternatives fail on the last of those four and take the
second with it, for the same reason: half the questions have no process to be a property of.

## Why an entity of its own

The prerequisites say to publish the question "onto the entity", meaning the
`process:instance` that raised it. That reads as sufficient until you have to name the entity a
particular record goes on, and twice there is none.

A **trigger gate** is posed by a firing, before the action applies. There is no
`process:instance` yet, and if the gate is denied there never will be — the `Spawns` field is
ids-to-be. `mirror.Reflect` is also the wrong writer for it by construction, since it subscribes
to a *process's* read plane and a firing has none.

A **`call_agent` child** has no entity either. `NewChild` builds a real `Process` with its own
supervisor, so it raises its own questions, but nothing mints a `process:instance` for it —
`mirror` explicitly does not project `child` events (index, not warehouse). Its questions are
posed to the parent's resolver and would have nowhere to be recorded.

Both failures are the same shape, which is what makes it structural rather than a pair of
awkward cases: the record needs somewhere to live, and *whose* it is turns out not to answer
that. (The tempting third argument — that one run holds several open questions at once — does
not hold. A parent is suspended inside the `call_agent` frame while its child runs, its own gate
having been answered before the child started.)

So the record lives on its own, keyed by the question id that already exists
(`GateQuestion.ID`, `<origin>~d<n>`):

```
gate:question:review@demo:pr:1~d3
```

`gate` joins `trigger:*` and `process:*` as the engine's own machinery — out of the default
match set, so a rule that wants questions names them, and the loop cannot ignite on its own
docket.

The process entity keeps a pointer rather than a copy. `pending` stops being a bare id and
becomes the ref of the open question; `pendingGate` goes, because the gate name is a field of
the record now. Both stay strings, so the shape of the process entity does not change — only
what `pending` denotes, which the [contract notice](../contributing.md#coordinating-a-break)
already filed has to be corrected to say.

## What it carries

```tony
id:        review@demo:pr:1~d3
origin:    process:instance:review@demo:pr:1      # or trigger:rule:review, for a firing
subject:   demo:pr:1
gate:      deploy
mode:      ask
tool:      run_bash
effects:                                          # the derived classification, not the raw call
  verbs:     [exec]
  resources: [{ kind: process, ref: "…" }]
  paths:     ["infra/**"]
spawns:    "review@demo:pr:2"                     # ids-to-be, for a gate raised before a spawn
resolver:  resolvers:review:docs                  # who it is addressed to — always populated
posedAt:   2026-08-01T09:12:44Z
escalatesAt: 2026-08-01T13:12:44Z                 # absolute, so a restart cannot slide it
status:    pending                                # pending | answered | stale
```

Three of those repay explanation.

**`effects`, not the tool call.** A resolver decides on the derived classification — verbs and
typed resources — because that is what travels across systems and what a grant predicates on.
The raw `run_bash` string is in the journal for anyone who wants it; putting it here would make
the record the transcript.

**`resolver` is always populated**, and needs no cleverness to be: every resolver is an
entity, so a question addressed to one carries its ref, and a question nobody is bound to
carries none. Empty means unbound rather than unknown, which is a distinction a consumer can
act on.

**`escalatesAt` is an instant, and the binding declares a duration.** A resolver says
`escalatesAfter: 4h`, because a binding cannot know when a question will be posed; the record
stamps `posedAt + escalatesAfter` when it is. The goal record already keeps absolute deadlines
for the same reason — a question that outlives a daemon must not have its clock slide on the way
back up.

## The answer lands on it, afterwards

A verdict reaches the process through the control plane — that is
[the note's first rule](../attached-resolvers.md#answering-is-a-verb-not-a-write), and nothing
about a record changes it. What lands here is the *result*, written from the mirrored `decision`:

```tony
status:   answered
verdict:  proceed
by:       resolvers:review:docs
why:      "docs are inside the remit"
answeredAt: 2026-08-01T09:13:02Z
```

Written after the fact and read by nobody as an instruction. The moment something resolves a
gate *because* a verdict appeared here, the command entity is back and the steering invariant is
broken.

Answering is CAS-once on `status`, which makes the race an outcome rather than an error: the
loser's answer lands as a `stale` record beside the applied one, carrying its own `by`. That is
the behaviour the runner contract already specifies and the local docket does not have.

## What it makes true

- **The docket survives a restart.** `verse gates` lists a slice instead of
  `Registry.Pending()`, so a question whose work outlived the daemon is still there — which is
  what `process control` demands and the in-memory docket cannot do.
- **A rule can route on it.** `{system: gate, kind: question, value: {resolver: …, verbs: […]}}`
  selects the questions addressed to a named resolver, which is how a charter becomes a
  resolver's body.
- **Process gates and trigger gates are one thing.** Both write records; only `origin` differs.
  The parity the design note claims stops being an assertion.
- **The evidence trail is a fact rather than a log line.** Who answered, when, why, and what
  they were answering, at a ref anybody can read.

## What it costs, and the corner to watch

A question is now a write, and an answered question is a second one. Gates are rare next to
actions — the vector decides what asks at all — so the volume is bounded by the ask-set rather
than by the run, and a process whose vector is `auto` writes nothing here. The corner is a
charter that gates a chatty tool: that is a charter choosing it, and it is visible in the same
place it was chosen.

Retention is the open one. A pending question is small and finite; an answered one is history,
and nothing here reaps it. The lifecycle machinery exists (`verse.meta.vanishing` and the
reaper), and pointing it at settled questions is a decision this note does not make.

## What this does not do

No resolver entity, no layering, no sponsorship. The record is the
thing all of those address; none of them is needed to build it, and a charter that merely
*reads* questions is useful on its own — a rule that notices a question standing unanswered for
an hour needs nothing else in the design.
