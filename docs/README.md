# verse documentation

Verse is a small runtime for governing SDLC work across systems it does not own, on a dial of
agentic autonomy. You install a *charter* of trigger rules; the runtime reacts to changes in a
shared entity store and does the work with governed agent tasks, and every reaction passes a
gate.

The pages are grouped by who reads them. Start with the first group, then go to the group for
your job.

## Start here

Read these in order.

1. [Overview](overview.md) — the model, the parts, and the decisions that hold it up.
2. [Tutorial](tutorial.md) — entities, tasks, and the charters that relate them. Each section
   seeds itself with one command. It runs on your own machine and needs no credentials.
3. [Control](control.md) — how to stop work without ending it. Read it before you let verse
   act on anything that matters.
4. [Build on verse](build.md) — three recipes: write a charter, write a runner, write an io.
   Each is the smallest thing that works, with every step.

[The glossary](glossary.md) defines each term once. Use it whenever a page uses a word you do
not know.

## Run a verse

For an operator: starting verse, driving it, and saying who may do what.

- [Starting a verse](reference/up.md) — `verse up`, its flags, and the config file.
- [Commands](reference/README.md) — every command, and a file per topic.
- [Permissions, walked](perms-guide.md) — your own entities, a task and its gates, a charter
  and what it owns, a gate with nobody behind it, a gate with a resolver.
- [Tasks](reference/tasks.md) — running a task by hand, and working one as its executor.
- [Runners](runners.md) — where work runs, and what removing a runner does to the work on it.
  The surface is [reference/runners.md](reference/runners.md).
- [Connect and scopes](reference/connect.md) — reflecting your working tree into a scope.
- [Kubernetes deploy](https://github.com/signadot/verse/blob/main/deploy/README.md) — build
  the image and run verse on a cluster.

## Write a charter

For a charter author: the rules, what they read, and what they may write.

- [The trigger model](trigger-model.md) — the model of a rule. The fields of a rule are in
  the five pages below.
- [The condition of a rule](reference/condition.md) — `subject`, `on` and `when`, and what a
  match can say.
- [The action of a rule](reference/action.md) — `patch`, `derive`, `spawn`, `calls` and `seq`,
  and what an action reads.
- [The gates of a rule](reference/gates.md) — what a gate asks, who answers it, and how it
  settles.
- [The charter](reference/charter.md) — packages, what a package reads and writes, and how a
  charter is stored.
- [Schedules](reference/schedules.md) — schedules and their ticks, which make time a cause.
- [Entities](reference/entities.md) — addresses, reads, writes and watches.
- [Strata](strata.md) — a declared level ordering, and `derive`.
- [Convergence](convergence.md) — what ends a firing, and what is checked at install.
- [Provider selection](provider-selection.md) — how a doer is chosen for a spawn.
- [Permissions](perms.md) — one owner per node, and grants for everybody else. The detail is
  [reference/perms.md](reference/perms.md).
- [An automatic supervisor](auto-supervisor.md) — a worked charter where an agent decides,
  and promotes what is not its to decide.
- [Representation](reference/representation.md) — tony, JSON and YAML: what verse reads and
  what it writes.

## Build a runner, a resolver or an io

For somebody extending verse with a program of their own.

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
- [attached-resolver.sh](https://github.com/signadot/verse/blob/main/scripts/attached-resolver.sh)
  — five acts of supervision, one command each.

## How verse is built

For somebody changing verse itself. These pages say why a part is shaped the way it is. You do
not need them to run verse, to write a charter, or to build on it.

- [Change events](change-events.md) — why `Op` is `set`/`delete`, and what the entity grain
  costs.
- [The store](reference/store.md) — what the store guarantees.
- [The cycle throttle](cycle-throttle.md) — how a runaway across rules is caught at runtime.
  A charter author needs one section of it, [what a trip does](cycle-throttle.md#what-a-trip-does).
- [External tasks](external-tasks.md) — how the task seam was chosen.
- [Deployment and steering](deployment-and-steering.md) — why control is not a store write.
- [Content federation](content-federation.md) — how `connect` federates a tree, and what a
  task reads through.
- [Writing a controller](controllers.md) — the `Handler` contract and its invariants.

## Elsewhere

- [tony](https://signadot.github.io/tony-format/) — the document language a charter is
  written in, and [docd](https://signadot.github.io/tony-format/docd/), the store behind a
  durable verse.
- [go-tony](https://pkg.go.dev/github.com/signadot/tony-format/go-tony) on pkg.go.dev —
  including [`ir/kpath`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath)
  (the addressing a task chain uses),
  [`mergeop`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) (the patch
  and match operators), and
  [`system/libctl`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/system/libctl)
  (the controller `Handler`).
