# [Tutorial](README.md) -- Charters, continued

[The charters page](charters.md) has the four kinds of rule. This page has the rest of what a
rule can say. It also keeps a charter in a file, and finds out why a rule did nothing. Each
shape is installed and fired from the text shown, in the same charter at `verse-tutorial`. The
page needs [a verse running](README.md#start) and nothing else. You do not need the pages
before it.

## The backlog

A rule is a statement about the world, so installing it acts on what already stands:

```sh
verse data verse-tutorial patch 1 -put '{stage: open}'
verse data verse-tutorial patch 1                # stage: open — nothing is watching
verse charter -a verse-tutorial -f - <<'EOF'
rules:
- name: triage-2
  condition:
    subject: {path: "verse-tutorial.patch.*", value: {stage: open}}
  action:
    patch: {stage: triaged}
EOF
verse data verse-tutorial patch 1                # stage: triaged — installing it acted
```

Remove the rule, write another entity, and nobody reacts. Install it again, and it acts on that
one too. A rule is not a subscription to future changes.

## The latch

A rule that starts a task has to move its subject out of its own selection, or it would start the
same task at every commit that touches the subject. The **[latch](../glossary.md#latch)** is the
patch that does it, and verse refuses at install a spawning rule that has none:

```sh
verse charter -a verse-tutorial -f - <<'EOF'
rules:
- name: review-2
  condition:
    subject: {path: "verse-tutorial.latch.*", value: {stage: open}}
  action:
    patch: {stage: reviewing}
    spawn:
      doer: {script: 'echo reviewing "$SUBJECT"; sleep 2', with: {network: {mode: open}, env: {SUBJECT: $subject}}}
    onDone:
      patch: {stage: reviewed}
EOF
verse data verse-tutorial latch 1 -put '{stage: open}'
verse data verse-tutorial latch 1                # stage: reviewing, then reviewed
```

`stage: reviewing` is the latch: once the action has written it, the condition no longer selects
the entity. `onDone` is a completion [hook](../glossary.md#hook): when that task reaches `done`,
the engine fires it about the same subject
([completion hooks](../reference/action.md#completion-hooks-one-trigger-chaining-visible)). A
script reads the subject from its environment, `with.env`, rather than from its text: data
reaches a script as a variable, never spliced into a command.

## Steps in order

`seq` runs its elements one after another, each when the previous one completes. The outer
`onDone` runs after the whole sequence, and a hook on one element says what a failure there
means:

```sh
verse charter -a verse-tutorial -f - <<'EOF'
rules:
- name: build-2
  condition:
    subject: {path: "verse-tutorial.seq.*", value: {stage: open}}
  action:
    patch: {stage: building}
    seq:
    - spawn:
        doer: {script: 'echo lint "$SUBJECT"', with: {network: {mode: open}, env: {SUBJECT: $subject}}}
    - spawn:
        doer: {script: 'echo test "$SUBJECT"', with: {network: {mode: open}, env: {SUBJECT: $subject}}}
      onFailed:
        patch: {stage: broken}
    onDone:
      patch: {stage: built}
EOF
verse data verse-tutorial seq 1 -put '{stage: open}'
verse flow show verse-tutorial.seq.1
```

```tony
- roots:
  - chain: build-2
    children:
    - chain: build-2.seq[0]
      id: verse-tutorial:build-2.seq.0@verse-tutorial.seq.1
      kind: seq
      lastAction: script
      status: done
    - chain: build-2.seq[1]
      children:
      - chain: build-2.seq[1].onFailed
        kind: onFailed
        status: skipped
      id: verse-tutorial:build-2.seq.1@verse-tutorial.seq.1
      kind: seq
      lastAction: script
      status: done
    - chain: build-2.onDone
      kind: onDone
      status: applied
    kind: root
    status: done
  subject: 'verse-tutorial.seq."1"'
```

Each node is one action in the charter. Its `chain` is where that action sits in its rule,
and a task's `id` is built from it. The tree is not a second model of the work: it is the
charter and what has run, read together. Some statuses come from no task, and they are why the
tree is useful: `applied` is an action that only set fields, `skipped` a branch its parent's
outcome did not choose, and `pending` one that may still fire
([`seq`](../reference/action.md#seq-sequence-along-the-chain-parallel-across-triggers)).

## A cause, not a condition

[The charters page](charters.md#2-a-rule-that-asks-first) used `when`: *while* CI is green,
every ready entity ships, and one that becomes ready later ships too. `on` says something
different: fire **at the moment** CI went green, over whatever was waiting then.

```sh
verse charter -a verse-tutorial -f - <<'EOF'
rules:
- name: release-2
  condition:
    subject: {path: "verse-tutorial.on.*", value: {stage: waiting}}
    on:      {path: "verse-tutorial.ci.release", value: {state: green}}
  action:
    patch: {stage: released}
EOF
verse data verse-tutorial on 1 -put '{stage: waiting}'
verse data verse-tutorial ci release -put '{state: green}'
verse data verse-tutorial on 1                   # stage: released

verse data verse-tutorial on 2 -put '{stage: waiting}'   # CI is still green…
verse data verse-tutorial on 2                   # …and this one stays waiting
```

The install prints a note about this rule: `on` reads only the CI entity, so an entity that
arrives later waits for CI to fall and rise again. Here, that is the point. Verse asks `on` at
the previous commit and at this one, and fires on the rising edge. So an edge does not fire
again while it stays true. And an `on` rule does not act on the backlog: a predicate that
already holds never rose. When you mean "whenever this holds", write `when`.

## Reading something the rule is not about

The `$references` of an action read the entity the firing is about. To copy from another
entity, name it:

```tony
action:
  patch:
    branch:   !get-path {at: [verse-tutorial, repo, $subject.value.head], path: branch}
    branches: !list-paths {at: "verse-tutorial.repo.*", path: branch}
```

`!get-path` answers with one node and `!list-paths` with a list. Both copy, and neither tracks:
a later change to the entity read does not reach what was written
([`!get-path` and `!list-paths`](../reference/action.md#get-path-and-list-paths-reaching-another-entity)).

## A charter in a file

The rules so far were typed into the shell. A charter you keep is a file. Save this as
`triage.tony`:

```tony
- name: triage-3
  condition:
    subject: {path: "verse-tutorial.ticket.*", value: {stage: open}}
  action:
    patch: {stage: triaged}
```

```sh
verse charter -a verse-tutorial -f triage.tony
```

```
package verse-tutorial: installed 1 trigger(s)
```

A file installs as one step: if verse refuses one rule in it, it installs none of them. A rule
whose name is already installed is replaced, and the other rules stay.

An install that succeeds may print more lines. Read them:

```
  held: rule waits waits for stratum intake, which is not installed. It fires on nothing until that stratum is.
  note: rule asks: gate "approval" blocks, and its refusal does not advance the subject past its own predicate (stage): …
```

`held:` is a rule that names a stratum nobody installed: it is stored, and it fires on nothing
until the stratum is there. `note:` is a rule that is legal and may not do what you meant; the
note says why, and usually what to write instead.

## When a rule does nothing

A rule that does nothing may have selected nothing, found its condition false, been denied by
a gate, or failed. Only the last one is a fault, and verse keeps it on the rule. This rule spells `value`
wrongly:

```sh
verse charter -a verse-tutorial -f - <<'EOF'
rules:
- name: assign
  condition:
    subject: {path: "verse-tutorial.defect.*", value: {stage: open}}
  action:
    patch: {stage: assigned, owner: $subject.valeu.reporter}
EOF
verse data verse-tutorial defect 1 -put '{stage: open, reporter: ana}'
verse data verse-tutorial defect 1               # stage: open — the rule fired and failed
verse charter verse-tutorial rules assign
```

```tony
…
name: assign
trouble:
- at: "…"
  chain: assign
  count: 1
  kind: failed
  subject: 'verse-tutorial.defect."1"'
  why: "$subject.valeu.reporter is not present in the payload that fired this action"
```

`verse charter` prints a note for every rule in trouble, and `verse status` counts the failed
firings. Some mistakes the install does not catch at all, such as a misspelled field inside
`spawn:`; [what an install does not check](../reference/charter.md#what-an-install-does-not-check)
lists them, and [when a rule does nothing](../trigger-model.md#when-a-rule-does-nothing) says how
to clear trouble.

## Stopping a rule

A [suspension](../glossary.md#suspension) stops the work about a charter and a subject without
ending it: firings, tasks and questions. A rule is named with its charter's address:

```sh
verse flow suspend -charter verse-tutorial:triage-2 -why "reading the triage rule"
verse data verse-tutorial patch 2 -put '{stage: open}'
verse data verse-tutorial patch 2                # stage: open: nothing fired
verse flow -charter verse-tutorial:triage-2      # held by you, why, since when
verse flow resume -charter verse-tutorial:triage-2
verse data verse-tutorial patch 2                # stage: triaged, as it stands now
```

Verse does not replay what happened during the suspension. It acts on the world as it stands
when the suspension is lifted. [Control](../control.md) gives the flags.

## Reading the rules back

Rules install and read back in tony, so inspecting a rule is the same format as writing one:

```sh
verse charter verse-tutorial                     # the charter, as the file that installs it
verse charter verse-tutorial > charter.tony      # keep it, edit it, install it again
verse charter -a verse-tutorial -f charter.tony
```

A charter is durable: each rule and each stratum is an entity, and a verse that restarts has
them ([the charter is durable](../reference/charter.md#the-charter-is-durable)).

When you are done with the tutorial, take its charter away. What the charter wrote stays, and
`verse data verse-tutorial -rm` takes that away too:

```sh
verse charter -r verse-tutorial
```

## Where to next

- [The charter reference](../trigger-model.md) is the one sentence every rule follows, and
  the page for each part of a rule.
