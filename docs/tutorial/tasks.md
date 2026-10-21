# [Tutorial](README.md) -- Tasks

A [task](../glossary.md#task) is a unit of work that verse governs. An agent or a script does
the work, on a runner. You can watch a task, tell it something while it runs, and stop it. A task
asks a [question](questions.md) before it does what it may not do alone. What it says last is its
**answer**.

This page needs [a verse running](README.md#start). The runs that go to an agent need a model
key, and every other run here is a script.

## Run one

A run is a [spawn](../glossary.md#spawn), written as a charter writes one, in a file or on
stdin. `verse up`'s runner builds only the host's network, and a spawn that names no network has
none, so each spawn here names it:

```sh
echo '{script: "echo summarizing; sleep 3", environment: {systems: {network: {mode: open}}}}' |
  verse task run -f -                               # prints a run-… id
echo '{script: "echo done", environment: {systems: {network: {mode: open}}}}' |
  verse task run -f - --wait                        # …and block for the result
verse task
verse task get <id>                # status, prompt, agent, runner, result, pending gates
verse task say <id> "also mention the dial"        # tell the agent something mid-run
verse task autonomy <id> --file autonomy.tony   # replace its autonomy
verse task cancel <id>
```

A `script` runs as a shell script. With a model key configured, a spawn with a `prompt`, such as
`{prompt: "summarize the README", environment: {systems: {network: {mode: open}}}}`, gives your
sentence to an *agent* instead ([the embedded runner](../reference/up.md#the-embedded-runner)).
`say` gives the agent new instructions. `autonomy` replaces the task's autonomy, which says what
it does unasked, at the task's next boundary.

## The environment and the autonomy

A task stops at each [step](../glossary.md#step-of-a-task): writing a file, running a command,
pushing, calling a sub-agent. Two parts of the spawn decide them.

- Its **environment** says what the task may do at all: each system it names, with the verbs it
  gives. A step outside it is refused, and nobody is asked. `files: {write: {}}` gives file
  writes; `files: {}`, a block naming no verb, gives every verb of `files`.
- Its **autonomy** says, among those, what happens at each: a list of rows, the first that
  matches a step deciding it, `allow`, `ask` or `deny`. With no autonomy every step is asked
  about, so the last row, the default, is the one place a run is made unattended.

A script you wrote is one action and no step: it runs, because you wrote it. An agent chooses its
steps, so it has only the ones its environment gives; given none, it reads, and answers. This
one may write files, and asks first:

```sh
cat > write.tony <<'END'
prompt: 'Write a file hello.txt containing "hi".'
environment:
  systems:
    network: {mode: open}
    files: {write: {}}
autonomy:
- match: {system: files}
  then: ask
- then: deny
END

verse task run -f write.tony
verse question
# - gate: autonomy[0]
#   id: run-f3e31ed0390f~d1
#   step: files.write
#   tool: write_file
verse question approve run-f3e31ed0390f~d1 --as you@example.com
```

`autonomy[0]` is the row that decided. The last row, `then: deny`, is the default: it decides
whatever no row above matched. The network is a system too, and `mode: open` gives the task the
host's network, the only one `verse up` builds.

To have the task ask about one kind of step, write a row for it. This spawn gives the task the
shell, asks before each command, and gives the question ten minutes:

```sh
cat > shell.tony <<'END'
prompt: 'Run `date -u` and say what it printed.'
environment:
  systems:
    network: {mode: open}
    commands: {}
autonomy:
- name: shell
  match: {system: commands, verb: run}
  then: {ask: {within: "10m", default: deny}}
- then: allow
END

verse task run -f shell.tony                                      # run-4e919ce01f99
verse question
# - gate: shell
#   id: run-4e919ce01f99~d1
#   step: commands.run
#   tool: run_bash
#   …
verse question approve run-4e919ce01f99~d1 --as ana@example.com   # the command runs
verse task get run-4e919ce01f99                                   # status: done
```

The question is an ask like the ones on [the questions page](questions.md). It is on the same
docket, and you answer it the same way. If nobody answers in ten minutes, the answer is no: the
command never runs, and the agent hears the refusal and plans again.

`run --attach` streams the transcript and asks you in the terminal. You can also answer the same
gate from anywhere by its id. [The autonomy](../reference/tasks.md#the-autonomy) has the rows
in full.

## The answer

What a task says last is its answer: a script's output, or an agent's final message. `verse task
get` shows it as `result`. An agent answers in words. When something will read the answer as
data, give the run an **answer contract**: the shape its final message must have.

```sh
cat > triage.tony <<'END'
prompt: "Is 'the login page fails' a bug or a feature, and how severe is it?"
environment: {systems: {network: {mode: open}}}
answer:
  kind:     !or [bug, feature]
  severity: !or [low, normal, high]
END

verse task run -f triage.tony
```

Verse tells the agent the contract when it starts, and checks the answer when it stops. An answer
that does not fit goes back to the agent with what was wrong. If the answer never fits, the task
fails. A script cannot be asked again, so verse refuses a contract on a script
([the answer contract](../reference/action.md#the-answer-contract)). The contract matters most
in a [charter](charters.md), where a rule reads the answer's fields.

## A task is data

Verse writes every task into the store as `verse.task.instance.<id>`. So to watch tasks, you
watch entities:

```sh
verse watch verse task instance '*'                   # every task, as it changes
verse data verse task instance '*' -trim '{status}'   # …or just the status of each
```

The end of a task is a change to an entity, so a *rule* can react to it. A flow of several steps
continues this way: through the store.

## Steering

Two acts stop work without ending it. A [hold](../glossary.md#hold) stops one run at its next
boundary. A [suspension](../glossary.md#suspension) stops the work about a charter and a
subject. This page has runs only, so here is the hold:

```sh
echo '{script: "sleep 30; echo woke", environment: {systems: {network: {mode: open}}}}' |
  verse task run -f -                          # run-…
verse flow                                     # the live tasks, and what is suspended
verse flow suspend -task <id>                  # hold that run at its next boundary
verse flow resume  -task <id>
verse flow cancel  -task <id>                  # end that run (destructive)
```

A [flow](../glossary.md#flow) is a query over live tasks, so `verse flow` lists nothing when
nothing runs. A task can also run on a remote verse, or on a runner you install, and the same
verbs steer it ([runners-walk.md](../platform/runners-walk.md)). [Control](../control.md) has the
flags. Next, [charters](charters.md).
