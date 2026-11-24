# [Tutorial](README.md) -- Charters, continued

[The charters page](charters.md) has the four kinds of rule. This page has the rest of what a
rule can say. It also keeps a charter in a file, and finds out why a rule did nothing. Each
shape is installed and fired from the text shown, in the same charter at `viff-tutorial`. The
page needs [a viff instance running](README.md#start) and nothing else. You do not need the pages
before it.

The rules on this page carry one team's work on `acme/app` from a user's report to a deploy:

| Address | What is there | Rule |
|---|---|---|
| `viff-tutorial.report.*` | a bug report from a user | `triage-report` |
| `viff-tutorial.fix.*` | a fix, which a script reviews | `review-fix` |
| `viff-tutorial.build.*` | a build of a fix: lint, then test | `build` |
| `viff-tutorial.deploy.*` | a deploy that waits for the release pipeline | `deploy` |
| `viff-tutorial.incident.*` | an incident, routed to the team for its area | `route` |

Each stage has an address of its own, so each rule selects only its own data, and a write you
make reaches the one rule it is for.

## The backlog

A rule is a statement about the world, so installing it acts on what already stands:

```sh
viff data viff-tutorial report 1 -put '{stage: open, title: "login fails after a password reset"}'
viff data viff-tutorial report 1               # stage: open — nothing is watching
viff charter -a viff-tutorial -f - <<'EOF'
rules:
- name: triage-report
  condition:
    subject: {path: "viff-tutorial.report.*", value: {stage: open}}
  action:
    patch: {stage: triaged}
EOF
viff data viff-tutorial report 1               # stage: triaged — installing it acted
```

Remove the rule, write another report, and nobody reacts. Install it again, and it acts on that
one too. A rule is not a subscription to future changes.

## The latch

A rule that starts a task has to move its subject out of its own selection, or it would start the
same task at every commit that touches the subject. The **[latch](../glossary.md#latch)** is the
patch that does it, and viff refuses at install a spawning rule that has none:

```sh
viff charter -a viff-tutorial -f - <<'EOF'
rules:
- name: review-fix
  condition:
    subject: {path: "viff-tutorial.fix.*", value: {stage: open}}
  action:
    patch: {stage: reviewing}
    spawn:
      script: 'echo reviewing "$SUBJECT"; sleep 2'
      environment: {vars: {SUBJECT: $[subject.ref]}, systems: {network: {mode: open}}}
    onDone:
      patch: {stage: reviewed}
EOF
viff data viff-tutorial fix 1 -put '{stage: open}'
viff data viff-tutorial fix 1                  # stage: reviewing, then reviewed
```

`stage: reviewing` is the latch: once the action has written it, the condition no longer selects
the fix. `onDone` is a completion [hook](../glossary.md#hook): when that task reaches `done`,
the engine fires it about the same subject
([completion hooks](../reference/action.md#completion-hooks-one-trigger-chaining-visible)). A
script reads the subject from its environment's vars, `environment: {vars: …}`, rather than from its text: data
reaches a script as a variable, never spliced into a command.

## Steps in order

`seq` runs its elements one after another, each when the previous one completes. The outer
`onDone` runs after the whole sequence, and a hook on one element says what a failure there
means:

```sh
viff charter -a viff-tutorial -f - <<'EOF'
rules:
- name: build
  condition:
    subject: {path: "viff-tutorial.build.*", value: {stage: open}}
  action:
    patch: {stage: building}
    seq:
    - spawn:
        script: 'echo lint "$SUBJECT"'
        environment: {vars: {SUBJECT: $[subject.ref]}, systems: {network: {mode: open}}}
    - spawn:
        script: 'echo test "$SUBJECT"'
        environment: {vars: {SUBJECT: $[subject.ref]}, systems: {network: {mode: open}}}
      onFailed:
        patch: {stage: broken}
    onDone:
      patch: {stage: built}
EOF
viff data viff-tutorial build 1 -put '{stage: open}'
viff flow show viff-tutorial.build.1
```

```tony
- roots:
  - chain: build
    children:
    - chain: build.seq[0]
      id: viff-tutorial:build.seq.0@viff-tutorial.build.1
      kind: seq
      lastAction: script
      status: done
    - chain: build.seq[1]
      children:
      - chain: build.seq[1].onFailed
        kind: onFailed
        status: skipped
      id: viff-tutorial:build.seq.1@viff-tutorial.build.1
      kind: seq
      lastAction: script
      status: done
    - chain: build.onDone
      kind: onDone
      status: applied
    kind: root
    status: done
  subject: 'viff-tutorial.build."1"'
```

Each node is one action in the charter. Its `chain` is where that action sits in its rule,
and a task's `id` is built from it. The tree is not a second model of the work: it is the
charter and what has run, read together. Some statuses come from no task, and they are why the
tree is useful: `applied` is an action that only set fields, `skipped` a branch its parent's
outcome did not choose, and `pending` one that may still fire
([`seq`](../reference/action.md#seq-sequence-along-the-chain-parallel-across-triggers)).

## A cause, not a condition

[The charters page](charters.md#2-a-rule-that-asks-first) uses `when`: *while* CI is green,
every ready release ships, and one that becomes ready later ships too. `on` says something
different: fire **at the moment** the release pipeline goes green, over whatever is waiting then.

```sh
viff charter -a viff-tutorial -f - <<'EOF'
rules:
- name: deploy
  condition:
    subject: {path: "viff-tutorial.deploy.*", value: {stage: waiting}}
    on:      {path: "viff-tutorial.ci.release", value: {state: green}}
  action:
    patch: {stage: deployed}
EOF
viff data viff-tutorial deploy 1 -put '{stage: waiting}'
viff data viff-tutorial ci release -put '{state: green}'
viff data viff-tutorial deploy 1               # stage: deployed

viff data viff-tutorial deploy 2 -put '{stage: waiting}'   # the pipeline is still green…
viff data viff-tutorial deploy 2               # …and this one stays waiting
```

The install prints a note about this rule: `on` covers only the pipeline's data, so a deploy
that arrives later waits for the pipeline to fall and rise again. Here, that is the point. Viff
asks `on` at the previous commit and at this one, and fires on the rising edge. So an edge does not fire
again while it stays true. And an `on` rule does not act on the backlog: a predicate that
already holds never rose. When you mean "whenever this holds", write `when`.

## Reading something the rule is not about

The expressions of an action read the data the firing is about. To copy from other data, name
it. Say each repository the team keeps is at `viff-tutorial.repo.<name>`, with its default
branch, and each fix names its repository in `repo`. This action, on a fix, copies the branch the
fix goes to, and the default branch of every repository:

```tony
action:
  patch:
    base:     !get-path {at: [viff-tutorial, repo, .[subject.value.repo]], path: branch}
    branches: !list-paths {at: "viff-tutorial.repo.*", path: branch}
```

`!get-path` answers with one node and `!list-paths` with a list. Both copy, and neither tracks:
a later change to the data read does not reach what was written
([`!get-path` and `!list-paths`](../reference/action.md#get-path-and-list-paths-reaching-other-data)).

## A charter in a file

The rules so far were typed into the shell. A charter you keep is a file. Save the triage rule
as `triage.tony`, with one more field, which puts each report in the support queue:

```tony
- name: triage-report
  condition:
    subject: {path: "viff-tutorial.report.*", value: {stage: open}}
  action:
    patch: {stage: triaged, queue: support}
```

```sh
viff charter -a viff-tutorial -f triage.tony
```

```
package viff-tutorial: installed 1 trigger(s)
`viff c` shows every charter and where each may write
```

A file installs as one step: if viff refuses one rule in it, it installs none of them. A rule
whose name is already installed is replaced, and the other rules stay: `triage-report` is
replaced, and `review-fix`, `build` and `deploy` are as they were.

An install that succeeds may print more lines. Read them:

```
  ttl: it is removed at 2026-11-01T09:00:00Z, as -r removes it; installing it again renews it.
  held: rule triage-intake is waiting for stratum intake. It fires on nothing until then.
  note: rule approve-deploy: gate "approval" blocks, and its refusal does not advance the subject past its own predicate (stage): …
```

`ttl:` is a charter that stands for a while: a charter file with `at:` and `ttl: 72h` is removed
72 hours after it is installed, as `viff charter -r` removes it
([the charter reference](../reference/charter.md)). `held:` is a rule that names a stratum
nobody installed: it is stored, and it fires on nothing until the stratum is there. `note:` is a
rule that is legal and may not do what you meant; the
note says why, and usually what to write instead.

## When a rule does nothing

A rule that does nothing may have selected nothing, found its condition false, been denied by
a gate, or failed. Only the last one is a fault, and viff keeps it on the rule. This rule routes
each incident to the team for its area, and spells `value` wrongly:

```sh
viff charter -a viff-tutorial -f - <<'EOF'
rules:
- name: route
  condition:
    subject: {path: "viff-tutorial.incident.*", value: {stage: open}}
  action:
    patch: {stage: routed, team: .[subject.valeu.area]}
EOF
viff data viff-tutorial incident 1 -put '{stage: open, area: billing}'
viff data viff-tutorial incident 1             # stage: open — the rule fired and failed
viff charter viff-tutorial rules route
```

```tony
…
name: route
trouble:
- at: "…"
  chain: route
  count: 1
  kind: failed
  subject: 'viff-tutorial.incident."1"'
  why: 'error evaluating "subject.valeu.area": no "valeu" in subject'
```

`viff charter` prints a note for every rule in trouble, and `viff status` counts the failed
firings. Some mistakes the install does not catch at all, such as a misspelled field inside
`spawn:`; [what an install does not check](../reference/charter.md#what-an-install-does-not-check)
lists them, and [when a rule does nothing](../trigger-model.md#when-a-rule-does-nothing) says how
to clear trouble.

## Stopping a rule

A [suspension](../glossary.md#suspension) stops the work about a charter and a subject without
ending it: firings, tasks and questions. A rule is named with its charter's address:

```sh
viff flow suspend -charter viff-tutorial:triage-report -why "reading the triage rule"
viff data viff-tutorial report 2 -put '{stage: open, title: "export times out"}'
viff data viff-tutorial report 2               # stage: open: nothing fired
viff flow -charter viff-tutorial:triage-report # held by you, why, since when
viff flow resume -charter viff-tutorial:triage-report
viff data viff-tutorial report 2               # stage: triaged, as it stands now
```

Viff does not replay what happened during the suspension. It acts on the world as it stands
when the suspension is lifted. [Control](../control.md) gives the flags.

## Reading the rules back

Rules install and read back in tony, so inspecting a rule is the same format as writing one:

```sh
viff charter viff-tutorial                     # the charter, as the file that installs it
viff charter viff-tutorial > charter.tony      # keep it, edit it, install it again
viff charter -a viff-tutorial -f charter.tony
```

A charter is durable: each rule and each stratum is data in the store, and a viff instance that restarts has
them ([the charter is durable](../reference/charter.md#the-charter-is-durable)).

When you are done with the tutorial, take its charter away. What the charter wrote stays, and
`viff data viff-tutorial -rm` takes that away too:

```sh
viff charter -r viff-tutorial
```

## Where to next

- [The charter reference](../trigger-model.md) is the one sentence every rule follows, and
  the page for each part of a rule.
