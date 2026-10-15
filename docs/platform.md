# Platform docs

These pages are for somebody who runs a verse, extends it, or changes it. They are read in the
repository, and the site does not publish them. Somebody who uses verse (writes charters, runs
tasks, answers gates) starts at [the docs index](README.md).

## Run a verse

- [Starting a verse](reference/up.md) — `verse up`, its flags, and the config file.
- [Platform commands](platform/commands.md) — `up`, `down`, `runner`, `io` and `resolver`,
  and the smoke test.
- [Runners](runners.md) — where work runs, and what removing a runner does to the work on it.
  The surface is [reference/runners.md](reference/runners.md).
- [Where a task runs, walked](platform/runners-walk.md) — a remote verse, and a runner you
  install, one command each.
- [Kubernetes deploy](../deploy/README.md) — build the image and run verse on a cluster.
- [Permissions on the platform](platform/perms.md) — the `system:` parts, the root and the
  config, the seed, and how installed things get their records.

## Build a runner, a resolver or an io

- [Build a runner and an io](platform/build-runner-and-io.md) — the smallest runner and the
  smallest io, with every step.
- [Seams](seams.md) — the eight places that can be a network, and which side opens each.
- [Task control](task-control.md) — the contract a runner serves.
- [Attached resolvers](attached-resolvers.md) — a service that answers gates. The surface is
  [reference/resolvers.md](reference/resolvers.md).
- [Ios](io.md) — connectors: reflecting git, Slack and Linear in, and acting back out. The
  surface is [reference/io.md](reference/io.md), and the ios verse ships are in
  [reference/iod.md](reference/iod.md).
- [Examples](reference/examples/README.md) — a runner, a coding runner and an io to
  copy and change.

## How verse is built

These pages say why a part is shaped the way it is.

- [Architecture](platform/architecture.md) — the parts of verse, the task seam, the data and
  failure model, the extension points and the deployment.
- [Change events](change-events.md) — why `Op` is `set`/`delete`, and what the entity grain
  costs.
- [The store](reference/store.md) — what the store guarantees.
- [The cycle throttle](cycle-throttle.md) — how a runaway across rules is caught at runtime.
- [External tasks](external-tasks.md) — how the task seam was chosen.
- [Deployment and steering](deployment-and-steering.md) — why control is not a store write.
- [Platform glossary](platform/glossary.md) — the terms of running, extending and changing
  verse.
- [Control on the platform](platform/control.md) — the routes of a hold, and what a
  suspension does to rules.
- [Scopes on the platform](platform/scopes.md) — docd, logd and the engine of a
  scope.
