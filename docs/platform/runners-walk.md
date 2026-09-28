# Where a task runs, walked

This page walks through running a task away from the verse you started: on a remote verse,
behind a broker, with a person, or on a runner you install. It continues
[§3 of the tutorial](../tutorial.md#3-tasks).

A task need not run on this verse. The same verbs drive three other kinds of task:

| Where the task runs | How you set it up |
|---|---|
| on a remote verse | `up -runner <url>` |
| behind a dial-in broker that a firewalled worker claims | `up -broker` |
| with a **person**, who does it by hand | `verse task do …` |

Verse drives all three through one handle, so nothing above changes.

These `up` flags choose for the whole verse. A single spawn can also choose for itself. Install
a [runner](../glossary.md#runner) and name it on the spawn, and that work runs there. Every spawn
that names no runner goes to the default.

```sh
go run ./docs/reference/examples/runner -addr :9800   # a runner you can copy and change
verse runner add demo http://localhost:9800           # `reach: a runner` — the install probes it
verse task run -runner verse.runners.demo "fetch the data; retrain; report"
verse ps -runner verse.runners.demo
```

Runners are a subject of their own:

- [Runners](../runners.md) covers how to install one, how to name it on a spawn, and what removing
  one does to the work on it.
- [Task control](../task-control.md) describes the seam and the dial-in protocol.
- [External tasks](../external-tasks.md) explains why the seam is at that point.
- [reference/tasks.md](../reference/tasks.md) has the commands.
