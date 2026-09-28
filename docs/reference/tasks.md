# Tasks

A [task](../glossary.md#task) is a unit of work that verse governs. An agent, a shell
command, a [worker](../glossary.md#worker) behind a firewall or a person does the work.
[Tutorial §3](../tutorial.md#3-tasks) runs one task. This page is what a user needs to
run a task, watch it, steer it and answer its gates.

## Running one

```sh
verse task run "echo summarizing; sleep 2"       # prints a run-… id
verse task run -wait "echo done"                 # …and block for the final output
verse task run -attach 'Write a file hello.txt containing "hi".'    # …and stream it
verse task run -driver scripted "go test ./..."
verse task run -driver calls "$(cat merge.tony)"    # the actions themselves, no model
verse task run -answer-contract shape.tony "…"    # …and declare the shape its answer must have
verse task run -gates gates.tony "…"     # …under gates of its own, each with its question
verse task
verse task get <id>        # snapshot: status, prompt, driver, runner, result, pending gates
verse task say <id> "also mention the dial"
verse task vector <id> ask -as walter@example.com
verse task vector <id> -file vector.tony
verse task cancel <id>
```

`say` and `vector` do different things:

| Command | What it does |
|---|---|
| `say` | prompts the agent again, with new instructions |
| `vector` | replaces the [vector](../glossary.md#vector) that governs how much the task decides alone. Verse applies it at the next [boundary](../glossary.md#boundary), with no restart. |

`-as` names who made the change. Both commands work wherever the task runs. The local runtime
applies them directly. A brokered or remote worker gets them through its inbox.

The `spawn` of a [rule](../glossary.md#rule) runs a task in the same way, from the same
`launch.Spec`. What a charter may say about a spawn is in
[the trigger model](action.md#action--what-fires).

### `-answer-contract`: what finished means

```sh
verse task run -answer-contract shape.tony "check every reference in docs/runners.md"
```

`-answer-contract <file>` names the contract file. The file is a tony match pattern. It is
the ad-hoc form of a charter's `answer:`, and it is the run's definition of **done**.

Without a contract, finished means only that the model stopped calling tools. A model that
describes its next step also stops calling tools, and from outside you cannot tell the two
apart. A run can end in the middle of an investigation, with a page of working notes as its
`result` and `done` on its event stream.

With a contract, verse does three things:

- it checks the final message as soon as the model stops;
- on a mismatch, it sends the model a message that names what is wrong;
- when the run never satisfies the contract, the run **fails**.

Every rule already declares a branch for a failure. A charter that cannot read the fields of
what it got has no such branch.

A contract also does a second job: it tells *nothing to do* from *something went wrong*.
Those two are the same on disk and the same in an exit status, and a runner cannot tell which
one happened.

Keep the contract small, and let it govern only what a rule will match on. A
`summary: !irtype string` field looks harmless, and it is a trap. A model writes
`summary: nothing was wrong`, which is not valid tony. The run then fails for a reason that
has nothing to do with the work.

Put the prose account outside the governed document. `answer.Extract` lifts a fenced block
out of the final message, so the rest of the message can be free prose.

## The drivers

A task has one of three [drivers](../glossary.md#driver). Use `calls` when the work has a
right answer.

| Driver | What it does |
|---|---|
| `scripted` | runs its prompt as a shell command |
| `llm` | gives the prompt to a model, which decides what to do one turn at a time |
| `calls` | takes the actions themselves. The charter wrote them, and nothing decides anything at run time. |

```tony
spawn:
  vector: auto
  calls:
  - tool: run_bash
    input: {command: "git merge --ff-only origin/$branch"}
  - tool: push
    input: {destination: main}
```

The list is a field. So:

- the engine checks the list when the rule fires;
- `verse c rules <name>` prints it;
- verse expands each argument on its own. A `$branch` with a quote in it is one argument, and
  not text joined into a document.

The presence of `calls` selects the driver. You may write `driver: calls`, and it must not
contradict the list.

Each entry is an ordinary action. Verse derives it, gates it and reports it on the event
stream. So a gate written for an agent's push also holds a push in a list, and nothing later
can tell which driver proposed the action.

A list does not get the parts that belong to a model:

- no answer contract. Nobody is there to repair an answer, so a spawn that carries a contract
  fails.
- no delegation;
- no new plan after a result.

An entry may say which of its outcomes end the list:

```tony
- tool: push
  input: {destination: retire}
  fatal: [err]        # a remote declining ONE ref is not a reason to stop
```

The outcomes are:

| Outcome | Meaning |
|---|---|
| `err` | the step failed |
| `blocked` | a gate or the far side refused the step |
| `nonzero` | a command exited non-zero |

The field has three states, and the empty one is not the default:

| written | means |
|---|---|
| *(absent)* | all three end the list. A list is ordered because its calls depend on each other. |
| `fatal: []` | nothing ends it. The list runs to the end, whatever happens to this call. |
| `fatal: [err]` | these |

`scripted` is this driver, with a list of one command that verse builds for it. When you type
`verse task run -driver calls "$(cat merge.tony)"`, the CLI reads the document with the same
grammar that a charter uses, and sends the field. So a list that does not parse is a message
on your terminal.

## The dial

The drivers have different default vectors. The difference is the purpose of a
[gate](../glossary.md#gate-of-a-task): a gate asks about what you have **not** already
authorized.

**`scripted` as you type it runs with no gate.** `verse task run -driver scripted` sends
`auto` for you, because the prompt *is* the shell command. After
`verse task run "go test ./..."`, a question "run a shell command?" would ask you to confirm
the sentence you just wrote.

That reason is about who made the request, so it applies only to what you type. **A charter
that spawns `driver: scripted` is asked**, for each command, like any other action. The
charter typed nothing, and the reader of a charter is not the person who wrote it. A charter
that means to run with nobody watching says so, on the same line that an `llm` spawn uses:

```tony
spawn: { driver: scripted, vector: auto, prompt: "echo reviewing $subject" }
```

`vector: auto` is also how a charter says "the firing already decided this". The rule's own
`gates:` run for each firing, before the action. `vector: auto` stops the same decision from
being made twice. A driver that ignored the `vector:` of the spawn would disobey, without a
word, a charter that asks to be asked.

`llm` asks before an action with an effect, because the agent *chose* the action. Its reads
run freely. These block until somebody approves them:

- writing a file;
- running a shell command;
- delegating to a sub-agent.

Approving a command that the agent made up is a real decision.

The set of actions is not fixed at four. **A runtime may add an action of its own.** The
coding runner adds `push`, which sends work to a destination that its environment declares.
What arrives at the gate is an ordinary action with an ordinary typed effect. So a vector
decides it in the same way, and `verse gate` shows it in the same way.

```sh
verse gate                                  # task gates, and engine gates that ASK
# - gate: write
#   id: run-f3e31ed0390f~d1
#   mode: ASK
#   task: run-f3e31ed0390f
#   reason: "verb write"
#   tool: write_file
#   where: task
verse gate approve run-f3e31ed0390f~d1 -as you@example.com    # or: task approve run-… run-…~d1
```

**Verse checks who may answer before it delivers the answer.** These may answer:

- the owner of the task, or the owner of the charter, for what their own work asked;
- a gate's `answerers`, or the resolver that a question is held out to;
- a person, through the docket's default, for a question held out to no resolver.

The worker that works the task holds nothing on the question that it raised, and verse
refuses its answer. A refusal is a 403 that names who holds what, and which record decided
([perms.md](../perms.md)).

Whoever may answer may also pass the question on, with
`verse gate escalate <id> -as <who> [-say <why>]`. The question goes to the next resolver that
matches its subject, or to the docket when there is none.

**A charter gate is listed here, whatever it takes to settle it.**

- A gate with `need: 2` lists with its `options`, and with its `ballots` as answers arrive.
  `verse gate approve <id> -as <who>` casts one ballot. The record settles in the same write as
  the ballot that reaches the threshold.
- A gate with a `deadline` lists the deadline. It settles on its default when the clock
  passes.

**A provider selection is listed here too.** A `spawn` with a `provider` and
`select: ask|vote` poses a question. Its options are the eligible doers. Each option carries
the envelope that the doer would run under, and `chain` names the action that the question
holds up. You answer it by option:

```sh
verse gate verse.trigger.treat.provider~aa92f8cc -answer fixer -as you@example.com
```

`within` is the deadline of the selection, and it has no default. When the clock passes with
no pick, the question settles on `none`, and nothing spawns.

The `~` that separates the gate from its task needs no quotes and no escape, in a shell or in
a URL.

On a terminal, `run -attach` asks inline instead
(`⏸ gate "write" (write_file) — approve? [y/N]`). Answer `y` to allow, and anything else to
deny. A denied gate comes back to the agent as an error, and the agent makes a new plan. When
the output is piped (not a tty), `attach` only streams. The gate is the same question in both
cases. It stays in `verse gate`, and anybody can resolve it by id from anywhere.

### A gate's question

A task's own gate poses the same record that a charter gate poses, and says the same things
about it:

- how many ballots settle it;
- what it settles on;
- how long it stands.

A named vector is only a default: `auto`, `ask` or `park`. A run that wants gates of its own
writes them, as the `gates:` of a spawn or as a file given to `-gates`. The shape is the one
that `task vector -file` reads:

```tony
- name: shell
  mode: ask
  match: {verbs: [exec]}
  need: 2                                    # proceed takes two ballots; one deny refuses
  deadline: {within: "4h", default: deny}    # and the clock decides silence
- name: delegate
  mode: ask
  match: {verbs: [call-agent]}
```

`match` is over what the action does, not over the name of the tool:

| Dimension | What it matches |
|---|---|
| `verbs` | `read`, `write`, `create`, `delete`, `exec`, `network`, `call-agent`, `resolve`, `push` |
| `tools` | the name of the tool |
| `paths`, `resources`, `branches`, `onPush` | what the action touches |

Every dimension that a gate writes must hold. A dimension that the gate leaves out holds for
anything.

`need`, `options` and `deadline` mean what they mean on a
[charter gate](gates.md#gates--whether-it-proceeds). Verse refuses them in the same way when
they cannot be posed as written. It refuses them at the spawn or at `task vector`, and never
at the boundary that a running task reaches.

**A run that asks before its one command** needs no credentials. A scripted run is one shell
command, which is `exec`. So a gate over `exec` stops it before it runs:

```sh
cat > gates.tony <<'EOF'
- name: shell
  mode: ask
  match: {verbs: [exec]}
  deadline: {within: "10m", default: deny}
EOF

verse task run -driver scripted -gates gates.tony "date -u"      # run-4e919ce01f99
verse gate
# - gate: shell
#   id: run-4e919ce01f99~d1
#   mode: ASK
#   reason: "verb exec"
#   tool: run_bash
#   effects:
#     resources:
#     - kind: task
#       ref: "date -u"
#     verbs:
#     - exec
#   deadline:
#     default: deny
#     within: 10m
verse gate approve run-4e919ce01f99~d1 -as ana@example.com       # the command runs
verse task get run-4e919ce01f99                                   # status: done
```

Without `-gates`, the same command runs straight through, because `-driver scripted` as you
type it is `auto`.

If nobody answers the question, the deadline settles it on `deny` after ten minutes:

- the command never runs;
- the question's record says `by: deadline` and `verdict: deny`;
- the run ends `failed`, because a refused call ends a list.

**The gates apply on top of the default of `-vector`, and the strictest gate that matches
wins.** The order is `auto`, then `ask`, then `park`. So a gate makes an action stricter than
the default, and never looser. Under `-vector ask`, a `mode: auto` gate over `exec` changes
nothing, and verse asks about the command with `reason: "no gate matched"`. With `-gates` and
no `-vector`, the default is `auto`, because the gates do the asking.

**The deadline is the question's own clock, and it settles the question.** With
`default: deny`, a run with nobody watching does not stay held on a question that nobody will
find. When the clock passes, verse refuses the action as `by: deadline`. The refusal comes
back to the agent, and the agent makes a new plan, as it does after a person's no.

| Default | When the clock passes |
|---|---|
| `default: deny` | verse refuses the action |
| `default: proceed` | the action goes through |
| no default | the question settles on `none`. The loop also reads this as a refusal: nobody made it, and the record says so. |

A resolver's own `deadline` is a different clock. It passes the question up to the layer
above, and does not settle it. Both instants are on the record, and the earlier one fires
first.

**A delegation's question offers the child modes.** A gate over `call_agent` asks how verse
supervises the child. So its options are modes, not a yes and a no:

| Option | What happens |
|---|---|
| `proceed` | the child runs under the parent's gates and resolver |
| `careful` | verse asks again about every action the child takes |
| `autonomous` | the child and its subtree answer their own gates, within the granted scope |
| `deny` | nothing spawns |

Answer by option, as for any question with options:

```sh
verse gate run-f3e31ed0390f~d2 -answer careful -as you@example.com
```

A gate that wrote `options` of its own over a delegation keeps them. A `need` on such a gate
is the threshold for every mode.

<a id="who-you-addressed"></a>
### The rough edge: who you addressed

Which rule applies depends on how you addressed the runtime, and the runtime cannot tell.

When you ask an *agent* to do something, you expect it to ask before a write. You chose a
loop, and you watch it.

When you give it a *command*, such as `verse task run "go test ./..."`, you addressed a
runner, not an agent. But with credentials configured, the default driver is llm. So the
command that you just typed comes back as `⏸ gate "shell" (run_bash) — approve?`. The
question asks nothing that you had not already decided. It shows only that verse chose an
agent to carry out your command.

The driver picks the vector, unless the spawn says otherwise. Verse cannot read which of the
two things you meant by the prompt, so you say it:

| You want | You pass |
|---|---|
| to run the prompt as a command | `-driver scripted`. This is its purpose. |
| to keep the agent and not be asked | `-vector auto` |
| to change after the task starts | `task vector <id> auto` |

A rule meets the same problem from the other side. Its gates approve *the firing*. They do
not approve the actions that the spawned agent then chooses. The rule can say that it already
decided, with `vector: auto` on the spawn. But then the charter asserts the equivalence; the
runtime does not derive it. And `vector: auto` covers all actions or none, while the firing may
have authorized one action and not another.

## The mirror

The [mirror](../glossary.md#mirror-of-tasks) writes every task into the store as
`verse.task.instance.<id>`. So you observe the fleet of tasks by observing entities. A
completion is an entity change, and a rule can react to it.

```sh
verse watch verse task instance '*'
verse data verse task instance <id>
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

`observed: false` appears while the mirror cannot reach the runner that holds the task. The
other fields of the entity are then the last thing the mirror heard, not the current truth.

The instance record outlives the task. These stay after the live registry retires the task:

- the result;
- the last `say`;
- the last tool output;
- the terminal `status`.

`verse flow show` reads them for history. The mirror is an index, not a store of everything.
It publishes what an observer needs, not the transcript.

### One id, many runs

A task id names the *work*, not the run. Verse derives the id from the action that spawns and
from the subject. So a rule that fires twice about one subject spawns the same id twice. This
makes a second firing idempotent, and it is what `verse flow show demo.pr.1` addresses.

`verse.task.instance.<id>` is therefore the latest [run](../glossary.md#run). Each run has a
record of its own:

```sh
verse data verse task run '<id>@<occurrence>'
verse data verse task run '*' -if '{flow: "demo.pr.1"}'
```

The `occurrence` is the store revision that the run is about. For a firing, it is the commit
that caused the firing. For a task that a person spawned by hand, it is the revision at which
verse created the record. The occurrence is on both records, and it tells one run from the
next.

Read the instance to learn what this work is doing now. Read the runs to learn what happened
the other times. A failed run followed by a successful one leaves both run records. The
instance alone cannot show both: it holds what the newest run wrote, and nothing of the run
before.

Verse writes a run record twice, at the start of the run and at its end. So a run that never
finishes still leaves a record. **Nothing prunes run records.** They are small, and their
number grows with runs, not with events. To remove them, use `verse data -rm`, or a charter
that marks the stale ones [vanishing](../change-events.md) and acts on the edge.

## The question record

Verse publishes a gate that stops for an answer as an entity of its own,
`verse.gate.question.<id>`. The task points at it (`pending: verse.gate.question.<id>`).

```tony
payload:
  id:      review@demo.pr.1~d3
  origin:  review@demo.pr.1
  gate:    write-guard
  mode:    ASK
  status:  pending
  subject: demo.pr.1
  tool:    edit_file
  effects: {verbs: [write], paths: [cfg/app.tony]}
```

`effects` is what the action *would do*. Verse derives it before it poses the gate. So a rule
can select the questions it cares about without reading prose:

```sh
verse data verse gate question '*' -if '{status: pending, effects: {verbs: [write]}}'
```

The question record is a record, not a mailbox. Answers go through the resolve API. After an
answer, the entity settles to `status: answered`, with the verdict and who gave it. A question
outlives its answer.

`resolver` names the resolver that the question is held out to. Verse picks it in this
order:

1. the resolver that a spawn or a rule named;
2. else the standing resolver that matches the subject;
3. else none. The field is absent, and the question is on the docket that a person reads.

`reownedFrom` appears when a question changes hands. `reownedWhy` says what moved it:

| `reownedWhy` | What moved the question |
|---|---|
| `escalated` | its owner declined it |
| `overdue` | the resolver's deadline passed |
| `removed` | its owner was uninstalled |

In each case the question goes to the layer above: the standing resolver that matches its
subject, or else the docket that a person reads. The question itself does not change. An
escalation changes who answers, not what is being answered.

`delivered: false` appears when verse has accepted an answer, and the worker that the answer
is for has not yet taken it. This happens with a brokered task, where the answer waits on the
inbox until a worker dials in. The gate closes to other answers as soon as verse accepts one.
What is still in doubt is whether the party that must act on the answer knows it. The field
clears when the worker acknowledges the answer.

```sh
verse data verse gate question '*' -if '{delivered: false}'   # answers still in flight
```

Every answer names its answerer: `verse gate approve <id> -as you@example.com`. Two people
who answer at once is a normal case.

| Case | What verse answers |
|---|---|
| two answers at once | exactly one applies. Verse records the other under `stale` and tells its sender (`409`, naming who won). |
| an answer to an id that nobody asked about | `404` |

A `404` means: look for your typo. A `409` means: somebody else answered first.

A restart settles what nobody waits on:

- a question that a task raised survives the restart exactly as its task does;
- verse settles a question that a firing raised, because what waited on the answer was a
  goroutine in the verse process.

See [task control](../task-control.md).

A completion hook (`onDone` and the others) reads the task's own payload: `$status`,
`$result`, `$data`, `$err`, `$chain`. The task's flow is its subject, so the hook's `$subject`
already names it.

**`status: done` means that the task *stopped*, not that the work succeeded.** A hook that
advances state should name evidence (`patch: {stage: reviewed, severity: $data.severity}`),
and not trust a bare claim. See [bindings](action.md#bindings) and
[the answer contract](action.md#the-answer-contract).

## Steering

A [flow](../glossary.md#flow) is not stored. It is a query over live tasks. So steering
takes a selection, not a ref. Steering a whole set needs no new mechanism: it is the same
selector with a wider setting. A task that you run by hand is a flow too. It is the flow of
its `-subject`, or of its own id when it names none.

The verbs, their flags, and how a hold on a run differs from a suspension are in
[control](../control.md). This section says what the verbs do to a task.

```sh
verse flow                                     # live tasks and suspensions
verse flow show demo.pr.2                      # the tree: what ran, what is running, what comes next
verse flow suspend -task review@demo.pr.2      # hold that run at its next boundary
verse flow resume  -task review@demo.pr.2
verse flow say     -subject demo.pr.2 "hurry" # to every live task about it
verse flow cancel  -task review@demo.pr.2      # end that run (destructive)
```

`say` and `cancel` answer with three things: how many tasks they reached, the selection they
reached them by, and the verb. `suspend` and `resume` answer with the row that they held or
lifted.

```tony
affected: 1
subject: demo.pr.2
verb: say
```

The verbs land at each task's next boundary. So you can hold, redirect or stop a run while
it runs, and every *other* flow keeps reacting.

A scripted task is one command:

- `cancel` stops it at once, by killing the command;
- a hold and a `say` land at its next boundary, which for a task of one command is when the
  task ends.

The verbs take effect during the run of a task with many steps, such as one with the `llm`
driver, where every tool call is a boundary.

A [hold](../glossary.md#hold) on a run is the operator's ad-hoc stop: "hold this, I want to
look". It differs from two other things:

- `cancel` ends the work;
- a `park` gate is a hold that the *charter* declares for each action.

A hold on a run is also forgetful. It holds that task, and the rules go on firing beside it.
To stop the work about something, and keep it stopped, suspend it instead. A suspension stops
new firings, withdraws the questions from the docket, and makes hooks record and start
nothing.

```sh
verse flow suspend -subject demo.pr.2 -why "a person is reviewing it by hand"
verse flow show demo.pr.2                      # heldBy: [<the suspension>] on the live task
verse flow resume -task review@demo.pr.2       # refused, 409: a suspension holds it
verse flow resume -subject demo.pr.2
```

A task knows whose hold it is under: `heldBy` on its record and in `flow show`. So a hold and
a suspension do not undo each other. Lifting one resumes only what nothing else holds. An
operator's hold outlives a suspension, whichever came first.

Every verb here asks for `control` of each task that it reaches, as `task cancel` does. The
task's owner holds `control`, and so does the worker that its claim granted. When verse
refuses a `say` for any one of its tasks, it refuses the whole `say`, and names that task.

With `-scope`, every verb here reaches the tasks that the scope started, and none that the
scope only sees. The verbs are `verse flow suspend|resume|say|cancel`,
`task cancel|say|vector` and the runtime's `/tasks/{id}/pause|resume`. Aimed at the work of
baseline, a verb with `-scope` refuses and says so. So
`verse flow cancel -scope me -task review@demo.pr.2` never stops baseline's review of that PR
by accident. Drop the flag to address baseline. See
[what a scope owns](connect.md#what-a-scope-owns).

## Where a task runs is a seam

A task runs in the verse process by default. A spawn can name another place with `runner:`,
and `verse task` shows where each task runs. Every command on this page works wherever the task
runs. How the backends, the broker and a worker work is in
[tasks-worker.md](../platform/tasks-worker.md), in the repository.

### Executing one by hand

A task on the broker waits for a worker, and a person can be that worker. How a worker claims
the task, raises gates and completes it with `verse task do` is in
[tasks-worker.md](../platform/tasks-worker.md#executing-one-by-hand), in the repository.

## Why it is this way

The reasons for this design are in [reference-tasks.md](../design/reference-tasks.md), in the repository.
