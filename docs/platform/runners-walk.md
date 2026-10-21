# Where a task runs, walked

This page walks through running a task away from the verse you started: on a remote verse,
or on a runner you install. It continues
[the tasks page of the tutorial](../tutorial/tasks.md).

A task need not run on this verse. The same verbs drive a task on a remote verse:
`up -runner <url>`. Verse drives it through the same handle, so nothing above changes.

That `up` flag chooses for the whole verse. A single spawn can also choose for itself. Install
a [runner](../glossary.md#runner) and name it on the spawn, and that work runs there. Every spawn
that names no runner goes to the default.

```sh
go run ./docs/reference/examples/runner -addr :9800   # a runner you can copy and change
verse runner add demo http://localhost:9800           # `reach: a runner` — the install probes it
echo '{runner: verse.runners.demo, agent: {name: sample}, prompt: "fetch the data; retrain; report", environment: {systems: {network: {mode: open}}}}' |
  verse task run -f -
verse ps -runner verse.runners.demo
```

Runners are a subject of their own:

- [Runners](../runners.md) covers how to install one, how to name it on a spawn, and what removing
  one does to the work on it.
- [Task control](../task-control.md) describes the seam.
- [External tasks](../external-tasks.md) explains why the seam is at that point.
- [reference/tasks.md](../reference/tasks.md) has the commands.
