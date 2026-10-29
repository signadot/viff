# Tasks

A [task](../glossary.md#task) is a unit of work that verse governs. An agent, a script or a
remote [worker](../glossary.md#worker) does the work.
[The tasks page of the tutorial](../tutorial/tasks.md) runs one task. This page is what you need
to run a task, watch it, steer it and answer its questions.

## Running one

```sh
cat > spawn.tony <<'EOF'
prompt: summarize the open pull requests of acme/app
environment:
  systems:
    network: {mode: open}
    git: [{repo: acme/app, fetch: {}}]
    github: [{repo: acme/app, list-prs: {}, read-pr: {}}]
autonomy:
- {then: allow}
EOF
verse task run -f spawn.tony               # prints a run-… id
verse task run -f spawn.tony --wait        # …and block for the final output
verse task run -f spawn.tony --attach      # …and stream it
echo '{script: "cd app && go test ./...", environment: {systems: {network: {mode: open}, git: [{repo: acme/app, fetch: {}}]}}}' |
  verse task run -f -                      # a script, no model, from stdin
verse task
verse task get <id>        # snapshot: status, prompt, agent, runner, result, pending gates, autonomy
verse task say <id> "also say which ones wait on a review"   # an ask to the task: said to <id>: <ask id>
verse task autonomy <id> '[{match: {system: github, verb: read-pr}, then: ask}, {then: allow}]' --as you@example.com
verse task autonomy <id> --file autonomy.tony
verse task cancel <id>
```

The spawn checks out acme/app, which the task may only fetch, and gives the two reads of its pull
requests. Its environment gives nothing that writes, so its autonomy lets every step go
unasked.

`task run` takes a spawn, written as a charter's `spawn:` is: the work (`prompt` or `script`),
the `agent` a prompt is for, the `environment` the run works in, its `autonomy`, and the rest
([the action of a rule](action.md#action-what-fires)). It means only what it says: a spawn that
names no network has none, and a runner that builds only the host's network, as `verse up`'s
does, refuses it until it writes `network: {mode: open}`. Only how this terminal follows the run
is a flag: `--attach`, `--wait`, `--converse`, `--name` and `--as`.

`say` and `autonomy` do different things:

| Command | What it does |
|---|---|
| `say` | gives the agent new instructions |
| `autonomy` | replaces the [autonomy](../glossary.md#autonomy), what each step gets, with a whole one. The runner checks each row against the task's environment, which is fixed for the run, and applies it at the next [boundary](../glossary.md#boundary), with no restart. A replacement decides among what the environment gives and adds nothing to it: a row that can match nothing the environment gives is refused. |

`autonomy --as` names who made the change. `say` is made as whoever `verse` acts as. Both
commands work wherever the task runs: the local runtime applies them directly, and a remote
worker gets them through its runner.

The `spawn` of a [rule](../glossary.md#rule) runs a task the same way. What a charter may say
in a spawn is in [the action of a rule](action.md#action-what-fires).

### `answer`: what finished means

```tony
prompt: check every link in app/docs/install.md
answer: {broken: !irtype []}
environment:
  systems:
    network: {mode: open}
    git: [{repo: acme/app, fetch: {}}]
```

A spawn's `answer` is a tony match pattern. It is the run's definition of **done**, whether a
charter or `task run` wrote the spawn.

Without a contract, a run is finished when the model stops calling tools. A model that only
describes its next step also stops calling tools, and from outside the two look the same. So a
run can end in the middle of its work, with a page of notes as its `result`.

With a contract, verse:

- checks the final message as soon as the model stops;
- on a mismatch, tells the model what is wrong, so it can answer again;
- fails the run if it never satisfies the contract.

A contract also tells *nothing to do* apart from *something went wrong*, which look the same in
an exit status.

Keep the contract small: let it cover only what a rule will match on. A field such as
`summary: !irtype string` looks harmless, and it is a trap: a model writes
`summary: nothing was wrong`, which is not valid tony, and the run fails for a reason that has
nothing to do with the work. Put the prose outside the governed part instead. Verse reads the
answer from a fenced block in the final message, so the rest of the message can be free text
([the answer contract](action.md#the-answer-contract)).

A contract is for an agent. A script's output is its result as it is, so verse refuses a spawn
that gives a script a contract.

<a id="the-drivers"></a>
<a id="the-doer"></a>
## Who does the work, and with what

A task's work is a `prompt`, for an agent, or a `script`, which is its own doer.

| Spawn | What it does |
|---|---|
| `prompt: …`, `agent: {name: <name>}` | gives the prompt to the agent of that name, on a runner that offers it (`verse` is verse's own; a runner may offer `claude` or `codex`). The agent decides what to do, one turn at a time. Without `agent`, the prompt is for verse's default agent (`defaultAgent`, `verse` by default). |
| `script: "…"` | runs the script once, by its `#!` line or with bash. Its output is the result. It takes no `agent`: data reaches it as the environment's `vars`. |

An agent's settings are written beside its name, `agent: {name: codex, model: gpt-5-codex,
effort: high}`, each one its runner's offer takes, with a value it takes. The task's record says
which agent ran and with what, the runner's defaults filled in.

```tony
spawn:
  script: |
    git -C app merge --ff-only "mirror/$BRANCH"
    verse-call-tool git-push -branch main
  environment:
    vars: {BRANCH: $branch}
    systems:
      network: {mode: open}
      git:
      - repo: acme/app
        from: main
        fetch: {}
        push: {ref: refs/heads/main, force: "false", delete: "false"}
  autonomy:
  - {then: allow}
```

`environment` is what the run works in, and the runner enforces it. `vars` are variables set in
the task's shell: how a script gets data from the subject without putting it into its text.
`systems` is what the task may do, one block per system, and it is the whole of it: a step no
block gives is refused before any row is read ([systems](../systems/README.md#a-block-gives-verbs)).

| System | What its block says |
|---|---|
| `git` | a repository checked out in the task's workspace: `repo` (`owner/name`, or `host/owner/name`), `from` (the branch the checkout starts on; absent is the default branch), and the verbs it gives with their shapes, `fetch: {}` and the `push` shapes ([git](../systems/git.md)). |
| `git-issue` | a repository's own tracker, of a repository `git` checks out. `{repo: acme/app}` gives every one of its verbs ([git-issue](../systems/git-issue.md)). |
| `network` | what the run may reach: `{mode: none}`, `{mode: open}` (the host's network), or `{mode: allow, tcp-client: [{host: …, port: …}]}`. Absent is `none`. It is built once, when the run starts, and the runner refuses a mode its sandbox cannot build ([the network](../systems/inside.md#the-network)). |
| `github` | a GitHub repository's pull requests, of a repository `git` checks out: `create-pr`, `update-pr`, `list-prs` and `read-pr`, where the runner's forge is GitHub ([github](../systems/github.md)). |
| `github-issue` | a GitHub repository's issues, of a repository `git` checks out: `create`, `comment` and `close` ([github-issue](../systems/github-issue.md)). |
| `linear-issue` | Linear's issues: `create` in a `team`, `comment` and `close` on an `issue` ([linear-issue](../systems/linear-issue.md)). |
| `slack` | Slack's channels: `post`, `reply`, `read` and `react`, each in a `channel` ([slack](../systems/slack.md)). |
| `verse` | verse itself, acted on as the person the task runs for: `ask-create`, `ask-get`, `ask-list`, `data-get`, `data-put`, `data-rm`, `charter-get`, `charter-check`, `charter-install` and `charter-remove`, the verbs of `verse mcp` ([verse](../systems/verse.md)). |
| `files`, `commands`, `agent`, `question` | what a doer does inside its run. `files: {}` gives every write, `commands: {run: {command: "make *"}}` the commands it names ([files, commands and agent](../systems/inside.md), [question](../systems/question.md)). |

A script calls the task's tools from its shell with `verse-call-tool`: `git-push`, `git-fetch`,
`github-create-pr`, `question-ask`, and the others its environment gives. Each call is an action
of the task. Verse decides it, and reports it on the event stream, exactly as it does an agent's.
So a row written for an agent's push also holds a push that a script makes. A refused call is
an answer the script reads (a non-zero exit, and the reason on stdout). It does not end the
run.

The runner refuses a spawn with both a prompt and a script, neither, or an agent for a script.
It refuses what it does not offer: an agent it does not have, a setting its agent does not take,
a system it does not hold, an environment beyond its ceilings, and an environment missing a step
its agent can't run without (Codex needs `commands` and `files`).

<a id="the-dial"></a>
<a id="the-policy"></a>
## The autonomy

A task decides before each **step**: a write to a file, a command, a push, a pull request, a
call to a sub-agent, asking a person (`question.ask`), a connection under a network of `allow`.
Reading a file or listing a directory is no step, and nothing decides before it.

A step outside the environment is refused first, saying what the environment gives. Within it, a
spawn's `autonomy` says what happens to each step. It is one ordered list of rows. The first row
that matches a step decides it.

```tony
autonomy:
- {match: {system: git, verb: push, ref: "refs/heads/verse/auto/*", exists: "false"}, then: allow}
- {match: {system: git, verb: push, ref: "refs/heads/verse/auto/*", own: "true"}, then: allow}
- {match: {system: git, verb: push}, then: {ask: {need: 2, answerers: [group:release], within: 4h, default: deny}}}
- {then: ask}
```

| Row | What it says |
|---|---|
| `then: deny` | the step is refused in the row's name, and nobody is asked |
| `then: ask` | the step waits for a person first. `{ask: {…}}` says how: who is asked, how many ballots, and how long the question stands. It starts nothing of its own: the task waits on the answer, and what it does about a yes is the step |
| `then: allow` | the step goes ahead |
| the last row, with no `match` | what every step no row above matched gets: the task's dial |

**No autonomy asks on every step.** A spawn that writes none is decided by the default row
alone, `{then: ask}`, so each step the environment gives waits for a person. The last row is the
one place a run is made unattended: `{then: allow}` lets every step the environment gives go
ahead unasked, and the rows above it are the exceptions.

**The environment is what the task may do; the autonomy decides among it.** A row never gives
a step the environment does not: "never `main`" is a `git` block whose push shapes leave `main`
out, and "no shell" is an environment with no `commands`. A row that can match nothing the
environment gives is refused where it is written, since it would never decide.

**A connection is decided too.** Under `network: {mode: allow}`, the task's proxy decides each
connection as the step `network.tcp-client`: outside the environment's `tcp-client` shapes it is
refused, and within them a row on the network allows or denies it. A connection can't wait for an
answer, so such a row never asks, and the default row doesn't decide a connection: a host the
shapes give is reached unless a row on the network denies it. What the proxy refused is on the
task's record. Under `open` or `none` there is nothing to decide
([the network](../systems/inside.md#the-network)).

**A script is one action, and not a step.** It runs whatever the autonomy says. What is decided
is each call it makes.

**An agent asks before a step it chose.** Its reads run freely. Under rows that `ask` on
`files`, `commands`, `git` and `agent`, each write, command, push and call to a sub-agent waits
until somebody approves it. Approving a command the agent made up is a real decision.

**A row on `files` or `commands` is advisory.** It decides the act as the doer names it. A
write through the shell is a `commands.run` step, not `files.write`, and a `command` pattern
matches the whole string, so `git push*` misses `cd x && git push`. A push is the runner's own
act, and the network is built into the run
([files, commands and agent](../systems/inside.md#a-row-on-files-or-commands-is-advisory)).

An `allow` row is how a charter says "the firing already decided this":
`{match: {system: git, verb: push}, then: allow}`. The rule's own `gates:` run before the
action, once for each firing. The allow stops the same decision from being made twice. A runner
that ignored a spawn's `autonomy:` would silently disobey a charter that asks to be asked.

```sh
verse question                              # task questions, and engine gates that ASK
# - gate: autonomy[1]
#   id: run-f3e31ed0390f~d1
#   from: {task: run-f3e31ed0390f}
#   reason: "files.write path=docs/x.md"
#   step: files.write
#   fields: {path: docs/x.md}
#   tool: write_file
verse question approve run-f3e31ed0390f~d1 --as you@example.com  # or: task approve run-… run-…~d1
```

A question names the row that asked it: the row's `name`, or its place in the list, such as
`autonomy[1]`. Its clock, its default and its `need` are the row's, whatever the runner sends.
`turn`, `ask` and `question-ask` name the questions that are no row's, a conversation's turn and
a task's own. `environment` names the refusal of a step outside the environment, which no row
decides. So no row takes one of those four names.
The `~` between the task and the gate needs no quotes, in a shell or in a URL.

**Verse checks who may answer before it delivers the answer** ([tasks and
gates](../perms.md#tasks-and-gates)). The worker that works the task may not answer the question
it raised. A person may answer through a [resolver](../attached-resolvers.md) they granted, and
the answer is still theirs. A charter gate is listed here too, with its `options`, `ballots` and
`deadline` ([the tally](gates.md#the-tally)).

With an identity, `run --attach` and `attach` ask inline instead
(`⏸ gate "autonomy[1]" (write_file) — approve? [y/n]`). Answer `y` to allow and `n` to deny. Any
other answer is asked again; it is not read as a no. A denied step comes back to the agent as an
error, and the agent makes a new plan. Without an identity, `attach` only streams, and prints
the `verse question` command that answers. It is the same question either way: it stays in
`verse question`, and anybody who may answer it can, by id, from anywhere.

### A row's question

An `ask` row poses the same record that a charter gate poses. It says how many ballots settle
it, what silence settles on, and how long it stands. It is a row of a spawn's `autonomy:`, a
charter's or one given to `task run`:

```tony
- name: shell
  match: {system: commands, verb: run}
  then: {ask: {need: 2,                            # proceed takes two ballots; one deny refuses
               within: "4h", default: deny}}      # and the clock decides silence
- name: main
  match: {system: git, verb: push, ref: refs/heads/main}
  then: {ask: {answerers: [group:release]}}
- name: sub-agents
  match: {system: agent, verb: call-sub-agent}
  then: ask
- {match: {system: git, verb: push, ref: "refs/heads/release/*"}, then: deny}
- {then: allow}
```

`match` names a step by its system and, if it wants to, by its verb and the step's params. A
verb or a param the row leaves out matches anything: `{system: git}` is every git call. `*`
matches within a segment and `**` across. A command matches as a whole string, with `*`
matching anything. The last row names no step, and is what a call of a named verb gets when no
row above matched it. Verse refuses an autonomy that ends with a named step, or that puts a row
with no match before the end.

Each step is a verb of a system, written `<system>.<verb>`. The tool a doer calls for it is
`<system>-<verb>`: `git-push` is the call, and `git.push` is the step a row matches. The step's
params are the verb's:

| Step | Params |
|---|---|
| `git.push` | `repo`, `from`, `ref`, `delete`, `force`; `exists` and `own`, filled by the runner |
| `github.create-pr` | `repo`, `base`, `head`; `own`, filled by the runner |
| `github.update-pr` | `repo`, `head`, `base`; `own`, filled by the runner |
| `github.list-prs` | `repo` |
| `github.read-pr` | `repo`, `pr` |
| `files.write` | `path` |
| `commands.run` | `command` |
| `agent.call-sub-agent` | `agent` |
| `network.tcp-client` | `host`, `port` (under a network of `allow`) |

The issue trackers' verbs (`git-issue`, `github-issue`, `linear-issue`), Slack's and verse's
are on [their pages](../systems/README.md), one page for each system. Verse keeps each system's verbs
and params as a document at `verse.systems.<name>` (`verse d verse systems git` reads one).
A runner's tools are checked against these documents when it starts.

An ask's `need`, `within`, `default`, `answerers` and `labels` mean what `need`, `deadline`,
`answerers` and `labels` mean on a [charter gate](gates.md#gates-whether-it-proceeds). Verse
refuses them the same way when they cannot be posed as written: at the spawn, or at
`task autonomy`, and never in the middle of a run. A row's question is answered `proceed` or
`deny`, so `default` is one of the two. It also refuses a param the step does not have,
a pattern its field is never spelled as, and a system or a verb that no document describes. A
`repo` is `host/owner/name` in lower case (`github.com/acme/app`), a
`ref` is a full ref (`refs/heads/main`, never `main`), a pull request's `base` and `head` are
branch names (`main`), `delete`, `force`, `exists` and `own` are `true` or `false`, a Linear `team` or `issue` is
uppercase (`ENG`, `ENG-42`), and a Slack `channel` is lowercase with no `#` (`deploys`). A
param written `null` is refused rather than dropped: to match any value, leave it out.

**Whom a row asks.** A row that names nobody asks the docket. `answerers: [$principal]` asks
whom the task acts for, which for a run you start is you. Verse refuses `$principal` for a task
that acts for nobody: one run as the system, or a rule's task without `as: subject`. A task that
acts for somebody else asks them with every question that names nobody: an ask's `then` asks its
asker, and a spawn `as: subject` asks the subject's owner. The task's record shows it, as
`answerers: [$principal]` beside `actsFor:`. A row's answerers are checked as a gate's are
([gates](gates.md#gates-whether-it-proceeds)): a group it names has to exist, as
`group:release` does above only where somebody made it, and `need` can be no more than the
people it names.

**A run that asks before its one command.** An agent told to run the tests runs a command, which
is `commands.run`. So a row over it stops the command before it runs:

```sh
cat > tests.tony <<'EOF'
prompt: run the tests of app
environment:
  systems:
    network: {mode: open}
    git: [{repo: acme/app, fetch: {}}]
    commands: {}
autonomy:
- name: shell
  match: {system: commands, verb: run}
  then: {ask: {within: "10m", default: deny}}
- then: allow
EOF

verse task run -f tests.tony                                      # run-4e919ce01f99
verse question
# - gate: shell
#   id: run-4e919ce01f99~d1
#   reason: "commands.run command=go test ./..."
#   step: commands.run
#   fields: {command: "go test ./..."}
#   tool: run_bash
#   deadline:
#     default: deny
#     within: 10m
verse question approve run-4e919ce01f99~d1 --as ana@example.com   # the command runs
verse task get run-4e919ce01f99                                   # status: done
```

The environment gives a checkout of acme/app that the task may only fetch, and `commands`. So
the run has the repository and a shell, and nothing else: no `write_file`, no sub-agent, no push.
The last row is the dial. It lets a fetch go, and decides no command, since the `shell` row
matches every command.

If nobody answers, the deadline settles the question on `deny` after ten minutes. The command
never runs. The question's record says `by: deadline` and `verdict: deny`. The refusal comes back
to the agent, which makes a new plan, as it does after a person's no. So a run that nobody is
watching does not wait for ever ([a deadline settles](gates.md#a-deadline-settles)).

The runner reads the deadline's duration and refuses the step when it passes. What silence
settles on, and the `labels`, are verse's: verse adds them when it records the question.

A resolver's own `deadline` is a different clock. It passes the question up to the next layer,
and does not settle it. Both times are on the record, and the earlier one fires first.

### A question for the person

A task can ask the person it runs for. The `question-ask` tool poses a question and waits until
it is answered, and the answer is the tool's result. It is the step `question.ask`, the task's
where its environment gives `question` (`question: {}`), and a row such as
`{match: {system: question}, then: allow}` lets it put the question unasked. An agent has it
as a tool, and a script calls it with `verse-call-tool question-ask`. The question is an ordinary ask: on the docket, in
`verse question`, in Slack, answerable from any of them, and withdrawn if the task is cancelled.

It waits on whom the task acts for: whoever started it, or the asker it runs for. A task that
acts for nobody, such as a rule's task without `as: subject`, asks the docket. A call that names
`answerers` (grantees, as a gate names them: `user:*`, `group:owners`) asks them instead. This is
how a system charter's script asks anybody at all. Its one option, `reply`, takes words:

```sh
verse question run-4708ef20b92d~2 --answer reply --say "the second one" --as you@example.com
```

Attached, you see the question as a prompt:

```
{cursor: 4 kind: pending id: run-4708ef20b92d~2 tool: question-ask text: "which one?" …}
  ❓ which one?
  > the second one
  → sent
```

A line you type while no prompt is up waits for the task's next question. It never answers a
question you had not seen yet, which on a yes-or-no gate would deny an action you never saw. A
`reply` takes everything waiting as one message, with its paragraphs. `options` on the ask makes
it a menu instead of words.

A question waits until it is answered, unless the task gives it a deadline. `within` (a
duration, such as `30m`) limits the wait, and with `options`, `default` names the option that
silence settles on. When the time passes, the task reads `nobody answered within 30m`, and the
default if there was one, and goes on.

```sh
verse-call-tool question-ask -question "ship it?" -options yes -options no -within 30m
```

An autonomy row on `question` decides the asking as any autonomy row decides its step: an
`allow` puts the question at once, and an `ask` would ask before the question is put.

### An ask for the person

A task that asks as the person it runs for, and goes on, uses the
[verse system](../systems/verse.md)'s `verse-ask-create`, as they would use `verse ask`. The
ask's `from` is `{principal: <them>, task: <this task>}`, and it stands after the task ends. Its
`then` and `else` run as them when it settles, and the task reads it back with `verse-ask-get`
to see whether it was answered. A spawn in its `then` or `else` is held within the task:
an environment no wider than the task's, less `verse.charter-install`, an autonomy no looser, the task's runner (`runner:`
names it) and the task's credential where it spends one. An answer in its `then` or `else` is
refused, at any depth: it would be theirs, on any ask that waits on them. Once raised, the ask is theirs, and they change it as any
of their own. The task's record carries its launch, `environment` and `virtualKey` (the address
its spawn named, never the key), so a restart that resumes the task holds a `then` within it as
before.

A task may also install charters as that person, with `verse-charter-install`. A spawn among
their rules is held within the task in the same way, on any runner, and never gives
`verse.charter-install`: what a task installs cannot install charters
([the verse system](../systems/verse.md)).

### A conversation

`task run --attach` is a conversation, unless you pass `--no-converse`. Whenever the model goes
quiet, you get the turn: the same kind of record, `gate: turn`, with the options `reply` and
`end`. The run ends when you end it, not when the model stops.

- At the terminal, the turn is a bare prompt. A line is your next message, and Ctrl-D ends the
  conversation.
- From anywhere else, answer with `verse question <id> --answer reply --say "…"` or
  `--answer end`. A `task say` while the turn waits is your reply.

Every agent holds a conversation: verse's own, Claude and Codex. The verse agent is told it is
in one, so it says its piece and stops, rather than calling `question-ask`, and `yield` with
reason `done` is the model ending the conversation. Claude and Codex go quiet at the end of each
turn, and your reply is their next. The last message is the result.
If the run has a finisher, it runs once, after the end.

```
{cursor: 2 kind: say task: run-… text: "Hey there! What's on your mind?"}
{cursor: 3 gate: turn id: run-…~t1 kind: pending options: [{id: reply says: "say something"} {id: end says: "end the conversation"}] text: "your turn" …}
  > tell me a joke
  → sent
```

Without `--attach`, nobody is there to take a turn, so the run ends when the model stops. A
charter cannot ask for a conversation, because nobody would be on the other end. A turn has no
deadline: an abandoned conversation stays on the docket until `task cancel`, and a restart
orphans it like any question.

<a id="who-you-addressed"></a>
### Which one you meant: an agent or a command

When you ask an *agent* to do something, you expect it to ask before it writes. When you give a
*command*, you expect it to run. A spawn says which: `prompt: "go test ./..."` goes to an agent,
and with no `commands` in its environment the agent has no shell to run it in. So you write it:

| You want | You write |
|---|---|
| to run it as a command | `script: "go test ./..."` |
| to keep the agent and not be asked | `commands: {}` in the environment, and a row allowing what it may do, `{match: {system: commands}, then: allow}` |
| to change how it is decided after the task starts | `task autonomy <id>` with the rows, each reaching what the task's environment gives |

A rule meets the same problem from the other side. Its gates approve *the firing*, not the
actions that the agent then chooses. The rule says it already decided with `allow` rows on the
spawn, and asks with rows about the steps it did not decide. Verse takes the charter's word for
that.

## The mirror

The [mirror](../glossary.md#mirror-of-tasks) writes every task into the store as
`verse.task.instance.<id>`. So you watch tasks as you watch any data. A task that completes is
data that changes, and a rule reacts to it as it reacts to any data.

```sh
verse watch verse task instance '*'
verse data verse task instance <id>
```

```tony
payload:
  autonomy: [{match: {system: github, verb: read-pr}, then: ask}, {then: allow}]
  autonomyBy: user:you@example.com
  agent: {name: verse, model: claude-sonnet-5}
  lastAction: github-list-prs
  lastOutput: summarizing
  runner: local
  status: done
```

`agent` is who did the work and with what: the agent's name and its settings, as the spawn asked
until the runner reports and as the runner resolved them once the task runs, the runner's
defaults filled in. A script's record says `script: true` instead. `git` is the repositories the
spawn gave the task, in the spawn's words, and `refused` the connections its proxy refused.

`observed: false` appears while the mirror cannot reach the runner that holds the task. The
other fields are then the last thing the mirror heard, not the current state.

The record outlives the task. After the task ends, it keeps the result, the last `say`, the
last tool output and the final `status`. `verse flow show` reads them for history. The mirror is
an index, not a transcript: it keeps what an observer needs.

### One id, many runs

A task id names the *work*, not one run of it. Verse derives the id from the spawning action and
the subject. So a rule that fires twice about one subject spawns the same id twice, and the
second firing changes nothing that the first did not. `verse flow show demo.pr.1` addresses the
work by this id.

`verse.task.instance.<id>` is the latest [run](../glossary.md#run). Each run also has a record
of its own:

```sh
verse data verse task run '<id>@<occurrence>'
verse data verse task run '*' -if '{flow: "demo.pr.1"}'
```

The `occurrence` is the store revision the run is about. For a firing, it is the commit that
caused the firing. For a task you started by hand, it is the revision at which verse created the
record. Both records carry it.

Read the instance to see what the work is doing now. Read the runs to see what happened the
other times: a failed run and the successful one after it leave two run records, and the
instance holds only the newest.

Verse writes a run record at the start of the run and again at its end, so a run that never
finishes still leaves a record. **Nothing removes run records.** They are small. To remove them,
use `verse data -rm`, or a charter that marks the old ones [vanishing](../change-events.md).

## The question record

A gate that stops for an answer is an [ask](../glossary.md#ask), at `verse.ask.<id>`, on the
docket beside every other ask. The task points at it (`pending: verse.ask.<id>`).

```tony
payload:
  id:      review@demo.pr.1~d3
  from:    {task: review@demo.pr.1}
  gate:    write-guard
  status:  pending
  subject: demo.pr.1
  tool:    write_file
  step:    files.write
  fields:  {path: cfg/app.tony}
```

`step` and `fields` are what the action *would do*. The runner derives them before it asks. So a
rule can select the questions it cares about without reading prose:

```sh
verse data verse ask '*' -if '{status: pending, step: files.write}'
```

The question record is a record, not a mailbox. Answers go through `verse question`. After an
answer, the record says `status: answered`, with the verdict and who gave it. A settled question
stays for its keep, like any ask, and is then removed ([asking](../asking.md)). An answer
through a resolver carries `via` beside `by`: `by` is the party it answered for, and `via` the
resolver ([resolvers](../attached-resolvers.md#answering)).

Every answer names who gave it: `verse question approve <id> --as you@example.com`. Two people
may answer at once:

| Case | What verse answers |
|---|---|
| two answers at once | exactly one applies. Verse records the other under `stale` and tells its sender (`409`, naming who won). |
| an answer to an id that nobody asked | `404` |

A `404` usually means a typo. A `409` means somebody else answered first.

At a restart, a question that a task raised survives exactly as its task does. A question that a
firing raised is settled `orphaned`, because what waited on its answer was inside the verse
process ([task control](../task-control.md)). If its rule still selects the subject, the
reinstalled charter poses it again under the same id, with the ballots already cast
([a question outlives the verse that asked it](gates.md#a-question-outlives-the-verse-that-asked-it)).
If not, it stays orphaned.

**`status: done` means that the task *stopped*, not that the work succeeded.** A hook that moves
state on should name evidence (`patch: {stage: reviewed, severity: $data.severity}`), not trust
a bare claim ([bindings](action.md#bindings), [the answer contract](action.md#the-answer-contract)).

## Steering

A [flow](../glossary.md#flow) is a query over live tasks, so steering takes a selection, not one
task. A task you run by hand is a flow too: the flow of its spawn's `subject`, or of its own id when
it names none. The verbs, their flags, and how a hold differs from a suspension are in
[control](../control.md). This section says what they do to a task.

```sh
verse flow                                     # live tasks and suspensions
verse flow show demo.pr.2                      # the tree: what ran, what is running, what comes next
verse flow suspend -task review@demo.pr.2      # hold that run at its next boundary
verse flow resume  -task review@demo.pr.2
verse flow say     -subject demo.pr.2 "hurry" # to every live task about it
verse flow cancel  -task review@demo.pr.2      # end that run (destructive)
```

`say` and `cancel` answer with how many tasks they reached, the selection, and the verb.
`suspend` and `resume` answer with the row they held or lifted.

```tony
affected: 1
subject: demo.pr.2
verb: say
```

The verbs land at each task's next boundary. So you can hold, redirect or stop a run while it
runs, and every *other* flow keeps going ([a hold on a run](../control.md#a-hold-on-a-run)).

```sh
verse flow suspend -subject demo.pr.2 -why "a person is reviewing it by hand"
verse flow show demo.pr.2                      # heldBy: [<the suspension>] on the live task
verse flow resume -task review@demo.pr.2       # refused, 409: a suspension holds it
verse flow resume -subject demo.pr.2
```

Every verb here needs `control` of each task it reaches ([who may](../control.md#who-may)).
With `-scope`, a verb reaches only the tasks that the scope started
([what a scope owns](scopes.md#what-a-scope-owns)). So
`verse flow cancel -scope me -task review@demo.pr.2` never stops baseline's review of that pull
request by accident. Drop the flag to address baseline.

## Where a task runs is a seam

A task runs in the verse process by default. A spawn can name another place with `runner:`, and
`verse task` shows where each task runs. Every command on this page works wherever the task
runs. How a runner works is in [runners.md](../runners.md).

## Why it is this way

The reasons for this design are in [reference-tasks.md](../design/reference-tasks.md), in the
repository.
