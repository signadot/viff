# The action of a rule

This page describes the `action` field of a [rule](../glossary.md#rule): `patch`, `derive`,
`spawn` and `seq`. It also describes what an action can read, the answer contract,
completion hooks, and the ids and progress facts that a firing writes. The model that the
action belongs to is in [the charter reference](../trigger-model.md).

Most examples on this page are a team's pull requests of `acme/app`, kept as records at
`demo.pr.<n>` with a `stage` and the branch `head`. The examples of `derive` and of a read in a
condition are from comment-drift, the charter that reviews the verse repository's own `main`
([the charter](charter.md)).

## `action`: what fires

The [action](../glossary.md#action) of a rule is data, not code. An action needs no target of
its own. It acts on the [subject](../glossary.md#subject) of the condition.

| Field | What it does |
|---|---|
| `patch` | changes the payload of the subject ([patch](#patch)) |
| `derive` | writes data at an address that is not the subject ([derive](#derive)) |
| `spawn` | starts a task ([spawn](#spawn)) |
| `seq` | runs a list of actions in order ([seq](#seq-sequence-along-the-chain-parallel-across-triggers)) |
| `onDone` | an action that fires when the task ends `done` ([completion hooks](#completion-hooks-one-trigger-chaining-visible)) |
| `onFailed` | an action that fires when the task ends `failed` ([completion hooks](#completion-hooks-one-trigger-chaining-visible)) |
| `onCancelled` | an action that fires when the task ends `cancelled` ([completion hooks](#completion-hooks-one-trigger-chaining-visible)) |
| `onOrphaned` | an action for a task that ends `orphaned`, which no tracked task does ([completion hooks](#completion-hooks-one-trigger-chaining-visible)) |

An action writes to the store, and it can also act on a system outside verse. A `patch` or a
`derive` that writes to an [iod](../glossary.md#iod)'s out place asks the iod to act: to post a
comment, to set a label, to merge. The iod carries the write out to its system, and writes back
what happened ([the out half](io.md#the-out-half)). For work that needs a judgment or a
checkout, the action spawns a task.

**Every rule has an action, and verse refuses at install a rule that would do nothing.** Any one
of `patch`, `derive`, `spawn` or `seq` is enough. A rule with none of them would install, watch
its subtree for the life of the store, and never write anything.

Completion hooks do not count as an action. A hook is what happens when something finishes, and
without a spawn or a seq nothing finishes. For the same reason, verse refuses an `onX` without a
spawn or a seq. A gate does not count either: on a rule with no action, a gate asks a question
whose answer nothing reads.

**An action resolves everything, then writes.** The order is:

1. resolve the `patch` and every `derive`;
2. write the patch;
3. write each derived record;
4. `spawn`.

A reference that does not resolve fails the firing before anything is written. So does a read
of an address where nothing stands. The subject stays where it was, so the rule fires again on the
next commit that touches it. The failure is kept on the rule
([when a rule does nothing](../trigger-model.md#when-a-rule-does-nothing)). The same holds for a
completion hook.

The writes are separate commits. If the store fails between two of them, what landed stays.

### `patch`

`patch` writes to the payload of the subject. It is a state transition.

A [patch](../glossary.md#patch) is a tony patch, not an assignment. A nested write merges into
the subtree it names, and does not replace it. So two rules that write under one key do not
overwrite each other's fields. The
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) operators apply.
`patch: { tmp: !delete null }` removes a key. Setting fields cannot say that. Writing a scalar
(`patch: 42`) replaces the whole payload.

A nested write merges into an object. The subject may hold an array or a scalar under that key.
Then nothing is there to merge into, and verse refuses the firing. It does not replace what
stands there:

```yaml
# subject: {stage: counting, votes: [{by: ana}, {by: bo}]}
patch:
  stage: counted
  votes: {bo: yes}      # refused: the patch merges an object into votes, where an array stands
```

The refusal fails the firing, as any patch does that could not be established.

To replace a container on purpose, write the whole value at that key (`patch: {votes: [...]}`).
That is a value and not a merge, and it says so.

A key that the subject does not carry yet is a different case. A patch builds it. Every derive
that grows structure depends on this.

### `derive`

`derive` writes data at an address that is *not* the subject. A `patch` cannot do this. A
[derive](../glossary.md#derive) is a list of entries. Each entry names an address as a path, and
what goes there: `- {path: "repos.verse.drift.head", value: …}`. Its content may reference its
own subject. It may also reference other data, at or below its own level.

A derive is installable only inside a declared order of levels, and it must land strictly above
the rule that writes it. See [strata](../strata.md).

**Without a name, the engine chooses the address, and the address says where the record came
from.** The address is the place, then the rule, then the segments of the subject. A rule
`proposal-appeared` that selects `git.verse.ref.*` and derives into
`[repos, verse, merge, proposal]` writes

    repos.verse.merge.proposal.proposal-appeared.git.verse.ref."verse/auto/…"

This address has **nine** segments: four for the place, one for the rule, four for the subject.
No two rules can address the same record.

**So a reader of a derivation says how deep one is.** The depth follows from the depth of the
subjects that the producing rule selects. `[repos, verse, merge, proposal, "*"]` has five
segments. It selects the *rule* level, whose payload is the whole subtree of that rule's
derivations, and not any one of them.
[The pattern says how deep its subjects are](condition.md#the-pattern-says-how-deep-its-subjects-are)
says what a wrong depth does.

**Naming one is how a derive stays shallow.** `id:` puts the record at the place plus that one
segment: `repos.verse.merge.proposal."verse/auto/…"`, which has five segments. A reader already
spells that address. A derive usually wants an `id:` when its subjects stand under one place and
the rules that read them name the place literally.

One firing derives one record, unless it says `!each`. `!each` fans out over a list and derives
one record per element. The address of each takes a key read out of the element. The rule is the
same, with the key in the address. With it, the twelve findings of a task become twelve facts,
and not one long paragraph. See
[strata](../strata.md#each-when-one-answer-holds-many-facts).

A single derive may carry a `key:` too:
`- {path: "repos.verse.merge.train", key: $subject.value.sha, value: …}`. Its address is then a
function of (rule, subject, key), and not of (rule, subject). It gives one record per value of
something, where the same subject keeps moving. See
[strata](../strata.md#a-key-on-a-single-derive).

**`id:` is that key without the rule and the subject.** The difference is for the reader. A keyed
record stands at `repos.verse.drift.head.head-moved.git.verse.head.aaaa1111`: the place, the
rule, the subject, the key. Nothing in the grammar builds that address from a subject. So a
reader that wants the record must *describe* it, with a `"*"` per segment and a predicate on a
field it happens to know. It cannot name it.

With `id: $subject.value.sha` the record is `repos.verse.drift.head.aaaa1111`, and a reader
spells it: `!get-path {at: [repos, verse, drift, head, $subject.value.sha], path: result.report}`.

`id:` gives the author the job of keeping addresses unique.

| Field | Two subjects of one rule that compute the same value | What it claims |
|---|---|---|
| `id:` | share one record | the value identifies the record **across subjects**. This is true when the subject is a singleton, as `git.verse.head` is. |
| `key:` | get one record each | the value identifies the record within one subject only |

The *place* stays the engine's in both cases, because a derive may write only inside its own
stratum. Verse checks this at install. Verse refuses a derive that carries both `id:` and `key:`.

`!each` takes both too, and there the difference matters more.

- Keyed, every element lands under (rule, subject, key). A fan-out can only create its **own**
  copies.
- Named, every element lands at the place plus the value. In this way the fan-out of one rule
  reaches records that another rule names. It is the only way to say "mark every one of those
  that is already there".

A derive merges at depth, so the mark lands beside the facts, and not over them.

### An install re-fires, so a derive must own what it writes

Installing a charter evaluates its rules against what is already in the store
([the backlog](condition.md#the-backlog)). So an install
acts on the world, and not only on what happens next. A `derive` is a patch merged at depth, so
it overwrites exactly the fields it names.

So a derive that names a field that *another* rule owns rewrites that field **every time the
charter is installed**. It writes over whatever state the flow had reached. A rule that has not
changed, applied again, sets a flow at `reviewed: baseline` back to `pending` and `gathered`.
Nothing fails: the derive writes its own value again.

So a derive states the facts it owns. The rule that follows it starts from an **absence**:

```tony
# the derive says what it knows, and nothing about the flow's state
derive:
- path: "notes.row"
  value: {sha: $subject.name}
# …and the next rule latches on the absence, which is nobody's to overwrite
condition:
  subject:
    path: "notes.row.*"
    value: !not.has-path reviewed
```

For a charter that does it the other way, the install prints a note that names both rules and
the field.

### `spawn`

`spawn` launches a [task](../glossary.md#task). Verse tags the spawned task with the subject.
The tag makes the task part of the flow of that subject.

A spawn answers four questions, and each is a field:

| Question | Field | What it names |
|---|---|---|
| what is the work | `prompt` or `script` | a prompt, the job in text, for an agent; or a script, which is the work and its own doer. Exactly one. |
| who does it | `agent` | for a prompt, the agent that does it (`agent: {name: verse}`), with its settings; absent is verse's default agent |
| what it works in | `environment` | the variables its shell is given (`vars`), and the systems it may use (`systems`): the whole of what the task may do |
| what each step gets | `autonomy` | one ordered list of rows over the task's steps: the first row that matches a step denies it, asks about it, or allows it, and the last row decides every step no row above matched |

The spawn can also carry a [contract](#the-answer-contract) (`answer`) that the result must
satisfy.

**The work and who does it.** A `prompt` is for an agent. `agent` names one by the name runners
offer it under (`verse` is verse's own agent); without `agent`, it is verse's default agent
(`defaultAgent`, `verse` by default). Verse routes the spawn to a runner that offers it
([runners](../runners.md#where-a-spawn-runs)). A `script` is a shell script; it runs by its `#!` line, or
under bash, and takes no `agent`. A spawn with both, or neither, is refused.

An agent's settings are written beside its name: `agent: {name: codex, model: gpt-5-codex,
effort: high}`. Each is one the runner's offer declares, with a value it takes; one it does not
declare is refused. They take `$vars`, so a charter can write `model: $subject.value.model`.
`agent.annotations` are settings for the runner, such as `verse.portkey/virtual-key`. Verse
writes that one itself from `virtualKey`, the address of the model credential the run spends
([virtual key](../glossary.md#virtual-key)), and refuses it written in `annotations`.

**The environment.** `environment` is what the run works in. `vars` are variables set in the
task's shell, by name: how a script gets data from the subject without splicing it into its
text, `vars: {BRANCH: $subject.value.head}`. A runner refuses a name it owns. `systems` is what
the task may do, one block per [system](../systems/README.md), and it is the whole of it: a step
no block gives is refused before any row is read. A block names the verbs it gives, each with its
shapes; a block that names no verb gives every verb of its system, the network apart.

| System | What its block does |
|---|---|
| `git` | a repository the task works in: `{repo: acme/app, from: main, fetch: {}, push: [{ref: !glob "refs/heads/verse/auto/*", exists: false}, {ref: !glob "refs/heads/verse/auto/*", own: true}]}`. `repo` is `owner/name` at GitHub, or `host/owner/name` on another forge; `from` omitted is the default branch. Both take `$vars`, so a charter can write `from: $subject.value.head`. Its `push` shapes are what may leave it, by `ref`, `delete`, `force`, and `exists` and `own`, which the runner fills from the forge; a charter that ships writes them rather than an empty block, which would give every push. The record of the task carries `git`, so a reader sees what the run worked on without the charter. |
| `git-issue` | a repository's own tracker, of a repository `git` checks out: `{repo: acme/app}` gives all its verbs |
| `github`, `github-issue`, `linear-issue`, `slack` | the forge's pull requests, the trackers' issues, and Slack's channels, each a block of its own: `{repo: acme/app}` for `github` and `github-issue`; Linear's and Slack's blocks name their verbs and shapes, such as `{post: {channel: deploys}}` ([the systems](../systems/README.md)) |
| `verse` | verse itself, acted on as the person the task runs for: reading and writing data, asks, and installing charters (`verse.charter-install`). A charter a task installs launches within the task, less `verse.charter-install` ([the verse system](../systems/verse.md), [the charter](charter.md)) |
| `network` | what the run may reach: `{mode: none}`, `{mode: open}` (the host's network), or `{mode: allow, tcp-client: [{host: api.github.com}, {host: pypi.org, port: 443}]}`. Absent is `none`. The runner builds it once, when the run starts, and refuses a mode its sandbox cannot build: an unconfined runner, such as `verse up`'s, builds only `open` ([the network](../systems/inside.md#the-network)). |
| `files`, `commands`, `agent`, `question` | what a doer does inside its run: `files: {}`, `commands: {run: {command: !regexp "^make "}}` |

**A script is one action, and no step.** Nothing asks before it runs: verse decided to start
it. What it may do beyond its workspace is call the task's tools from its shell with
`verse-call-tool`: `git-push`, `github-create-pr` and `git-fetch` for the repositories its
environment gives, their issue trackers' verbs (`git-issue-create`, `github-issue-comment`,
`linear-issue-close`, …), Slack's (`slack-post`, `slack-read`, …), and `question-ask`. Each call
is an action of the task, and the same step an agent's use of the tool is, so the same rows
decide it. A refusal is an answer the script reads, not the end of its run.

**The autonomy.** A step is what a task is about to do, a verb of a
[system](../systems/README.md): `git.push`, `github.create-pr`, `files.write`, `commands.run`,
`agent.call-sub-agent`, the trackers' and Slack's acts, and a connection under a network of
`allow`, `network.tcp-client` ([the table](tasks.md#the-autonomy)). A step outside the
environment is refused first. `autonomy` is one ordered list of rows over the rest: the first row
that matches a step decides it, and the last row, which names no step, decides every step no row
above matched. With no autonomy, every step is asked about: the last row is the one place a run
is made unattended. A firing that already answered its own `gates` allows the steps it decided,
`{match: {system: git, verb: push}, then: allow}`, and does not ask again for each action. A row
on the network allows or denies, since a connection can't wait for an answer, and the default row
doesn't decide a connection: a host the environment's `tcp-client` shapes give is reached unless a
row on the network denies it.

Each row poses its question, or refuses, at the step it matches, at every boundary of the
task, and not once for the firing:

```tony
autonomy:
- name: release
  match: {system: git, verb: push, ref: refs/heads/main}
  then:
    ask:
      need: 2
      answerers: [group:release]
      within: 4h
      default: deny
      labels: {slack: C0C3KHG9B0W}
- {match: {system: git, verb: push}, then: allow}
- {then: ask}
```

| Field | What it does |
|---|---|
| `name` | what the question names, and what a hook's `onDenied` refers to; absent, the row is named by its place, `autonomy[0]`. Two rows of one name are refused, and so are `turn`, `ask`, `question-ask` and `environment`, the questions that are no row's |
| `match` | the step's `system`, which every row but the default names, its `verb`, left out for any of the system's, and the step's fields: `repo`, `from`, `ref`, `delete` and `force` for `git.push`, and `exists` and `own`, which the runner fills from the forge; `repo`, `base`, `head`, `draft` and `own` for `github.create-pr`; `repo`, `head`, `base` and `own` for `github.update-pr`; `path` for `files.write`; `command` for `commands.run`; `agent` for `agent.call-sub-agent`; `repo` and `issue` for a tracker's acts, `team` for a Linear filing; `channel`, and `thread` or `emoji`, for Slack's; `host` and `port` for `network.tcp-client`. Each [system's page](../systems/README.md) lists its verbs and their fields. A field left out matches anything. Each field is a tony match pattern: a value matches itself, `null` any value, `!glob "refs/heads/*"` a glob whose `*` stops at a `/`, `!regexp "^refs/heads/verse/"` a Go regexp, and `!or [a, b]` either ([the autonomy](tasks.md#the-autonomy)). A field is written as the runner spells it: `repo` as `host/owner/name` in lower case (`github.com/acme/app`), `ref` a full ref (`refs/heads/main`), `base` and `head` a branch's name (`main`), `delete` and `force` `true` or `false`, unquoted, a Linear `team` or `issue` uppercase, a Slack `channel` lowercase with no `#`; a pattern it is never spelled as is refused. A field the step does not have is refused, and so is a `$`: a match is a pattern over what the task does, not bound by the firing. A row with no `match` is the default, and it is last. A row that can match nothing the environment gives is refused, since it would never decide. |
| `then` | `deny`, `ask` or `allow`; or `{ask: {…}}`, an ask with its shape |
| `then.ask.need` | how many approvals the step takes; absent is one |
| `then.ask.answerers` | who is asked, in the grammar a rule's gate uses (`user:…`, `group:…`, `$principal`); absent is the docket |
| `then.ask.within`, `then.ask.default` | how long the question stands, and what silence settles on |
| `then.ask.labels` | what the question says about itself for whoever watches asks: `team: payments`, `slack: C…` |

The runner reads the step, the approvals, the answerers and the deadline's duration, and says
which gate matched. Verse adds the rest when it poses the question. See
[a gate's question](tasks.md#a-rows-question).

What a run may do is its environment, and the runner refuses the rest and asks nobody. The
environment lies within the runner's ceilings: a runner publishes, for each system it holds, the
ceiling its operator wrote, blocks in the environment's form, and an environment beyond one fails
the spawn when the rule is installed, naming the ceiling, as does one its sandbox can't build.
The runner holds the environment to its ceilings again at the launch and at each act
([the verse runner](runnerd.md#what-it-offers)). The autonomy is the dial over the rest.

An unknown step in a row, an agent no runner offers, a setting no runner offering it takes, or an
unknown field inside `spawn:` (`runner:` among them: verse routes a spawn to a runner that offers
its doer, [runners](../runners.md#where-a-spawn-runs)) fails the spawn when the rule is installed.
Verse does not fall back to a default, and it drops nothing.

| Field | What it names |
|---|---|
| `timeout` | how long the run may take, as a duration. Verse cancels it after that, on every runner. |
| `credential` | `{audience: <name>}` asks verse for a token that vouches for the task to a system outside verse: the rule's gate let this work through. It is for a rule's own spawn, of a rule that writes a gate, `mode: auto` included. See [a credential for what a gate let through](gates.md#a-credential-for-what-a-gate-let-through). |
| `virtualKey` | the address of the model credential the run spends, such as `acme.keys.review`. Verse reads it as whoever installed the rule, so a rule cannot name a key its owner may not read. Absent is the runner's default ([virtual key](../glossary.md#virtual-key)). |
| `retain` | how many of the task's events verse keeps for a reader who attaches late. Absent is the default. |

A spawn names no resolver. A question goes to the people a row or a gate names, and each of
them grants the [resolver](../attached-resolvers.md) that answers for them (`verse resolver
grant`). Verse refuses a `resolver:` on a spawn at install.

```tony
spawn:
  agent: {name: verse}
  environment: {systems: {network: {mode: open}}}
  prompt:   "Review $subject for security defects."
  answer:
    severity: !or [low, high]
    note:     !irtype string
  autonomy:
  - name: write
    match: {system: files, verb: write}
    then: ask
  - then: allow
```

### `as: subject` — a task for whoever wrote the record

The task of a rule acts as the installer of the rule. `as: subject` makes it act for the owner of
the record that fired it as well. Here a team lets each person file a request for an agent, as a
record under `requests`, a place the rule's installer owns and grants people to create in:

```tony
- name: run-request
  condition:
    subject:
      path: "requests.*"
      value: {stage: filed}
  action:
    patch: {stage: running}
    spawn:
      agent: {name: verse}
      environment: {systems: {network: {mode: open}}}
      prompt: $subject.value.prompt
      as: subject
```

The task acts as the pair of them, `pair:<owner> via:<the rule's address>`, on the owner's
authority. A consequence belongs to whoever raised it: asked "may I merge this?", an answerer's
yes lets the owner merge what the owner may. Neither the answerer nor the installer needs that
right, and the installer's reach is not lent to the owner.

A rule may say `as: subject` only where its subject lies in a place the installer owns: the
settled part of the subject's path, the last segment left out. Anywhere else, the installer
could select somebody's records and run a task of the installer's design as them, so verse
refuses the charter at install.

The task record is the owner's, and says so as `actsFor:`. Its own gates ask the owner. Where the
owner is the system, the task acts as the rule alone.

`subject` is the only value that `as` takes. Verse refuses any other at install. It also refuses
a charter that writes `actsFor:` itself, because the engine sets it.

### A script — when the charter knows the actions

A spawn can carry the work itself, instead of a prompt for an agent to work it out. A merge is a
few git commands. A model that finds them again at each firing adds nothing but the chance of
doing something else.

```tony
spawn:
  script: |
    git -C app merge --ff-only "mirror/$BRANCH"
    verse-call-tool git-push -branch main
  environment:
    vars: {BRANCH: $subject.value.head}
    systems:
      network: {mode: open}
      git:
      - repo: acme/app
        from: main
        fetch: {}
        push: {ref: refs/heads/main, force: false, delete: false}
  autonomy:
  - {then: allow}
```

The environment gives the push to `main` and no other, so no row has to refuse the rest. The
script runs in the workspace its `git` blocks checked out, with its `vars` in its shell.
Its calls to `verse-call-tool` are the task's actions. Each is a step, as an agent's call is: the
same rows decide it, and the task's record shows it. So a row written for an agent's push holds
this push too. A call
the runner declines is an answer on the call's output, and the script decides what it means:
`verse-call-tool git-push -branch "$BRANCH" -delete || true` is a sweep that one declined ref does
not stop.

**A script's text is not bound.** `$subject` in a prompt is the subject; in a script it is the
shell's, and verse leaves it alone. A value from the firing reaches a script as a variable in
the environment's `vars`, which the shell reads and never parses: spliced into the text, a branch named
`x;curl …` would be a program.

The result of the task is the script's output. The task ends `done` when the script exits 0,
and `failed` with the reason otherwise, so a hook never fires `onDone` for a command that did
not work.

A script does not get what belongs to a model:

- no `answer` contract, because nothing can repair one. A spawn that carries both is refused.
- no sub-agents to call.
- no choice of the next action from what the last result said.

The last one is the cost. A script is right for a few git commands, and wrong for anything that
branches.

### Bindings

A [binding](../glossary.md#binding) is a name that begins with `$`. Every field of **the payload
an action reads** is bound by name (`$stage`, `$path`, …). `$subject` is bound too, to the
resolved subject. Which payload the action reads has exactly two answers. The answer depends on
*why* the action runs.

**A firing of a rule reads the payload of the subject itself.** The change that dispatched the
firing may be at a different address. Its fields would mean nothing on the subject being acted
on. So `review $subject` in the prompt of a spawn names the PR the firing is about, even when the
change that fired was a task's and not the PR's. `$stage` is the stage of that PR, and
not of the task.

**A completion hook reads the payload of the finished task.** So does an element of a sequence
that a hook drives. `$status`, `$result`, `$data` and `$err` are the facts it exists to read. The
engine makes that payload itself, and does not read it from the store. The mirror may not have
written the terminal facts yet. This is why those bindings resolve at all, and why they are
flat. They are facts about a run, and no address holds them.

`$subject` is a record. The bare form is its address.

| Binding | Value |
|---|---|
| `$subject` | the whole ref, `demo.pr.1` |
| `$subject.ref` `.name` | the address, and its last segment: the key that the subject stands at |
| `$subject.value` | the payload of the subject **at the commit that caused the firing**, and not at the moment the action runs. The two differ by however long a gate took to be answered. |
| `$subject.commit` | the revision of that commit |

**A completion hook reads at the same commit that its rule fired at.** A task that finishes wakes
the hook, and no write does. So the hook has no cause of its own. It takes the cause of the
firing, which the record of the task carries as `occurrence`. So the hook survives a restart,
even when the engine that runs the hook never saw the firing.

In this way a rule is one statement about one moment. `$subject.value` in an `onDone` is the
subject as the condition selected it. It is not the subject as the action left it one step
earlier. A charter whose task does not write its own subject gets the reading its author meant.

A task that the charter did not start has no such commit. There `.value` and `.commit` fail by
name. They do not resolve to a silent zero.

**In the strings of a spawn**, its prompt, its agent's name and settings, its `vars` and the
attributes of its systems, a binding is replaced by its text. A field that holds a leaf is its own
text, `3` and not `"3"`; a field that holds an object or a list is tony. `$subject`, `.ref` and
`.name` bind as in the table. `.value` binds as the subject's whole payload, and every path into
it binds as well, `$subject.value.handoff.summary`. A field written with a head comment binds as
it would without one.

`$subject.value` in a spawn is what it is in a patch. In a rule's own firing it is the subject.
**In a completion hook's spawn it is the subject at the commit its rule fired at, as in the
hook's patch, and not the finished task's record.** The task's own facts are flat: `$status`,
`$result`, `$data`, `$err`.

**A reference that reaches past what is bound refuses the launch**, as a patch refuses an absent
reference. That is a binding followed by `.` and a field, or by `[`, that the payload does not
hold: `$subject.value.never`, or `$stage.never` where `stage` holds a string. It is never replaced
by the shorter binding's text with the rest of the reference after it. The refusal is a failed
task: the action's patch has already landed, and its `onFailed` runs with `$err` naming the
reference, so it can write the subject down as failed:

```tony
action:
  patch: {stage: developing}
  spawn:
    agent: {name: verse}
    prompt: "open a pull request whose body is $subject.value.handoff.summary"
  onFailed:
    # "spawn refused: prompt: $subject.value.handoff.summary is not present in the payload that fired this action"
    patch: {stage: failed, why: $err}
```

A bare `$name` that nothing binds is left as written, and the spawn launches. In prose or in a
shell value it is as likely `$HOME` or `$5` as a binding. A binding followed by anything else
ends there: `$stage.` at the end of a sentence is the stage and a full stop.

A completion hook's spawn for a task that the charter did not start has no commit to read at, so
there `$subject.value` refuses the launch by name.

### `!get-path` and `!list-paths`: reaching other data

Everything in [Bindings](#bindings) reads the subject of the firing, or the task that just
finished. A [path read](../glossary.md#path-read) reaches data that is neither of those:

```tony
action:
  patch:
    repo: !get-path
      at: [git, repo, $subject.value.head]
    branch: !get-path
      at: [git, repo, $subject.value.head]
      path: branch
    branches: !list-paths
      at: "git.repo.*"
      path: branch
    # …and a predicate narrows the walk to the ones that look like this (list only)
    green: !list-paths
      at: "git.repo.*"
      value: {state: green}
      path: branch
    # the string form is the kinded path, for an address with nothing to expand in it
    head:     !get-path git.verse.head
    # and `at:` names the address as a path, at whatever depth the tree is
    says: !get-path
      at: "repos.verse.drift.head.aaaa1111"
      path: result.report
    open: !list-paths
      at: "repos.verse.drift.finding.*"
      value: {status: open}
      path: says
```

The address is `at:` in a read, and `path:` in a condition. In a read, `path:` is the field path
inside the payload.

**One grammar, split by cardinality, and the names say which.** `!get-path` answers with one
node. `!list-paths` answers with a list, and it is where the globs are.

The charter shows which one you get. It does not depend on whether a segment is a `*`.
`!get-path` **refuses** a wildcard in its last segment or in its `path`. The refusal names the
tag that answers that question instead.

| Form | What it is |
|---|---|
| a string | the address as a kinded path (`git.verse.head`), with nothing in it to expand. It may go on into the payload (`git.verse.head.sha`). |
| `at` | the address as a path of at least two segments. Every segment above the last is a literal or `*`. The last is a literal, a `$reference`, or (list only) a `*` or an operator. |
| `path` | a kpath into the payload. Wildcards are list-only. Omitted, you get the whole payload. |
| `value` | a predicate on the payload, **list only**: of the addresses the path selects, the ones that look like this |

No segment above the last is a reference or a glob. The level check asks whether a read lands at
or below the stratum of the rule. It asks this of the literal prefix, because that is all that is
known at install.

`!get-path` refuses a `value`, for the same reason it refuses a wildcard. A predicate can name
several addresses. An address where nothing stands does not satisfy one, and one where data
stands might not either.

On the list form, a predicate makes the read *cheaper*. A `*` already walks everything under the
place, and the predicate narrows it.

Neither form is a query that moves with time. Both are answered from one snapshot at the causing
commit. So verse evaluates a predicate against the same world that the rule saw.

The dots of the string form are its segments. A `$reference` has dots of its own, so it cannot
be written inside one. Write an address that expands a reference with `at:`.

**In an action, they copy and do not track.** Verse reads the value once. If it changes
afterwards, nothing runs again. This rule fires again only when its own subject changes, or when
an `on` or `when` that it carries covers the target. Add the `on` if you want to follow the
target.

### A condition may read too, and that one does track

A read in the predicate of `condition.subject` is answered before the predicate is matched. So a
rule can select on how its subject compares to other data. A match alone cannot say that. A
match tests a field against a literal or an operator, and never against a value read from
elsewhere. `!let` binds the read, and the body compares:

```tony
- name: proposal-outrun
  condition:
    subject:
      path: "repos.verse.merge.proposal.*"
      value: !let
        let:
        - tip: !get-path
            at: "git.verse.head"
            path: sha
        in:
          state: open
          base: !not .[tip]
  action:
    patch: { state: stale }
```

This rule selects every open proposal cut from a commit that is not the tip of main. It answers
"which of these can still land", with no walk of ancestry and nothing to poll.

**The read joins what the rule reads.** This is how it differs from a read in an action. Verse
evaluates the rule `proposal-outrun` again when `git.verse.head` moves, over proposals that did
not change.

Verse skips a rule at a commit that touched nothing the rule reads. A condition that compares
against other data reaches a different verdict as soon as that data moves. So the read must
count, or the skip would be wrong.

**The read cannot name `$subject`.** In an action the subject is settled, and the action
addresses other data by its fields. A condition *chooses* the subject, so no one subject is
there to read from yet. Verse refuses it at install, and says so. It does not say "unknown
reference".

**A missing `!get-path` target fails the firing**, as any unresolved reference does. So "nothing
is there" cannot turn into "something is there, and it is nothing". An empty `!list-paths` is an
empty **list**. "Which one" has an answer or does not. "All of them", of nothing, is correctly
nothing.

**Inside a `derive`, the reads must resolve at or below the stratum of the rule.** See
[strata](../strata.md), where that is the whole of what they cost. In a `patch` they have no
such limit.

**The tag is the whole spelling.** A plain `{get-path: …}` key is ordinary data, and stays data.
So a charter written as JSON cannot write a path read, because JSON has no tags.

### The answer contract

`answer` is the shape that the final message of the task must have. The
[answer contract](../glossary.md#answer-contract) is written as a tony **match pattern**: the
`accept` clause of a schema. It uses the vocabulary that `when` uses, and it is stored in the same
way, under the same `!raw` escape:

```tony
spawn:
  agent: {name: verse}
  environment: {systems: {network: {mode: open}}}
  prompt:   "Review $subject."
  answer:
    class:    !or [bug, nit, risk]
    severity: !or [low, high]
    why:      !irtype string
```

Verse checks the contract when the model stops. That is the only moment it can check it. A run
ends when the model stops calling tools, so stopping and reporting are one act.

A failure goes back to the agent as a **diagnosis**, and the agent is asked to fix it. The
diagnosis says:

- the path;
- what was expected there;
- what was found, keeping absent apart from present but wrong;
- every such failure, and not only the first.

The diagnosis is the match's own account of how it walked (`tony.Explaining`). So a failure
inside an operator is located as precisely as the operator knows it: `findings[1].severity`, and
not "findings". Repair is limited to two attempts. A model that cannot produce the shape with the
complaint in front of it will not learn it in more turns.

A run whose answer never satisfies its contract **fails**. This has four consequences.

- `patch: { class: $data.class }` cannot fail at patch time, because a task that could not
  produce `class` did not succeed. The failure lands on `onFailed`, for which every rule already
  declares a branch. It does not land on a hook that nobody watches.
- The check is on the answer, not the layout. Verse unwraps a fenced answer, so the repair
  attempts go to the content.
- The contract is a guarantee about *shape*, not about content. A repair loop pushes a model to
  comply. So the agent is also told to call `yield` with the reason `cannot`, and not to invent a
  value it could not determine. An honest failure is worth more than a well-formed wrong answer.
- A sub-agent (`call_agent`) does not inherit the contract. A child answers a subtask that its
  parent worded, and its result comes back as one more tool result. Only the final message of the
  parent is what a charter reads.

A contract that cannot be parsed fails the firing where it is written, before any call to a
model. A script, with no model to ask again, is refused with a contract. It does not run
the spawn unchecked. The charter downstream was written in the belief that the contract holds,
so a contract dropped without a word is worse than none.

A `patch` resolves references too. There a reference is a **deref by kpath**, not a template
substitution. It names a value, and does not compute one:

```tony
onDone:
  patch:
    stage:    reviewed
    severity: $result            # the task's own answer, as a fact on the subject
    step:     $verse.review.step # any depth, and list indices: $result.findings[0].id
```

The grammar of references is that of [tony](https://signadot.github.io/tony-format/), not of
verse. `$result.findings[0].id` resolves by handing `result.findings[0].id` to the same kpath
navigation that addresses an action inside its rule (`review.seq[2].onFailed`). The familiar
`$name` spelling *is* that kpath at depth one.

A result patched onto the subject is an ordinary fact, and an ordinary condition matches it. The
condition needs no knowledge of tasks. This closes the loop:

```tony
subject:
  path: "demo.pr.*"
  value: { stage: reviewed, severity: high }
```

References in a patch follow four rules.

**Whole value against interpolation.** A value that is **exactly** one reference resolves to the
node of that reference. So a number stays a number, and an object stays an object. A reference
**embedded in text** interpolates and gives a string, because that is how it was written.
Predicates on the payload are exact about type. So a `3` flattened to `"3"` is a value that
`value: {count: 3}` does not match.

```tony
# the number 3
patch: { count: $n }
# the string "saw 3"
patch: { note: "saw $n" }
```

**A composite has no text form.** `"saw $result"`, where `$result` is an object, is an error. It
is not a rendering. With a rendering, the same charter would mean different things, depending on
what a task returned. Reference the object on its own to write the whole value.

**An absent reference fails the firing.** Verse does not drop it. The store records an absent key
as different from a null one. Omitting the reference would turn "the task never said" into "the
task said nothing", and a match reads those as different facts. A field written null resolves to
null. The failure is the same one that a failed write takes: the action stops, and its spawn does
not start.

**There is no escape.** `$$` is not a literal dollar, and `a$$b` references `$b`. Anything that
is not a well-formed reference is left exactly as written: `$5.00`, `100$`, a bare `$`.

The strings of a spawn use a flatter substitution:

- top-level fields only;
- always as text;
- an unresolved `$var` stays in place, and does not fail.

So `$verse.review.step` derefs in a `patch`, and does not in a `spawn.prompt`.

A field that holds a **document** renders as tony in a prompt. A charter that puts a ledger in
front of an agent puts a document there:

```tony
prompt: "Outstanding findings:\n$findings\n\nThe diff:\n…"
```

Leaves do not change. `$stage` and `$count` bind exactly their own bytes (`3`, never `"3"`). A
null field binds empty.

### Completion hooks: one trigger, chaining visible

An `Action` carries one [hook](../glossary.md#hook) for each terminal status:

| Hook | Status |
|---|---|
| `onDone` | `done` |
| `onFailed` | `failed` |
| `onCancelled` | `cancelled` |
| `onOrphaned` | `orphaned` |

Each hook is itself a full `Action` (`patch` + `spawn` + its own hooks). So a chain
(`review → merge → approve`) nests `onDone`s, and reads from top to bottom in one rule.

When the `spawn` of the action reaches a terminal status, the engine fires the matching hook
about the *same subject*, for exactly that task. The engine finds the hook through the registry
of live tasks, keyed by task id. So a hook needs no `flow` match and no separate rule.

```tony
action:
  patch:  { stage: reviewing }
  spawn:
    agent: {name: verse}
    prompt: "review the pull request at $subject"
    environment:
      systems:
        network: {mode: open}
        git: {repo: acme/app, from: $subject.value.head, fetch: {}}
    autonomy:
    - then: allow
  onDone:
    patch: { stage: reviewed }
```

The alternative is a *decoupled* completion rule. That is a separate rule that fires on the
change to `verse.task.instance` and names the PR by pattern. Use it when a completion should fan
out into several reactions, advance a different subject, or be written separately. Use the hook
for a linear chain that one rule owns, with the task step visible in the rule.

The four hooks are named fields. So an unknown status cannot be written: no field exists to write
it in. Verse refuses a misspelled `onDoen` at install, as it refuses any field an action does not
have.

Hooks survive a restart. Resume restores them from the chain of the task, so `done`, `failed`
and `cancelled` still fire.

`onOrphaned` never fires. Orphaning and adopting are the two alternatives in reconcile. A task
that can be attached again is adopted, and never marked orphaned. A task that cannot is
orphaned, and never tracked. So no tracked task reaches that status.

### `seq`: sequence along the chain, parallel across triggers

`seq` spells the depth axis directly:

```tony
- name: review
  condition:
    subject:
      path: "demo.pr.*"
      value: { stage: open }
  gates:
  - name: review
    mode: ask
  action:
    seq:
    - spawn:
        script: 'cd app && make lint'
        environment:
          systems:
            network: {mode: open}
            git: {repo: acme/app, from: $subject.value.head, fetch: {}}
    - spawn:
        agent: {name: verse}
        prompt: "review the pull request at $subject"
        environment:
          systems:
            network: {mode: open}
            git: {repo: acme/app, from: $subject.value.head, fetch: {}}
        autonomy:
        - then: allow
      onFailed:
        patch: { stage: rejected }
    - patch: { stage: reviewed }
    onDone:
      patch: { stage: ready }
```

There is no `par`. Breadth is what independent rules that match the same condition do.

A [seq](../glossary.md#seq) follows these rules.

- `seq` and `spawn` are mutually exclusive on one action. `patch` beside `seq` is allowed: the
  patch applies, then the sequence runs.
- The sequence owns the `done` slot of each element. So verse refuses at install an explicit
  `onDone` on any element, the last element too. The list already says what comes next.
  `onFailed`, `onCancelled` and `onOrphaned` stay on each element as ways out, and firing one ends
  the sequence.
- `onDone` on the action that *carries* the seq means "after the whole sequence". So a sequence is
  a unit with its own completion, and nesting composes.
- An element with no spawn completes at once. So `seq: [{patch: …}, {patch: …}]` is two ordered
  writes, and not an error. `seq` sequences actions, not tasks.
- Gates gate the sequence, not the step. Gates are per firing, and a sequence is one firing, so
  one `ask` approves the whole sequence.
- No chain may contain two `seq` segments. The rule is stated over chains, and not over elements,
  because a sequence inside the `onFailed` of an element nests just as much.

### Chains and ids

Verse identifies a spawned task by `<chain>@<subject>`. The
[chain](../glossary.md#chain-of-an-action) is the
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath) of the action inside
its rule: dotted field names, and brackets for positions in a list. Hooks descend by field name,
and elements of a sequence by index:

| the action | chain | id for `demo.pr.1` |
|---|---|---|
| the trigger's own | `review` | `review@demo.pr.1` |
| its done-hook | `review.onDone` | `review.onDone@demo.pr.1` |
| a nested done-hook | `review.onDone.onDone` | `review.onDone.onDone@demo.pr.1` |
| the third sequence step | `review.seq[2]` | `review.seq.2@demo.pr.1` |
| that step's failure branch | `review.seq[2].onFailed` | `review.seq.2.onFailed@demo.pr.1` |

The chain addresses the action, and does not name it privately. So the chain is deterministic.

| Use | What the deterministic chain gives |
|---|---|
| a re-fire | is idempotent, because a spawn dedups by id |
| a lookup back into the charter | is how resume restores the hooks of a task, and how a sequence finds its next step |
| the task's record | publishes it as `chain`, with its root as `trigger`. So "every task of this rule" is a predicate on the payload, and not a glob over ids. |

The id drops the brackets, and joins with `@`. The dotted form is lossless, because the field set
of `Action` is closed. A segment after `seq` is an element, and a segment that matches a hook
name is a field.

A rule is identified by its scope, the address of its charter, and its name. Names are unique only
within one charter. Every charter in a store shares one task runtime and one docket. So every id
that a firing makes begins with the charter that made it:

- the address of a package, and `:`;
- the name of a scope, and `::`;
- nothing, for the charter of baseline.

The task and its gate questions carry it:

| the charter | task id | gate question id |
|---|---|---|
| baseline's | `review@demo.pr.1` | `verse.trigger.review.ok~…` |
| the package at `repos.verse.drift` | `repos.verse.drift:review@demo.pr.1` | `repos.verse.drift:verse.trigger.review.ok~…` |
| scope `me`'s | `me::review@demo.pr.1` | `me::verse.trigger.review.ok~…` |

For this reason the name of a rule may not contain `:`, as it may not contain `@`.

### Progress facts

A parallel branch is woken by a condition, and a condition matches facts. So a sequence publishes
its position. Without it, the breadth axis could not observe the depth axis at all. The engine
writes the position on the subject, under one reserved `verse` root, as each element completes:

```tony
verse:
  charter:             # what the engine records about rules acting on this record
    review:            # keyed by trigger
      step:  2         # how many elements have completed
      steps: 3         # len(seq), known at install
      shape: "a3f1…"   # hash of the declared shape, known at install
      done:  true      # latched when step reaches steps
      ended: failed    # …or the terminal status an off-ramp stopped it at
  meta:                # what verse records about the record itself
    vanishing: "2026-07-29T13:04:05Z"   # the intent to remove it — see Lifecycle
```

`meta.vanishing` is described in
[Lifecycle](../trigger-model.md#lifecycle-how-data-leaves).

A join reads the position at whatever depth it needs:

```tony
subject:
  path: "demo.pr.*"
  value:
    verse:
      charter:
        review: { done: true }
```

`value` matches structurally, so it matches a payload that carries any amount of other content.

| Field | What it says |
|---|---|
| `done` | the engine latches it when `step` reaches `steps` |
| `ended` | the terminal status other than `done` at which an element stopped the sequence. Its own `onFailed`/`onCancelled`/`onOrphaned` runs, and the sequence stops there. |
| `shape` | a hash of the declared sequence. Verse compares it at each advance. |

A join that asks for `done` does not match a run that gave up. A sequence that concluded either
way does not run again at this shape. Editing the sequence changes the shape, and only that frees
it.

When `shape` does not match, the charter changed under the sequence. The sequence fails with a
reason. It does not run whatever now stands at `seq[k+1]`. The error says that the shape changed
and names the rule. It cannot say what changed.

## Why it is this way

The reasons for this design are in [reference-action.md](../design/reference-action.md), in the repository.
