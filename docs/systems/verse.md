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

The ask is the person's, not the task's. Its `from` names them, with the task beside them, and
nothing waits on it. Its `then` and `else` run as that person when it settles. A spawn in either
is held within the task when the ask is raised: an environment no wider than the task's, an
autonomy no looser, the task's runner and the task's credential. A task that needs the answer to
go on uses `question-ask`.

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
