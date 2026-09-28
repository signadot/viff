# Platform docs

These pages are for somebody who runs a verse, extends it, or changes it. They are read in the
repository, and the site does not publish them. Somebody who uses verse (writes charters, runs
tasks, answers gates) starts at [the docs index](README.md).

## Run a verse

- [Starting a verse](reference/up.md) — `verse up`, its flags, and the config file.
- [Runners](runners.md) — where work runs, and what removing a runner does to the work on it.
  The surface is [reference/runners.md](reference/runners.md).
- [Kubernetes deploy](../deploy/README.md) — build the image and run verse on a cluster.

## Build a runner, a resolver or an io

- [Build on verse](build.md#write-a-runner) — the smallest runner and the smallest io, with
  every step.
- [Seams](seams.md) — the nine places that can be a network, and which side opens each.
- [Task control](task-control.md) — the contract a runner serves, and the protocol a worker
  dials in with.
- [Attached resolvers](attached-resolvers.md) — a service that answers gates. The surface is
  [reference/resolvers.md](reference/resolvers.md).
- [Ios](io.md) — connectors: reflecting git, Slack and Linear in, and acting back out. The
  surface is [reference/io.md](reference/io.md), and the ios verse ships are in
  [reference/iod.md](reference/iod.md).
- [Examples](reference/examples/README.md) — a runner, a coding runner, a worker and an io to
  copy and change.
- [The coding runner, walked](reference/examples/coderunner/walkthrough.md) — deploying the
  coding runner and running a task on it.
- [attached-resolver.sh](../scripts/attached-resolver.sh) — five acts of supervision, one
  command each.

## How verse is built

These pages say why a part is shaped the way it is.

- [Change events](change-events.md) — why `Op` is `set`/`delete`, and what the entity grain
  costs.
- [The store](reference/store.md) — what the store guarantees.
- [The cycle throttle](cycle-throttle.md) — how a runaway across rules is caught at runtime.
- [External tasks](external-tasks.md) — how the task seam was chosen.
- [Deployment and steering](deployment-and-steering.md) — why control is not a store write.
- [Content federation](content-federation.md) — how `connect` federates a tree, and what a
  task reads through.
- [Writing a controller](controllers.md) — the `Handler` contract and its invariants.
