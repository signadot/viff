# Tutorial

Five sections, each one you can drive. Two things exist in a verse: **entities**, which are what
the world is, and **tasks**, which are the work being done to it. Everything after §3 relates the
two.

Every section has a script that puts the world there in one command. For the shape of the system
read [the overview](overview.md). For the flat command surface, the config layers and the
per-topic detail this tutorial skips, read [the reference](reference/README.md).

---

## 1. Start

```sh
go build -o verse ./cmd/verse     # or: make bin
export VERSE_ADDR=localhost:9797  # where clients reach it (this is the default)
./verse up                        # engine + task runtime + store, in one process
```

Leave it running. Every other subcommand is a client, so open a second terminal. To see it
working before reading about it:

```sh
./scripts/seed-demo.sh     # a charter, the entities it reacts to, one gate waiting for you
```

`up` decides three things for the whole runtime, and the defaults need no attention now. The
**store** is a fresh docd on a temp dir; `--data <dir>` keeps it. The default **driver** is a real
llm agent when credentials are configured, and `scripted` — a prompt run as a shell command — when
they are not. An agent's **root** is an isolated temp dir it prints at start, which is why a file
an agent writes is not in your cwd.

All three, plus the config layers, the `o` binary `up` resolves to host its daemons, and the flags
for pointing at a store or task runner you already have, are in [reference/up.md](reference/up.md).

---

## 2. Entities

An entity is anything the flow refers to — a PR, a build, a person, a CI status — addressed by a
path and carrying a document. The path is three segments (system, kind, id) in the ordinary case,
and more where a charter keeps its own tree. That is the whole data model.

At a command you type those segments as separate words, `demo pr 1`; in a document they are
written as a kinded path, `demo.pr."1"`. One address.
[§Addressing](reference/entities.md#addressing) is the whole grammar, and this section reads
without it.

```sh
./scripts/seed-demo.sh --clean   # if you ran it above: its rules react to what is seeded next
./scripts/seed-entities.sh       # a dozen entities, three systems, nothing watching them
```

### Read

The path narrows by *where* an entity lives; `--if` narrows by *what it says*,
as a [tony match](https://signadot.github.io/tony-format/) against the payload:

```sh
verse data                                         # the whole verse, one document
verse data demo pr                                 # by where it lives
verse data demo pr 1                               # one entity, whole
verse data --at 'demo.pr.*.stage'                  # one field of each, keyed by id
verse data --if '{stage: open}'                    # by what it says
verse data --if '{stage: !or [open, merged]}'      # …an operator, not just equality
verse data --if '{urgent: true}' --if-at labels    # …rooted inside the payload
verse data demo pr --trim '{stage}'                # …and how much of each entity to print
verse data --paths demo pr                         # just the addresses
verse data --at demo.pr.1                          # …an address copied out of a document
```

A verse is one document, `{system: {kind: {id: payload}}}`, and `verse data` answers it at a path,
so the same address works at every depth and the answer is the subtree standing there. An id is a
key in it rather than a token inside a rendered `at:`, which is what `--paths` is for. A read, a
`--put` and a `--rm` all take the same address: two spellings of one grammar, steps and a kinded
path, laid out in [reference/entities §Addressing](reference/entities.md#addressing).

**An address is typed as steps**, which is what a shell makes easy: each step arrives as its own
word, so nothing needs escaping on the way in. `--paths` answers the same way, so a listing hands
straight back.

```sh
verse data demo pr a.b labels urgent           # steps: an id with a dot in it, unquoted
verse data --paths demo pr                     # demo pr a.b
verse data --paths demo pr | xargs -L1 verse data
```

One spelling in and out. A step is a whole word, so a dot inside one is part of the name. The
shell's own quoting appears only where a segment holds a space or a `*`, and it is single quotes,
which `xargs` understands.

**A positional is a step and nothing else.** `verse data demo.pr.1` names a system called
`demo.pr.1`, because a word cannot be both one segment and several. A whole address in one
token is taken where it is *marked*, which says which grammar applies instead of guessing it:

```sh
verse data --at demo.pr.1           # an address copied out of a document
verse data --at 'demo.pr.*.stage'   # …and the one spelling that carries a wildcard
```

Flags go on either side of the path — `verse data --paths demo pr` and
`verse data demo pr --paths` are the same command.

`--if-at` takes a dot-separated field path and roots `--if` there, so a predicate about a
subtree need not restate the shape above it: `--if-at labels --if '{urgent: true}'` is
`--if '{labels: {urgent: true}}'`, and it also reaches a subtree that is not an object. A
path that leads nowhere does not match.

The pattern language is tony's rather than verse's. `!or`, `!glob`, `!not`, `!and` and the rest are
[tony match operators](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop), and they
mean the same thing here, in a charter's `value`, and in an answer contract. Learn it once. What
verse adds is *where* a match may be asked, which is
[§4](#4-charters--relating-entities-to-tasks).

### Write

A put **merges**. Fields you do not name are left alone, which is why the answer is the
entity as it now stands rather than an echo of what you sent:

```sh
verse data demo pr 1 --put '{owner: dee}'        # changes the owner field, keeps the stage
verse data demo pr 1 labels urgent --put true   # one field, deep
verse data demo pr 1 labels urgent --rm         # and gone again
verse data demo scratch 1 --rm                  # the whole entity
```

A payload typed on the command line is read as tony, which is why `{stage: open}` needs no quotes
around either half and why commas are optional. Save it as `.json` or `.yaml` and the same command
reads that instead: they are dialects of one document model rather than three supported formats
([representation](reference/representation.md)).

A write can also refuse to happen. Each condition is checked *with* the write, in one step at
the store, so nothing can change in between:

```sh
verse data demo pr 1  --put '{stage: open}' --create   # only if nothing is there yet → refused
verse data demo pr 99 --put '{stage: open}' --update   # only if something is        → refused
# write refused: a condition did not hold

verse data demo pr 4 --put '{owner: dee}' --expect '{stage: merged}'   # only if it still looks like this
```

That is how two writers advance one entity without losing each other's fields: CAS spelled as a
predicate, so you say what you believe about the entity rather than which revision you read it at.
`--expect` is the same predicate language `--if` reads with, under a different flag because `--if`
on this command is the read's filter. A refused write leaves the entity alone and exits non-zero.

### Watch

```sh
verse watch --if '{stage: open}' demo
```

```tony
{op: set at: demo.pr.1 rev: 1 to: {labels: {area: auth urgent: true} owner: ana stage: open …}}
{op: set at: demo.pr.3 rev: 3 to: {labels: {area: store urgent: true} owner: ana stage: open …}}
{op: set ref: demo.pr.5 rev: 5 to: {labels: {area: engine} owner: cy stage: open …}}
```

Two things to read off that. It **replays**: a watch is level-triggered at the open, so what
already matches arrives before anything new does, and you never watch a stream whose beginning you
missed. And each change is one line, so a watch stays greppable. `to` is the entity's whole state
as of that commit rather than the delta, and `op` is `set` — created *or* updated — or `delete`.

Now, in another terminal, write to it and watch the line arrive:

```sh
verse data demo pr 2 --put '{stage: open}'
```

Then advance it past the filter, with `'{stage: reviewing}'`, and note that nothing arrives: the
filter is a predicate over the state rather than a subscription to the entity.

More on refs, deep writes, cursors and `--since` is in
[reference/entities.md](reference/entities.md).

---

## 3. Tasks

A task is a governed unit of work: an agent, a shell command, a remote worker, a person. Run one
directly:

```sh
verse task run --driver scripted "echo summarizing; sleep 3"   # prints a run-… id
verse task run --driver scripted --wait "echo done"            # …and block for the result
verse task
verse task get <id>                # status, prompt, driver, runner, result, pending gates
verse task say <id> "also mention the dial"        # re-prompt the agent mid-run
verse task vector <id> ask --as walter@example.com        # slide its autonomy (auto|ask|park)
verse task cancel <id>
```

`--driver` is explicit here because the default is not: with LLM creds configured a bare
`task run "echo done"` hands your sentence to an *agent* ([reference/up.md](reference/up.md#the-driver)).

`say` and `vector` are different levers. `say` gives the agent new instructions. `vector` swaps the
gate vector governing how much it must confirm, applied at its next boundary with no restart.

### The dial

The two drivers carry different vectors, and the difference is the whole point of a gate: it asks
about what you have **not** already authorized.

`verse task run --driver scripted` runs ungated, because its prompt *is* the shell command and you
authorized it by typing it. That is an argument about **who addressed the request**, so it holds
for what you type and nothing else. A charter spawning `driver: scripted` typed nothing, asks about
the command it is about to run, and says `vector: auto` when it means to run unattended. `llm` asks
before effects, because the agent *chose* them. Reads run freely; writing a file, running a command,
and delegating to a sub-agent block:

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
verse gate approve run-f3e31ed0390f~d1 --as you@example.com
```

`run --attach` spawns *and* streams the transcript, prompting you inline on a terminal
(`⏸ gate "write" (write_file) — approve? [y/N]`), while the same gate stays resolvable from
anywhere else by its id, since it is the same question either way. There is a rough edge in which
of the two rules applies when you hand verse a *command* and it satisfies it with an *agent*.
[reference/tasks.md](reference/tasks.md#the-rough-edge-who-you-addressed) states it, and
[authority](roadmap/authority.md) is where it gets fixed.

### A task is an entity too

This is the join between entities and tasks. Every task is mirrored into the store as
`verse.task.instance.<id>`, in verse's own half of the tree, since it is a record verse keeps about
its own running. So observing the fleet is observing entities:

```sh
verse watch verse task                        # the whole fleet's lifecycle
verse data verse task instance <id>      # its result, still there after it finished
verse data verse task --trim '{status}'         # …or just where everything got to
```

```tony
payload:
  lastAction: run_bash
  lastOutput: summarizing
  runner: local
  status: done
  vector: ASK
  vectorBy: user:walter@example.com
```

Because a completion is an entity change, a *rule* can react to it. That is how multi-step flows
close: through the store rather than through a scheduler, and it is §4.

### Steering

In-flight work is addressed by what it is about, and steering is out-of-band from observation, so
it never queues behind a backlog of changes. There are two ways to stop work without ending it, and
the fork between them is the whole of this section: **hold one run**, or **suspend the work** about
a charter and a subject.

```sh
verse flow                                     # the live tasks, and what is suspended
verse flow --subject 'demo.latch.*'            # …narrowed: * per segment
verse flow suspend --task <id>                 # hold that run at its next boundary
verse flow resume  --task <id>
verse flow say     --subject demo.latch.1 "hurry"
verse flow cancel  --task <id>                 # end that run (destructive)
```

A flow is not a stored thing. It is a query over live tasks, so `verse flow` lists nothing until
something is running, and a `say` reports how many tasks it reached. §4 gives you one to steer.
Every verb is addressed by flags, and none of them means "everything" when left out: `verse flow
suspend` on its own prints its usage and does nothing. See [control](control.md) for the flags
whole, and [deployment and steering](deployment-and-steering.md) for why control never rides the
change stream.

Holding a run is forgetful: it holds that task, and the rule that started it goes on firing. To
stop a rule — or everything about a subject — and keep it stopped, suspend it instead. Against [the
seeded demo](#1-start), where `demo-review` is holding `demo.pr.1` at a gate:

```sh
verse flow suspend --charter :demo-review --why "reading the review rule"   # baseline's rule, about every subject: its charter has no address
verse gate                                   # the question is withdrawn: nobody can answer it now
verse data demo pr 2 --put '{stage: open}'   # demo-review does not fire
verse flow --charter :demo-review            # held by you, why, since when, what it holds
verse flow resume --charter :demo-review    # both PRs are asked about, once, as they stand now
```

Nothing that happened meanwhile is replayed: the lift arms the rule again, and it acts on the world
as it is. A task, while its work is suspended, holds at its next boundary and says whose hold it is
under, so `verse flow resume --task` will not run it past a suspension.

### Where a task runs is a seam

It need not run here. The same verbs drive a task hosted on a remote verse (`up --runner <url>`),
one behind a dial-in broker that a firewalled worker claims (`up --broker`), and one a **person**
executes by hand (`verse task do …`). Verse drives all three through one handle, so nothing above
changes.

Those choose for the whole verse. A single spawn can also choose for itself: install a runner, name
it, and that work runs there, while every spawn that names none goes to the default.

```sh
go run ./docs/reference/examples/runner -addr :9800   # a runner you can copy and change
verse runner add pool.demo http://localhost:9800      # `reach: a runner` — the install probes it
verse task run --runner verse.runners.pool.demo "fetch the data; retrain; report"
verse ps --runner verse.runners.pool.demo
```

That is its own subject. [Runners](runners.md) covers installing one, naming it on a spawn, and
what removing one does to the work on it. [Task control](task-control.md) is the seam and the
dial-in protocol, [external tasks](external-tasks.md) is why the seam is cut there, and
[reference/tasks.md](reference/tasks.md) has the commands.

---

## 4. Charters — relating entities to tasks

A **charter** is a list of rules. A rule says: when a matching entity changes and the gates
resolve, act on it — patch it, spawn a task about it, react to what that task did. Entities and
tasks, related declaratively. Nothing else in verse authors a flow.

Every rule has the same four parts, and the whole firing rule is one sentence:

> At each commit, if the cause has risen and the condition holds, fire once for **every
> entity `subject` selects** — one firing per entity, each with its own gate, its own task
> id, its own budget.

What varies between rules is which parts are present. Each variation is one command:

```sh
./scripts/charter-shapes.sh                  # list them
./scripts/charter-shapes.sh show    <shape>  # the rule, in tony
./scripts/charter-shapes.sh run     <shape>  # install it, then make it fire
./scripts/charter-shapes.sh install <shape>  # …or the halves apart, which is its own lesson
./scripts/charter-shapes.sh drive   <shape>
./scripts/charter-shapes.sh clean            # remove every shape and what it acted on
```

Each shape acts on its own kind, `demo.latch.*` or `demo.seq.*`, so several can be installed at
once without arguing over an entity.

### patch — a rule need not spawn anything

```tony
- name: shape-patch
  condition:
    subject: {path: [demo, patch, *], value: { stage: open }}
  action:
    patch: { stage: triaged }
```

```sh
./scripts/charter-shapes.sh run patch
verse data demo patch 1     # stage: triaged
```

The action has no target of its own: it acts on the condition's `subject`.

A rule also acts on the **backlog**, which is worth seeing rather than being told:

```sh
./scripts/charter-shapes.sh remove patch          # nothing is watching
verse data demo patch 2 --put '{stage: open}'
verse data demo patch 2                    # stage: open — nobody reacted
./scripts/charter-shapes.sh install patch
verse data demo patch 2                    # stage: triaged — installing it acted
```

A trigger's observation replays what it reads when it opens, so a rule is a statement about the
world rather than a subscription to future changes. You never have to re-touch an entity to
announce a state it has held all along, and a restart re-reaches the same verdicts.

### latch — spawn a task, and react to it finishing

```tony
- name: shape-latch
  condition:
    subject: {path: [demo, latch, *], value: { stage: open }}
  action:
    patch:  { stage: reviewing }
    spawn:  { driver: scripted, vector: auto, prompt: "echo reviewing $subject; sleep 2" }
    onDone: { patch: { stage: reviewed } }
```

`$subject`, and every field of the subject's payload, is bound in the action. `onDone` is a
completion hook: when *that* task reaches `done`, the engine fires the hook about the same subject,
so the task step and its follow-up read top-to-bottom in one rule. `onFailed`, `onCancelled` and
`onOrphaned` are the other branches.

The shape that matters here is the **latch**. The action advances `stage` past `open`, so an entity
it has acted on is not selected again. A rule with no latch is considered at every commit touching
what it reads, and a spawning rule that cannot latch is *refused at install*, because that is an
infinite loop with a bill attached.

Two things keep that from being fragile. Writing the same value twice produces no commit, so a
set-to-the-same-value is inert, and a re-firing about a subject that already has work running is not
a second firing.

### ask — a human decides

```tony
  gates:
  - { name: approval, mode: ask, onDenied: { patch: { stage: rejected } } }
```

```sh
./scripts/charter-shapes.sh run ask
verse gate
# - gate: approval
#   id: trigger.shape-ask.approval~093e7c94
#   mode: ASK
#   origin: shape-ask
#   reason: "trigger shape-ask gate approval on demo.ask.1"
#   spawns: shape-ask@demo.ask.1
#   subject: demo.ask.1
verse gate approve trigger.shape-ask.approval~093e7c94 --as you@example.com
```

`gate` is the gate's own name, `origin` the rule that raised it, `subject` what it is about, and
`spawns` what a "yes" will start — settled before the question is asked, even though nothing is
running yet. The id is a function of (trigger, gate, subject), so a rule re-evaluated at every
commit asks **once**: a firing that finds its question already standing joins it instead of posting
a copy. [§5](#5-gates-and-the-docket) is the rest of gates.

`onDenied` is the other half of the answer, and the one that is easy to leave out. A denied firing
writes nothing, so without it the subject stays in the state that asked, and the next commit that
touches it asks the same question again, over the answer you already gave. Saying what a no means is
what stops that, and verse warns at install if you do not.

### A service that answers them

The docket is not only for people. A **resolver** is a service bound to a run, or to a class of
subjects, that reads the run's own stream and answers its gates, through the same path
`verse gate approve` takes, with `by` naming the binding instead of a person. It can hand a question
up a layer, carry a deadline that does the same thing more slowly, and be made the only answer that
counts. The same binding can put a person, or a group, on that rung instead of a service
(`method: {driver: person, who: oscar@example.com}`), and a person climbs the ladder with
`verse gate escalate`.

```sh
./scripts/attached-resolver.sh              # five acts, one command each
./scripts/attached-resolver.sh run layered  # named → standing → ⊤, in two hops
```

[Attached resolvers](attached-resolvers.md) is the argument;
[reference/resolvers.md](reference/resolvers.md) is the surface.

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

`seq` is the depth axis. Each element starts when the previous one completes, `onDone` on the
action carrying it means "after the whole sequence", and a per-element `onFailed` is an off-ramp.
There is deliberately no `par`: breadth is what independent rules matching the same condition
already do.

Ask what actually happened, which is where the charter and the runtime are read through one
address:

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
      prompt: "echo lint demo.seq.1"
    - chain: shape-seq.seq[1]
      children:
      - chain: shape-seq.seq[1].onFailed
        status: skipped
      status: done
    - chain: shape-seq.onDone
      kind: onDone
      status: applied
  subject: demo.seq.1
```

Each node is one action in the charter, addressed by its `chain`, the position of that action
inside its rule, and that same string is the id the task spawns under
(`shape-seq.seq.0@demo.seq.1`). The tree is not a second model of the work. It is the charter and
the runtime read together. Statuses that come from no task are the point: `applied` fired but only
set fields, `skipped` is a branch its parent's outcome did not answer to, `pending` may still fire,
and `failed`, with the reason beside it in `err`, is that same set-only branch when its write did
not land.

### when — a condition on another entity

```tony
  condition:
    subject: {path: [demo, when, *], value: { stage: ready }}
    when:    {path: [ci, status, shapes], value: { state: green }}
```

The subject says what to act on; `when` says whether this is a moment to act. *While* CI is green,
every ready entity ships, and one arriving afterwards is picked up, because the rule is re-evaluated
at every commit touching either subtree.

### on — a cause, not a condition

```tony
    on: {path: [ci, status, release], value: { state: green }}
```

Same two entities, a different sentence: fire **at the moment** CI went green, over whatever was
waiting *then*. `on` is asked at the previous commit and at this one, and fires on the rising
edge:

```sh
./scripts/charter-shapes.sh run on
verse data demo on 1                        # stage: released

verse data demo on 2 --put '{stage: waiting}' # CI is still green…
verse data demo on 2                        # …and this one stays `waiting`
```

Two properties are the point of choosing `on` over `when`. **An edge does not re-fire while it
stays true**: the predicate never fell, so `demo.on.2` waits for CI to go red and green again. And
an `on` rule **does not act on the backlog**, for the same reason, since a predicate that already
holds never rose.

When you mean "whenever this holds", write `when`. The engine warns at install when a narrow `on`
leaves part of what the rule reads uncovered, which is the case above.

### derive — writing something that is not the subject

Every rule so far acts on its `subject` and nothing else. That is what makes a firing checkable,
but on its own it leaves a charter one way to bring an entity into being *elsewhere*: spawn an
agent and let it write, which puts the write beyond anything install-time analysis can see.

`derive` is that write, said in the charter. Because a charter that can write anywhere can write in
a circle, it comes with the thing that bounds it: a **stratum**, a declared level.

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
    subject: {path: [demo, derive, *], value: { stage: open }}
  action:
    patch: { stage: raised }
    derive:
      derived:
        feature:
          priority: urgent
          about: $subject
```

```sh
./scripts/charter-shapes.sh run derive
verse charter                                                  # the ordering
verse data derived feature shape-derive@demo.derive.1
```

Two rules, checked at install and neither reading the store: a rule's **subject** must be
inside its own stratum, and a **derive** must land in a strictly higher one. A charter file may
hold both strata and rules — `verse c -a` installs the ordering first.

The derived entity's id is the *engine's*, a function of (rule, subject), so the rule name and the
subject are both in it. No two rules can address the same derived entity, and a re-fire writes the
same address rather than a second thing. A `!each` fan-out adds the element's key to that address
and changes nothing else here; see [strata](strata.md#each--when-one-answer-holds-many-facts). The
engine also records what it is about, at `verse.meta.derivedFrom`.

Declaring nothing is a complete answer. Every other shape here has no `stratum:` and is checked
against nothing. The one thing an unstratified rule may not do is `derive`, because "strictly
above" has no answer for a rule with no level. See [strata](strata.md).

### Reading something the rule is not about

An action's `$references` read the entity the firing is about: `$stage`, `$subject`,
`$subject.value.head`. To copy from an entity it is *not* about, name one:

```tony
action:
  patch:
    branch:   !get-path {at: [git, repo, $subject.value.head], path: branch}
    branches: !list-paths {at: [git, repo, "*"], path: branch}
```

One grammar split by cardinality. `!get-path` answers with one node and refuses anything that could
name several; `!list-paths` answers with a list and is where the globs live. Both are read at the
commit that caused the firing, so a rule held at a gate for a day still writes what it was evaluated
against. Both **copy rather than track**, so add an `on` if you want to follow the thing you read.
See [the trigger model](trigger-model.md#get-path-and-list-paths--reaching-another-entity).

### The rest of the grammar

Rules install and read back in tony, so inspecting one is a round trip rather than a second
format:

```sh
verse c -a -f charter.tony # or: cat charter.tony | verse c -a -f -
verse charter
verse c rules shape-latch
verse c -r rules shape-latch
verse charter                     # and `verse c rules <name>` for one of them
```

A charter is durable. Each rule is an entity (`verse.trigger.rule.<name>`) and so is each stratum
(`verse.trigger.strata.<name>`), so the program a verse comes back up running is the one it went
down with. Every field, both grammars, the answer contract a spawn can demand of an agent, chains
and ids, and what a match can say are in [the trigger model](trigger-model.md).

---

## 5. Gates and the docket

A gate is the autonomy dial, per action. Three settings answer immediately: `auto` fires, `ask`
asks one person ([§4](#ask--a-human-decides)), `park` holds. A rule's gates are conjunctive, so a
deploy needing both a security and a release-manager sign-off is two `ask` gates.

Some questions are not one person's to answer in one click. A gate can say how many it takes:

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

It is the same question every gate poses, with a threshold on one option: `proceed` takes two
ballots, and one `deny` still refuses. The listing is a document like every other, so `verse
gates -o yaml`, a read into one entry, and a diff against the docket a minute ago all work.

The rule fired and the action is waiting, not on a person but on a tally. Answer it, each answer
naming its answerer, since a question is settled attributably:

```sh
verse gate approve verse.trigger.shape-vote.approvals~e23d0b21 --as alice@example.com
# verse.trigger.shape-vote.approvals~e23d0b21: recorded: 1 of 2 for proceed
verse gate approve verse.trigger.shape-vote.approvals~e23d0b21 --as bob@example.com
# approved verse.trigger.shape-vote.approvals~e23d0b21
verse data demo vote 1              # stage: merged
```

The record then reads `status: answered`, `verdict: proceed`, and `ballots` naming who chose what
and when: the audit trail on the entity rather than a line that scrolled past. An answerer who
changes their mind replaces their own ballot rather than casting a second.

Who may cast one is the question's own to say. The charter's owner answers what their rule
asked, and so does anyone the gate names:

```tony
  gates:
  - { name: approvals, need: 2, answerers: [oscar@example.com, group:payments] }
```

Ana, not named, is refused, and told so. A gate that names nobody is held out to every person,
which is the open verse's default; a worker raising a gate of its own says who it is for with
`task do gate --to`; and whoever may answer may hand the question on instead, `verse gate
escalate <id>`, to the next binding covering its subject. `need` over the answerers is what one
or many means: one ballot each, `need: 1` a race, `need` equal to their number all of them.
[Permissions](perms.md) is the whole of who may do what.

A question is an ordinary entity (`verse.gate.question.<id>`), so the docket is a view of the
store, and a rule can react to a question like anything else. It is checked against the world
while it stands: if the subject moves out of what the rule selects, the question is moot and is
withdrawn, with the reason on the record, rather than carried.

It also survives verse going down. The record and its ballots are entities. What dies is the
firing waiting on them, and a start gives it back: the rule is re-evaluated, re-poses the same
question, and finds the ballots already cast. A question nothing can answer any more — its rule
left the charter, its subject is gone — is settled `orphaned`, with a `why`, rather than left on
the docket looking answerable. See
[a question outlives the verse](trigger-model.md#a-question-outlives-the-verse-that-asked-it).

### "No" is a real outcome

A vote you can only win is not a vote. A gate names its options when yes and no at one each is
not the question, and a deadline says what silence means:

```tony
options: [{id: proceed, need: 2}, {id: deny, need: 2}]    # two rejects → deny
deadline: {within: "4h", default: deny}                    # the clock defaults it to deny
options: [{id: ship, need: 1}, {id: hold, need: 1}, {id: drop, need: 2}]   # not yes or no at all
```

Quote the duration. A tony literal may not start with a digit and commas are optional, so
`within: 4h` reads as the number `4` beside a separate literal `h`: well-formed, and not what you
wrote. Verse refuses it at install and says so.

Waiting for a *fact* is not a gate. A rule that should act once CI is green says so in `when:`,
which is what a condition is for.

### The same machinery answers *who*

The second question hides in every `spawn`: who does the work. Replace a fixed `driver` with a
`provider` selection and the eligible doers become the options on the same question a gate poses,
answered by option (`verse gate <id> --answer <doer> --as <who>`) the same three ways: `auto` picks
by rule, `ask` lets one person pick, `vote` lets the group. Doers
are ordinary entities, so you add or retire them by writing entities rather than by editing the
charter, and a doer may only ever *narrow* the authority the spawn granted. That is its own
subject: [provider selection](provider-selection.md).

---

## 6. Where to go next

Everything below is a subject rather than a step. Each of these runs, and each says where its own
corners are.

**Choosing who acts** — [provider selection](provider-selection.md). A registry of doers as
entities, selection by capability and a match over attributes, and the narrowing rule that
makes a registry anyone can write safe to select from. *Left to do: richer voting
disciplines — ranked, weighted, secret.*

**Isolation** — [reference/connect.md](reference/connect.md#scopes) and
[scope lifecycle](roadmap/scope-lifecycle.md). Every command takes `--scope`; a scope is a
real copy-on-write overlay, and `verse disconnect --scope` drops the whole thing. Its writes to
the world are copies; its steers, answers and removals reach what the scope created and are
refused at what it merely sees ([what a scope owns](reference/connect.md#what-a-scope-owns)).
*Left to do: scopes as first-class, listable, durable things.*

**Your working tree, in the verse** — [reference/connect.md](reference/connect.md) and
[content federation](content-federation.md). `verse connect` federates a tree into its own
scope and spawns a validator per file, serving contents through the store so the agent need
not share a filesystem with you. *Left to do: file reads are still truncated.*

**Remote runners, brokers, and human-executed work** —
[task control](task-control.md), [external tasks](external-tasks.md),
[reference/tasks.md](reference/tasks.md). *Left to do: the inbox long-poll.*

**Reflecting external systems** — [sources](sources.md). Git, GitHub, Linear into an owned
slice of the verse, on hint-as-hint plus poll-reconcile — and a source is a program you deploy
and install, the way a runner is: `verse source add git.laptop <url>`.

**Ordering what a charter writes** — [strata](strata.md). Declared levels, a `derive` that may
only climb, and the two install checks that follow — plus a plain statement of what it does not
buy, since a charter under strata still does not converge and was never going to. *Left to do:
fencing a spawned agent's writes to its own level and above, which is
[authority](roadmap/authority.md)'s.*

**The store underneath** — [reference/store.md](reference/store.md) and
[change events](change-events.md). Where verse's deeper properties come from: copy-on-write
scopes, CAS writes, edge-faithful watches, and the entity — not the transaction — as the
reactive grain.

**Running it somewhere** — [deployment and steering](deployment-and-steering.md) and the
[Kubernetes deploy](https://github.com/signadot/verse/blob/main/deploy/README.md).

And the two flat references you will come back to:
[the command surface](reference/README.md) and [the trigger model](trigger-model.md).
