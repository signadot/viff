# Tutorial

This tutorial has five sections, and you can run every command in them. A verse holds two
kinds of thing:

| Thing | What it is |
|---|---|
| an [entity](glossary.md#entity) | a part of the world, such as a pull request or a CI status |
| a [task](glossary.md#task) | work that is done to the world |

Every section after §3 relates entities to tasks.

Every section has a script that sets up its world in one command. For the shape of the system,
read [the overview](overview.md). For the full command surface, the config layers and the
detail of each topic, read [the reference](reference/README.md).

1. [Start](#1-start)
2. [Entities](#2-entities) — [read](#read) · [write](#write) · [watch](#watch)
3. [Tasks](#3-tasks) — [the dial](#the-dial) ·
   [a task is an entity too](#a-task-is-an-entity-too) · [steering](#steering) ·
   [where a task runs is a seam](#where-a-task-runs-is-a-seam)
4. [Charters — relating entities to tasks](#4-charters--relating-entities-to-tasks) —
   [patch](#patch--a-rule-need-not-spawn-anything) ·
   [latch](#latch--spawn-a-task-and-react-to-it-finishing) · [ask](#ask--a-human-decides) ·
   [seq](#seq--steps-in-order) ·
   [when](#when--a-condition-on-another-entity) · [on](#on--a-cause-not-a-condition) ·
   [derive](#derive--writing-something-that-is-not-the-subject) ·
   [reading something the rule is not about](#reading-something-the-rule-is-not-about) ·
   [the rest of the grammar](#the-rest-of-the-grammar)
5. [Gates and the docket](#5-gates-and-the-docket) —
   [a service that answers them](#a-service-that-answers-them) ·
   ["no" is a real outcome](#no-is-a-real-outcome) ·
   [the same machinery answers *who*](#the-same-machinery-answers-who)
6. [Where to go next](#6-where-to-go-next)

---

## 1. Start

You need Go 1.27.1 or later.

```sh
go build -o verse ./cmd/verse
export PATH="$PWD:$PATH"          # so that `verse` is this build, in every terminal you use
export VERSE_ADDR=localhost:9797  # where clients reach it (this is the default)
verse up                          # engine + task runtime + store, in one process
```

Leave `verse up` running. Every other subcommand is a client, so open a second terminal. To
see verse work before you read about it, run the demo:

```sh
./scripts/seed-demo.sh     # a charter, the entities it reacts to, one gate waiting for you
```

You have not said who you are, and you do not need to yet. Toward a verse on your own machine,
the CLI acts as `user:<login>@localhost`, where `<login>` is your login name. That user owns
what you write. [The permissions walk](perms-guide.md#before-the-first-step-who-you-are) shows
how to name yourself.

`up` decides three things for the whole runtime. The defaults need no attention now.

| Thing | Default |
|---|---|
| the **store** | a fresh docd on a temp dir. `-data <dir>` keeps it. |
| the default **driver** | a real llm agent when credentials are configured, and `scripted` when they are not. `scripted` runs a prompt as a shell command. |
| an agent's **root** | an isolated temp dir. For this reason, a file that an agent writes is not in your cwd. With credentials configured, `up` prints the root at start. |

[reference/up.md](reference/up.md) describes all three. It also describes the config layers
and the `o` binary that `up` resolves to host its daemons. Its flags point verse at a store or
a task runner you already have.

---

## 2. Entities

An entity is anything the flow refers to, such as a PR, a build, a person or a CI status. An
entity has an [address](glossary.md#address) and carries a document. The address is as long as
what it names: `demo.pr.1` for a pull request, and longer where a charter keeps its own tree.
That is the whole data model.

At a command you type the segments of an address as separate words: `demo pr 1`. In a document
the same address is written as a kinded path: `demo.pr."1"`. Both spellings name one address.
[§Addressing](reference/entities.md#addressing) gives the whole grammar. You can read this
section without it.

```sh
./scripts/seed-demo.sh --clean   # if you ran it above: its rules react to what is seeded next
./scripts/seed-entities.sh       # a dozen entities, three systems, nothing watching them
```

### Read

The address narrows a read by *where* an entity lives. `-if` narrows it by *what the entity
says*. `-if` is a [tony match](https://signadot.github.io/tony-format/) against the payload.

```sh
verse data                                         # the whole verse, one document
verse data demo pr '*'                             # by where it lives
verse data demo pr 1                               # one entity, whole
verse data -at 'demo.pr.*.stage'                   # one field of each, keyed by name
verse data demo pr '*' -if '{stage: open}'         # by what it says
verse data demo pr '*' -if '{stage: !or [open, merged]}'  # …an operator, not just equality
verse data demo pr '*' -if '{urgent: true}' -if-at labels # …rooted inside the payload
verse data demo pr '*' -trim '{stage}'             # …and how much of each entity to print
verse data -paths demo pr '*'                      # just the addresses
verse data -at demo.pr.1                           # …an address copied out of a document
verse data demo '..' -if '{stage: open}'           # every node that says so, at any depth
```

The store is one document. `verse data` reads it at an address and prints the node at that
address. For this reason, the same address works at every depth.

| Step | What it selects |
|---|---|
| `*` | any name at one position |
| `..` | every depth beneath |

A question states its depth with these steps. It does not take a depth from somewhere else.

A name is a key in the document, not a token inside a rendered `at:`. `-paths` prints the
names. A read, a `-put` and a `-rm` all take the same address. The address has two spellings
of one grammar, steps and a kinded path.
[reference/entities §Addressing](reference/entities.md#addressing) describes both.

**An address is typed as steps.** Each step is its own shell word, so nothing needs escaping.
`-paths` prints addresses as steps too, so you can pass a listing straight back:

```sh
verse data demo pr a.b labels urgent           # steps: an id with a dot in it, unquoted
verse data -paths demo pr '*'                  # demo pr a.b
verse data -paths demo pr '*' | xargs -L1 verse data
```

Verse uses one spelling for input and output. A step is a whole word, so a dot inside a step is
part of the name. The shell's own quoting appears only where a segment holds a space or a `*`.
That quoting is single quotes, which `xargs` understands.

A name that would read as something else is also printed inside double quotes. The quoted form
reads back as that name:

| Name | Printed as |
|---|---|
| a field called `[0]` | `'"[0]"'` |
| a name that is an asterisk | `'"*"'` |

The asterisk is quoted because a `*` step on its own selects every field there.

**A positional is a step and nothing else.** `verse data demo.pr.1` names one segment called
`demo.pr.1`, because a word cannot be both one segment and several. Verse takes a whole address
in one token only where a flag *marks* it. The flag says which grammar applies, so verse does
not guess:

```sh
verse data -at demo.pr.1            # an address copied out of a document
verse data -at 'demo.pr.*.stage'    # …wildcards and all, the same as: demo pr '*' stage
```

Flags go on either side of the address. `verse data -paths demo pr '*'` and
`verse data demo pr '*' -paths` are the same command.

`-if-at` takes a dot-separated field path and roots `-if` there. A predicate about a subtree
then does not repeat the shape above it. `-if-at labels -if '{urgent: true}'` is the same as
`-if '{labels: {urgent: true}}'`. `-if-at` also reaches a subtree that is not an object. A path
that leads nowhere does not match.

The pattern language belongs to tony, not to verse. `!or`, `!glob`, `!not`, `!and` and the
others are [tony match operators](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop).
They mean the same thing in a read, in a charter's `value`, and in an answer contract. You learn
them once. Verse adds *where* a match may be asked, and
[§4](#4-charters--relating-entities-to-tasks) shows that.

### Write

A put **merges**. Fields that you do not name stay as they are. For this reason, verse answers
with the entity as it stands after the write, not with an echo of what you sent:

```sh
verse data demo pr 1 -put '{owner: dee}'         # changes the owner field, keeps the stage
verse data demo pr 1 labels urgent -put true    # one field, deep
verse data demo pr 1 labels urgent -rm          # and gone again
verse data demo scratch 1 -rm                   # the whole entity
```

Verse reads a payload typed on the command line as tony. For this reason, `{stage: open}` needs
no quotes around either half, and commas are optional. If you save the payload as `.json` or
`.yaml`, the same command reads that format. The three are dialects of one document model, not
three separate formats ([representation](reference/representation.md)).

A write can also carry a condition. The store checks each condition *with* the write, in one
step, so nothing can change in between:

```sh
verse data demo pr 1  -put '{stage: open}' -create     # only if nothing is there yet → refused
verse data demo pr 99 -put '{stage: open}' -update     # only if something is        → refused
# write refused: a condition did not hold

verse data demo pr 4 -put '{owner: dee}' -expect '{stage: merged}'     # only if it still looks like this
```

With these conditions, two writers can advance one entity and keep each other's fields. This is
CAS written as a predicate: you say what you believe about the entity, not which revision you
read. `-expect` uses the same predicate language as `-if`. It has a different flag because `-if`
on this command is the filter of the read. When verse refuses a write, the entity stays as it
was, and the command exits non-zero.

### Watch

```sh
verse watch -if '{stage: open}' demo pr '*'
```

```tony
{at: 'demo.pr."1"' op: set rev: 97 to: {labels: {area: auth urgent: true} owner: ana stage: open …}}
{at: 'demo.pr."3"' op: set rev: 97 to: {labels: {area: store urgent: true} owner: ana stage: open …}}
{at: 'demo.pr."5"' op: set rev: 97 to: {labels: {area: engine} owner: cy stage: open …}}
```

Read two things from that output.

1. The watch **replays**. A watch is level-triggered when it opens: what already matches arrives
   before anything new. You never watch a stream whose beginning you missed.
2. Each change is one line, so you can grep a watch.

In each line, `to` is the whole state of the entity at that commit, not the delta. `op` is
`set` (created *or* updated) or `delete`.

In another terminal, write to the entity, and see the line arrive:

```sh
verse data demo pr 2 -put '{stage: open}'
```

Then move the entity past the filter with `'{stage: reviewing}'`. Nothing arrives, because the
filter is a predicate over the state, not a subscription to the entity.

[reference/entities.md](reference/entities.md) covers refs, deep writes, cursors and `-since`.

---

## 3. Tasks

A task is a governed unit of work. An agent, a shell command, a remote worker or a person does
the work. Run a task directly:

```sh
verse task run -driver scripted "echo summarizing; sleep 3"    # prints a run-… id
verse task run -driver scripted -wait "echo done"              # …and block for the result
verse task
verse task get <id>                # status, prompt, driver, runner, result, pending gates
verse task say <id> "also mention the dial"        # re-prompt the agent mid-run
verse task vector <id> ask         # slide its autonomy (auto|ask|park)
verse task cancel <id>
```

These commands give `-driver` explicitly because the default is not always `scripted`. With LLM
credentials configured, a bare `task run "echo done"` gives your sentence to an *agent*
([reference/up.md](reference/up.md#the-driver)).

`say` and `vector` do different things:

| Command | What it changes |
|---|---|
| `say` | It gives the agent new instructions. |
| `vector` | It replaces the [vector](glossary.md#vector), the list of gates that says how much the task must confirm. The change applies at the next boundary of the task, with no restart. |

### The dial

Verse has three [drivers](glossary.md#driver): `scripted`, `calls` and `llm`. `scripted` and
`llm` carry different vectors. A gate asks about what you have **not** already authorized.

| Driver | Default vector |
|---|---|
| `verse task run -driver scripted` | runs ungated. Its prompt *is* the shell command, and you authorized it when you typed it. |
| `llm` | asks before effects. Reads run freely. Writing a file, running a command and delegating to a sub-agent block. |

The `scripted` rule holds only for what you type yourself. A charter that spawns
`driver: scripted` typed nothing, so its task asks about the command it is about to run. The
charter says `vector: auto` when it means to run unattended.

```sh
verse task run 'Write a file hello.txt containing "hi".'
verse gate
# - gate: write
#   id: run-f3e31ed0390f~d1
#   mode: ASK
#   task: run-f3e31ed0390f
#   reason: "verb write"
#   tool: write_file
#   where: task
verse gate approve run-f3e31ed0390f~d1 -as you@example.com
```

`run -attach` spawns the task *and* streams its transcript. On a terminal, it asks you inline:
`⏸ gate "write" (write_file) — approve? [y/N]`. The same gate can also be answered from anywhere
else by its id, because it is the same question either way.

One case is not settled: you give verse a *command*, and verse runs it with an *agent*. Which of
the two rules applies then is a rough edge.
[reference/tasks.md](reference/tasks.md#the-rough-edge-who-you-addressed) states it.

### A task is an entity too

Here entities and tasks join. Verse mirrors every task into the store as
`verse.task.instance.<id>`. The `verse` address is where verse keeps the records of its own
running. To observe the tasks, you observe entities:

```sh
verse watch verse task instance '*'                   # the whole fleet's lifecycle
verse data verse task instance <id>                   # its result, still there after it finished
verse data verse task instance '*' -trim '{status}'   # …or just where everything got to
```

```tony
lastAction: run_bash
lastOutput: summarizing
runner: local
status: done
vector: ASK
vectorBy: user:scott@localhost
```

The end of a task is a change to an entity, so a *rule* can react to it. That is how a flow of
several steps continues: through the store, not through a scheduler. §4 shows it.

### Steering

You address work in progress by what it is about. Steering is separate from observation, so a
steering command never waits behind a backlog of changes. Two acts stop work without ending it:

| Act | What it stops |
|---|---|
| **hold one run** | one task, at its next boundary |
| **suspend the work** | the work about a charter and a subject |

This section is about the choice between them.

```sh
verse flow                                     # the live tasks, and what is suspended
verse flow -subject 'demo.latch.*'             # …narrowed: * per segment
verse flow suspend -task <id>                  # hold that run at its next boundary
verse flow resume  -task <id>
verse flow say     -subject demo.latch.1 "hurry"
verse flow cancel  -task <id>                  # end that run (destructive)
```

A [flow](glossary.md#flow) is not a stored thing. It is a query over live tasks. For this reason,
`verse flow` lists nothing until something runs, and a `say` reports how many tasks it reached.
§4 gives you a task to steer.

Every verb takes flags to say what it acts on. A verb with no flags does not mean "everything":
`verse flow suspend` alone prints its usage and does nothing. [Control](control.md) gives all the
flags. [Deployment and steering](deployment-and-steering.md) explains why control never travels
on the change stream.

A [hold](glossary.md#hold) acts on one task only. The rule that started the task goes on firing.
To stop a rule, or all the work about a subject, and keep it stopped, use a
[suspension](glossary.md#suspension).

The next commands use [the seeded demo](#1-start), where `demo-review` holds `demo.pr.1` at a
gate. §2 removed the demo and seeded other entities at `demo.pr.*`, so first set the demo up
again:

```sh
./scripts/seed-entities.sh --clean
./scripts/seed-demo.sh
```

```sh
verse flow suspend -charter :demo-review -why "reading the review rule"     # baseline's rule, about every subject: its charter has no address
verse gate                                   # the question is withdrawn: nobody can answer it now
verse data demo pr 2 -put '{stage: open}'    # demo-review does not fire
verse flow -charter :demo-review             # held by you, why, since when, what it holds
verse flow resume -charter :demo-review     # both PRs are asked about, once, as they stand now
```

Verse does not replay what happened during the suspension. When you lift it, the rule is armed
again and acts on the world as it is then. While its work is suspended, a task holds at its next
boundary and says whose hold it is under. For this reason, `verse flow resume -task` does not
run a task past a suspension.

### Where a task runs is a seam

A task need not run on this verse. The same verbs drive three other kinds of task:

| Where the task runs | How you set it up |
|---|---|
| on a remote verse | `up -runner <url>` |
| behind a dial-in broker that a firewalled worker claims | `up -broker` |
| with a **person**, who does it by hand | `verse task do …` |

Verse drives all three through one handle, so nothing above changes.

These `up` flags choose for the whole verse. A single spawn can also choose for itself. Install
a [runner](glossary.md#runner) and name it on the spawn, and that work runs there. Every spawn
that names no runner goes to the default.

```sh
go run ./docs/reference/examples/runner -addr :9800   # a runner you can copy and change
verse runner add demo http://localhost:9800           # `reach: a runner` — the install probes it
verse task run -runner verse.runners.demo "fetch the data; retrain; report"
verse ps -runner verse.runners.demo
```

Runners are a subject of their own:

- [Runners](runners.md) covers how to install one, how to name it on a spawn, and what removing
  one does to the work on it.
- [Task control](task-control.md) describes the seam and the dial-in protocol.
- [External tasks](external-tasks.md) explains why the seam is at that point.
- [reference/tasks.md](reference/tasks.md) has the commands.

---

## 4. Charters — relating entities to tasks

A [charter](glossary.md#charter) is a list of [rules](glossary.md#rule). A rule says: when a
matching entity changes and the gates resolve, act on the entity. The action can patch it, spawn
a task about it, or react to what that task did. Rules relate entities and tasks by declaration.
Nothing else in verse authors a flow.

Every rule has the same four parts, and one sentence gives the whole firing rule:

> At each commit, if the cause has risen and the condition holds, fire once for **every
> entity `subject` selects** — one firing per entity, each with its own gate, its own task
> id, its own budget.

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
    subject: {path: "demo.patch.*", value: { stage: open }}
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

When a rule opens its observation, it replays what it reads. So a rule is a statement about the
world, not a subscription to future changes. You never need to touch an entity again to announce
a state it already holds. After a restart, verse reaches the same verdicts.

### latch — spawn a task, and react to it finishing

```tony
- name: shape-latch
  condition:
    subject: {path: "demo.latch.*", value: { stage: open }}
  action:
    patch:  { stage: reviewing }
    spawn:  { driver: scripted, vector: auto, prompt: "echo reviewing $subject; sleep 2" }
    onDone: { patch: { stage: reviewed } }
```

The action binds `$subject` and every field of the subject's payload. `onDone` is a completion
[hook](glossary.md#hook). When *that* task reaches `done`, the engine fires the hook about the
same subject. So the task step and its next step read top to bottom in one rule. `onFailed`,
`onCancelled` and `onOrphaned` are the other branches.

The important shape here is the **[latch](glossary.md#latch)**. The action moves `stage` past
`open`, so the rule does not select an entity again after it has acted on it. Verse considers a
rule with no latch at every commit that touches what the rule reads. Verse *refuses at install*
a spawning rule that cannot latch, because that rule is an infinite loop that costs money.

Two facts make the latch robust:

- Writing the same value twice produces no commit, so a write of the same value has no effect.
- A new firing about a subject that already has work running is not a second firing.

### ask — a human decides

```tony
  gates:
  - { name: approval, mode: ask, onDenied: { patch: { stage: rejected } } }
```

```sh
./scripts/charter-shapes.sh run ask
verse gate
# - gate: approval
#   id: verse.trigger.shape-ask.approval~c619ac44
#   mode: ASK
#   origin: shape-ask
#   reason: 'trigger shape-ask gate approval on demo.ask."1"'
#   spawns: shape-ask@demo.ask.1
#   subject: 'demo.ask."1"'
verse gate approve verse.trigger.shape-ask.approval~c619ac44 -as you@example.com
```

The fields of a [question](glossary.md#question) in the listing:

| Field | What it is |
|---|---|
| `gate` | the name of the [gate](glossary.md#gate-of-a-rule) |
| `origin` | the rule that raised it |
| `subject` | what the question is about |
| `spawns` | what a "yes" will start. Verse settles this before it asks, even though nothing runs yet. |

The id is a function of (trigger, gate, subject). Verse evaluates a rule again at every commit,
but the rule asks **once**. A firing that finds its question already standing joins it. It
does not post a copy. [§5](#5-gates-and-the-docket) covers the rest of gates.

`onDenied` is the other half of the answer, and it is easy to leave out. A denied firing writes
nothing. Without `onDenied`, the subject stays in the state that asked. The next commit that
touches the subject then asks the same question again, over the answer you already gave. The
`onDenied` action says what a "no" means, and that stops the repeat. Verse warns at install if a
gate has no `onDenied`.

### seq — steps in order

```tony
  action:
    seq:
    - spawn: { driver: scripted, vector: auto, prompt: "echo lint $subject" }
    - spawn:    { driver: scripted, vector: auto, prompt: "echo test $subject" }
      onFailed: { patch: { stage: broken } }
    onDone:
      patch: { stage: built }
```

`seq` sets the order of steps.

- Each element starts when the previous one completes.
- `onDone` on the action that carries the `seq` runs after the whole sequence.
- An `onFailed` on one element is an exit from the sequence.

There is no `par`. Independent rules that match the same condition already run in parallel.

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
    subject: {path: "demo.when.*", value: { stage: ready }}
    when:    {path: "ci.status.shapes", value: { state: green }}
```

The subject says what to act on. `when` says whether this is a moment to act. *While* CI is
green, every ready entity ships. An entity that becomes ready later is picked up too, because
verse evaluates the rule again at every commit that touches either subtree.

### on — a cause, not a condition

```tony
    on: {path: "ci.status.release", value: { state: green }}
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
- name: shapes-source
  level: 0
  match: demo
- name: shapes-derived
  level: 10
  match: derived
---
- name: shape-derive
  stratum: shapes-source
  condition:
    subject: {path: "demo.derive.*", value: { stage: open }}
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

Verse checks two rules at install. Neither check reads the store.

1. A rule's **subject** must be inside the rule's own stratum.
2. A **derive** must land in a strictly higher stratum.

A charter file may hold both strata and rules. `verse c -a` installs the strata first.

The *engine* chooses the address of the derived entity. The address is the address that the
derive names, then the rule, then the subject's own segments:
`derived.feature.shape-derive.demo.derive.1`. No two rules can address the same derived entity,
and a new firing writes the same address, not a second entity. An `id:` or `key:` beside `path:`
in the entry chooses the name under that address instead. A `!each` fan-out adds the key of the
element to the address and changes nothing else here. See
[strata](strata.md#each--when-one-answer-holds-many-facts). The engine also records what the
entity is about, at `verse.meta.derivedFrom`.

A charter that declares no strata is complete. Every other shape here has no `stratum:`, and
verse checks it against nothing. The one thing a rule with no stratum may not do is `derive`,
because "strictly above" has no answer for a rule with no level. See [strata](strata.md).

### Reading something the rule is not about

The `$references` of an action read the entity that the firing is about: `$stage`, `$subject`,
`$subject.value.head`. To copy from an entity that the firing is *not* about, name that entity:

```tony
action:
  patch:
    branch:   !get-path {at: [git, repo, $subject.value.head], path: branch}
    branches: !list-paths {at: "git.repo.*", path: branch}
```

The two tags are one grammar, split by how many nodes they return:

| Tag | Answer |
|---|---|
| `!get-path` | one node. It refuses an address that could name several. |
| `!list-paths` | a list. Globs belong here. |

Verse reads both at the commit that caused the firing. So a rule held at a gate for a day still
writes what verse evaluated it against. Both **copy and do not track**. Add an `on` if you want
to follow what you read. See
[the trigger model](reference/action.md#get-path-and-list-paths--reaching-another-entity).

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

A charter is durable. Each rule is an entity (`verse.trigger.rule.<name>`), and so is each
stratum (`verse.trigger.strata.<name>`). So when a verse starts again, it runs the program it
ran before it stopped. [The trigger model](trigger-model.md) describes the model. The reference
describes the fields:

- [the condition](reference/condition.md), with what a match can say;
- [the action](reference/action.md), with the answer contract a spawn can demand of an agent,
  and chains and ids;
- [the gates](reference/gates.md).

---

## 5. Gates and the docket

A gate is the autonomy dial for one action. Three settings answer immediately:

| Mode | What happens |
|---|---|
| `auto` | the action fires |
| `ask` | one person is asked ([§4](#ask--a-human-decides)) |
| `park` | the action is held |

A rule's gates are conjunctive. A deploy that needs a sign-off from security and from the
release manager has two `ask` gates.

Some questions need more than one person and more than one click. A gate can say how many
answers it needs:

```tony
  gates:
  - { name: approvals, need: 2 }
```

```sh
./scripts/charter-shapes.sh run vote
verse gates
```

```tony
- gate: approvals
  id: verse.trigger.shape-vote.approvals~e23d0b21
  mode: ASK
  options:
  - id: proceed
    need: 2
  - id: deny
    need: 1
  origin: shape-vote
  reason: 'trigger shape-vote gate approvals on demo.vote."1"'
  subject: 'demo.vote."1"'
  tool: verse:trigger:shape-vote
  where: engine
```

A gate always poses the same question. Here one option has a threshold: `proceed` needs two
ballots, and one `deny` still refuses. The listing is a document like every other. So
`verse gates -o yaml`, a read of one entry, and a diff against the
[docket](glossary.md#docket) of a minute ago all work.

The rule fired, and the action waits, on a count of ballots and not on one person. Answer the
question. Each answer names its answerer, because verse records who settled a question:

```sh
verse gate approve verse.trigger.shape-vote.approvals~e23d0b21 -as alice@example.com
# verse.trigger.shape-vote.approvals~e23d0b21: recorded: 1 of 2 for proceed
verse gate approve verse.trigger.shape-vote.approvals~e23d0b21 -as bob@example.com
# approved verse.trigger.shape-vote.approvals~e23d0b21
verse data demo vote 1              # stage: merged
```

The record then reads `status: answered` and `verdict: proceed`. Its `ballots` field names who
chose what, and when. The audit trail is on the entity, not in a line that scrolled past. An
answerer who changes their mind replaces their own ballot and does not cast a second one.

The question itself says who may cast a ballot. The owner of the charter answers what their rule
asked, and so does anyone the gate names:

```tony
  gates:
  - { name: approvals, need: 2, answerers: [oscar@example.com, group:payments] }
```

Verse refuses Ana, who is not named, and tells her so. Other cases:

- A gate that names nobody is open to every person. That is the default of an open verse.
- A worker that raises a gate of its own says who the gate is for with `task do gate -to`.
- Anyone who may answer may pass the question on instead, with `verse gate escalate <id>`. The
  question goes to the next [resolver](glossary.md#resolver) that matches its subject. A resolver
  holds questions out to a service or a person;
  [a service that answers them](#a-service-that-answers-them) shows one.

`need` over the answerers says whether one answer or many settle the question. Each answerer has
one ballot. `need: 1` is a race, and a `need` equal to the number of answerers needs all of them.
[Permissions](perms.md) covers who may do what.

A question is an ordinary entity (`verse.gate.question.<id>`). So the docket is a view of the
store, and a rule can react to a question like anything else. Verse checks the question against
the world while it stands. If the subject moves out of what the rule selects, the question is
moot. Verse withdraws it and writes the reason on the record.

A question also lasts when verse goes down. The record and its ballots are entities. The firing
that waits on them ends, and the next start gives it back. Verse evaluates the rule again. The
rule poses the same question again and finds the ballots already cast. A question that nothing
can answer any more is settled `orphaned`, with a `why`. Its rule left the charter, or its
subject is gone. Verse does not leave it on the docket as if it could be answered. See
[a question outlives the verse](reference/gates.md#a-question-outlives-the-verse-that-asked-it).

### A service that answers them

The docket is not only for people. A **resolver** is an installed record that holds questions
out to an **[answerer](glossary.md#answerer)**. It holds the questions of one run, or of a class
of subjects. An answerer is a service that reads the run's own stream and answers its gates.
It answers through the same path as `verse gate approve`, and `by` names the resolver instead of
a person.

A resolver can:

- pass a question up to the layer above;
- carry a deadline, which passes the question up when time runs out;
- be made the only answer that counts.

The same resolver can put a person, or a group, at that level instead of a service
(`method: {driver: person, who: oscar@example.com}`). A person passes a question up the ladder
with `verse gate escalate`.

```sh
./scripts/attached-resolver.sh              # five acts, one command each
./scripts/attached-resolver.sh run layered  # named → standing → ⊤, in two hops
```

[Attached resolvers](attached-resolvers.md) gives the argument.
[reference/resolvers.md](reference/resolvers.md) gives the surface.

### "No" is a real outcome

A vote you can only win is not a vote. A gate names its options when "yes and no, one each" is
not the question. A deadline says what silence means:

```tony
options: [{id: proceed, need: 2}, {id: deny, need: 2}]    # two rejects → deny
deadline: {within: "4h", default: deny}                    # the clock defaults it to deny
options: [{id: ship, need: 1}, {id: hold, need: 1}, {id: drop, need: 2}]   # not yes or no at all
```

Quote the duration. A tony literal may not start with a digit, and commas are optional. So
`within: 4h` reads as the number `4` beside a separate literal `h`. That is well-formed, and it
is not what you wrote. Verse refuses it at install and says so.

Waiting for a *fact* is not a gate. A rule that should act once CI is green says so in `when:`,
because that is what a condition is for.

### The same machinery answers *who*

A second question is in every `spawn`: who does the work? Replace a fixed `driver` with a
`provider` selection. The eligible [doers](glossary.md#doer) then become the options of the same
question that a gate poses. You answer by option
(`verse gate <id> -answer <doer> -as <who>`), in the same three ways:

| Mode | Who picks |
|---|---|
| `auto` | a rule picks |
| `ask` | one person picks |
| `vote` | the group picks |

Doers are ordinary entities. You add or retire a doer by writing entities, not by editing the
charter. A doer may only *narrow* the authority that the spawn granted.
[Provider selection](provider-selection.md) covers this subject.

---

## 6. Where to go next

Each topic below is a subject, not a step. Each one runs, and each page states its own limits.

**Choosing who acts** — [provider selection](provider-selection.md). A registry of doers as
entities, and selection by capability and by a match over attributes. A narrowing rule makes it
safe to select from a registry that anyone can write. *Left to do: richer voting
disciplines — ranked, weighted, secret.*

**Isolation** — [reference/connect.md](reference/connect.md#scopes). Every command takes
`-scope`. A [scope](glossary.md#scope) is a real copy-on-write overlay, and
`verse disconnect -scope` drops the whole scope. The writes of a scope to the world are copies.
Its steers, answers and removals reach what the scope created. Verse refuses them at what the
scope only sees ([what a scope owns](reference/connect.md#what-a-scope-owns)).
*Left to do: scopes as first-class, listable, durable things.*

**Your working tree, in the verse** — [reference/connect.md](reference/connect.md) and
[content federation](content-federation.md). `verse connect` federates a tree into its own
scope and spawns a validator for each file. It serves file contents through the store, so the
agent need not share a filesystem with you. *Left to do: file reads are truncated.*

**Remote runners, brokers, and human-executed work** —
[task control](task-control.md), [external tasks](external-tasks.md),
[reference/tasks.md](reference/tasks.md). *Left to do: the inbox long-poll.*

**Reflecting external systems** — [ios](io.md). An [io](glossary.md#io) brings Git, GitHub or
Linear into an owned address in the store. It takes a hint as a hint, and it reconciles by
polling. The source half is a program you deploy and install, as you do a runner:
`verse io add laptop <url> -root git`.

**Ordering what a charter writes** — [strata](strata.md). Declared levels, a `derive` that may
only climb, and the two install checks that follow. The page also states what strata do not
give you: a charter under strata still does not converge, and strata do not make it converge.
*Left to do: fencing a spawned agent's writes to its own level and above.*

**The store underneath** — [reference/store.md](reference/store.md) and
[change events](change-events.md). The deeper properties of verse come from the store:

- copy-on-write scopes;
- CAS writes;
- watches that are faithful to each edge;
- the entity, not the transaction, as the unit that verse reacts to.

**Running it somewhere** — [deployment and steering](deployment-and-steering.md) and the
[Kubernetes deploy](https://github.com/signadot/verse/blob/main/deploy/README.md).

You will come back to two flat references:
[the command surface](reference/README.md) and [the trigger model](trigger-model.md).

---

## Why it is this way

### Why a scripted task you type runs ungated

A gate asks about what you have not already authorized. The ungated `scripted` default is an
argument about **who addressed the request**. When you type a shell command, you authorized that
command. So the argument holds for what you type and nothing else. An `llm` task asks before
effects because the agent *chose* them.

### Why a spawning rule must latch

A rule with no latch is considered at every commit that touches what it reads. If that rule
spawns, it spawns again and again, and every task costs money. So verse refuses it at install.

### Why derive exists

A rule that acts only on its `subject` has firings that verse can check. But without `derive`,
a charter has one way to bring an entity into being at another address: spawn an agent and let
the agent write. That puts the write outside anything that analysis at install can see. `derive`
states that write in the charter, where install can check it against the strata.

### Why there is no par

A `seq` gives order. Parallel work needs no construct of its own, because independent rules that
match the same condition already run in parallel.
