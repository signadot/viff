# Tutorial

This tutorial has four sections and a list of where to go next, and you can run every command in
them. A verse holds two
kinds of thing:

| Thing | What it is |
|---|---|
| an [entity](glossary.md#entity) | a part of the world, such as a pull request or a CI status |
| a [task](glossary.md#task) | work that is done to the world |

Every section has a script that sets up its world in one command. The rules that relate entities
to tasks are a [charter](glossary.md#charter): [the introduction to charters](charter-intro.md)
shows the shapes a rule takes, and [Write a charter](build.md) writes one. For the shape of the system, read [the overview](overview.md). For the
full command surface and the detail of each topic, read [the reference](reference/README.md).

1. [Start](#1-start)
2. [Entities](#2-entities)
3. [Tasks](#3-tasks)
4. [Gates and the docket](#4-gates-and-the-docket)
5. [Where to go next](#5-where-to-go-next)

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

Toward a verse on your own machine, the CLI acts as `user:<login>@localhost`, and that user owns
what you write. [The permissions walk](perms-guide.md#before-the-first-step-who-you-are) shows
how to name yourself.

`up` picks a store, a default driver and a root for agents. The defaults need no attention now.
One matters later: an agent works in an isolated temp dir, so a file that it writes is not in
your cwd. [reference/up.md](reference/up.md) describes all three, and the flags that point verse
at a store or a task runner you already have.

---

## 2. Entities

An entity is anything the flow refers to, such as a PR, a build, a person or a CI status. An
entity has an [address](glossary.md#address) and carries a document. That is the whole data
model.

At a command you type the segments of an address as separate words: `demo pr 1`. In a document
the same address is written as a kinded path: `demo.pr."1"`.
[Addressing](reference/entities.md#addressing) gives the whole grammar.

```sh
./scripts/seed-demo.sh --clean   # if you ran it above: its rules react to what is seeded next
./scripts/seed-entities.sh       # a dozen entities, three systems, nothing watching them
```

### Read

The address narrows a read by *where* an entity lives. `-if` narrows it by *what the entity
says*, with a [tony match](https://signadot.github.io/tony-format/) against the payload.

```sh
verse data                                         # the whole verse, one document
verse data demo pr '*'                             # by where it lives
verse data demo pr 1                               # one entity, whole
verse data demo pr '*' -if '{stage: open}'         # by what it says
verse data demo pr '*' -if '{stage: !or [open, merged]}'  # …an operator, not just equality
verse data demo pr '*' -trim '{stage}'             # …and how much of each entity to print
verse data -paths demo pr '*'                      # just the addresses
verse data -paths demo pr '*' | xargs -L1 verse data     # …passed straight back
verse data -at 'demo.pr.*.stage'                   # a whole address in one token goes after -at
```

Each step of an address is its own shell word, so nothing needs escaping. A positional is a step
and nothing else: `verse data demo.pr.1` names one segment called `demo.pr.1`.

The match operators (`!or`, `!glob`, `!not` and the others) belong to tony. They mean the same
thing in a read, in a charter and in an answer contract.
[Reading](reference/entities.md#reading) gives the rest of the read surface.

### Write

A put **merges**. Fields that you do not name stay as they are. Verse answers with the entity as
it stands after the write:

```sh
verse data demo pr 1 -put '{owner: dee}'         # changes the owner field, keeps the stage
verse data demo pr 1 labels urgent -put true    # one field, deep
verse data demo pr 1 labels urgent -rm          # and gone again
```

A write can carry a condition. The store checks it with the write, in one step:

```sh
verse data demo pr 1  -put '{stage: open}' -create     # only if nothing is there yet → refused
verse data demo pr 4 -put '{owner: dee}' -expect '{stage: merged}'     # only if it still looks like this
```

[Writing](reference/entities.md#writing) and
[conditional writes](reference/entities.md#conditional-writes) give the detail.

### Watch

```sh
verse watch -if '{stage: open}' demo pr '*'
```

```tony
{at: 'demo.pr."1"' op: set rev: 97 to: {labels: {area: auth urgent: true} owner: ana stage: open …}}
{at: 'demo.pr."5"' op: set rev: 97 to: {labels: {area: engine} owner: cy stage: open …}}
```

The watch **replays**: what already matches arrives before anything new. Each change is one
line, and `to` is the whole state of the entity at that commit. In another terminal, run
`verse data demo pr 2 -put '{stage: open}'` and see the line arrive. Then move it to
`'{stage: reviewing}'`. Nothing arrives, because the filter is a predicate over the state.
[reference/entities.md](reference/entities.md) covers cursors, `-since` and the rest.

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

These commands give `-driver` explicitly. With LLM credentials configured, a bare
`task run "echo done"` gives your sentence to an *agent*
([the driver](reference/up.md#the-driver)). `say` gives the agent new instructions. `vector`
replaces the [vector](glossary.md#vector), the list of gates that says how much the task must
confirm, at the task's next boundary.

### The dial

A gate asks about what you have **not** already authorized. `verse task run -driver scripted`
runs ungated, because you typed the command. An `llm` task asks before effects: reads run
freely, and writing a file, running a command and delegating block. A charter that spawns
`driver: scripted` typed nothing, so its task asks too, unless the charter says `vector: auto`.

```sh
verse task run 'Write a file hello.txt containing "hi".'
verse question
# - gate: write
#   id: run-f3e31ed0390f~d1
#   tool: write_file
verse question approve run-f3e31ed0390f~d1 --as you@example.com
```

`run -attach` streams the transcript and asks you inline. The same gate can be answered from
anywhere by its id. [The dial](reference/tasks.md#the-dial) gives the vectors in full.

### A task is an entity too

Here entities and tasks join. Verse mirrors every task into the store as
`verse.task.instance.<id>`, so to observe tasks you observe entities:

```sh
verse watch verse task instance '*'                   # the whole fleet's lifecycle
verse data verse task instance '*' -trim '{status}'   # …or just where everything got to
```

The end of a task is a change to an entity, so a *rule* can react to it. That is how a flow of
several steps continues: through the store. [The introduction to charters](charter-intro.md) shows it.

### Steering

You address work in progress by what it is about. Two acts stop work without ending it: a
[hold](glossary.md#hold) stops one run at its next boundary, and a
[suspension](glossary.md#suspension) stops the work about a charter and a subject.

```sh
verse flow                                     # the live tasks, and what is suspended
verse flow -subject 'demo.latch.*'             # …narrowed: * per segment
verse flow suspend -task <id>                  # hold that run at its next boundary
verse flow resume  -task <id>
verse flow say     -subject demo.latch.1 "hurry"
verse flow cancel  -task <id>                  # end that run (destructive)
```

A [flow](glossary.md#flow) is a query over live tasks, so `verse flow` lists nothing until
something runs. To suspend a rule, set [the demo](#1-start) up again (§2 replaced its entities):

```sh
./scripts/seed-entities.sh --clean
./scripts/seed-demo.sh
verse flow suspend -charter :demo-review -why "reading the review rule"
verse question                               # the question is withdrawn: nobody can answer it now
verse data demo pr 2 -put '{stage: open}'    # demo-review does not fire
verse flow -charter :demo-review             # held by you, why, since when, what it holds
verse flow resume -charter :demo-review      # both PRs are asked about, once, as they stand now
```

Verse does not replay what happened during the suspension. [Control](control.md) gives the
flags and what a lift does. A task can also run on a remote verse, on a runner you install, or
with a person who does it with `verse task do`. The same verbs steer it
([runners-walk.md](platform/runners-walk.md)).

---

## 4. Gates and the docket

A [rule](glossary.md#rule) in a charter acts when an entity matches it, and its gates decide
whether it may ([the introduction to charters](charter-intro.md)). A gate is the autonomy dial for
one action: `auto` fires, `ask` asks one person, and `park` holds. A rule's gates are conjunctive. A gate can also say how many answers it needs, as
`need: 2` does in the `vote` shape:

```sh
./scripts/charter-shapes.sh run vote
verse question
```

```tony
- gate: approvals
  id: verse.trigger.shape-vote.approvals~e23d0b21
  options: [{id: proceed, need: 2}, {id: deny, need: 1}]
  subject: 'demo.vote."1"'
```

`proceed` needs two ballots, and one `deny` still refuses. The listing is a document, so
`verse question -o yaml` and a diff against the [docket](glossary.md#docket) of a minute ago
(`verse question --all`) both work. Each answer names its answerer:

```sh
verse question approve verse.trigger.shape-vote.approvals~e23d0b21 --as alice@example.com
# verse.trigger.shape-vote.approvals~e23d0b21: recorded: 1 of 2 for proceed
verse question approve verse.trigger.shape-vote.approvals~e23d0b21 --as bob@example.com
# approved verse.trigger.shape-vote.approvals~e23d0b21
verse data demo vote 1              # stage: merged
```

The record then reads `verdict: proceed`, and its `ballots` say who chose what. The owner of the
charter may answer, and so may anyone the gate names in `answerers`
([who may answer](reference/gates.md#who-may-answer)).

A question is an [ask](glossary.md#ask): an ordinary entity at `verse.ask.<id>`, so a rule can
react to it, and it lasts when verse goes down. A **resolver** is an installed service that
answers asks for whoever grants it, through the same path as `verse question approve`
([resolvers](attached-resolvers.md)). [Asking](asking.md) covers raising an ask with `verse ask`.

### "No" is a real outcome

A gate names its options when "yes and no, one each" is not the question. A deadline says what
silence means:

```tony
options: [{id: proceed, need: 2}, {id: deny, need: 2}]    # two rejects → deny
deadline: {within: "4h", default: deny}                    # the clock defaults it to deny
options: [{id: ship, need: 1}, {id: hold, need: 1}, {id: drop, need: 2}]   # not yes or no at all
```

Quote the duration. Unquoted, `4h` reads as the number `4` beside a literal `h`, and verse
refuses it at install ([a deadline settles](reference/gates.md#a-deadline-settles)).

### The same machinery answers *who*

A `spawn` also asks who does the work. Replace a fixed `driver` with a `provider` selection, and
the eligible [doers](glossary.md#doer) become the options of the same question. You answer by
option (`verse question <id> --answer <doer> --as <who>`), and `auto`, `ask` or a vote picks.
Doers are ordinary entities, and a doer may only *narrow* the authority that the spawn granted
([provider selection](provider-selection.md)).

---

## 5. Where to go next

- **Isolation and your working tree**: [scopes](reference/connect.md#scopes) and
  [content federation](content-federation.md).
- **Remote runners and human work**: [task control](task-control.md) and
  [external tasks](external-tasks.md).
- **External systems**: [ios](io.md) bring Git, GitHub or Linear into the store.
- **The store underneath**: [reference/store.md](reference/store.md) and
  [change events](change-events.md).
- **Running it somewhere**: [deployment and steering](deployment-and-steering.md).

The two flat references are [the command surface](reference/README.md) and
[the trigger model](trigger-model.md). The reasons for this design are in
[design/tutorial.md](design/tutorial.md).
