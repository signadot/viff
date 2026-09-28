# Write a charter

This page is for somebody who writes a [charter](glossary.md#charter). It holds one recipe:
the smallest charter that works, the steps to run it on your own machine, and a list of what
verse expects and does not check.

| Recipe | What you build | What you need |
|---|---|---|
| [Write a charter](#write-a-charter) | one [rule](glossary.md#rule) in a file, installed and fired | a running verse |

The recipes for a [runner](glossary.md#runner) and an [io](glossary.md#io) are for somebody
who extends verse. How to build them is in
[build-runner-and-io.md](platform/build-runner-and-io.md), in the repository.

The recipe needs no credentials and no cluster. The first build downloads Go modules, and the
first `verse up` may download the `o` binary. After that the recipe needs no network.

## Before you start

The recipe talks to a verse on your own machine. Start one as
[the tutorial](tutorial.md#1-start) does, from the root of the checkout, and leave it running.

```sh
go build -o verse ./cmd/verse
export PATH="$PWD:$PATH"
export VERSE_ADDR=localhost:9797
verse up
```

Type the commands of the recipe in a second terminal, from the root of the checkout, with the
same `PATH` and `VERSE_ADDR`.

A block with no language shows what a command prints. A line with `…` is shortened.

## Write a charter

A charter is a list of rules. A rule says what verse does when an entity matches a
condition. An *entity* is a node in the store of verse. It has an address, such as
`demo.ticket.1`, and a payload.

### A rule in a file

Save this rule as `triage.tony`.

```tony
- name: triage
  condition:
    subject:
      path: "demo.ticket.*"
      value: { stage: open }
  action:
    patch: { stage: triaged }
```

The [subject](glossary.md#subject) is the entity that a firing is about. A *firing* is one
run of the action, for one subject. The action names no target, because it acts on the
subject.

| Field | What it says |
|---|---|
| `name` | the name of the rule. A name is unique in a charter. |
| `condition.subject` | which entities the rule is about. `path` is a pattern over addresses, and `*` stands for any one segment. `value` is a predicate on the payload. |
| `action.patch` | the fields to write. Verse merges them into the payload of the subject. |

### Install it

```sh
verse charter -a -f triage.tony
```

```
baseline: installed 1 trigger(s)
```

`baseline` is the charter that has no address. Verse applies a file as one step. If verse
refuses one rule of the file, it installs none of them.

An install that succeeds may print more lines under the first one. Read them.

| Line | What it means |
|---|---|
| `held:` | The rule names a `stratum` that is not installed. Verse stores the rule, and the rule fires on nothing until the stratum is installed. |
| `note:` | The rule is legal, and it may not do what you meant. The note says why. |

A rule named `waits`, with a stratum `intake` that nobody installed, prints a `held:` line:

```
baseline: installed 0 trigger(s)
  held: rule waits waits for stratum intake, which is not installed. It fires on nothing until that stratum is.
```

A [gate](glossary.md#gate-of-a-rule) is a check that a firing passes before its action
fires. A rule named `asks`, with a gate of the mode `ask` and no `onDenied`, prints a
`note:` line:

```
baseline: installed 1 trigger(s)
  note: rule asks: gate "approval" blocks, and its refusal does not advance the subject past its own predicate (stage): … Say what a no means — `onDenied: {patch: {…}}` on the gate, …
```

### Make it fire

Write an entity that the condition matches, and read it back.

```sh
verse data demo ticket 1 -put '{stage: open, title: "the login page is slow"}'
verse data demo ticket 1
```

```
at: 'demo.ticket."1"'
payload:
  stage: open
  title: "the login page is slow"
```

```
stage: triaged
title: "the login page is slow"
```

On a command line you type an address as separate words, `demo ticket 1`. In a document the
same address is `demo.ticket."1"`.

The rule fires once for this entity. After the patch the payload does not match
`stage: open`, so the rule does not select the entity again.

A rule also acts on what the store already holds. If `demo.ticket.7` is open before you
install `triage`, the install triages it.

### When it does nothing

A rule that does nothing looks the same from outside in four cases, and only a failed action
is a fault ([when a rule does nothing](trigger-model.md#when-a-rule-does-nothing)). Verse
keeps that case on the rule. To see it, save this rule as `assign.tony`. A
reference such as `$subject.value.reporter` reads a field of the subject, and this rule
spells `value` wrongly.

```tony
- name: assign
  condition:
    subject:
      path: "demo.bug.*"
      value: { stage: open }
  action:
    patch: { stage: assigned, owner: $subject.valeu.reporter }
```

```sh
verse charter -a -f assign.tony
verse data demo bug 1 -put '{stage: open, reporter: ana}'
verse data demo bug 1
```

The install accepts the rule. The entity does not change:

```
reporter: ana
stage: open
```

`verse charter` prints every rule, and `verse charter rules assign` prints this one. Both
mark a rule whose firings failed with `trouble`.

```sh
verse charter rules assign
```

```
…
name: assign
trouble:
- at: "2026-09-27T23:49:26Z"
  chain: assign
  count: 1
  kind: failed
  subject: 'demo.bug."1"'
  why: "$subject.valeu.reporter is not present in the payload that fired this action"
```

`verse status` counts the same faults over every rule. The line is absent when no firing
has failed.

```
firings:
  failed: 1
```

[When a rule does nothing](trigger-model.md#when-a-rule-does-nothing) describes both kinds of
trouble, and how to clear them.

### Three things to add next

Each example below acts on a place of its own, so you can install all three beside `triage`.

**A gate.** With `mode: ask`, the firing waits at the gate until a person or an agent
answers. `onDenied` is the action that fires when the answer is no. Save this as
`approve.tony`.

```tony
- name: approve-change
  condition:
    subject:
      path: "demo.change.*"
      value: { stage: open }
  gates:
  - name: approval
    mode: ask
    onDenied:
      patch: { stage: rejected }
  action:
    patch: { stage: approved }
```

```sh
verse charter -a -f approve.tony
verse data demo change 1 -put '{stage: open}'
verse gate
verse gate approve verse.trigger.approve-change.approval~5910e931
verse data demo change 1
```

`verse gate` lists the questions that wait. A question reaches the list a moment after the
write, so an empty list means that you read too early. Give `verse gate approve` the `id`
that the listing prints.

```
- gate: approval
  id: verse.trigger.approve-change.approval~5910e931
  mode: ASK
  …
  origin: approve-change
  reason: 'trigger approve-change gate approval on demo.change."1"'
  subject: 'demo.change."1"'
  …
```

```
approved verse.trigger.approve-change.approval~5910e931
```

```
stage: approved
```

[The gates of a rule](reference/gates.md#gates--whether-it-proceeds) has the other modes.

**A spawn with an `onDone` hook.** `spawn` starts a task. A task is a unit of work that
verse governs. The driver `scripted` runs the prompt as a shell command. `onDone` is a
*hook*: an action that fires when the task ends with the status `done`. Save this as
`review.tony`.

```tony
- name: review
  condition:
    subject:
      path: "demo.review.*"
      value: { stage: open }
  action:
    patch: { stage: reviewing }
    spawn:
      driver: scripted
      vector: auto
      prompt: "echo reviewed $subject"
    onDone:
      patch: { stage: reviewed }
```

```sh
verse charter -a -f review.tony
verse data demo review 1 -put '{stage: open}'
verse data demo review 1
verse task
```

The task takes a moment. If the entity still says `reviewing`, read it again.

```
stage: reviewed
```

```
- cursor: 6
  driver: scripted
  …
  id: review@demo.review.1
  …
  status: done
```

The patch to `reviewing` is the **latch**. A latch is a write that takes the subject out of
the selection of its own rule, so the rule does not spawn a second task for it. Verse
refuses at install a rule that spawns and writes no field that its subject reads.
[Completion hooks](reference/action.md#completion-hooks-one-trigger-chaining-visible) has
the other hooks.

**A derive.** `derive` writes an entity that is not the subject. A rule with a `derive`
names a *stratum*, which is a named part of the store with a level. The derive must land
in a stratum of a higher level. One file may hold strata and rules. Save this as
`flag.tony`.

```tony
- name: requests
  level: 0
  match: demo
- name: findings
  level: 10
  match: finding
---
- name: flag
  stratum: requests
  condition:
    subject:
      path: "demo.request.*"
      value: { stage: open }
  action:
    patch: { stage: flagged }
    derive:
    - path: "finding.request"
      value: { priority: urgent, about: $subject }
```

```sh
verse charter -a -f flag.tony
verse data demo request 1 -put '{stage: open}'
verse data finding request flag demo request 1
```

```
baseline: installed 2 stratum(a) and 1 trigger(s)
```

```
about: 'demo.request."1"'
priority: urgent
verse:
  meta:
    derivedFrom:
      rule: flag
      subject: 'demo.request."1"'
```

Verse chooses the address of the derived entity. The address is the place the derive names,
then the name of the rule, then the segments of the subject. [Strata](strata.md) has the
rules of the levels.

### What the install does not check

The install refuses an unknown field of a rule, an unknown operator in a `value` predicate,
and a spawn with no latch. The install accepts each rule in the table below.

| What the rule holds | What happens |
|---|---|
| an unknown field inside `spawn:`, such as `promt:` | Verse drops the field and says nothing. The task runs without it. `verse charter rules <name>` prints the rule without the field. |
| a key spelled wrongly inside `patch:` or inside the `value` of a `derive` | Verse writes the key as you spelled it. The keys of a payload are yours. |
| a reference that names nothing, such as `$subject.valeu.x` | The firing fails and writes nothing. The rule gets `trouble`. |
| a spawn with a `runner:` that is not installed, or a `driver:` that the runtime does not have | The patch of the action lands, and the task fails at once. `verse task` shows `status: failed` and the reason in `err`. The rule gets no `trouble`. |
| a spawn with a `provider` whose `capability` has no registered provider | The patch of the action lands, and no task starts. The rule gets `trouble`, which names the capability. |
| a condition over a place that the owner of the rule may not read | The rule selects nothing, and nothing reports it. A rule reads the store as its owner does. |

In the fourth and fifth rows the subject has already passed the latch, so the rule does not
fire for it again. For the fourth row, give the action an `onFailed` hook. The hook fires
when the spawn fails, and it writes a state that you can see. In the fifth row no task
starts, so no hook runs, and the `trouble` on the rule is where the failure shows.
