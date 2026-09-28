# The trigger model

A [rule](glossary.md#rule) is one entry of a [charter](glossary.md#charter). A rule has a
condition, a list of gates and an action. When the condition holds and every gate passes, the
action fires.

This page is the model of a rule: its parts, how it fires, and the two grammars that it is
written in. The [tutorial](tutorial.md) installs a rule and drives it, and this page is the
model behind the tutorial. [Where the rest is](#where-the-rest-is) lists the pages that say
what each field of a rule means.

A rule is about entities. An [entity](glossary.md#entity) is a [node](glossary.md#node) at an
[address](glossary.md#address), and it carries a small [payload](glossary.md#payload). An
address is a path in the tree of the [store](glossary.md#store). An address is as long as its
writer makes it.

| What is written | Address |
|---|---|
| the git [io](glossary.md#io) reflects a commit under its repository | `git.acme.commit.9fe1` |
| verse keeps a task | under `verse.task.instance` |
| a [package](glossary.md#package) keeps its findings under its own address | `verse-dev.drift.finding.abc` |

## Where the rest is

This page is the model. These pages hold the fields of a rule, and the other things that a
charter author looks up.

| Page | What it holds |
|---|---|
| [The condition of a rule](reference/condition.md) | `subject`, `on` and `when`, and what a match can say |
| [The action of a rule](reference/action.md) | `patch`, `derive`, `spawn`, `calls` and `seq`, what an action reads, the answer contract, hooks, ids and progress facts |
| [The gates of a rule](reference/gates.md) | what a gate asks, who answers it, and how it settles |
| [The charter](reference/charter.md) | packages, `reads:`, `verse charter`, and how a charter is stored |
| [Schedules](reference/schedules.md) | time as a cause: schedules, ticks and the calendar |
| [Control](control.md#suspending) | what a suspension does to rules |

## The trigger

A rule has four parts.

| Part | What it is |
|---|---|
| the name | what the rule is called |
| the condition | what the rule is about, and what causes it |
| the gates | what guards the action |
| the action | what fires |

A fifth part is optional: `stratum:`. It names the declared level that the rule lives in. A
[stratum](glossary.md#stratum) constrains what the subject of the rule may be, and where a
`derive` may land ([strata](strata.md)). A rule with no `stratum:` is unconstrained. Every
rule on this page has none.

A rule in a charter:

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

The same parts as Go types:

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

One sentence says how a rule fires. Everything else on this page follows from it.

> At each commit **C**, for each rule: if `on` is vacuous or **¬on(C-1) ∧ on(C)**, and
> `when` holds at **C**, fire once for **every entity `subject` selects at C**.

C-1 is the commit before C. An `on` is vacuous when the rule has no `on`.

Three things follow from the sentence.

**A [firing](glossary.md#firing) is about one entity.** A
[subject](glossary.md#subject) that selects N entities gives N firings. Each firing has its
own gate question and its own spawn id. No firing is about a set.

**Verse reads everything at one snapshot.** The condition and the selection both come from the
store as it stood at C. So a firing cannot act on an entity under a condition that stopped
holding before C. The edge compares C-1 with C. It does not compare C with what the engine saw
last. So a replayed commit reaches the same verdict as the live commit. Verse replays a commit
at a catch-up and at a restart.

**A vacuous `on` is the ordinary shape.** The rule `review` in [The trigger](#the-trigger) has
no `on`, so verse considers it at every commit that touches anything the rule reads. The
charter ends the firing, and the dispatcher does not. An action ends in one of two ways:

- Its write takes its subject out of the selection. The action fires once, and after that the
  rule selects nothing.
- It writes the same value again. That write produces no commit, so no next evaluation
  follows.

A rule has a [latch](glossary.md#latch), or it runs at every commit. Verse refuses at install
a spawning action that cannot latch.

A rule is fully declarative. So a charter can be installed over the wire, persisted, or
reflected as an entity. The engine interprets the rule directly. There is no compiled form.

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

`review` reads as *while a PR is open, review it*. The rule names no cause, so verse considers
it at every commit that touches `demo.pr`. Its patch moves each PR past `open`, so the rule
settles. Verse allows the spawn only *because* of that patch: it refuses a spawning action
that has nothing to latch on.

`ship-green` reads as *at the moment CI goes green, ship everything reviewed*. It fires once,
at the edge, over the set as it stood at that commit. With `when` in place of `on`, the rule
says *while* CI is green, and it also acts on a PR that is reviewed later.

## Two layers: point-in-time matches, and matches asked over time

The model has one noun and two grammars. The noun is the entity. The grammars are the match
and the formula.

A *[match](glossary.md#match)* is a tony selector over the store at one point in time. It says
which entities satisfy it now. A match holds no time.

A *formula* is a match that verse asks of the store as the store changes. For a cause, verse
asks it at two revisions. For a condition, verse asks it at one. Nothing composes a formula: a
condition has no `until`, no `quorum` and no `deadline`. The whole content of a formula is its
match.

| concept | grammar | what it expresses |
|---|---|---|
| `subject` | match, point-in-time | the entities a firing is *about*, selected at the commit. Required |
| flow | match, point-in-time | which live tasks to steer, by their subject |
| `on` | formula, a match at two revisions | the **cause**: fire at the commit where this first holds, having not held before |
| `when` | formula, a match at one revision | the **condition**: this must hold at that commit for the firing to proceed |
| gate | neither. It is a question posed to somebody. | proceed when whoever answers settles it: one yes, a threshold of them, or a clock's default |

Every concept of a rule is one of the two grammars: `when`, `subject` and flows. A
[gate](glossary.md#gate-of-a-rule) is neither, because it asks somebody. For each concept,
know which of the two it is.

The choice is simple. To name entities at an instant, write a match. To ask whether a match
holds as the store changes, write a formula.

`subject` and the addressing of a flow are point-in-time, so they are plain matches. Verse
selects a subject at the commit of the firing, and never accumulates it.

`on` and `when` are asked of the store as it changes, so they are formulas. They are the same
predicate asked at different moments. For this reason they share a grammar and a type.

| Field | Asked at | What it does |
|---|---|---|
| `on` | C-1 and C | fires on ¬P → P. It is the cause, and it is an edge. |
| `when` | C | gates. It is the condition, and it is a level. |

A gate is neither grammar. A gate asks somebody, and what the gate says is how the answer
settles ([gates](reference/gates.md#gates--whether-it-proceeds)).

The vocabulary of a match is that of [tony](https://signadot.github.io/tony-format/), and not
verse's own. In tony, the same operators serve matching, patching and diffing.
[Structured matching, patching and diffing done right](https://dev.to/scott_cotton_dc9ce3e7e632/structured-matching-patching-and-diffing-done-right-22ci)
argues for that design.

## The commit is the unit

The sentence in [The rule](#the-rule) is a question about the store at a
[revision](glossary.md#revision). It is not a question about one entity. So verse asks it once
for each [commit](glossary.md#commit).

A docd commit is atomic across many branches. `LogdStore` reports the commit whole: one
`Commit`, which carries one change for each entity that the commit touched.

The engine uses the changes to decide whether to ask. It skips a rule at a commit that touched
none of the subtrees that the rule reads. The skip is exact, and it is not a heuristic.

The skip is exact for this reason. If nothing that the rule reads changed, then
`on(C) = on(C-1)`, so there is no edge. A rule with a vacuous `on` would evaluate the same
predicates over the same state and write the same values, and those writes produce no commit.
In both cases the result is the same as not running. This is why evaluation at every commit is
affordable.

The store must give this framing, because a consumer cannot recover it. The changes of one
commit share a revision. A consumer that reads them one at a time cannot tell whether the next
change belongs to the same commit, unless it waits for the change after that one. The store
built the changes from a single delta, so it knows exactly.

[Change events](change-events.md#one-commit-many-entities-the-entity-is-the-reactive-grain)
says how the store builds a commit from one delta. It also says why the per-entity grain is
inside the transaction and does not replace it.

## When a rule does nothing

A rule does nothing in four cases, and from outside the four look the same.

| Case | What it is |
|---|---|
| the rule selected nothing | ordinary |
| its condition did not hold | ordinary |
| a gate denied it | ordinary |
| a firing went wrong | a fault. Verse keeps it on the rule. |

`verse charter` marks a rule whose firings went wrong with `trouble`. Verse writes `trouble`.
It is not a field of a rule. `verse charter` prints this:

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
| `failed` | An action did not do what the rule says: a reference did not resolve, a patch or a derive failed, no doer was eligible for a spawn, a gate's `pose` did not expand. | `chain`, the place in the rule (`copies`, `copies.onDone`, `copies.approval.onDenied`), and `subject` |
| `unread` | The rule could not read the store at a commit. It did not evaluate that commit, and it does not look at it again. | `rev`, the commit |

Each entry is the latest of its kind. `count` is the number of times it happened since the
rule was installed or the daemon started. `at` is the last time. Installing the rule again
clears the entry.

`verse status` counts both kinds over every charter, as `firings: {failed, unread}`. The line
is absent when nothing went wrong.

Verse also writes a completion hook that fails on the record of its task, under `hook`.

A spawn that starts and then fails is not the trouble of the rule. It is a task that failed.
Two examples are a spawn that names a runner that is not installed, and a spawn that names a
driver that the runtime does not have. The record of the task says so (`verse task`), and the
`onFailed` hook of the rule fires.

## Flow — a subject-match over live tasks

A [flow](glossary.md#flow) is not a stored key. It is a match that verse evaluates over the
subjects of *live* tasks:

```sh
verse flow -subject demo.pr.1       # tasks whose subject matches demo.pr.1
verse flow -subject 'demo.pr.*'     # …every pr
```

A match relates tasks, because that is what a match *does*. So a "relational flow" is not a
second primitive. It is a broader match on the subject.

`verse flow` lists the live tasks that a selection names. A verb applies the same selection
and acts on what the selection names. A flow is a query, and verse maintains no index for it.

[Control](control.md) describes how to hold, suspend and cancel that work.

## Lifecycle: how an entity leaves

An entity does not simply disappear. Somebody first writes the intent to remove it, as
`verse.meta.vanishing`. The value is the instant at which the intent was recorded. The removal
follows separately.

The order follows from what a cause is. A rule fires on the rising edge of a store predicate:
*something matching this now exists*. A rule cannot say the negative. A match asks what is
there, so "this stopped existing" has no spelling. The written intent turns the negative into
a positive, and a departure becomes an ordinary rising edge:

```tony
- name: note-departures
  condition:
    on:      {path: "demo.pr.*", value: { verse: { meta: { vanishing: null } } }}
    subject: audit.record.latest
  action:
    patch: { saw: "a departure" }
```

Match the *presence* of the marker, and not its value. `vanishing: null` is how tony writes
"present, with any value". The value is a timestamp, and it may become a record: who asked,
why, a grace period. A charter that matches one particular value must be rewritten when the
value changes shape.

Three things follow. The first is the most important.

**A vanishing entity is not a subject.** A rule acts on what it selects: it patches it, spawns
about it, raises a gate on it. Work on an entity that is leaving is work about something that
will not be there. So `subject: {path: "demo.pr.*"}` skips a vanishing entity. A rule that
names the marker opts in, and sees the entity again. The reaper works in this way.

A *named* subject (`demo.pr.1`) is skipped too. This is the one case where naming does not
take precedence over selecting. You may name an entity that does not exist yet. You may not
name an entity that is leaving.

Without this, a rule that reacts to a departure acts on the departing entity. A patch over an
absent entity creates it. So if the removal lands first, the rule creates the entity again. It
comes back as an ordinary live entity, holding only what the rule wrote, with no marker on it.

To react to a departure, write somewhere else: to an audit record or to an archive. The entity
that is leaving is not a subject.

The raw read (`verse data`) still shows a vanishing entity, because the purpose of the marker
is to make the intent visible. Only the selection of a rule hides it.

**The removal itself is inert.** When the entity goes, a predicate over it falls from true to
false, and nothing fires on a falling edge. The intent was the moment to react. The removal is
bookkeeping.

**The reaper is an ordinary rule.** It names the marker, so it selects what is leaving, and it
removes it. `!delete null` is the removal. It is the same at the root as at a field:

```tony
- name: reap
  condition:
    subject: {path: "demo.pr.*", value: { verse: { meta: { vanishing: null } } }}
  action:
    patch: !delete null
```

A bare `patch: null` writes the payload `null`. The entity then stands at its address and
holds nothing. It is not gone. Removal is `!delete`, and it is one operator at any depth.

| Patch | What it removes |
|---|---|
| `{tmp: !delete null}` | a key |
| `patch: !delete null` | the entity |

Reaping is therefore a convention, and not a mechanism. Nothing in the engine knows how to
reap. A charter says when to reap, what the grace period is, and whether anything happens
first.

## Limits today

**A condition does not compose over time.** Verse asks `on` and `when` of the store at a
revision, so each must be answerable at one revision. Each is a match. Verse refuses the
temporal operators `!until`, `!quorum`, `!fact` and `!deadline` by name wherever it reads a
charter.

| What you want to say | Where you write it |
|---|---|
| a threshold | on the question of a gate, as `need:` |
| a deadline | on the question of a gate, as `deadline:` |
| to wait for a fact | in the match of the condition |
| a time | as a written fact: a [schedule's tick](reference/schedules.md#schedules-time-as-a-cause), or the calendar derived from it |

**A rule may declare the stratum that it lives in.** Then its subject must lie inside the
pattern of that stratum, and any `derive` must land strictly above it. To declare nothing is a
complete answer: a charter with no `stratum:` anywhere is unconstrained. See
[strata](strata.md).

**A guard about nothing existing has no spelling.** A match asks what is there.

| Statement | Can a match say it? |
|---|---|
| "an entity exists whose payload is not X" | yes, with `!not` |
| "no entity matching M exists", such as *ship when there is no open blocker* | no |

The model is existence-positive throughout. For this reason
[a departure is written down](#lifecycle-how-an-entity-leaves), and not inferred. A `when`
about absence follows the same practice: somebody writes the absence as a positive fact, such
as a count or a summary entity. This is a practice of the author, and not a grammar.

All of this depends on [change events](change-events.md). [Convergence](convergence.md) says
whether a charter written on change events settles, and what evaluation at every commit costs.
