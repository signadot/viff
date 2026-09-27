# The trigger model

One noun and two grammars carry the whole model. An entity is a node at an address — a path in
the tree, as long as the thing writing it makes it: the git source reflects a commit under its
repository (`git.acme.commit.9fe1`), verse keeps a task under `verse.task.instance`, and a package
keeps its findings under its own address (`verse-dev.drift.finding.abc`) — carrying a small
payload. A match names entities at an instant;
a formula is a match asked of the verse as it moves. Every trigger concept, `when` and `subject`
and flows, lands on one of those two, and a gate on neither: it asks somebody. Knowing which is
the thing to be unambiguous about.

The [tutorial](tutorial.md) installs a rule and drives it. This page is the model behind it,
and the pages listed below say what each field of a rule means.

The match vocabulary is [tony](https://signadot.github.io/tony-format/)'s rather than verse's. The
same operators serve matching, patching and diffing there, which is the argument made in
[Structured matching, patching and diffing done right](https://dev.to/scott_cotton_dc9ce3e7e632/structured-matching-patching-and-diffing-done-right-22ci).

## Where the rest is

This page is the model. The fields of a rule, and what else a charter author looks up, are in
the pages below.

| Page | What it holds |
|---|---|
| [The condition of a rule](reference/condition.md) | `subject`, `on` and `when`, and what a match can say |
| [The action of a rule](reference/action.md) | `patch`, `derive`, `spawn`, `calls` and `seq`, what an action reads, the answer contract, hooks, ids and progress facts |
| [The gates of a rule](reference/gates.md) | what a gate asks, who answers it, and how it settles |
| [The charter](reference/charter.md) | packages, `reads:`, `verse charter`, and how a charter is stored |
| [Schedules](reference/schedules.md) | time as a cause: schedules, ticks and the calendar |
| [Control](control.md#suspending) | what a suspension does to rules |

## Two layers: point-in-time matches, and matches asked over time

A *match* is a point-in-time tony selector over the entity-verse: which entities satisfy this
right now. There is no time in it.

A *formula* is a match asked of the verse as it moves: at two revisions for a cause, at one for a
condition. Nothing composes it — there is no `until`, no `quorum`, no `deadline` in a condition.
A formula's whole content is its match.

| concept | grammar | what it expresses |
|---|---|---|
| `subject` | match — point-in-time | the entities a firing is *about*, selected at the commit. Required |
| flow | match — point-in-time | which live tasks to steer, by their subject |
| `on` | formula — a match at two revisions | the **cause**: fire at the commit where this first holds, having not held before |
| `when` | formula — a match at one revision | the **condition**: this must hold at that commit for the firing to proceed |
| gate | neither — a question posed to somebody | proceed when whoever answers settles it: one yes, a threshold of them, or a clock's default |

The cut is simple. Name entities at an instant and you want a match; ask whether one holds as
the verse moves and you want a formula. `subject` and flow-addressing are point-in-time, since a
subject is selected at the commit a firing happens at and never accumulated, so they are plain
matches.

`on` and `when` are asked of the verse's evolution, so they are formulas. A gate is neither: it
asks somebody, and what it says is how the answer settles ([gates](reference/gates.md#gates--whether-it-proceeds)).
`on` and `when` are the same predicate asked at different moments — `on` at C-1 and C, firing on ¬P → P, and `when` at
C, gating — which is why they share a grammar and a type. Cause and condition, told apart by edge
and level.

## The trigger

A rule has four parts: a name, the condition that causes it, the gates that guard it, and the
action that fires. A fifth is optional: `stratum:`, the declared level it lives in, which
constrains what its subject may be and where a `derive` may land ([strata](strata.md)). Omitted,
it is unconstrained, which is what every rule here means.

```tony
- name: review
  # what it is about
  condition:
    subject: {path: "demo.pr.*", value: { stage: open }}
  # the guard
  gates:
  - { name: approval, mode: ask }
  # the consequence
  action:
    patch: { stage: reviewing }
    spawn: { driver: scripted, vector: auto, prompt: "echo reviewing $subject" }
    onDone:
      patch: { stage: reviewed }
```

```go
type Trigger struct {
    Name      string
    Condition Condition   // the cause: what it is about, and what fires it
    Gates     []Gate      // the guard — conjunctive: all must resolve to proceed
    Action    Action      // patch / spawn — declarative data, no Go closure
}

// Condition is the ECA "cause": what fires, what must hold, and what it is about.
type Condition struct {
    Subject Match     // REQUIRED, point-in-time — the entities this firing acts on
    On      *Formula  // optional — the CAUSE: a rising edge of a store predicate
    When    *Formula  // optional — the CONDITION: must hold at the same commit
}
```

## The rule

One sentence, and everything else in this document follows from it:

> At each commit **C**, for each trigger: if `on` is vacuous or **¬on(C-1) ∧ on(C)**, and
> `when` holds at **C**, fire once for **every entity `subject` selects at C**.

Three things are worth reading off it before anything else.

**A firing is about one entity.** N selected members are N firings, each with its own gate
question and its own spawn id. There is no firing "about a set".

**Everything is read at one snapshot.** The condition and the selection both come from the store as
it stood at C, so a firing cannot act on a member under a condition that had already stopped
holding. The edge is C-1 against C rather than "what the engine saw last time", which is what makes
a replayed commit — a catch-up, a restart — reach the same verdict as the live one.

**A vacuous `on` is the ordinary shape.** The rule above has no `on`, so it is considered at every
commit touching anything it reads. What ends it is the charter rather than the dispatcher. An action
whose write takes its subject out of the selection fires once and then selects nothing, and one that
writes the same value again produces no commit and so raises no next evaluation. Latch, or run at
every commit; a spawning action that cannot latch is refused at install.

A trigger is fully declarative, so a charter can be installed over the wire, persisted, or reflected
as an entity, and the engine interprets it directly. There is no compiled form.

## When a rule does nothing

A rule that did nothing looks the same from outside in four cases. Three are ordinary: it
selected nothing, its condition did not hold, or a gate denied it. The fourth is a fault, and
verse keeps it on the rule.

`verse charter` marks a rule whose firings have gone wrong with `trouble`. This is what it
prints, and `trouble` is verse's to write, not a field of a rule:

```
- name: copies
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {patch: {stage: seen, reviewer: $assignee}}
  trouble:
  - kind: failed
    chain: copies
    subject: 'demo.pr."1"'
    why: ...
    at: "2026-09-28T09:14:03Z"
    count: 3
```

There are two kinds.

| `kind` | What happened | What the entry says |
|---|---|---|
| `failed` | An action did not do what the rule says: a reference did not resolve, a patch or a derive failed, a spawn was refused, a gate's `pose` did not expand. | `chain`, the place in the rule (`copies`, `copies.onDone`, `copies.approval.onDenied`), and `subject` |
| `unread` | The rule could not read the verse at a commit. It did not evaluate that commit, and it does not look at it again. | `rev`, the commit |

Each entry is the latest of its kind. `count` is how many times it has happened since the rule
was installed or the daemon started, and `at` is the last time. Installing the rule again
clears it.

`verse status` counts both over every charter, as `firings: {failed, unread}`. The line is
absent when nothing has gone wrong.

A completion hook that fails is also written on its task's record, under `hook`.

## Flow — a subject-match over live tasks

A flow is not a stored key. It is a match evaluated over the subjects of *live* tasks:

```sh
verse flow -subject demo.pr.1       # tasks whose subject matches demo.pr.1
verse flow -subject 'demo.pr.*'     # …every pr
```

Relating tasks is what a match *does*, so "relational flows" are not a second primitive to add
later. They are broader subject-matches. `verse flow` lists the live tasks a selection names, and
a verb applies the same selection and acts on what it names. A query, not a maintained index.
Holding, suspending and cancelling that work is [control](control.md).

## Two rules, read out loud

```json
{ "name": "review",
  "condition": { "subject": { "path": "demo.pr.*", "value": { "stage": "open" } } },
  "gates":     [ { "name": "approval", "mode": "ask" } ],
  "action":    { "patch": { "stage": "reviewing" }, "spawn": { "driver": "scripted", "vector": "auto", "prompt": "review $subject" } } }
{ "name": "ship-green",
  "condition": { "on":      { "path": "ci.status.*", "value": { "state": "green" } },
                 "subject": { "path": "demo.pr.*", "value": { "stage": "reviewed" } } },
  "action":    { "patch": { "stage": "shipped" } } }
```

`review` reads as *while a PR is open, review it*. No cause is named, so it is considered at every
commit touching `demo.pr`, and its patch advances each PR past `open` so it settles. Its spawn is
allowed only *because* of that patch: a spawning action with nothing to latch on is refused.

`ship-green` reads as *at the moment CI goes green, ship everything reviewed*. It fires once,
at the edge, over the set as it then stood. Written with `when` instead of `on` it would say
*while* CI is green, and pick up a PR reviewed afterwards.

## The commit is the unit

A trigger's rule is a question about the store at a revision rather than about one entity, so it is
asked once per commit. A docd commit is atomic across many branches, and `LogdStore` reports it
whole: one `Commit`, carrying one change per entity it touched.

That framing has to come from the store, because it cannot be recovered downstream. Changes of
one commit share a revision, but a consumer reading them one at a time cannot tell whether the
next belongs to the same commit without waiting for the one after it — while the store, which
built them from a single delta, knows exactly.

What the engine does with the changes is decide whether asking is worth it. A trigger is skipped at
a commit that touched none of the subtrees it reads, and that skip is exact rather than a heuristic.
If nothing it reads changed then `on(C) = on(C-1)`, so there is no edge, and a vacuous rule would
evaluate the same predicates over the same state and write the same values, which produce no commit.
Either way it is indistinguishable from not running, which is what makes per-commit evaluation
affordable.

How the store builds a commit out of one delta — and why the per-entity grain sits inside the
transaction rather than instead of it — is in
[change events](change-events.md#one-commit-many-entities-the-entity-is-the-reactive-grain).

## Lifecycle: how an entity leaves

An entity does not simply disappear. The intent to remove it is written first, as
`verse.meta.vanishing` — the instant it was recorded — and the removal follows separately.

That order exists because of what a cause is. A trigger fires on the rising edge of a store
predicate: *something matching this now exists*. There is no way to say the negative — a match
asks what is there, so "this stopped existing" has no spelling. Writing the intent down turns
the negative into a positive, and a departure becomes an ordinary rising edge:

```tony
- name: note-departures
  condition:
    on:      {path: "demo.pr.*", value: { verse: { meta: { vanishing: null } } }}
    subject: audit.record.latest
  action:
    patch: { saw: "a departure" }
```

Match the marker's *presence*, where `vanishing: null` is tony's present-any-value idiom, rather
than its value. The value is a timestamp and may grow into a record — who asked, why, a grace
period — and a charter that matched on a particular value would have to be rewritten when it does.

Three things follow, and the first is the one to know:

**A vanishing entity is not a subject.** A rule acts on what it selects — patches it, spawns
about it, raises a gate on it — and doing any of that to something on its way out is work about
something that will not be there. So `subject: {path: "demo.pr.*"}` skips it, and naming
the marker is the opt-in that sees it again — which is how the reaper works.

A *named* subject (`demo.pr.1`) is skipped too, and this is the one place naming does not beat
selecting. You may name something that does not exist yet; you may not name something on its way
out. Otherwise a rule reacting to a departure acts on the departing entity, and since a patch over
an absent entity creates, a removal landing first means the rule resurrects it: back as an ordinary
live entity, stripped to whatever the rule wrote, with no marker on it. React to a departure by
writing somewhere else, to an audit record or an archive. The thing that is going is not a subject.

The raw read (`verse data`) still shows it, because making the intent visible is the whole point.
Only the rule-facing selection hides it.

**The removal itself is inert.** When the entity finally goes, a predicate over it falls from
true to false, and nothing fires on a falling edge. The reactive moment was the intent; the
removal is bookkeeping.

**The reaper is an ordinary trigger.** It selects what is leaving — by naming the marker — and
removes it. `!delete null` is the removal, at the root exactly as at a field:

```tony
- name: reap
  condition:
    subject: {path: "demo.pr.*", value: { verse: { meta: { vanishing: null } } }}
  action:
    patch: !delete null
```

A bare `patch: null` writes the payload `null`: an entity that stands there holding nothing, rather
than one that is gone. Removal is `!delete`, and it is the same operator at the root as at a field,
so `{tmp: !delete null}` removes a key and `patch: !delete null` removes the entity. One operator,
any depth.

That is what makes this a convention rather than a mechanism. Nothing in the engine knows how to
reap. A charter says when, and what grace period, and whether anything should happen first.

## Limits today

A condition does not compose over time. `on` and `when` are asked of the store at a revision,
so they have to be answerable at one: a match. The temporal grammar that once lived on gates
(`!until`, `!quorum`, `!fact`, `!deadline`) is refused by name wherever a charter is read; a
gate's threshold and deadline are the question's (`need:`, `deadline:`), and waiting for a
fact is the condition's own match. Time enters as a written fact instead: a
[schedule's tick](reference/schedules.md#schedules-time-as-a-cause), or the calendar derived from it.

A rule may declare the stratum it lives in, and then its subject must fall inside that stratum's
pattern and any `derive` must land strictly above it. Declaring nothing is a complete answer: a
charter with no `stratum:` anywhere is unconstrained. See [strata](strata.md).

A cycle across rules is caught at runtime rather than at install. Two triggers each writing what
the other selects on defeat every install-time protection here, and
[the cycle throttle](cycle-throttle.md) bounds them by causal depth instead. Everything else about
whether a charter settles is in [convergence](convergence.md), including the one rules-level
ambiguity that is warned about rather than refused.

A guard about **nothing existing** has no spelling. A match asks what is there, so "an entity exists
whose payload is not X" is sayable with `!not`, and "no entity matching M exists" — *ship when there
is no open blocker* — is not. The model is existence-positive throughout, which is why
[a departure is written down](#lifecycle-how-an-entity-leaves) rather than inferred. The same
discipline answers a `when` about absence, by making the absence a positive fact somebody writes: a
count, or a summary entity. That is a discipline rather than a grammar.

The substrate all of this leans on is [change events](change-events.md). Whether a charter written
on it settles, and what per-commit evaluation costs, is [convergence](convergence.md).
