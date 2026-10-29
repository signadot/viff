# Where a task runs, walked

This page walks through running a task away from the verse you started, on a runner you
install. It continues [the tasks page of the tutorial](../tutorial/tasks.md).

A task need not run on this verse. Install a [runner](../glossary.md#runner) that offers an agent,
and a spawn for that agent runs there: verse routes each spawn to a runner that offers its doer.
Verse drives it through the same handle, so nothing above changes.

```sh
openssl rand -hex 32 > runner.token                   # how the runner proves itself
go run ./docs/reference/examples/runner -addr :9800 -token-file runner.token   # a runner you can copy and change
verse runner add demo http://localhost:9800 -token-file runner.token          # `reach: a runner` — the install probes it
echo '{agent: {name: sample}, prompt: "fetch the data; retrain; report", environment: {systems: {network: {mode: open}}}}' |
  verse task run -f -
verse ps -runner verse.runners.demo
```

Runners are a subject of their own:

- [Runners](../runners.md) covers how to install one, where verse routes a spawn, and what
  removing one does to the work on it.
- [Task control](../task-control.md) describes the seam.
- [External tasks](../external-tasks.md) explains why the seam is at that point.
- [reference/tasks.md](../reference/tasks.md) has the commands.
