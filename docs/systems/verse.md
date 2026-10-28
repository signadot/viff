# verse

The `verse` system is verse itself, acted on by a task as the person it runs for would act on it.
Each verb is a verb of `verse` and of [`verse mcp`](../reference/mcp.md), and its tool takes what
that MCP tool takes. An agent uses verse the same way whether it runs outside verse, through
`verse mcp`, or inside a task, through this system.

A task's own questions, which it waits on as a task, are the [question system](question.md)'s.

## What a task can do

A task has the verse system's tools when its environment gives `verse` and its runner reaches
verse with a token of its own. Each is a step, decided by the autonomy row that matches it.

```tony
spawn:
  agent: {name: verse}
  runner: verse.runners.gpu
  environment:
    systems:
      network: {mode: open}
      verse: {}
      git:
      - repo: acme/app
        from: main
        fetch: {}
        push: [{ref: refs/heads/verse/ask}, {ref: refs/heads/main}]
  prompt: "Prepare verse/ask, and ask dana whether to land it."
  autonomy:
  - {then: allow}
```

| Tool | Mirrors | What it does | Step |
|---|---|---|---|
| `verse-ask-create` | `verse ask <name> -q`, `verse_ask_create` | raises an ask as the person the task runs for: `question`, `to`, `due`, `on_silence`, `then`, `else`, `about`, `keep`, `label` ([asking](../asking.md)). It does not wait: the result is the ask's id, and the ask stands after the task ends. | `verse.ask-create` |
| `verse-ask-get` | `verse ask <name\|id>`, `verse_ask` | reads one of the person's asks, by `name_or_id`: the name it was asked under or its id. The result is the ask as `verse ask` shows it: its `status` and `answer`, `pending` until it settles (or `why`, where it settled with no answer), what it asked and of whom, its due, and what its `then` and `else` did. | `verse.ask-get` |
| `verse-ask-list` | `verse ask`, `verse_ask` | lists the person's asks, each as `verse ask` lists it: its id, question, who is asked, its due, its `status`, and its `answer`, `pending` until it settles. | `verse.ask-list` |
| `verse-data-get` | `verse data`, `verse_data` | reads the verse at an address as the person: `at`, `if`, `if_at`, `trim`, and with `paths` the addresses beneath it, `depth` levels deep, one kinded path per line. | `verse.data-get` |
| `verse-data-put` | `verse data <address> --put`, `verse_data_put` | writes a `value` at an address `at` as the person, merged into what stands there; with `create`, `update` or `expect` only on that condition, with `grants` for a node it creates. The result is what stands there after the write. | `verse.data-put` |
| `verse-data-rm` | `verse data <address> --rm`, `verse_data_rm` | removes what stands at an address `at` as the person, an entity or one field of one. | `verse.data-rm` |
| `verse-charter-get` | `verse charter`, `verse_charter` | reads the charter at a `path` as the person: an installed charter as its file, with what the file cannot say (a rule held, suspended or in trouble, the charters under the address) as notes after it; a rule or level of one as it stands; with `paths` one kinded path per rule and level, `depth` levels deep. `at`, `if` and `trim` as `verse charter` takes them. | `verse.charter-get` |
| `verse-charter-check` | `verse charter --check`, `verse_charter_check` | asks whether `charters`, one tony document apiece, would install at the address `at`: each charter's verdict, and what the install would add, change and take away. Nothing is installed. | `verse.charter-check` |

The ask is the person's, not the task's. Its `from` names them, with the task beside them, and
nothing waits on it. Its `then` and `else` run as that person when it settles. A spawn in either
is held within the task when the ask is raised: an environment no wider than the task's, an
autonomy no looser, the task's runner and the task's credential. A task that needs the answer to
go on uses `question-ask`. One that looks again later reads the ask back with `verse-ask-get`,
which says whether it was answered.

A task reads and writes with the data tools as the person: what they cannot see, the task cannot
either, and what they may not write is refused in verse's words.

A task reads the charters with `verse-charter-get` and asks whether its own would install with
`verse-charter-check`, both as the person. It installs nothing.

```sh
verse-call-tool verse-ask-create -question "Land verse/ask?" -to user:dana@example.com -due 48h \
  -then '{spawn: {script: "git fetch origin verse/ask && git push origin FETCH_HEAD:refs/heads/main",
    runner: verse.runners.gpu, autonomy: [{then: allow}],
    environment: {systems: {network: {mode: open},
      git: [{repo: acme/app, from: main, fetch: {}, push: [{ref: refs/heads/main}]}]}}}}'
```

The `then` above runs on the task's runner, with an environment that gives no more of `acme/app`
than the task's, checked out from the same `main`, and an autonomy no looser. A `then` naming another runner, or a push the task's
environment does not give, is refused when the ask is raised.

## Where the credential lives

In the runner. The task holds none. Each verb is a request to verse's API, which the runner
carries for the task with its own token, naming the task
([permissions](../reference/perms.md#runners-at-the-door)). Verse makes the request as the task's
owner, only while the task runs. A runner carries nothing but these verbs for a task. A refusal
of the runner itself, its token or the task, is an error to the task, and a refusal of the ask
is a no. A runner that reaches no verse doesn't offer the system, and refuses a launch that
names it.
