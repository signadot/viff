# Glossary

This page defines each term of viff once. The terms are grouped by subject, and an
alphabetical index is at the end.

Where one thing has several names, the entry lists the others under "Also written". Where one
name means several things, each meaning has its own entry, with a qualifier in parentheses.

## The store

### store

The store is the one tree in which a viff instance keeps its data. It is durable, and it can be
watched. One store is shared by everyone in the viff instance, people and agents, and every commit to it
names who made it.

Also written: entity store, entity-viff, the viff.

See [The store](reference/store.md).

### address

An address names one position in the store. It is a list of segments, of any length, such as
`demo.pr.1`.

Also written: path, ref (in the Go source), `at` (the field and the flag), place (an address
that somebody writes at, or owns).

See [Data](reference/entities.md#addressing).

### kinded path

A kinded path is an address written as one string, with dots between the segments. Quotes
keep a dot inside a segment: `demo.pr."a.b"` has three segments.

Also written: kpath.

See [Data](reference/entities.md#addressing).

### step

A step is one segment of an address, typed as one word on the command line:
`viff data demo pr 1`.

See [Data](reference/entities.md#addressing).

### pattern

A pattern is an address in which `*` stands for any one segment, such as `demo.pr.*`. It
names every node that it reaches. A pattern may also hold a [descent](#descent), `..`.

See [Data](reference/entities.md#addressing).

### descent

A descent is `..` in a pattern. It reaches every depth beneath where it stands, the node itself
included: `viff data demo '..' stage` reads every `stage` beneath `demo`. Reads, watches and the
subjects of rules take a descent. A write, a claim, a grant and a permissions record each name one
place, and refuse it.

See [Data](reference/entities.md#-every-depth-beneath).

### node

A node is what stands at an address: an object, an array, a scalar or a null.
`demo.pr.1.stage` is a node inside the node `demo.pr.1`.

See [Data](reference/entities.md#what-stands-at-a-path).

### payload

The payload of a node is the value that the node holds, such as `{stage: open}`. It may be
any value.

See [Data](reference/entities.md).

### data

Data is what viff knows about the world: a pull request, a CI run, a person, a running task.
Each piece of data stands at an [address](#address) and holds a [payload](#payload).
`viff data demo pr 1` reads the data at `demo.pr.1`, and
`viff data demo pr 1 -put '{stage: open}'` writes it. A rule reacts to a change in data
whoever made it: a person, an iod, a rule or viff itself.

See [Data](reference/entities.md).

### entity

An entity is the data at one address: the subject of a rule, a row of a listing, an event of a
watch. "Entity" is the word of the code (`entity.Entity`). The docs say [data](#data).

See [Data](reference/entities.md).

### root (of the store)

The root of the store is the node above every other node. Its address is empty. Its
[record](#record) says who may create a node at the top: `viff perms --root` shows it.

In a page about permissions, "the root" has this meaning.

See [Permissions](perms.md#the-top-of-the-tree).

### viff's own half

Viff's own half is the part of the store under the first segment `mirror`, such as
`mirror.task.instance.review-1`. Viff keeps its own records there.

See [Permissions](perms.md#viffs-own-half).

### commit

A commit is one atomic change to the store. It names every address that it changed.

See [Change events](change-events.md#one-commit-many-entities-the-entity-is-the-reactive-grain).

### revision

A revision is the number of a commit. Revisions grow, and the numbers have gaps, so a reader
uses one only as a cursor: `viff watch -since 42`.

Also written: rev, cursor (of the store).

See [Data](reference/entities.md#watching).

### scope

A scope is a named copy-on-write view of the store: `viff data -scope me demo x 1`. A write
lands in the scope only. A read sees the scope on top of the rest of the store.

See [Scopes](reference/scopes.md#scopes).

### baseline

Baseline is the store outside every scope. A command that names no scope acts on baseline.
For the charter, see [baseline charter](#baseline-charter).

See [Scopes](reference/scopes.md#scopes).

## Rules

### charter

A charter is the [rules](#rule) you install at one address, from one file: `viff charter -a
-f charter.tony`. The file lists the rules under `rules:`. It may also hold `strata:` and
`schedules:`. A package's file says where it goes with `at:`, and what it reads with `reads:`.

See [The charter](reference/charter.md).

### rule

A rule is one entry of a charter: a name, a [condition](#condition), a list of
[gates](#gate-of-a-rule) and an [action](#action). When the condition holds and every gate
passes, the action fires. An installed rule is data at `mirror.trigger.rule.<name>`, or
`mirror.trigger.rule."<address>:<name>"` for a rule of a package.

Also written: trigger (the Go type `trigger.Trigger`, and the output of an install).

See [The charter reference](trigger-model.md#the-rule).

### baseline charter

The baseline charter is the charter installed at no address. Its rules may write anywhere.
`viff flow -charter :review` names its rule `review`.

See [Control](control.md#the-flags).

### package

A package is a charter installed at an address: `viff charter -a notes -f notes.tony`. It
runs in an engine of its own, and its rules write only inside that address.

See [The charter](reference/charter.md#a-charter-can-be-installed-at-an-address).

### reads declaration

The `reads:` line of a package names what its rules read outside its own address, such as
`reads: [git]`. Viff refuses to install a rule that reads what the package does not declare.

See [The charter](reference/charter.md#a-charter-can-be-installed-at-an-address).

### outbox

An outbox is a place inside a package where its rules ask an iod to act on the iod's system.
The package declares it with `sends:`: `sends: [{from: out.slack, to: slack.acme.out}]`.
The iod reads each input there, carries it out to the system, and writes its status beside it.

See [The charter](reference/charter.md#a-charter-can-be-installed-at-an-address).

### match

A match selects nodes at one revision: `{path: "demo.pr.*", value: {stage: open}}`. The
`path` is a [pattern](#pattern). The optional `value` is a [predicate](#predicate).

See [The condition of a rule](reference/condition.md#what-a-match-can-say).

### predicate

A predicate is the `value` of a match: a test on the payload of a node. Over an object, the
payload must hold each field that the predicate names, with that value.

See [The condition of a rule](reference/condition.md#what-a-match-can-say).

### operator

An operator is a tag, written `!name`, that changes how viff reads a value in a predicate or
a patch: `!glob "hotfix-*"`, `!delete`. Viff refuses a tag that it does not know.

See [The condition of a rule](reference/condition.md#what-a-match-can-say).

### condition

The condition of a rule has three fields: `subject`, `on` and `when`. Some pages call the
`when` alone "the condition".

See [The condition of a rule](reference/condition.md).

### subject

The `subject` of a condition is the match that selects the data the rule acts on. It is
required. The subject of a firing, a task or a question is the one address that it is about.

See [The condition of a rule](reference/condition.md#subject-what-the-firing-is-about-and-required).

### when

`when` is an optional match in a condition. The rule fires only if it holds at the commit. It
may be about data that is not the subject.

See [The condition of a rule](reference/condition.md#on-and-when-the-cause-and-the-condition).

### on

`on` is an optional match in a condition. The rule fires at the commit where the `on` holds
and did not hold at the commit before. That change is an edge.

Also written: the cause.

See [The condition of a rule](reference/condition.md#on-and-when-the-cause-and-the-condition).

### level rule

A level rule is a rule with no `on`. Viff considers it at every commit that touches what the
rule reads.

This "level" is the opposite of an edge. For the other meanings, see
[level (of a stratum)](#level-of-a-stratum) and
[level (of a permission)](#level-of-a-permission).

See [The condition of a rule](reference/condition.md#on-and-when-the-cause-and-the-condition).

### firing

A firing is one reaction of one rule at one commit, about one address. A subject that selects
three addresses gives three firings.

See [The charter reference](trigger-model.md#the-rule).

### action

The action of a rule is what the rule does when it fires. It is data: any of `patch`,
`derive`, `spawn` and `seq`, with optional hooks. It acts on the subject.

See [The action of a rule](reference/action.md#action-what-fires).

### patch

`patch` changes the payload of the subject: `patch: {stage: reviewing}`. Viff merges it into
the payload.

See [The action of a rule](reference/action.md#action-what-fires).

### derive

`derive` writes data that is not the subject. Only a rule in a [stratum](#stratum) may
derive, and only into a stratum of a higher level.

See [Strata](strata.md#derive-identity-not-state).

### spawn

`spawn` starts a [task](#task). Its fields say what the work is, who does it, what it works in
and how each step is decided: `prompt` or `script`, `agent`, `environment`, `autonomy`, `answer`,
`timeout` and `runner`. `viff task run -f <file>` and a row of an ask's `on`, `then: {spawn: …}`,
take the same spawn.

Also written: launch spec (the Go type `launch.Spec`).

See [The action of a rule](reference/action.md#action-what-fires).

### seq

`seq` is a list of actions that run in order. Each one starts when the one before it
completes.

See [The action of a rule](reference/action.md#seq-sequence-along-the-chain-parallel-across-triggers).

### hook

A hook is an action that fires when the task of a `spawn`, or a `seq`, ends, such as
`onDone`. `onDenied` is a field of a gate: it fires when somebody answers no.

See [The action of a rule](reference/action.md#completion-hooks-one-trigger-chaining-visible)
and [The gates of a rule](reference/gates.md#ondenied-what-a-no-means).

### latch

A latch is a write, by the action of a rule, that takes the subject out of what the rule
selects. A rule that selects `{stage: open}` and patches `{stage: reviewing}` has one. Viff
refuses a rule that spawns and has no latch.

See [Convergence](convergence.md#what-ends-a-firing).

### binding

A binding is a name that an action's expressions read when the rule fires: `subject`, read as
`.[subject.ref]` for the address of the subject, or `$[subject.ref]` inside text. An
[account binding](#account-binding) is another thing.

See [The action of a rule](reference/action.md#bindings).

### path read

A path read is a reference, inside a rule, to a node that is not the subject. `!get-path`
reads one node. `!list-paths` reads the nodes that a pattern reaches.

See [Strata](strata.md#path-reads-inside-a-derive).

### answer contract

The answer contract of a task is a pattern that the final message of the task must match. A
task that never matches it fails. It is `answer:` in a `spawn`, which is also what the file of
`viff task run -f` holds.

See [Tasks](reference/tasks.md#answer-what-finished-means).

### chain (of an action)

The chain of an action is its position inside its rule, written as a path, such as
`review.onDone` or `review.seq[2]`. The id of a spawned task is built from it:
`review.seq.2@demo.pr.1`, with the charter's address in front for a package.

A chain of this kind is a string, such as the `chain` field of a task.

See [The action of a rule](reference/action.md#chains-and-ids).

### chain (of firings)

A chain of firings is a sequence, about one subject, in which the write of each firing causes
the next. The number of installed rules bounds its depth. A firing beyond the bound is a
trip: viff delays it and logs the cycle.

A chain of this kind has a depth.

See [The cycle throttle](cycle-throttle.md#the-mechanism).

### stratum

A stratum is a named part of the store that a charter declares, with a level:
`{name: observe, level: 0, match: ["gh", "linear"]}`. The subject of a rule in a stratum lies
inside the stratum.

See [Strata](strata.md).

### level (of a stratum)

The level of a stratum is a number that orders the strata of one charter. A rule derives only
into a stratum of a higher level than its own.

See [Strata](strata.md#two-rules-and-they-are-the-whole-thing).

### held rule

A held rule is a rule that is installed but not armed: it names a stratum that is not
installed, or it was restored with a spawn no runner offers yet. It fires on nothing until what
it waits for arrives, and viff arms it then. `viff charter` prints it with `held: <what it waits for>`.

A held rule is not under a [hold](#hold).

See [Strata](strata.md#installing-and-the-order-it-does-not-depend-on).

### trouble

`trouble` is what `viff charter` prints on a rule whose firings went wrong. Its kinds are
`failed` and `unread`.

See [The charter reference](trigger-model.md#when-a-rule-does-nothing).

### schedule

A schedule is a record, `mirror.schedule.<name>`, that holds a period. Each time the period
passes, viff writes a [tick](#tick).

See [Schedules](reference/schedules.md).

### tick

A tick is the node that a schedule writes when it is due, at
`mirror.schedule.<name>.tick.<due>`. A rule takes the tick as its subject.

See [Schedules](reference/schedules.md).

## Gates and questions

### gate (of a rule)

A gate of a rule is a check that a firing passes before the action fires:
`gates: [{name: approval, mode: ask}]`. Every gate of the rule must pass.

`viff question` prints the question of such a gate with `where: engine`.

Also written: charter gate, trigger gate.

See [The gates of a rule](reference/gates.md#gates-whether-it-proceeds).

### gate (of a task)

A gate of a task is a row of its [autonomy](#autonomy) that asks:
`{name: release, match: {system: git, verb: push, ref: refs/heads/main}, then: {ask: {answerers: [user:ana@example.com]}}}`.
Its `match` names a [step](#step-of-a-task) by its system and verb and narrows it by the
step's fields; the row asks before the task takes such a step. A step its
[environment](#environment) does not give is refused before any row is read, and nobody is asked.

The question of such a gate has the id `<task id>~d<n>`.

See [Tasks](reference/tasks.md#a-rows-question).

### gate (raised by a worker)

A worker on a runner raises the gates of its task's autonomy itself. It reports a `pending`
frame, and viff puts the question on the docket. A worker never answers its own gate.

See [Tasks](reference/tasks.md#running-one).

### mode

A mode is one of two values on a gate of a rule. `auto` runs the action, and `ask` poses a
question and waits for the answer.

See [The gates of a rule](reference/gates.md#gates-whether-it-proceeds).

### ask

An ask is something asked of somebody, with a clock and with what follows the answer, in one
record at `mirror.ask.<id>`. A person raises one with `viff ask`. A rule's gate raises one when its
mode is not `auto`, and a task raises one with its `question-ask` tool. Once it has settled it stands for its
keep, a week unless said otherwise, and is then removed.

See [Asking](asking.md).

### question

A question is an [ask](#ask) as whom it asks sees it: one waiting on you. `viff question` lists
them. The record is the ask's, at `mirror.ask.<id>`.

`viff gate` is kept as an alias of `viff question`.

See [Tasks](reference/tasks.md#the-question-record).

### docket

The docket is every pending ask you can read, whoever raised it and whoever it waits on.
`viff question --all` prints it.

See [Questions](tutorial/questions.md#who-sees-it).

### option

An option is one outcome that a question can settle on. A question has the options `proceed`
and `deny`, unless its gate names others. The option it settles on is its `verdict`.

See [Charters](tutorial/charters.md#no-is-a-real-outcome).

### ballot

A ballot is the choice of one answerer on one question. The question keeps each ballot under
`ballots`, by the name of who cast it.

See [Questions](tutorial/questions.md#a-question-is-data).

### need

`need` is the number of ballots that an option takes to settle a question. On a gate,
`need: 2` means that `proceed` takes two ballots. One `deny` still refuses.

Also written: threshold.

See [Charters](tutorial/charters.md#no-is-a-real-outcome).

### answerers

`answerers` is the field of a gate that names who may answer its question, as a list of
[grantees](#grantee). Each of them must be able to read what the question is about. Where it
names nobody, whoever may write what the question is about answers it; for a question about
nothing, any person or agent does. On a task's own gate, `$principal` names whom the task acts
for. The `question-ask` tool asks that principal, unless the call names `answerers` of its own.
The owner of what asked may always answer.

See [Permissions reference](reference/perms.md#questions).

### pose

`pose` is the field of a gate of a rule that holds the question in words, for a person to
read.

See [Gates](reference/gates.md).

### reason

`reason` is the field of a question that says where the question comes from, such as
`files.write path=docs/x.md` for a gate of a task.

See [Tasks](reference/tasks.md#the-dial).

### deadline (of a gate)

The deadline of a gate says how long its question waits, and which option the question then
settles on: `deadline: {within: "4h", default: deny}`.

See [The gates of a rule](reference/gates.md#a-deadline-settles).

### resolver

A resolver is an installed service that answers asks for whoever grants it: a person, at
`viff.for.<person>`, or a place, by its owner. Its `match` says which of their asks it watches.
It stands at `mirror.resolvers.<name>`.

See [Resolvers](attached-resolvers.md).

### answerer

An answerer is whoever answers a question: a person it names, or a resolver answering for one of
them, whose answer is then that person's.

See [Resolvers](attached-resolvers.md#answering).

### onward ask

An onward ask is an ask a resolver raises inside an ask it serves, to consult somebody or to
escalate. It fits inside the ask it serves, and what it comes to is carried back as the
resolver's answer.

See [Resolvers](attached-resolvers.md#onward-asks).

### task

A task is a unit of work that viff governs and that you can steer. An agent, a shell command,
a worker or a person does the work. The id of a task names the work, and not one run.

See [Tasks](reference/tasks.md).

### run

A run is one execution of a task. A rule that fires again about the same subject starts a new
run under the same task id.

See [Tasks](reference/tasks.md#one-id-many-runs).

### occurrence

The occurrence of a run is the revision that the run is about. For a run that a rule started,
it is the commit that caused the firing.

See [Tasks](reference/tasks.md#one-id-many-runs).

### status (of a task)

| Status | Meaning |
|---|---|
| `running` | the work is in progress |
| `paused` | a hold stopped the run at a boundary, until somebody resumes it |
| `done` | the task finished |
| `failed` | the task ran and did not succeed, or viff and its runner lost each other, which its record says as `orphaned: true` |
| `cancelled` | an operator stopped the task |

See [architecture.md](platform/architecture.md#data-and-failure-model), in the repository.

### instance record

The instance record of a task is the data at `mirror.task.instance.<id>`. It holds the state of
the latest run: the status, the last output, the result, the runner.

See [Tasks](reference/tasks.md#the-mirror).

### run record

The run record of a run is the data at `mirror.task.run.<id>@<occurrence>`. Viff writes it
when the run starts and when the run ends.

See [Tasks](reference/tasks.md#one-id-many-runs).

### mirror (of tasks)

The mirror is the part of viff that writes the state of each task into the store, as the
instance record and the run records. It writes as `system:mirror`.

In a page about tasks, "the mirror" has this meaning.

See [Tasks](reference/tasks.md#the-mirror).

### executor

The executor of a task performs each action that the agent or script proposes, once the task's
[environment](#environment) gives it and its [autonomy](#autonomy) lets it.
A [worker](#worker) on a runner is an executor too.

See [External tasks](external-tasks.md#a-task-is-a-contract-not-a-runtime).

### tool

A tool is a named action that a task may take: an agent's, such as `read_file` or `run_bash`,
or one a script calls with `viff-call-tool`, such as `git-push` or `question-ask`. The runner
offers those whose verb the task's [environment](#environment) gives. The task's
[autonomy](#autonomy) decides each use of a tool that is a [step](#step-of-a-task).

See [The action of a rule](reference/action.md#action-what-fires).

### boundary

A boundary is the point between two actions of a task. The task's autonomy decides there
whether the next action is refused, asks or goes ahead, and a hold or a `say` lands there.

See [Control](control.md#a-hold-on-a-run).

<a id="policy"></a>
### autonomy

The autonomy of a task is an ordered list of rows over its steps. Each row names steps by
system, verb and fields, each field a tony match pattern (a value, `null` for any, `!glob`,
`!regexp`, `!or`), and says what happens to them: `deny`, `ask` or `allow`. The first row
that matches a step decides it. The last row names no step and decides every step that no row
above matched: it is the task's dial.

A task with no autonomy asks on every step, so the last row is the one place a run is made
unattended. The autonomy decides among what the task's [environment](#environment) gives and
never adds to it: a step outside the environment is refused before any row is read, and a row
that can match nothing the environment gives is refused where it is written. Under a network of
`allow`, each connection is decided too, and a row on the network allows or denies, never asks.
The autonomy can be replaced while the task runs:
`viff task autonomy <id> '[{match: {system: commands}, then: allow}, {then: ask}]'`. A row on
`files` or `commands` is advisory: it decides the act as the agent names it, and a write through
the shell is a command ([why](systems/inside.md#a-row-on-files-or-commands-is-advisory)).

Also written: the dial.

See [Tasks](reference/tasks.md#the-autonomy).

### environment

The environment of a task is what it works in: the variables its shell is given (`vars`), and
the [systems](#system) it may use (`systems`), one block per system, each naming the verbs it gives
and their shapes. It is the whole of what the task may do: a step no block gives is refused,
and the task is offered no tool for a verb it does not give. A block that names no verb gives
every verb of its system, the network apart, whose `mode` is its reach. It is fixed when the run
starts, and it lies within the runner's [ceilings](#ceiling).

See [Tasks](reference/tasks.md#the-doer).

### system

A system is something outside viff that viff reads or acts on: a repository (`git`), its
issue tracker (`git-issue`), a forge (`github`), Linear (`linear-issue`), Slack (`slack`). An
iod writes the system's state into the store, and carries a rule's writes back out to it. A
task calls the system's verbs through a runner. Viff keeps one document per system at
`mirror.systems.<name>`, naming its verbs and their params.

See [Systems](systems/README.md).

### ceiling

A ceiling is the limit a runner's operator sets on what any task may do with a system the
runner holds for it. It is written as an environment is, blocks of the system's attributes and
verbs, each attribute a tony match pattern, under `systems.<name>.ceiling` in the runner's configuration,
and it lists what it allows: a system with no ceiling is closed, the network apart. The runner
publishes it with its offers, beside what it `builds` of the system by its nature. A spawn's
[environment](#environment) lies within it: viff refuses to install one beyond it, and the
runner holds it again at the launch and at each act.

See [the viff runner](reference/runnerd.md#what-it-offers).

### step (of a task)

A step of a task is what the task is about to do, as the spec names it: `git.push`,
`github.create-pr`, `files.write`, `commands.run` or `agent.call-sub-agent`, with the fields of
that step: the repository and ref of a push, the path of a write, the command of a run, the
sub-agent called, and under a network of `allow` a connection, `network.tcp-client`. An autonomy
row's `match` names a step and narrows it by its fields. A script's own run is no step; the calls it makes are.

See [Tasks](reference/tasks.md#a-rows-question).

### event stream

The event stream of a task is the ordered list of the frames that the task emits. It keeps a
bounded number of frames, and a reader resumes it from a cursor.

Also written: read plane (of a task).

See [Task control](task-control.md#the-event-envelope-and-the-cursor).

### seam

A seam is a place where what does the work can change and the commands stay the same. Where a
task runs is a seam: in the viff process or on a runner, and every `viff task` command works
on both.

See [where a task runs is a seam](reference/tasks.md#where-a-task-runs-is-a-seam). How the
seams are built is in [Seams](seams.md), in the repository.

### runner

A runner is a place where viff runs a task. Viff connects to it.

| Runner | What it is |
|---|---|
| `mirror.runners.local` | the local runner: the `runnerd` `viff up` starts beside itself and registers, unless `localRunner: false` |
| an installed runner | another host, at a URL. It stands at `mirror.runners.<name>`, which keeps what the runner offers: the agents and scripts it runs, as the runner described itself when it was installed or last checked. |

See [Runners](runners.md).

### worker

A worker is the program on a [runner](#runner) that does a task and reports it. In
permissions, a worker acts as `runner:<identity>`, the identity on the runner's registration.

Also written: executor, and claimant, of a task it works; the
[claimant of a binding](#claimant-of-a-binding) is another thing.

See [Task control](task-control.md).

### claim (of a worker)

A claim is how a worker becomes the one working a task: the runner known at the spawn, or the
name a running status carries. Viff then grants that worker `read-write` and `control` on the
task, and writes every report as it.

A claim of this kind is about one task.

See [Runners](runners.md).

### agent

An agent is a registered program that works a prompt one turn at a time. `viff` is viff's
own agent. A runner offers others by name, such as `claude` or `codex`, each with the settings it
takes. A spawn names one, with its settings beside the name: `agent: {name: codex, model:
gpt-5-codex}`. A prompt that names none is for viff's default agent, `viff` unless the config's
`defaultAgent` says another, and viff routes it to a runner that offers it.

See [Tasks](reference/tasks.md#the-doer).

### doer

A doer is what does the work of a task: the [agent](#agent) a `prompt` is for, or a `script`,
which is its own. The task's record says which agent ran and with what settings as its `agent`,
or `script: true`.

See [Tasks](reference/tasks.md#the-doer).

### virtual key

A virtual key is the model credential that a task runs under. A spawn names it by address, as
`virtualKey: acme.keys.review`; viff reads the address as whoever named it, and the runner
gets it as the annotation `viff.portkey/virtual-key`.

See [the spawn](reference/action.md#action-what-fires).

## Iods

### iod

An iod is a program that connects viff to one external system, such as a git repository, in
both directions. It has two halves: a [source](#source) writes what the system holds into the
store, and a [sink](#sink) carries what a rule writes back out to the system. An installed iod stands at
`mirror.iods.<name>`.

Also written: connector, source (in the Go source, for the whole iod), registration.

See [Iods](io.md).

### root (of an iod)

The root of an iod is the first segment of every address that the iod writes, such as `git`.
`viff iod add` takes it as `-root git`.

See [Iods (reference)](reference/io.md).

### claim (of an iod)

A claim of an iod is a pattern that names what only that iod may write:
`owns: [{at: "git.viff"}]`. Viff refuses an iod whose claims overlap the claims of another.

Also written: owns, ownership claim.

See [Iods (reference)](reference/io.md#ownership).

### probe

A probe is the question viff asks an iod or a runner when it is installed: what are you?
`GET /io` asks an iod. The answer is one of `a source`, `not a source` and `unreachable`, and
only `not a source` refuses the install. `viff iod add -no-probe` skips it, and `viff iod
-probe` asks every iod again without recording the answer.

See [Iods (reference)](reference/io.md#routes).

### source

A source is the in half of an iod. Viff asks it what is true in the external system, and it
answers with a [reflection](platform/glossary.md#reflection). It does not write to the store.

See [Iods](io.md#the-seam-a-source-answers-viff-converges).

### reflect

To reflect an external system is to keep its state in the store, as data. The source
reads the system, and the [reflector](platform/glossary.md#reflector) writes the store.

See [Iods](io.md).

### hint

A hint is a message from an iod to viff that says which addresses went stale. It writes
nothing. Viff answers it by reflecting those addresses again.

See [Iods](io.md#the-core-law-a-hint-is-a-hint-reconcile-is-the-truth).

### sink

A sink is the out half of an iod. A rule, a task or a person writes an input, which is a node
with a `to` field. The sink carries it out: it makes the external system match the input, and
writes the result at `<name>~status`.

See [Iods (reference)](reference/io.md#the-out-half).

### input

An input is a request written for a [sink](#sink): a node with a `to` field, anywhere under the
iod's out place or in an [outbox](#outbox), such as
`viff data slack acme out hello -put '{to: "user:ana@acme.test", text: "…"}'`. Whoever writes
it decides where it stands.

See [Iods (reference)](reference/io.md#what-is-asked-and-what-became-of-it).

### status (of an input)

The status of an input is the node the iod writes beside it, at `<name>~status`: what it did,
and an `error` while a failure stands. Only the iod writes it. A status with `failed: permanent`
is not offered again until the input changes.

See [Iods (reference)](reference/io.md#what-is-asked-and-what-became-of-it).

## Permissions

### principal

A principal is who acts. It is a name with its kind in front, such as `user:ana@example.com`.
Every write names one. A principal is the name that the request gives, or the person that the
request's token proves where the viff instance names an identity provider.

See [Permissions](perms.md#principals-and-grantees).

### kinds of principal

A principal is of one of six kinds: `user:`, `agent:`, `runner:`, `system:`, `via:` and
`pair:`. A request presents the first three, and viff makes the other three. `pair:<asker>
<rule>` is a rule's task acting for whoever owns its subject.

See [Permissions reference](reference/perms.md#the-grammar).

### local user

The local user is the principal `user:<login>@localhost`. The CLI acts as the local user
toward a viff instance on the same machine, when no user is configured.

See [Permissions, walked](perms-guide.md#before-the-first-step-who-you-are).

### identity provider

An identity provider is a party that signs a statement of who a person is, and the viff instance
accepts that statement. It is an OpenID Connect issuer that the deployment names under
`auth.principal.providers`. `viff account login` logs in there, and the viff instance verifies the
token. The deployment's own iods are verified
the same way against their workload issuer (`auth.iod`), and so is a runner whose registration
names its token's subject. Slack and GitHub are not identity providers here; their accounts are
bound by a nonce.

See [Who a request is from](reference/perms.md#who-a-request-is-from).

### account binding

An account binding is the record that an account in another system is a principal: a Slack
member, a GitHub login, or an account at an identity provider. Only the system writes it, from
evidence it checked, at `mirror.identity.<provider>.account.<id>`. A person makes their own
with `viff account -link <provider>`, by saying a nonce from the account or by logging in. An iod
that acts for a person is admitted only through one. One account is one principal.

See [Accounts in other systems](reference/perms.md#accounts-in-other-systems).

### claimant (of a binding)

The claimant of an account binding is how the person who proved it was authenticated: the
identity provider that verified them, or `asserted` for a person who was whoever their request
said. It is what lets a reader, and a viff instance that verifies people, tell the two kinds of binding
apart.

See [Accounts in other systems](reference/perms.md#accounts-in-other-systems).

### first sight

A first sight is a binding made the first time an account is seen, from a verified email in one
of the provider's `domains`: by a token at the door, or by a trusted iod relaying the person. A
provider with no `domains` admits bound accounts only.

See [Who a request is from](reference/perms.md#who-a-request-is-from).

### trusted iod

A trusted iod is an iod that the deployment names under
`auth.principal.providers.<provider>.iods`. Its word counts for that provider's accounts: it may
relay a person it saw, such as the Slack member who typed an answer, and it may report a nonce it
heard. It proves which iod it is with its own token.

See [A person relayed by a trusted iod](reference/perms.md#a-person-relayed-by-a-trusted-iod).

### grantee

A grantee is who a grant is to: a principal, every principal of a kind (`user:*`), a
[group](#group), the audience of an address (`audience:<address>`, whoever holds the same level
there), the resolvers that answer for an address (`answering:<address>`), or everyone (`*`).

See [Permissions reference](reference/perms.md#the-grammar).

### group

A group is a named set of grantees, with an owner: `viff group add payments ana@example.com`.
It stands at `mirror.principal.group.<name>`.

See [Permissions](perms.md#grants).

### owner

The owner of a node may do anything to the node. The owner is fixed when the node is created:
it is the creator, or the owner of the node above. The owners of the nodes above are
co-owners.

See [Permissions](perms.md#ownership).

### record

A record is what viff keeps about the permissions of one node. It holds the `owner` of the
node, its `grants` and its `child-grants`, and on a question `under`, the address of what asked
it. `viff perms <path>` shows it. A node with no
record follows the nearest record above it.

Also written: permissions record, perms record.

See [Permissions reference](reference/perms.md#records).

### seed

The seed is the records that viff writes from the config at its first start: the root's record,
the record on `viff`, and the `admin` group. Viff writes them only where they are absent, so an
operator's later edit stands. `viff up -reseed-perms` writes them again over what stands.

See [Starting a viff](reference/up.md#configuration).

### mirror (of permissions)

In the Go source, the perms mirror is the set of all records, under `mirror.perms`. The docs
say "the records".

See [Permissions reference](reference/perms.md#records).

### grant (permission)

A grant is one line of a record, such as `- read: [user:ana@example.com]`. It gives a level
to grantees who are not the owner. It reaches down to the next record that names an owner.

See [Permissions](perms.md#grants).

### level (of a permission)

A level is what a principal may do at a node: `read`, `read-write`, `owner`,
[`control`](#control-permission-level) or `answer`. `answer` says which resolvers may answer
for the party the node stands for.

See [Permissions reference](reference/perms.md#levels).

### control (permission level)

`control` is the level that admits steering work: run, cancel, pause, resume, `say`, a change
of autonomy, and suspend. No other level includes it. The owner of a task holds it, and so does
the worker that claimed the task.

Written as code, `control` is this level.

See [Permissions reference](reference/perms.md#levels).

### child-owner

`child-owner` is the grant that lets its grantees create nodes beneath a node, and own what
they create. It gives nothing over a node that stands already.

See [Permissions](perms.md#grants).

### child-grants

`child-grants` is the field of a record that says which grants a node created beneath it
starts with. Viff copies it when the node is created, unless its creator gives the node grants
of their own (`viff data … -put … -grants`).

See [Permissions](perms.md#grants).

## Steering

### control plane

The control plane is the part of viff that steers. Viff serves it before observation, and it
never travels on the stream of changes.

For the permission level, see [control (permission level)](#control-permission-level).

See [Deployment and steering](deployment-and-steering.md#two-planes).

### steering

To steer is to act on work in progress: to answer a gate, to hold or resume, to `say`, to
cancel, to change the autonomy.

See [Basic concepts](concepts.md#what-viff-is-made-of).

### flow

A flow is the live tasks about one subject, or about the subjects that a pattern names. Viff
keeps no record of it: `viff flow show demo.pr.1` builds it.

See [Control](control.md).

### hold

A hold stops one run at its next boundary, until somebody resumes the run:
`viff flow suspend -task <id>`. It does not last across a restart of viff.

Also written: pause.

See [Control](control.md#a-hold-on-a-run).

### suspension

A suspension is a record that names a charter, a rule and a subject. It stops all the work
about them: firings, tasks and questions. It lasts across restarts, until somebody lifts it.

See [Control](control.md#a-suspension).

## Terms of the platform

These terms belong to running, extending or changing viff. Each is defined in
[the platform glossary](platform/glossary.md), in the repository.

### converge

See [converge](platform/glossary.md#converge), in the repository.

### cover (of a reflection)

See [cover (of a reflection)](platform/glossary.md#cover-of-a-reflection), in the repository.

### frame (of an event stream)

See [frame (of an event stream)](platform/glossary.md#frame-of-an-event-stream), in the repository.

### mirror (of a git repository)

See [mirror (of a git repository)](platform/glossary.md#mirror-of-a-git-repository), in the repository.

### observation plane

See [observation plane](platform/glossary.md#observation-plane), in the repository.

### reflection

See [reflection](platform/glossary.md#reflection), in the repository.

### reflector

See [reflector](platform/glossary.md#reflector), in the repository.

### root (of viff up)

See [root (of viff up)](platform/glossary.md#root-of-viff-up), in the repository.

### tombstone

See [tombstone](platform/glossary.md#tombstone), in the repository.

## Index

+ [account binding](#account-binding)

+ [action](#action)

+ [address](#address)

+ [agent](#agent)

+ [answer contract](#answer-contract)

+ [answerer](#answerer)

+ [answerers](#answerers)

+ [ask](#ask)

+ [autonomy](#autonomy)

+ [ballot](#ballot)

+ [baseline](#baseline)

+ [baseline charter](#baseline-charter)

+ [binding](#binding)

+ [boundary](#boundary)

+ [ceiling](#ceiling)

+ [chain (of an action)](#chain-of-an-action)

+ [chain (of firings)](#chain-of-firings)

+ [charter](#charter)

+ [child-grants](#child-grants)

+ [child-owner](#child-owner)

+ [claim (of a worker)](#claim-of-a-worker)

+ [claim (of an iod)](#claim-of-an-iod)

+ [claimant (of a binding)](#claimant-of-a-binding)

+ [commit](#commit)

+ [condition](#condition)

+ [control (permission level)](#control-permission-level)

+ [control plane](#control-plane)

+ [converge](#converge)

+ [cover (of a reflection)](#cover-of-a-reflection)

+ [data](#data)

+ [deadline (of a gate)](#deadline-of-a-gate)

+ [derive](#derive)

+ [descent](#descent)

+ [docket](#docket)

+ [doer](#doer)

+ [entity](#entity)

+ [environment](#environment)

+ [event stream](#event-stream)

+ [executor](#executor)

+ [firing](#firing)

+ [first sight](#first-sight)

+ [flow](#flow)

+ [frame (of an event stream)](#frame-of-an-event-stream)

+ [gate (of a rule)](#gate-of-a-rule)

+ [gate (of a task)](#gate-of-a-task)

+ [gate (raised by a worker)](#gate-raised-by-a-worker)

+ [grant (permission)](#grant-permission)

+ [grantee](#grantee)

+ [group](#group)

+ [held rule](#held-rule)

+ [hint](#hint)

+ [hold](#hold)

+ [hook](#hook)

+ [identity provider](#identity-provider)

+ [input](#input)

+ [instance record](#instance-record)

+ [iod](#iod)

+ [kinded path](#kinded-path)

+ [kinds of principal](#kinds-of-principal)

+ [latch](#latch)

+ [level (of a permission)](#level-of-a-permission)

+ [level (of a stratum)](#level-of-a-stratum)

+ [level rule](#level-rule)

+ [local user](#local-user)

+ [match](#match)

+ [mirror (of a git repository)](#mirror-of-a-git-repository)

+ [mirror (of permissions)](#mirror-of-permissions)

+ [mirror (of tasks)](#mirror-of-tasks)

+ [mode](#mode)

+ [need](#need)

+ [node](#node)

+ [observation plane](#observation-plane)

+ [occurrence](#occurrence)

+ [on](#on)

+ [onward ask](#onward-ask)

+ [operator](#operator)

+ [option](#option)

+ [outbox](#outbox)

+ [owner](#owner)

+ [package](#package)

+ [patch](#patch)

+ [path read](#path-read)

+ [pattern](#pattern)

+ [payload](#payload)

+ [pose](#pose)

+ [predicate](#predicate)

+ [principal](#principal)

+ [probe](#probe)

+ [question](#question)

+ [reads declaration](#reads-declaration)

+ [reason](#reason)

+ [record](#record)

+ [reflect](#reflect)

+ [reflection](#reflection)

+ [reflector](#reflector)

+ [resolver](#resolver)

+ [revision](#revision)

+ [root (of an iod)](#root-of-an-iod)

+ [root (of the store)](#root-of-the-store)

+ [root (of viff up)](#root-of-viff-up)

+ [rule](#rule)

+ [run](#run)

+ [run record](#run-record)

+ [runner](#runner)

+ [schedule](#schedule)

+ [scope](#scope)

+ [seam](#seam)

+ [seed](#seed)

+ [seq](#seq)

+ [sink](#sink)

+ [source](#source)

+ [spawn](#spawn)

+ [status (of an input)](#status-of-an-input)

+ [status (of a task)](#status-of-a-task)

+ [steering](#steering)

+ [step](#step)

+ [step (of a task)](#step-of-a-task)

+ [store](#store)

+ [stratum](#stratum)

+ [subject](#subject)

+ [suspension](#suspension)

+ [system](#system)

+ [task](#task)

+ [tick](#tick)

+ [tombstone](#tombstone)

+ [tool](#tool)

+ [trouble](#trouble)

+ [trusted iod](#trusted-iod)

+ [viff's own half](#viffs-own-half)

+ [virtual key](#virtual-key)

+ [when](#when)

+ [worker](#worker)
