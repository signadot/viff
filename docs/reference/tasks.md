# Tasks

A [task](../glossary.md#task) is a unit of work that verse governs. An agent, a shell
command or a remote [worker](../glossary.md#worker) does the work.
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
verse task say <id> "also mention the dial"   # an ask to the task: said to <id>: <ask id>
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
applies them directly. A remote worker gets them through its runner.

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
spawn:
  driver: scripted
  vector: auto
  prompt: "echo reviewing $subject"
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
decides it in the same way, and `verse question` shows it in the same way.

```sh
verse question                              # task gates, and engine gates that ASK
# - gate: write
#   id: run-f3e31ed0390f~d1
#   mode: ASK
#   task: run-f3e31ed0390f
#   reason: "verb write"
#   tool: write_file
#   where: task
verse question approve run-f3e31ed0390f~d1 --as you@example.com  # or: task approve run-… run-…~d1
```

**Verse checks who may answer before it delivers the answer.** Who may answer is in
[tasks and gates](../perms.md#tasks-and-gates). The worker that works the task may not answer
the question that it raised.

A person who answers may answer through a [resolver](../attached-resolvers.md) they granted, and
the answer is still theirs.

**A charter gate is listed here, whatever it takes to settle it**, with its `options`,
`ballots` and `deadline` ([the tally](gates.md#the-tally)).

**A provider selection is listed here too.** Its options are the eligible doers
([what you can see](../provider-selection.md#what-you-can-see)). You answer it by option:

```sh
verse question verse.trigger.treat.provider~aa92f8cc --answer fixer --as you@example.com
```

The `~` that separates the gate from its task needs no quotes and no escape, in a shell or in
a URL.

With an identity, `run -attach` and `attach` ask inline instead
(`⏸ gate "write" (write_file) — approve? [y/n]`). Answer `y` to allow and `n` to deny. Any
other answer is asked again; it is not read as a refusal. A denied gate comes back to the agent
as an error, and the agent makes a new plan. Without an identity, `attach` only streams, and
prints the `verse question` command that answers. The gate is the same question in both cases. It
stays in `verse question`, and anybody can resolve it by id from anywhere.

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

`need`, `options`, `deadline` and `answerers` mean what they mean on a
[charter gate](gates.md#gates--whether-it-proceeds). Verse refuses them in the same way when
they cannot be posed as written. It refuses them at the spawn or at `task vector`, and never
at the boundary that a running task reaches.

**Whom a task's gate asks.** A gate that names nobody asks the docket. `answerers: [$principal]`
asks whom the task acts for, which for a run you start is you. Verse refuses `$principal` for a
task that acts for nobody: one run as the system, or a rule's task without `as: subject`. A task
that acts for somebody else asks them with every question that names nobody: an ask's `then`
asks its asker, and a spawn `as: subject` asks the subject's owner. The task's record says so,
as `answerers: [$principal]` beside `asker:`.

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
verse question
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
verse question approve run-4e919ce01f99~d1 --as ana@example.com   # the command runs
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

**The deadline is the question's own clock, and it settles the question**
([a deadline settles](gates.md#a-deadline-settles)). With `default: deny`, a run with nobody
watching does not stay held on a question that nobody will find. A refusal comes back to the
agent, and the agent makes a new plan, as it does after a person's no.

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
verse question run-f3e31ed0390f~d2 --answer careful --as you@example.com
```

A gate that wrote `options` of its own over a delegation keeps them. A `need` on such a gate
is the threshold for every mode.

### A question for the person

A task can ask the person it runs for. The `ask` tool — offered to the `llm` driver and to a
call list — poses a question and blocks until it is answered, and the answer is the tool's
result. It is a gate's record: on the docket, in `verse question`, in Slack, answerable from any of
them, withdrawn if the task is cancelled. It names `$principal`, so it waits on whom the task acts
for: whoever launched it, or the asker it runs for. A task acting for nobody, a rule's without
`as: subject`, asks the docket. A call that names `answerers`, grantees as a gate's
`answerers` names them (`user:*`, `group:owners`), holds the question out to them instead, which
is how a system charter's call list asks anybody at all. Its one option, `reply`,
takes words:

```sh
verse question run-4708ef20b92d~3 --answer reply --say "the second one" --as you@example.com
```

Attached, the prompt is the question and a line:

```
{cursor: 4 kind: pending id: run-4708ef20b92d~3 tool: ask text: "which one?" …}
  ❓ which one?
  > the second one
  → sent
```

A line typed while no prompt is up waits for the task's next question — it cannot answer one it
was typed before, which on a yes-or-no gate would deny an action you never saw — and a `reply`
takes everything waiting as one message, so what you wrote while the task was working arrives
as one message with its paragraphs. `options` on the ask makes it a menu instead of words.

A question waits until it is answered, unless the task gives it a deadline. `within` (a duration,
`30m`) bounds the wait, and with `options`, `default` names the option silence settles on. When
the deadline passes the task reads `nobody answered within 30m`, and what it settled on if it had
a default, and goes on. The turn a conversation hands over has no deadline: a person may take it
whenever they come back.

A question is inside every grant and outside every vector: a `read-only` task may ask, and
`vector: ask` never asks whether the task may ask.

### A conversation

`task run -attach` is a conversation unless told `-no-converse`: whenever the model goes quiet,
you are handed the turn — the same record, `gate: turn`, with the options `reply` and `end` —
and the run ends when you end it, not when the model stops. At the terminal the turn is a bare
prompt; a line is your next message, and Ctrl-D ends the conversation. From anywhere else it is
`verse question <id> --answer reply --say "…"` or `--answer end`, and a `task say` while the turn
stands is the turn taken: its words are your reply. The model is told all this, so it says its piece and stops rather than
calling `ask` to hand over; `yield` with reason `done` is the model ending it. The last message
is the result, and the finisher, if any, runs once, after the end.

```
{cursor: 2 kind: say task: run-… text: "Hey there! What's on your mind?"}
{cursor: 3 gate: turn id: run-…~t1 kind: pending options: [{id: reply says: "say something"} {id: end says: "end the conversation"}] text: "your turn" …}
  > tell me a joke
  → sent
```

Without `-attach` nobody is there to take a turn, so the run ends when the model stops whatever
the flag says; a charter cannot ask for one, since a conversation with nobody on the other end
would stand for ever. A turn has no deadline: an abandoned conversation stays pending on the
docket, cancellable with `task cancel`, and is orphaned at a restart like any question.

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

Verse publishes a gate that stops for an answer as an [ask](../glossary.md#ask) of its own,
`verse.ask.<id>`, on the docket beside every other ask. The task points at it
(`pending: verse.ask.<id>`).

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
verse data verse ask '*' -if '{status: pending, effects: {verbs: [write]}}'
```

The question record is a record, not a mailbox. Answers go through the resolve API. After an
answer, the entity settles to `status: answered`, with the verdict and who gave it. A settled
question stands for its keep, like any ask, and is then removed ([Asking](../asking.md)).

An answer through a resolver carries `via` beside `by`: `by` is the party it answered for, and
`via` the resolver ([Resolvers](../attached-resolvers.md#answering)).

Every answer names its answerer: `verse question approve <id> --as you@example.com`. Two people
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

**`status: done` means that the task *stopped*, not that the work succeeded.** A hook that
advances state should name evidence (`patch: {stage: reviewed, severity: $data.severity}`),
and not trust a bare claim. See [bindings](action.md#bindings) and
[the answer contract](action.md#the-answer-contract).

## Steering

A [flow](../glossary.md#flow) is a query over live tasks, so steering takes a selection, not
a ref. A task that you run by hand is a flow too. It is the flow of
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
it runs, and every *other* flow keeps reacting. Where a hold lands on a scripted task, how a
hold differs from a suspension, and whose hold a task is under are in
[control](../control.md#a-hold-on-a-run).

```sh
verse flow suspend -subject demo.pr.2 -why "a person is reviewing it by hand"
verse flow show demo.pr.2                      # heldBy: [<the suspension>] on the live task
verse flow resume -task review@demo.pr.2       # refused, 409: a suspension holds it
verse flow resume -subject demo.pr.2
```

Every verb here asks for `control` of each task that it reaches
([who may](../control.md#who-may)). With `-scope`, a verb here, and the runtime's
`/tasks/{id}/pause|resume`, reaches only the tasks that the scope started ([what a scope owns](scopes.md#what-a-scope-owns)). So
`verse flow cancel -scope me -task review@demo.pr.2` never stops baseline's review of that PR
by accident. Drop the flag to address baseline.

## Where a task runs is a seam

A task runs in the verse process by default. A spawn can name another place with `runner:`,
and `verse task` shows where each task runs. Every command on this page works wherever the task
runs. How a runner works is in [runners.md](../runners.md).

## Why it is this way

The reasons for this design are in [reference-tasks.md](../design/reference-tasks.md), in the repository.
