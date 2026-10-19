# Charter reference

A [charter](glossary.md#charter) is a list of [rules](glossary.md#rule). A rule has a
condition, a list of gates, and an action. When the condition holds and every gate passes, the
action fires.

This page is the model of a rule: its parts, how it fires, and the two grammars it is written
in. To write a rule, start with [the tutorial's charters pages](tutorial/charters.md). They
install rules and fire them. This page is the model behind them, and
[where the rest is](#where-the-rest-is) lists a page for each part.

A rule is about entities. An [entity](glossary.md#entity) is a [node](glossary.md#node) at an
[address](glossary.md#address), with a small [payload](glossary.md#payload). An address is a
path in the tree of the [store](glossary.md#store). Its writer decides how long it is.

| Who writes it | Address |
|---|---|
| the git [iod](glossary.md#iod), for a commit in a repository | `git.acme.commit.9fe1` |
| verse, for a task | under `verse.task.instance` |
| a [package](glossary.md#package), for a finding of its own | `repos.verse.drift.finding.abc` |

An iod works in both directions. It writes what a system holds into the store, and a rule
reacts to it. When a rule writes to the iod's out place, the iod carries that write back to
the system: a comment, a label, a merge ([iods](io.md)).

## Where the rest is

These pages hold the parts of a rule, and the other things a charter author looks up.

| Page | What it holds |
|---|---|
| [The condition of a rule](reference/condition.md) | `subject`, `on` and `when`, and what a match can say |
| [The action of a rule](reference/action.md) | `patch`, `derive`, `spawn` and `seq`, what an action reads, the answer contract, hooks, ids and progress facts |
| [The gates of a rule](reference/gates.md) | what a gate asks, who answers it, and how it settles |
| [The charter](reference/charter.md) | packages, `reads:`, `verse charter`, how a charter is stored, and what an install does not check |
| [Schedules](reference/schedules.md) | time as a cause: schedules, ticks and the calendar |
| [Entities](reference/entities.md) | addresses, payloads, merging writes, conditional writes, selection and watching |
| [Strata](strata.md) | a declared order of levels, and `derive` |
| [Convergence](convergence.md) | what ends a firing, and what is checked at install |
| [Permissions](perms.md) | one owner per node, and grants for everybody else; [the detail](reference/perms.md) |
| [Control](control.md#suspending) | what a suspension does to rules |
| [An automatic supervisor](auto-supervisor.md) | a worked charter where an agent decides, and passes up what is not its to decide |
| [Representation](reference/representation.md) | tony, JSON and YAML: what verse reads and what it writes |

The commands, and everything else, are in [the rest of the reference](reference/README.md).

## The trigger

A rule has four parts.

| Part | What it is |
|---|---|
| the name | what the rule is called |
| the condition | what the rule is about, and what causes it |
| the gates | what must pass before the action |
| the action | what fires |

A fifth part is optional: `stratum:`. It names the declared level the rule lives in. A
[stratum](glossary.md#stratum) limits what the rule's subject may be, and where a `derive` may
land ([strata](strata.md)). A rule with no `stratum:` has no such limit. No rule on this page
has one.

A rule in a charter:

```tony
- name: review
  # what it is about
  condition:
    subject:
      path: "demo.pr.*"
      value:
        stage: open
  # what must pass first
  gates:
  - name: approval
    mode: ask
  # what fires
  action:
    patch:
      stage: reviewing
    spawn:
      doer: {script: 'echo reviewing "$SUBJECT"', with: {network: {mode: open}, env: {SUBJECT: $subject}}}
    onDone:
      patch:
        stage: reviewed
```

Every gate must pass. A rule with no gates fires without asking anybody.

A rule is data, with no code in it. So a charter can be installed over the wire, stored, and
reflected as an entity. The engine reads the rule directly; there is no compiled form. The Go
types are in [trigger.go](../trigger/trigger.go).

## The rule

One sentence says how a rule fires. Everything else on this page follows from it.

> At each commit **C**, for each rule: if `on` is vacuous or **¬on(C-1) ∧ on(C)**, and
> `when` holds at **C**, fire once for **every entity `subject` selects at C**.

C-1 is the commit before C. An `on` is vacuous when the rule has no `on`.

Three things follow.

**A [firing](glossary.md#firing) is about one entity.** A [subject](glossary.md#subject) that
selects N entities gives N firings. Each firing has its own gate question and its own spawn id.
No firing is about a set.

**Verse reads everything at one snapshot.** The condition and the selection both come from the
store as it stood at C. So a firing never acts under a condition that stopped holding before
C. The edge compares C-1 with C, not with what the engine saw last. So a commit that verse
reads again, after a restart or while catching up, gives the same answer as the first time.

**A vacuous `on` is the usual shape.** The rule `review` above has no `on`. So verse
considers it at every commit that touches anything the rule reads. The charter ends the
firing, not the engine. An action ends in one of two ways:

- Its write moves the subject out of the selection. The action fires once, and then the rule
  selects nothing.
- It writes the same value again. That write makes no commit, so nothing is evaluated next.

A rule has a [latch](glossary.md#latch), or it runs at every commit. Verse refuses at install
a spawning action that cannot latch.

## Two rules, read out loud

```tony
- name: review
  condition:
    subject:
      path: "demo.pr.*"
      value:
        stage: open
  gates:
  - name: approval
    mode: ask
  action:
    patch:
      stage: reviewing
    spawn:
      doer:
        agent: verse
        with:
          network: {mode: open}
      prompt: "review $subject"
      policy:
      - then: allow
- name: ship-green
  condition:
    on:
      path: "ci.status.*"
      value:
        state: green
    subject:
      path: "demo.pr.*"
      value:
        stage: reviewed
  action:
    patch:
      stage: shipped
```

`review` reads: *while a PR is open, review it*. Its patch moves each PR past `open`, so the
rule settles.

`ship-green` reads: *at the moment CI goes green, ship everything reviewed*. It fires once, at
the edge, over the PRs as they stood at that commit. With `when` in place of `on`, it says
*while CI is green*, and it also ships a PR that is reviewed later.

## Two layers: point-in-time matches, and matches asked over time

The model has one noun and two grammars. The noun is the entity. The grammars are the match
and the formula.

A *[match](glossary.md#match)* is a tony selector over the store at one moment. It says which
entities satisfy it now. A match holds no time.

A *formula* is a match that verse asks of the store as the store changes. For a cause, verse
asks it at two revisions. For a condition, it asks at one. A formula is its match and nothing
more.

| Concept | Grammar | What it says |
|---|---|---|
| `subject` | match, at one moment | the entities a firing is *about*, selected at the commit. Required |
| flow | match, at one moment | which live tasks to steer, by their subject |
| `on` | formula, a match at two revisions | the **cause**: fire at the commit where this first holds, having not held before |
| `when` | formula, a match at one revision | the **condition**: this must hold at that commit for the firing to go on |
| gate | neither: a question put to somebody | go on when the answers settle it: one yes, a threshold of them, or a clock's default |

`on` and `when` are the same kind of predicate, asked at different moments. So they share a
grammar and a type ([the condition of a rule](reference/condition.md#on-and-when-the-cause-and-the-condition)).

A [gate](glossary.md#gate-of-a-rule) is neither grammar. A gate asks somebody, and it says how
the answer settles ([gates](reference/gates.md#gates-whether-it-proceeds)).

The words of a match are [tony](https://signadot.github.io/tony-format/)'s, not verse's own.
In tony, the same operators serve matching, patching and diffing.
[Structured matching, patching and diffing done right](https://dev.to/scott_cotton_dc9ce3e7e632/structured-matching-patching-and-diffing-done-right-22ci)
argues for that design.

## The commit is the unit

The sentence in [the rule](#the-rule) is a question about the whole store at one
[revision](glossary.md#revision), not about one entity. So verse asks it once per
[commit](glossary.md#commit). The store reports each commit whole, with one change for each
entity it touched.

Verse skips a rule at a commit that touched nothing the rule reads. The skip is exact, not a
guess. If nothing the rule reads changed, `on` has no edge. A rule with a vacuous `on` would
read the same state and write the same values, and those writes make no commit. Either way,
skipping gives the same result as running. That is why evaluating at every commit is
affordable.

[Change events](change-events.md#one-commit-many-entities-the-entity-is-the-reactive-grain),
in the repository, says how the store builds a commit and why only the store can report it
whole.

## When a rule does nothing

A rule does nothing in four cases. From outside, the four look the same.

| Case | What it is |
|---|---|
| the rule selected nothing | ordinary |
| its condition did not hold | ordinary |
| a gate denied it | ordinary |
| a firing went wrong | a fault. Verse keeps it on the rule. |

`verse charter` marks a rule whose firings went wrong with `trouble`. Verse writes `trouble`;
it is not a field you write. `verse charter` prints this:

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
| `unread` | The rule could not read the store at a commit. It did not evaluate that commit, and it does not go back to it. | `rev`, the commit |

Each entry is the latest of its kind. `count` is how many times it happened since the rule was
installed or verse started. `at` is the last time. Installing the rule again clears the entry.

`verse status` counts both kinds over every charter, as `firings: {failed, unread}`. The line
is absent when nothing went wrong.

A completion hook that fails is written on the record of its task, under `hook`.

A spawn that starts and then fails is not the rule's trouble. It is a task that failed, such
as a spawn that names a runner that is not installed. The task's record says so
(`verse task`), and the rule's `onFailed` hook fires. A spawn whose doer names an agent the
runner does not offer is refused at install.

## Flow: a subject-match over live tasks

A [flow](glossary.md#flow) is not a stored key. It is a match that verse evaluates over the
subjects of *live* tasks:

```sh
verse flow -subject demo.pr.1       # tasks whose subject matches demo.pr.1
verse flow -subject 'demo.pr.*'     # …every pr
```

A match is how verse relates tasks. So a "relational flow" is not a second idea: it is a wider
match on the subject.

`verse flow` lists the live tasks a selection names. A verb takes the same selection and acts
on what it names. A flow is a query, and verse keeps no index for it.
[Control](control.md) says how to hold, suspend and cancel that work.

## Lifecycle: how an entity leaves

An entity does not just disappear. First somebody writes the intent to remove it, as
`verse.meta.vanishing`. Its value is the instant the intent was written. The removal comes
later.

The order follows from what a cause is. A rule fires on the rising edge of a store predicate:
*something matching this now exists*. A match asks what is there, so "this stopped existing"
cannot be said. The written intent turns the negative into a positive, and a departure becomes
an ordinary rising edge:

```tony
- name: note-departures
  condition:
    on:
      path: "demo.pr.*"
      value:
        verse:
          meta:
            vanishing: null
    subject: audit.record.latest
  action:
    patch:
      saw: "a departure"
```

Match the *presence* of the marker, not its value. `vanishing: null` is how tony writes
"present, with any value". The value is a timestamp today, and it may become a record: who
asked, why, a grace period. A charter that matches one value would have to change with it.

Three things follow. The first matters most.

**A vanishing entity is not a subject.** A rule acts on what it selects: it patches it, spawns
about it, raises a gate on it. Work about an entity that is leaving is work about something
that will not be there. So `subject: {path: "demo.pr.*"}` skips a vanishing entity. A rule
that names the marker opts in, and sees the entity again. That is how the reaper below works.

A *named* subject (`demo.pr.1`) is skipped too. You may name an entity that does not exist
yet. You may not name one that is leaving.

Without this, a rule that reacts to a departure would act on the departing entity. A patch
over an absent entity creates it. So if the removal landed first, the rule would create the
entity again, as an ordinary entity holding only what the rule wrote, with no marker. To react
to a departure, write somewhere else: an audit record, or an archive.

`verse data` still shows a vanishing entity, because the marker is there to make the intent
visible. Only a rule's selection hides it.

**The removal itself fires nothing.** When the entity goes, a predicate over it falls from
true to false, and nothing fires on a falling edge. The intent was the moment to react.

**The reaper is an ordinary rule.** It names the marker, so it selects what is leaving, and it
removes it. `!delete null` is the removal, at the root as at a field:

```tony
- name: reap
  condition:
    subject:
      path: "demo.pr.*"
      value:
        verse:
          meta:
            vanishing: null
  action:
    patch: !delete null
```

A bare `patch: null` writes the payload `null`. The entity then stands at its address and
holds nothing. It is not gone.

| Patch | What it removes |
|---|---|
| `{tmp: !delete null}` | a key |
| `patch: !delete null` | the entity |

So reaping is a convention, not a mechanism. Nothing in the engine knows how to reap. A
charter says when to reap, how long to wait, and what happens first.

## Limits today

**A condition does not combine moments.** Verse asks `on` and `when` of the store at a
revision, so each must be answerable at one revision. Each is a match. Verse refuses the
operators `!until`, `!quorum`, `!fact` and `!deadline` by name wherever it reads a charter.

| What you want to say | Where you write it |
|---|---|
| a threshold | on a gate's question, as `need:` |
| a deadline | on a gate's question, as `deadline:` |
| to wait for a fact | in the condition's match |
| a time | as a written fact: a [schedule's tick](reference/schedules.md#schedules-time-as-a-cause), or the calendar derived from it |

**"Nothing like this exists" has no spelling.** A match asks what is there.

| Statement | Can a match say it? |
|---|---|
| "an entity exists whose payload is not X" | yes, with `!not` |
| "no entity matching M exists", such as *ship when there is no open blocker* | no |

The model only says what exists. That is why
[a departure is written down](#lifecycle-how-an-entity-leaves) rather than inferred. A `when`
about absence works the same way: somebody writes the absence as a positive fact, such as a
count or a summary entity. That is the author's practice, not part of the grammar.

All of this rests on [change events](change-events.md). [Convergence](convergence.md) says
whether a charter written on change events settles, and what evaluating at every commit costs.
