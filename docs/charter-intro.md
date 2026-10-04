# Introduction to charters

A [charter](glossary.md#charter) is a list of [rules](glossary.md#rule). A rule says: when a
matching entity changes and the gates resolve, act on the entity. The action can patch it, spawn
a task about it, or react to what that task did. Rules relate entities and tasks by declaration.
Nothing else in verse authors a flow.

This page shows the shapes a rule takes, each installed and fired by one command. It runs on a
verse on your own machine, started as [the tutorial](tutorial.md#1-start) starts one.
[Write a charter](build.md#write-a-charter) then writes one of your own, step by step.

Every rule has the same four parts, and one sentence gives the whole firing rule
([the rule](trigger-model.md#the-rule)).

Rules differ in which parts are present. One command shows each variation:

```sh
./scripts/charter-shapes.sh                  # list them
./scripts/charter-shapes.sh show    <shape>  # the rule, in tony
./scripts/charter-shapes.sh run     <shape>  # install it, then make it fire
./scripts/charter-shapes.sh install <shape>  # …or the halves apart, which is its own lesson
./scripts/charter-shapes.sh drive   <shape>
./scripts/charter-shapes.sh clean            # remove every shape and what it acted on
```

Each shape acts on its own address, such as `demo.latch.*` or `demo.seq.*`. For this reason, you
can install several shapes at once, and they do not conflict over an entity.

### patch — a rule need not spawn anything

```tony
- name: shape-patch
  condition:
    subject:
      path: "demo.patch.*"
      value: { stage: open }
  action:
    patch: { stage: triaged }
```

```sh
./scripts/charter-shapes.sh run patch
verse data demo patch 1     # stage: triaged
```

The action has no target of its own. It acts on the [subject](glossary.md#subject) of the
condition.

A rule also acts on the **backlog**. See it happen:

```sh
./scripts/charter-shapes.sh remove patch          # nothing is watching
verse data demo patch 2 -put '{stage: open}'
verse data demo patch 2                    # stage: open — nobody reacted
./scripts/charter-shapes.sh install patch
verse data demo patch 2                    # stage: triaged — installing it acted
```

A rule is a statement about the world, not a subscription to future changes.

### latch — spawn a task, and react to it finishing

```tony
- name: shape-latch
  condition:
    subject:
      path: "demo.latch.*"
      value: { stage: open }
  action:
    patch: { stage: reviewing }
    spawn:
      driver: scripted
      vector: auto
      prompt: "echo reviewing $subject; sleep 2"
    onDone:
      patch: { stage: reviewed }
```

The action binds `$subject` and every field of the subject's payload. `onDone` is a completion
[hook](glossary.md#hook): when *that* task reaches `done`, the engine fires the hook about the
same subject ([completion hooks](reference/action.md#completion-hooks-one-trigger-chaining-visible)).

The important shape here is the **[latch](glossary.md#latch)**. The action moves `stage` past
`open`, so the rule does not select an entity again after it has acted on it. Verse refuses at
install a spawning rule that cannot latch ([the rule](trigger-model.md#the-rule)).

### ask — a human decides

```tony
  gates:
  - name: approval
    mode: ask
    onDenied:
      patch: { stage: rejected }
```

```sh
./scripts/charter-shapes.sh run ask
verse question
# - gate: approval
#   id: verse.trigger.shape-ask.approval~c619ac44
#   mode: ASK
#   origin: shape-ask
#   reason: 'trigger shape-ask gate approval on demo.ask."1"'
#   spawns: shape-ask@demo.ask.1
#   subject: 'demo.ask."1"'
verse question approve verse.trigger.shape-ask.approval~c619ac44 --as you@example.com
```

The fields of a [question](glossary.md#question) in the listing:

| Field | What it is |
|---|---|
| `gate` | the name of the [gate](glossary.md#gate-of-a-rule) |
| `origin` | the rule that raised it |
| `subject` | what the question is about |
| `spawns` | what a "yes" will start. Verse settles this before it asks, even though nothing runs yet. |

The rule asks **once**, however often verse evaluates it
([gates](reference/gates.md#gates--whether-it-proceeds)). [§4 of the tutorial](tutorial.md#4-gates-and-the-docket) covers
the rest of gates.

`onDenied` says what a "no" means. Without it, the next commit that touches the subject asks the
same question again ([`onDenied`](reference/gates.md#ondenied--what-a-no-means)).

### seq — steps in order

```tony
  action:
    seq:
    - spawn:
        driver: scripted
        vector: auto
        prompt: "echo lint $subject"
    - spawn:
        driver: scripted
        vector: auto
        prompt: "echo test $subject"
      onFailed:
        patch: { stage: broken }
    onDone:
      patch: { stage: built }
```

Each element starts when the previous one completes, and the outer `onDone` runs after the whole
sequence ([`seq`](reference/action.md#seq-sequence-along-the-chain-parallel-across-triggers)).

Ask what happened. The command reads the charter and the runtime through one address:

```sh
./scripts/charter-shapes.sh run seq
verse flow show demo.seq.1
```

```tony
- roots:
  - chain: shape-seq
    children:
    - chain: shape-seq.seq[0]
      id: shape-seq.seq.0@demo.seq.1
      status: done
      prompt: 'echo lint demo.seq."1"'
    - chain: shape-seq.seq[1]
      children:
      - chain: shape-seq.seq[1].onFailed
        status: skipped
      status: done
    - chain: shape-seq.onDone
      kind: onDone
      status: applied
  subject: 'demo.seq."1"'
```

Each node is one action in the charter. Its `chain` is the position of that action inside its
rule. The same string is the id that the task spawns under (`shape-seq.seq.0@demo.seq.1`). The
tree is not a second model of the work. It is the charter and the runtime read together.

Some statuses come from no task, and they are the point of the tree:

| Status | Meaning |
|---|---|
| `applied` | the action fired, but it only set fields |
| `skipped` | a branch that the outcome of its parent did not select |
| `pending` | the action may still fire |
| `failed` | a set-only branch whose write did not land. `err` gives the reason. |

### when — a condition on another entity

```tony
  condition:
    subject:
      path: "demo.when.*"
      value: { stage: ready }
    when:
      path: "ci.status.shapes"
      value: { state: green }
```

The subject says what to act on. `when` says whether this is a moment to act. *While* CI is
green, every ready entity ships. An entity that becomes ready later is picked up too, because
verse evaluates the rule again at every commit that touches either subtree.

### on — a cause, not a condition

```tony
    on:
      path: "ci.status.release"
      value: { state: green }
```

The entities are the same, but the rule says something different: fire **at the moment** CI
went green, over whatever was waiting *then*. Verse asks `on` at the previous commit and at this
one, and fires on the rising edge:

```sh
./scripts/charter-shapes.sh run on
verse data demo on 1                        # stage: released

verse data demo on 2 -put '{stage: waiting}' # CI is still green…
verse data demo on 2                        # …and this one stays `waiting`
```

Two properties are the reason to choose `on` over `when`:

- **An edge does not fire again while it stays true.** The predicate never fell, so
  `demo.on.2` waits for CI to go red and then green again.
- An `on` rule **does not act on the backlog**, for the same reason: a predicate that already
  holds never rose.

When you mean "whenever this holds", write `when`. The engine warns at install when a narrow `on`
leaves part of what the rule reads uncovered. The rule above is such a case.

### derive — writing something that is not the subject

Every rule so far acts on its `subject` and nothing else. `derive` lets a charter write an
entity at another address. A charter that can write anywhere can write in a circle, so `derive`
comes with a bound: a [**stratum**](glossary.md#stratum), a declared level.

```tony
strata:
- name: shapes-source
  level: 0
  match: demo
- name: shapes-derived
  level: 10
  match: derived
rules:
- name: shape-derive
  stratum: shapes-source
  condition:
    subject:
      path: "demo.derive.*"
      value: { stage: open }
  action:
    patch: { stage: raised }
    derive:
    - path: "derived.feature"
      value:
        priority: urgent
        about: $subject
```

```sh
./scripts/charter-shapes.sh run derive
verse charter                                                  # the ordering
verse data derived feature shape-derive demo derive 1
```

A rule's subject must be inside its own stratum, and a derive must land in a strictly higher
one. The engine chooses the address of the derived entity:
`derived.feature.shape-derive.demo.derive.1`. [Strata](strata.md) gives both install checks,
the address, and why a rule with no stratum may not `derive`.

### Reading something the rule is not about

The `$references` of an action read the entity that the firing is about: `$stage`, `$subject`,
`$subject.value.head`. To copy from an entity that the firing is *not* about, name that entity:

```tony
action:
  patch:
    branch:   !get-path {at: [git, repo, $subject.value.head], path: branch}
    branches: !list-paths {at: "git.repo.*", path: branch}
```

`!get-path` answers with one node, and `!list-paths` with a list. Both **copy and do not
track**
([`!get-path` and `!list-paths`](reference/action.md#get-path-and-list-paths--reaching-another-entity)).

### The rest of the grammar

Rules install and read back in tony, so inspecting a rule uses the same format as writing it:

```sh
./scripts/charter-shapes.sh show latch > charter.tony   # a rule, in a file
verse c -a -f charter.tony # or: cat charter.tony | verse c -a -f -
verse charter
verse c rules shape-latch
verse c -r rules shape-latch
verse charter                     # and `verse c rules <name>` for one of them
```

A charter is durable, because each rule and each stratum is an entity
([the charter is durable](reference/charter.md#the-charter-is-durable)). [The trigger model](trigger-model.md) describes the model. The reference
describes the fields:

- [the condition](reference/condition.md), with what a match can say;
- [the action](reference/action.md), with the answer contract a spawn can demand of an agent,
  and chains and ids;
- [the gates](reference/gates.md).
