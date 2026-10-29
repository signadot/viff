# Charter reference

A [charter](glossary.md#charter) is a set of [rules](glossary.md#rule), with what the rules
need: the levels they live in, the schedules they read, and the places they read and write. A rule
has a condition, a list of gates, and an action. When the condition holds and every gate passes, the
action fires.

This page is the model of a rule: its parts, how it fires, and the two grammars it is written
in. To write a rule, start with [the tutorial's charters pages](tutorial/charters.md). They
install rules and fire them. This page is the model behind them, and
[where the rest is](#where-the-rest-is) lists a page for each part.

A rule is about [data](glossary.md#data). Each piece of data is a [node](glossary.md#node) at
an [address](glossary.md#address), with a small [payload](glossary.md#payload). An address is a
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
| [The charter](reference/charter.md) | packages, `reads:`, `sends:`, `ttl:`, a charter a task installs, `verse charter`, how a charter is stored, and what an install does not check |
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

The examples on this page keep a team's pull requests as records at `demo.pr.<n>`, each with a
`stage`, the repository `repo` (such as `acme/app`) and the branch `head`. A rule in their
charter has an agent review each open pull request, once somebody says yes:

```tony
- name: review
  # what it is about: every open pull request
  condition:
    subject:
      path: "demo.pr.*"
      value:
        stage: open
  # what must pass first: somebody says yes
  gates:
  - name: approval
    mode: ask
  # what fires: the record moves on, and an agent reviews the branch
  action:
    patch:
      stage: reviewing
    spawn:
      agent: {name: verse}
      prompt: "review the pull request at $subject"
      environment:
        systems:
          network: {mode: open}
          git: {repo: $subject.value.repo, from: $subject.value.head, fetch: {}}
      autonomy:
      - then: allow
    onDone:
      patch:
        stage: reviewed
```

Every gate must pass. A rule with no gates fires without asking anybody.

A rule is data, with no code in it. So a charter can be installed over the wire, stored, and
read back like any other data. The engine reads the rule directly; there is no compiled form. The Go
types are in [trigger.go](../trigger/trigger.go).

## The rule

One sentence says how a rule fires. Everything else on this page follows from it.

> At each commit **C**, for each rule: if `on` is vacuous or **¬on(C-1) ∧ on(C)**, and
> `when` holds at **C**, fire once for **every address `subject` selects at C**.

C-1 is the commit before C. An `on` is vacuous when the rule has no `on`.

Three things follow.

**A [firing](glossary.md#firing) is about one address.** A [subject](glossary.md#subject) that
selects N addresses gives N firings. Each firing has its own gate question and its own spawn id.
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

The first rule is `review` above. The second ships what is reviewed when the checks on `main`
pass, as the team's CI records them at `ci.status.main`:

```tony
- name: ship-green
  condition:
    on:
      path: "ci.status.main"
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

`ship-green` reads: *at the moment CI on `main` goes green, ship everything reviewed*. It fires
once, at the edge, over the PRs as they stood at that commit. With `when` in place of `on`, it says
*while CI is green*, and it also ships a PR that is reviewed later.

## Two layers: point-in-time matches, and matches asked over time

The model has one noun and two grammars. The noun is the data at an address. The grammars are the
match and the formula.

A *[match](glossary.md#match)* is a tony selector over the store at one moment. It says which
addresses satisfy it now. A match holds no time.

A *formula* is a match that verse asks of the store as the store changes. For a cause, verse
asks it at two revisions. For a condition, it asks at one. A formula is its match and nothing
more.

| Concept | Grammar | What it says |
|---|---|---|
| `subject` | match, at one moment | the addresses a firing is *about*, selected at the commit. Required |
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
[revision](glossary.md#revision), not about one address. So verse asks it once per
[commit](glossary.md#commit). The store reports each commit whole, with one change for each
address it touched.

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

Verse keeps what went wrong at a rule's firings as the rule's `trouble`. It is not a field you
write. `verse charter` prints the rule as its file on stdout, and says its trouble on stderr. Take
a rule whose patch names a field the pull request does not have:

```tony
- name: copies
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {patch: {stage: seen, reviewer: $assignee}}
```

After three failed firings, `verse charter` writes this on stderr:

```
note: baseline: rule copies: failed ×3, last at 2026-09-28T09:14:03Z: … (in copies) about demo.pr."1"
```

The note says the kind, the count, when it last happened, why, the place in the rule, and the
subject. There are two kinds.

| `kind` | What happened | What the entry says |
|---|---|---|
| `failed` | An action did not do what the rule says: a reference did not resolve, a patch or a derive failed, no agent was eligible for a spawn, a gate's `pose` did not expand. | `chain`, the place in the rule (`copies`, `copies.onDone`, `copies.approval.onDenied`), and `subject` |
| `unread` | The rule could not read the store at a commit. It did not evaluate that commit, and it does not go back to it. | `rev`, the commit |

Each entry is the latest of its kind. `count` is how many times it happened since the rule was
installed or verse started. `at` is the last time. Installing the rule again clears the entry.

`verse status` counts both kinds over every charter, as `firings: {failed, unread}`. The line
is absent when nothing went wrong.

A completion hook that fails is written on the record of its task, under `hook`.

A spawn that starts and then fails is not the rule's trouble. It is a task that failed, such
as a spawn whose agent no runner offers any more, since the runner that did was removed after
the rule was installed. The task's record says so (`verse task`), and the rule's `onFailed` hook
fires. A spawn for an agent no runner offers, or one no runner's ceilings hold, is refused at
install.

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

## Lifecycle: how data leaves

Data does not just disappear. First somebody writes the intent to remove it, as
`verse.meta.vanishing`. Its value is the instant the intent was written. The removal comes
later.

The order follows from what a cause is. A rule fires on the rising edge of a store predicate:
*something matching this now exists*. A match asks what is there, so "this stopped existing"
cannot be said. The written intent turns the negative into a positive, and a departure becomes
an ordinary rising edge:

```tony
- name: note-a-departure
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
      saw: "a pull request began to leave"
```

The `on` rises when some pull request carries the marker and none did at the commit before. So
`audit.record.latest` says when the first of them began to leave. A second pull request that starts
to leave while the first is still there does not make the edge rise again. To record each departure,
derive one record per leaving pull request, with the marker in the subject
([strata](strata.md)).

Match the *presence* of the marker, not its value. `vanishing: null` is how tony writes
"present, with any value". The value is a timestamp today, and it may become a record: who
asked, why, a grace period. A charter that matches one value would have to change with it.

Three things follow. The first matters most.

**Vanishing data is not a subject.** A rule acts on what it selects: it patches it, spawns
about it, raises a gate on it. Work about data that is leaving is work about something that will
not be there. So `subject: {path: "demo.pr.*"}` skips an address whose data is vanishing. A rule
that names the marker opts in, and sees that data again. That is how the reaper below works.

A *named* subject (`demo.pr.1`) is skipped too. You may name an address that holds nothing yet.
You may not name one whose data is leaving.

Without this, a rule that reacts to a departure would act on the departing data. A patch at an
empty address creates data there. So if the removal landed first, the rule would create the data
again, holding only what the rule wrote, with no marker. To react to a departure, write somewhere
else: an audit record, or an archive.

`verse data` still shows vanishing data, because the marker is there to make the intent visible.
Only a rule's selection hides it.

**The removal itself fires nothing.** When the data goes, a predicate over it falls from
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

A bare `patch: null` writes the payload `null`. The data then stands at its address and holds
nothing. It is not gone.

| Patch | What it removes |
|---|---|
| `{tmp: !delete null}` | a key |
| `patch: !delete null` | the data at the address |

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
| "some address holds a payload that is not X" | yes, with `!not` |
| "no address matching M holds anything", such as *ship when there is no open blocker* | no |

The model only says what exists. That is why
[a departure is written down](#lifecycle-how-data-leaves) rather than inferred. A `when`
about absence works the same way: somebody writes the absence as a positive fact, such as a
count or a summary record. That is the author's practice, not part of the grammar.

All of this rests on [change events](change-events.md). [Convergence](convergence.md) says
whether a charter written on change events settles, and what evaluating at every commit costs.
