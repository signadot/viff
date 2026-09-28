# Glossary

This page defines each term of verse once. The terms are grouped by subject, and an
alphabetical index is at the end.

Where one thing has several names, the entry lists the others under "Also written". Where one
name means several things, each meaning has its own entry, with a qualifier in parentheses.

## The store

### store

The store is the one tree in which a verse keeps its data. It is durable, and it can be
watched.

Also written: entity store, entity-verse, the verse.

See [The store](reference/store.md).

### address

An address names one position in the store. It is a list of segments, of any length, such as
`demo.pr.1`.

Also written: path, ref (in the Go source), `at` (the field and the flag), place (an address
that somebody writes at, or owns).

See [Entities](reference/entities.md#addressing).

### kinded path

A kinded path is an address written as one string, with dots between the segments. Quotes
keep a dot inside a segment: `demo.pr."a.b"` has three segments.

Also written: kpath.

See [Entities](reference/entities.md#addressing).

### step

A step is one segment of an address, typed as one word on the command line:
`verse data demo pr 1`.

See [Entities](reference/entities.md#addressing).

### pattern

A pattern is an address in which `*` stands for any one segment, such as `demo.pr.*`. It
names every node that it reaches.

See [Entities](reference/entities.md#addressing).

### node

A node is what stands at an address: an object, an array, a scalar or a null.
`demo.pr.1.stage` is a node inside the node `demo.pr.1`.

See [Entities](reference/entities.md#what-stands-at-a-path).

### payload

The payload of a node is the value that the node holds, such as `{stage: open}`. It may be
any value.

See [Entities](reference/entities.md).

### entity

An entity is a node that something asks about: the subject of a rule, a row of a listing, an
event of a watch. It has an address and a payload.

See [Entities](reference/entities.md).

### root (of the store)

The root of the store is the node above every other node. Its address is empty. Its
[record](#record) says who may create a node at the top: `verse perms --root` shows it.

In a page about permissions, "the root" has this meaning.

See [Permissions](perms.md#the-top-of-the-tree).

### verse's own half

Verse's own half is the part of the store under the first segment `verse`, such as
`verse.task.instance.review-1`. Verse keeps its own records there.

See [Permissions](perms.md#verses-own-half).

### commit

A commit is one atomic change to the store. It names every entity that it changed.

See [Change events](change-events.md#one-commit-many-entities-the-entity-is-the-reactive-grain).

### revision

A revision is the number of a commit. Revisions grow, and the numbers have gaps, so a reader
uses one only as a cursor: `verse watch -since 42`.

Also written: rev, cursor (of the store).

See [Entities](reference/entities.md#watching).

### scope

A scope is a named copy-on-write view of the store: `verse data -scope me demo x 1`. A write
lands in the scope only. A read sees the scope on top of the rest of the store.

See [Connect and scopes](reference/connect.md#scopes).

### baseline

Baseline is the store outside every scope. A command that names no scope acts on baseline.
For the charter, see [baseline charter](#baseline-charter).

See [Connect and scopes](reference/connect.md#scopes).

### controller

A controller is a process that serves one subtree of the store. No rule fires on what a
controller serves.

Also written: docd controller, mount.

See [Writing a docd controller](controllers.md).

### federation

To federate a working tree is to make its files readable through the store. `verse connect`
writes the metadata of each file as an entity in a scope, and a controller serves the
contents.

See [Connect and scopes](reference/connect.md#what-one-command-does).

## Rules

### charter

A charter is a list of [rules](#rule) that you install: `verse charter -a -f charter.tony`. It
may also hold strata, schedules and the declaration of a package.

See [The charter](reference/charter.md).

### rule

A rule is one entry of a charter: a name, a [condition](#condition), a list of
[gates](#gate-of-a-rule) and an [action](#action). When the condition holds and every gate
passes, the action fires. An installed rule is the entity `verse.trigger.rule.<name>`.

Also written: trigger (the Go type `trigger.Trigger`, and the output of an install).

See [The trigger model](trigger-model.md#the-rule).

### baseline charter

The baseline charter is the charter installed at no address. Its rules may write anywhere.
`verse flow -charter :review` names its rule `review`.

See [Control](control.md#the-flags).

### package

A package is a charter installed at an address: `verse charter -a notes -f notes.tony`. It
runs in an engine of its own, and its rules write only inside that address.

See [The charter](reference/charter.md#a-charter-can-be-installed-at-an-address).

### reads declaration

The `reads:` line of a package names what its rules read outside its own address, such as
`reads: [git]`. Verse refuses to install a rule that reads what the package does not declare.

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

An operator is a tag, written `!name`, that changes how verse reads a value in a predicate or
a patch: `!glob "hotfix-*"`, `!delete`. Verse refuses a tag that it does not know.

See [The condition of a rule](reference/condition.md#what-a-match-can-say).

### condition

The condition of a rule has three fields: `subject`, `on` and `when`. Some pages call the
`when` alone "the condition".

See [The condition of a rule](reference/condition.md).

### subject

The `subject` of a condition is the match that selects the entities the rule acts on. It is
required. The subject of a firing, a task or a question is the one entity that it is about.

See [The condition of a rule](reference/condition.md#subject--what-the-firing-is-about-and-required).

### when

`when` is an optional match in a condition. The rule fires only if it holds at the commit. It
may be about an entity that is not the subject.

See [The condition of a rule](reference/condition.md#on-and-when--the-cause-and-the-condition).

### on

`on` is an optional match in a condition. The rule fires at the commit where the `on` holds
and did not hold at the commit before. That change is an edge.

Also written: the cause.

See [The condition of a rule](reference/condition.md#on-and-when--the-cause-and-the-condition).

### level rule

A level rule is a rule with no `on`. Verse considers it at every commit that touches what the
rule reads.

This "level" is the opposite of an edge. For the other meanings, see
[level (of a stratum)](#level-of-a-stratum) and
[level (of a permission)](#level-of-a-permission).

See [The condition of a rule](reference/condition.md#on-and-when--the-cause-and-the-condition).

### firing

A firing is one reaction of one rule at one commit, about one entity. A subject that selects
three entities gives three firings.

See [The trigger model](trigger-model.md#the-rule).

### action

The action of a rule is what the rule does when it fires. It is data: any of `patch`,
`derive`, `spawn` and `seq`, with optional hooks. It acts on the subject.

See [The action of a rule](reference/action.md#action--what-fires).

### patch

`patch` changes the payload of the subject: `patch: {stage: reviewing}`. Verse merges it into
the payload.

See [The action of a rule](reference/action.md#action--what-fires).

### derive

`derive` writes an entity that is not the subject. Only a rule in a [stratum](#stratum) may
derive, and only into a stratum of a higher level.

See [Strata](strata.md#derive--identity-not-state).

### spawn

`spawn` starts a [task](#task). Its fields say how the task runs, such as `driver`, `prompt`,
`vector`, `grant` and `runner`.

Also written: launch spec (the Go type `launch.Spec`).

See [The action of a rule](reference/action.md#action--what-fires).

### seq

`seq` is a list of actions that run in order. Each one starts when the one before it
completes.

See [The action of a rule](reference/action.md#seq-sequence-along-the-chain-parallel-across-triggers).

### hook

A hook is an action that fires when the task of a `spawn`, or a `seq`, ends.

| Hook | When it fires |
|---|---|
| `onDone` | the task ends with the status `done` |
| `onFailed` | the task ends with the status `failed` |
| `onCancelled` | the task ends with the status `cancelled` |
| `onDenied` | somebody answers no to a gate. It is a field of the gate, not of the action, and it needs no task. |

An action also takes `onOrphaned`, which does not fire.

See [The action of a rule](reference/action.md#completion-hooks-one-trigger-chaining-visible).

### latch

A latch is a write, by the action of a rule, that takes the subject out of what the rule
selects. A rule that selects `{stage: open}` and patches `{stage: reviewing}` has one. Verse
refuses a rule that spawns and has no latch.

See [Convergence](convergence.md#what-ends-a-firing).

### binding

A binding is a name that begins with `$`, inside an action. Verse replaces it with a value
when the rule fires.

| Binding | Value |
|---|---|
| `$subject` | the address of the subject |
| `$subject.name` | the last segment of that address |
| `$subject.value` | the payload of the subject |
| `$stage` | the field `stage` of the payload. Every top-level field binds in this way. |
| `$status`, `$result`, `$data`, `$err`, `$chain` | in a hook, the fields of the task that ended |

See [The action of a rule](reference/action.md#bindings).

### path read

A path read is a reference, inside a rule, to a node that is not the subject. `!get-path`
reads one node. `!list-paths` reads the nodes that a pattern reaches.

See [Strata](strata.md#path-reads-inside-a-derive).

### answer contract

The answer contract of a task is a pattern that the final message of the task must match. A
task that never matches it fails. It is `answer:` in a `spawn`, and `-answer-contract` on
`verse task run`.

See [Tasks](reference/tasks.md#-answer-contract-what-finished-means).

### chain (of an action)

The chain of an action is its position inside its rule, written as a path, such as
`review.onDone`. The id of a spawned task is `<chain>@<subject>`.

A chain of this kind is a string, such as the `chain` field of a task.

See [The action of a rule](reference/action.md#chains-and-ids).

### chain (of firings)

A chain of firings is a sequence, about one subject, in which the write of each firing causes
the next. The number of installed rules bounds its depth. A firing beyond the bound is a
trip: verse delays it and logs the cycle.

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

A held rule is a rule that names a stratum that is not installed. It fires on nothing until
the stratum is installed. `verse charter` prints it with `held: <stratum>`.

A held rule is not under a [hold](#hold).

See [Strata](strata.md#installing-and-the-order-it-does-not-depend-on).

### trouble

`trouble` is what `verse charter` prints on a rule whose firings went wrong.

| Kind | What went wrong |
|---|---|
| `failed` | an action did not do what the rule says |
| `unread` | the rule could not read a commit, and did not evaluate it |

See [The trigger model](trigger-model.md#when-a-rule-does-nothing).

### schedule

A schedule is a record, `verse.schedule.<name>`, that holds a period. Each time the period
passes, verse writes a [tick](#tick).

See [Schedules](reference/schedules.md).

### tick

A tick is the node that a schedule writes when it is due, at
`verse.schedule.<name>.tick.<due>`. A rule takes the tick as its subject.

See [Schedules](reference/schedules.md).

## Gates and questions

### gate (of a rule)

A gate of a rule is a check that a firing passes before the action fires:
`gates: [{name: approval, mode: ask}]`. Every gate of the rule must pass.

`verse gate` prints the question of such a gate with `where: engine`.

Also written: charter gate, trigger gate.

See [The gates of a rule](reference/gates.md#gates--whether-it-proceeds).

### gate (of a task)

A gate of a task is one entry of the [vector](#vector) of the task:
`{name: shell, mode: ask, match: {verbs: [exec]}}`. Its `match` selects actions by what they
do.

The question of such a gate has the id `<task id>~d<n>`.

See [Tasks](reference/tasks.md#a-gates-question).

### gate (raised by a worker)

A worker raises a gate when it reaches a decision that it may not make:
`verse task do gate <id> <name>`. A worker never answers its own gate.

The question of such a gate has the id `<name>-<hex>`, and no `mode`.

See [Tasks](reference/tasks.md#executing-one-by-hand).

### mode

A mode is one of three values on a gate.

| Mode | What happens |
|---|---|
| `auto` | the action runs, and nobody is asked |
| `ask` | the gate poses a question, and the action waits for the answer |
| `park` | the action is held. Its question stands on the docket, with `mode: PARK`, until somebody answers it. |

Where several gates of a task match one action, the strictest mode applies: `park`, then
`ask`, then `auto`.

See [Tasks](reference/tasks.md#the-dial).

### question

A question is what a gate poses when its mode is not `auto`. It is the entity
`verse.gate.question.<id>`, and it stays in the store after it is answered.

Also written: decision (in the CLI help), gate (`verse gate approve <id>` answers a question).

See [Tasks](reference/tasks.md#the-question-record).

### docket

The docket is the list of the questions that wait for an answer. `verse gate` prints it.

See [Tutorial](tutorial.md#5-gates-and-the-docket).

### option

An option is one outcome that a question can settle on. A question has the options `proceed`
and `deny`, unless its gate names others. The option it settles on is its `verdict`.

See [Tutorial](tutorial.md#no-is-a-real-outcome).

### ballot

A ballot is the choice of one answerer on one question. The question keeps each ballot under
`ballots`, by the name of who cast it.

See [Tutorial](tutorial.md#5-gates-and-the-docket).

### need

`need` is the number of ballots that an option takes to settle a question. On a gate,
`need: 2` means that `proceed` takes two ballots. One `deny` still refuses.

Also written: threshold.

See [Tutorial](tutorial.md#5-gates-and-the-docket).

### answerers

`answerers` is the field of a gate that names who may answer its question, as a list of
[grantees](#grantee). Where it names nobody, any person or agent may answer by default. The
owner of what asked may always answer.

See [Permissions reference](reference/perms.md#questions).

### pose

`pose` is the field of a gate of a rule that holds the question in words, for a person to
read.

See [Resolvers](reference/resolvers.md#the-question-record).

### reason

`reason` is the field of a question that says where the question comes from.

| Gate | What `reason` holds |
|---|---|
| of a task | what matched, such as `verb write`, or `no gate matched` |
| of a rule | the names of the rule, the gate and the subject |
| raised by a worker | the text that the worker gave with `-reason` |

See [Tasks](reference/tasks.md#the-dial).

### deadline (of a gate)

The deadline of a gate says how long its question waits, and which option the question then
settles on: `deadline: {within: "4h", default: deny}`.

A deadline of a gate settles the question.

See [Tasks](reference/tasks.md#a-gates-question).

### resolver

A resolver is an installed record that says who answers questions: a service, a person or a
group. It is the entity `verse.resolvers.<name>`.

See [Resolvers](reference/resolvers.md#the-resolver).

### answerer

An answerer is whoever answers a question. The answerer of a resolver is the service, the
person or the group that the resolver names.

See [Resolvers](reference/resolvers.md#the-resolver).

### standing resolver

A standing resolver is a resolver with a `condition`. It takes the questions about the
subjects that its condition selects. A resolver with `byRef: true` can be named, by a `spawn`
or by a rule.

See [Attached resolvers](attached-resolvers.md#two-doors-and-one-of-them-must-be-open).

### cover (of a resolver)

A cover is the record of one resolver that reads one stream of gates. It is the entity
`verse.covers.<name>@<stream>`, and it keeps the position that the resolver has read to.

See [Resolvers](reference/resolvers.md#the-two-entities).

### ladder

The ladder of a question is the order in which verse asks for an answer: the named resolver,
then the standing resolvers that match the subject, then the docket. To escalate a question
is to move it one place along: `verse gate escalate <id>`.

Also written: layers, rung.

See [Attached resolvers](attached-resolvers.md#layers).

### deadline (of a resolver)

The deadline of a resolver says how long the resolver has to answer:
`deadline: {within: "4h", then: escalate}`.

A deadline of a resolver escalates the question. It does not settle it.

See [Tasks](reference/tasks.md#a-gates-question).

### exclusive resolver

An exclusive resolver is a resolver with `exclusive: true`. Only its answer counts, unless
somebody answers with `-override`. Any other resolver is advisory: the first answer wins.

See [Attached resolvers](attached-resolvers.md#advisory-or-exclusive).

## Tasks and where they run

### task

A task is a unit of work that verse governs and that you can steer. An agent, a shell command,
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
| `unclaimed` | the task waits on a broker for a worker |
| `running` | the work is in progress |
| `done` | the task finished |
| `failed` | the task ran and did not succeed |
| `cancelled` | an operator stopped the task |
| `orphaned` | the process that ran the task ended, and the work did not complete |

See [Overview](overview.md#data-and-failure-model).

### instance record

The instance record of a task is the entity `verse.task.instance.<id>`. It holds the state of
the latest run: the status, the last output, the result, the runner.

See [Tasks](reference/tasks.md#the-mirror).

### run record

The run record of a run is the entity `verse.task.run.<id>@<occurrence>`. Verse writes it
when the run starts and when the run ends.

See [Tasks](reference/tasks.md#one-id-many-runs).

### mirror (of tasks)

The mirror is the part of verse that writes the state of each task into the store, as the
instance record and the run records. It writes as `system:mirror`.

In a page about tasks, "the mirror" has this meaning.

See [Tasks](reference/tasks.md#the-mirror).

### driver

The driver of a task decides what the task does next.

| Driver | What it does |
|---|---|
| `scripted` | runs the prompt as one shell command |
| `calls` | runs a list of actions that the charter wrote |
| `llm` | gives the prompt to a model, which chooses each action |

See [Tasks](reference/tasks.md#the-drivers).

### executor

The executor of a task performs each action that the driver proposes, after the gates pass.
The help of `verse task do` also says "executor" for a [worker](#worker).

See [External tasks](external-tasks.md#a-task-is-a-contract-not-a-runtime).

### tool

A tool is a named action that a task may take, such as `read_file` or `run_bash`. The `tools`
field of a `spawn` narrows the tools that the driver offers.

See [The action of a rule](reference/action.md#action--what-fires).

### boundary

A boundary is the point between two actions of a task. Verse evaluates the vector there, and
a hold or a `say` lands there.

See [Control](control.md#a-hold-on-a-run).

### vector

The vector of a task is the list of its gates, with a default mode for an action that no gate
matches. It can change while the task runs: `verse task vector <id> ask`.

Also written: gate vector, the dial.

See [Task control](task-control.md#the-two-names).

### grant (of a spawn)

The grant of a spawn is the limit on what the task may do at all. Verse refuses an action
outside it, and asks nobody. It is one word: `all`, `read-only`, `no-delegate` or `none`.

For a line of a record, see [grant (permission)](#grant-permission).

Also written: envelope (the grant with the tools), scope (in the Go source).

See [Provider selection](provider-selection.md#the-ceiling).

### frame (of a spawn)

The `frame` of a spawn is the standing instructions under which the task reads its prompt.
For an agent it is the system prompt.

A frame of this kind is a string in a `spawn` or on a provider.

See [Provider selection](provider-selection.md#the-registry).

### frame (of an event stream)

A frame is one event in the event stream of a task: a status, a pending gate, a decision, a
`say`. Each frame has a `kind` and a `cursor`.

See [Task control](task-control.md#the-event-envelope-and-the-cursor).

### event stream

The event stream of a task is the ordered list of the frames that the task emits. It keeps a
bounded number of frames, and a reader resumes it from a cursor.

Also written: read plane (of a task).

See [Task control](task-control.md#the-event-envelope-and-the-cursor).

### seam

A seam is an interface between two parts of verse that can become a network connection.

See [Seams](seams.md).

### runner

A runner is a place where verse runs a task. Verse connects to it.

| Runner | What it is |
|---|---|
| `local` | the verse process itself |
| an installed runner | another host, at a URL. It is the entity `verse.runners.<name>`. |
| `broker` | a [broker](#broker) |

See [Runners](runners.md).

### broker

A broker is a runner whose tasks wait for a worker to connect and do them. Verse and the worker
both connect to the broker, so the worker may be behind a firewall.

See [Task control](task-control.md#brokering-firewalled-workers-and-humans-without-inverting-verse).

### worker

A worker is a program or a person that connects to a broker, claims a task, does the work and
reports it. Verse reaches a runner. A worker reaches verse. In permissions, a worker acts as
`runner:<identity>`.

Also written: executor (in the help of `verse task do`), claimant.

See [Task control](task-control.md#the-worker-facing-dial-in-protocol).

### claim (of a worker)

A claim is how a worker takes a task on a broker. Verse then grants that worker `read-write`
and `control` on the task. With the CLI, the first `verse task do` claims.

A claim of this kind is about one task.

See [Task control](task-control.md#claim-identity-takeover).

### epoch

The epoch of a claim is a number that the broker makes at each claim. The broker rejects a
report that carries an older epoch.

See [Seams](seams.md#5-task-control-dialled-in).

### inbox

The inbox of a task on a broker is the queue of control for its worker, such as a `say`, a
cancel or the answer to a gate. `verse task inbox <id>` prints it.

See [Task control](task-control.md#the-worker-facing-dial-in-protocol).

### report

A report is what a worker sends to the broker about its task: what it said, what it ran, a
gate that it raised, its final status.

See [Task control](task-control.md#the-worker-facing-dial-in-protocol).

### dial URL

The dial URL of a task on a broker is the URL where a worker connects to claim the task. It is
the `dialURL` field of the instance record.

See [Tasks](reference/tasks.md#executing-one-by-hand).

### doer

A doer is who does the work of a `spawn`. A [provider selection](#provider-selection) picks
it, and the task records it: `doer: provider.treat.fixer`.

See [Provider selection](provider-selection.md).

### provider

A provider is a doer that is registered as an entity, `provider.<capability>.<id>`. Its
payload holds what a selection matches on, and what the provider gives to the spawn.

A "model provider" is another thing: the service that serves a model to the `llm` driver.

See [Provider selection](provider-selection.md#the-registry).

### provider selection

A provider selection is the `provider` field of a `spawn`. With it, verse chooses the doer
when the rule fires. The `select` field says who chooses: `auto`, `ask` or `vote`.

See [Provider selection](provider-selection.md#selecting).

### virtual key

A virtual key is the model credential that a task runs under. The `virtualKey` field holds
the address of the node that holds the key, such as `acme.keys.review`.

See [Provider selection](provider-selection.md#which-credential-it-runs-under).

### environment

An environment is a named workspace that a runner defines: where the code comes from, what may
run against it, where the result may go. A `spawn` names one with `environment:`.

See [Examples](reference/examples/README.md#the-environment-is-the-unit-of-narrowing).

### root (of verse up)

`-root` on `verse up` names the directory in which the tasks of the verse process act. It is a
directory on disk, and not an address.

Also written: the agent root.

See [Starting a verse](reference/up.md#the-agent-root).

## Ios

### io

An io is a program that connects verse to one external system, such as a git repository. It
has two halves, a [source](#source) and a [sink](#sink). An installed io is the entity
`verse.io.<name>`.

Also written: connector, source (in the Go source, for the whole io), registration.

See [Ios](io.md).

### root (of an io)

The root of an io is the first segment of every address that the io writes, such as `git`.
`verse io add` takes it as `-root git`.

See [Ios (reference)](reference/io.md).

### claim (of an io)

A claim of an io is a pattern that names what only that io may write:
`owns: [{at: "git.verse"}]`. Verse refuses an io whose claims overlap the claims of another.

Also written: owns, ownership claim.

See [Ios (reference)](reference/io.md#ownership).

### source

A source is the in half of an io. Verse asks it what is true in the external system, and it
answers with a [reflection](#reflection). It does not write to the store.

See [Ios](io.md#the-seam-a-source-answers-verse-converges).

### reflect

To reflect an external system is to keep its state in the store, as entities. The source
reads the system, and the [reflector](#reflector) writes the store.

See [Ios](io.md).

### reflection

A reflection is one answer of a source. Its `entities` are what the source read. Its `covers`
say where the answer is complete.

See [Ios (reference)](reference/io.md#the-contract-an-io-serves).

### cover (of a reflection)

A cover of a reflection is a pattern under which the reflection is complete, such as
`git.verse.ref.*`. Verse removes a stored entity that a cover reaches and that the reflection
does not hold.

See [Ios (reference)](reference/io.md#the-contract-an-io-serves).

### converge

To converge is to make the store match a reflection. Verse writes what changed, and tombstones
what a cover reaches and the reflection does not hold.

The page [Convergence](convergence.md) is about how rules come to rest.

See [Ios](io.md#the-seam-a-source-answers-verse-converges).

### tombstone

To tombstone an entity is to remove it from the store because its external system does not
hold it any more.

See [Ios](io.md#the-seam-a-source-answers-verse-converges).

### reflector

The reflector is the part of verse that drives one installed source. It asks for a full
reflection every minute, and for a part of one when a hint arrives. It converges the store
with each answer.

See [Ios (reference)](reference/io.md#the-reflector).

### hint

A hint is a message from an io to verse that says which addresses went stale. It writes
nothing. Verse answers it by reflecting those addresses again.

See [Ios](io.md#the-core-law-a-hint-is-a-hint-reconcile-is-the-truth).

### sink

A sink is the out half of an io. Somebody writes an input, which is a node with a `to` field.
The sink makes the external system match it, and writes the result at `<name>~status`.

See [Ios (reference)](reference/io.md#the-out-half).

### iod

`iod` is the binary that holds the five ios that verse ships. Each is a subcommand: `git`,
`git-issue`, `slack`, `linear` and `github`.

See [iod](reference/iod.md).

### mirror (of a git repository)

A mirror of a git repository is a copy of a remote repository, kept fetched.
`iod git -mirror <url>` reflects from one, and the coderunner example clones from one.

In a page about git, "mirror" has this meaning.

See [iod](reference/iod.md#git).

## Permissions

### principal

A principal is who acts. It is a name with its kind in front, such as `user:ana@example.com`.
Every write names one. Verse has no login: a principal is the name that the request gives.

See [Permissions](perms.md#principals-and-grantees).

### kinds of principal

| Kind | Who it is |
|---|---|
| `user:<email>` | a person |
| `agent:<name>` | an agent that drives verse through the API |
| `runner:<identity>` | a runner, or a worker that claimed a task |
| `system:<part>` | verse itself. The part says which part of verse wrote, such as `system:mirror`. |
| `via:<address>` | an installed thing that acts: a rule, an io or a resolver. It acts as whoever installed it. |

A request presents `user:`, `agent:` or `runner:`. Verse makes the other two.

See [Permissions reference](reference/perms.md#the-grammar).

### local user

The local user is the principal `user:<login>@localhost`. The CLI acts as the local user
toward a verse on the same machine, when no user is configured.

See [Permissions, walked](perms-guide.md#before-the-first-step-who-you-are).

### grantee

A grantee is who a grant is to. It is a principal, or a set of principals.

| Grantee | Who it admits |
|---|---|
| `user:ana@example.com` | that principal |
| `user:*` | every principal of the kind |
| `group:payments` | the members of the group |
| `*` | everyone |

See [Permissions reference](reference/perms.md#the-grammar).

### group

A group is a named set of grantees, with an owner: `verse group add payments ana@example.com`.
It is the entity `verse.principal.group.<name>`.

See [Permissions](perms.md#grants).

### owner

The owner of a node may do anything to the node. The owner is fixed when the node is created:
it is the creator, or the owner of the node above. The owners of the nodes above are
co-owners.

See [Permissions](perms.md#ownership).

### record

A record is what verse keeps about the permissions of one node. It holds the `owner` of the
node, its `grants` and its `child-grants`. `verse perms <path>` shows it. A node with no
record follows the nearest record above it.

Also written: permissions record, perms record.

See [Permissions reference](reference/perms.md#records).

### mirror (of permissions)

In the Go source, the perms mirror is the set of all records, under `verse.perms`. The docs
say "the records".

See [Permissions reference](reference/perms.md#records).

### grant (permission)

A grant is one line of a record, such as `- read: [user:ana@example.com]`. It gives a level
to grantees who are not the owner. It reaches down to the next record that names an owner.

For the word in a `spawn`, see [grant (of a spawn)](#grant-of-a-spawn).

See [Permissions](perms.md#grants).

### level (of a permission)

A level is what a principal may do at a node.

| Level | What it admits |
|---|---|
| `read` | to read the node and what is beneath it |
| `read-write` | `read`, and to write into the node |
| `owner` | `read-write`, and to delete the node, replace it and set its record |
| `control` | see [control (permission level)](#control-permission-level) |

See [Permissions reference](reference/perms.md#levels).

### control (permission level)

`control` is the level that admits steering work: cancel, pause, resume, `say`, a change of
vector, and suspend. No other level includes it. The owner of a task holds it, and so does
the worker that claimed the task.

Written as code, `control` is this level.

See [Permissions reference](reference/perms.md#levels).

### child-owner

`child-owner` is the grant that lets its grantees create nodes beneath a node, and own what
they create. It gives nothing over a node that stands already.

See [Permissions](perms.md#grants).

### child-grants

`child-grants` is the field of a record that says which grants a node created beneath it
starts with. Verse copies it when the node is created.

See [Permissions](perms.md#grants).

## Steering

### observation plane

The observation plane is the part of verse that reads: `get`, `list` and `watch`. A read may be
late.

See [Deployment and steering](deployment-and-steering.md#two-planes).

### control plane

The control plane is the part of verse that steers. Verse serves it before observation, and it
never travels on the stream of changes.

For the permission level, see [control (permission level)](#control-permission-level).

See [Deployment and steering](deployment-and-steering.md#two-planes).

### steering

To steer is to act on work in progress: to answer a gate, to hold or resume, to `say`, to
cancel, to change a vector.

See [Overview](overview.md#observation-and-control-are-separate-planes).

### flow

A flow is the live tasks about one subject, or about the subjects that a pattern names. Verse
keeps no record of it: `verse flow show demo.pr.1` builds it.

See [Control](control.md).

### hold

A hold stops one run at its next boundary, until somebody resumes the run:
`verse flow suspend -task <id>`. It does not last across a restart of verse.

Also written: pause.

See [Control](control.md#a-hold-on-a-run).

### suspension

A suspension is a record that names a charter, a rule and a subject. It stops all the work
about them: firings, tasks and questions. It lasts across restarts, until somebody lifts it.

See [Control](control.md#a-suspension).

## Index

+ [action](#action)

+ [address](#address)

+ [answer contract](#answer-contract)

+ [answerer](#answerer)

+ [answerers](#answerers)

+ [ballot](#ballot)

+ [baseline](#baseline)

+ [baseline charter](#baseline-charter)

+ [binding](#binding)

+ [boundary](#boundary)

+ [broker](#broker)

+ [chain (of an action)](#chain-of-an-action)

+ [chain (of firings)](#chain-of-firings)

+ [charter](#charter)

+ [child-grants](#child-grants)

+ [child-owner](#child-owner)

+ [claim (of a worker)](#claim-of-a-worker)

+ [claim (of an io)](#claim-of-an-io)

+ [commit](#commit)

+ [condition](#condition)

+ [control (permission level)](#control-permission-level)

+ [control plane](#control-plane)

+ [controller](#controller)

+ [converge](#converge)

+ [cover (of a reflection)](#cover-of-a-reflection)

+ [cover (of a resolver)](#cover-of-a-resolver)

+ [deadline (of a gate)](#deadline-of-a-gate)

+ [deadline (of a resolver)](#deadline-of-a-resolver)

+ [derive](#derive)

+ [dial URL](#dial-url)

+ [docket](#docket)

+ [doer](#doer)

+ [driver](#driver)

+ [entity](#entity)

+ [environment](#environment)

+ [epoch](#epoch)

+ [event stream](#event-stream)

+ [exclusive resolver](#exclusive-resolver)

+ [executor](#executor)

+ [federation](#federation)

+ [firing](#firing)

+ [flow](#flow)

+ [frame (of a spawn)](#frame-of-a-spawn)

+ [frame (of an event stream)](#frame-of-an-event-stream)

+ [gate (of a rule)](#gate-of-a-rule)

+ [gate (of a task)](#gate-of-a-task)

+ [gate (raised by a worker)](#gate-raised-by-a-worker)

+ [grant (of a spawn)](#grant-of-a-spawn)

+ [grant (permission)](#grant-permission)

+ [grantee](#grantee)

+ [group](#group)

+ [held rule](#held-rule)

+ [hint](#hint)

+ [hold](#hold)

+ [hook](#hook)

+ [inbox](#inbox)

+ [instance record](#instance-record)

+ [io](#io)

+ [iod](#iod)

+ [kinded path](#kinded-path)

+ [kinds of principal](#kinds-of-principal)

+ [ladder](#ladder)

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

+ [operator](#operator)

+ [option](#option)

+ [owner](#owner)

+ [package](#package)

+ [patch](#patch)

+ [path read](#path-read)

+ [pattern](#pattern)

+ [payload](#payload)

+ [pose](#pose)

+ [predicate](#predicate)

+ [principal](#principal)

+ [provider](#provider)

+ [provider selection](#provider-selection)

+ [question](#question)

+ [reads declaration](#reads-declaration)

+ [reason](#reason)

+ [record](#record)

+ [reflect](#reflect)

+ [reflection](#reflection)

+ [reflector](#reflector)

+ [report](#report)

+ [resolver](#resolver)

+ [revision](#revision)

+ [root (of an io)](#root-of-an-io)

+ [root (of the store)](#root-of-the-store)

+ [root (of verse up)](#root-of-verse-up)

+ [rule](#rule)

+ [run](#run)

+ [run record](#run-record)

+ [runner](#runner)

+ [schedule](#schedule)

+ [scope](#scope)

+ [seam](#seam)

+ [seq](#seq)

+ [sink](#sink)

+ [source](#source)

+ [spawn](#spawn)

+ [standing resolver](#standing-resolver)

+ [status (of a task)](#status-of-a-task)

+ [steering](#steering)

+ [step](#step)

+ [store](#store)

+ [stratum](#stratum)

+ [subject](#subject)

+ [suspension](#suspension)

+ [task](#task)

+ [tick](#tick)

+ [tombstone](#tombstone)

+ [tool](#tool)

+ [trouble](#trouble)

+ [vector](#vector)

+ [verse's own half](#verses-own-half)

+ [virtual key](#virtual-key)

+ [when](#when)

+ [worker](#worker)
