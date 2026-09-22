# verse documentation

Verse is a small runtime for governing SDLC work across systems it does not own, on a dial of
agentic autonomy. You install a *charter* of trigger rules; the runtime reacts to changes in a
shared entity store and does the work with governed agent tasks, and every reaction passes a
gate.

Read the overview first, and control before you let verse act. Walk the tutorial. The
architecture and the reference are for when a subject comes up.

## Welcome

- [Overview](overview.md) — the architecture, the model, and the decisions that hold it up.
- [Control](control.md) — how to stop work without ending it: a run's hold and a durable
  suspension, how long each lasts, what it reaches, whose hold it is, and who may.

## Walks

Things you drive, one step at a time.

- [Tutorial](tutorial.md) — entities, tasks, and the charters that relate them, in sections
  that each seed themselves with one command.
- [Permissions, walked](perms-guide.md) — your own entities, a task and its gates, a charter
  and what it owns, a gate with nobody behind it, a gate with a resolver.
- [An automatic supervisor](auto-supervisor.md) — a worked charter where an agent decides,
  and promotes what is not its to decide.
- [attached-resolver.sh](https://github.com/signadot/verse/blob/main/scripts/attached-resolver.sh)
  — five acts of supervision, one command each.

## Architecture

Why each subject is shaped the way it is. [Seams](seams.md) is the view across all of them:
the nine seams that can be a network, and which side opens each.

**Entities and the store** — everything coordinates through one observable, cursored store.

- [Change events](change-events.md) — why `Op` is `set`/`delete`, and what the entity grain
  costs.
- [Permissions](perms.md) — one owner per node, and grants for everybody else: the model, top
  down. The detail is [reference/perms.md](reference/perms.md).

**Charters and triggers** — a charter is a list of rules: when this changes and the gates
resolve, do this.

- [Convergence](convergence.md) — what ends a firing, and what is checked at install.
- [The cycle throttle](cycle-throttle.md) — how a runaway across rules is caught at runtime.
- [Strata](strata.md) — a declared level ordering, and `derive`.
- [Provider selection](provider-selection.md) — how a doer is chosen, and why it may only
  narrow what the spawn granted.

**Tasks and autonomy** — a task is a governed unit of agentic work, and how it runs is a seam.

- [Task control](task-control.md) — the base-URL contract and the dial-in protocol.
- [Attached resolvers](attached-resolvers.md) — a service that answers gates, and whose answer
  counts.
- [Runners](runners.md) — where work runs, and what removing a runner does to the work on it.
- [External tasks](external-tasks.md) — how the seam was chosen.

**Steering** — observation can lag; steering cannot. The acts themselves are
[control](control.md), above.

- [Deployment and steering](deployment-and-steering.md) — the two planes, and why a hold costs
  a cursor rather than a buffer.

**Federation** — external state reflected into the place in the verse that owns it.

- [Ios](io.md) — connectors: reflecting git, Slack and Linear in, and acting back out.
- [Content federation](content-federation.md) — how `connect` federates a tree, and what a
  task reads through.
- [Writing a controller](controllers.md) — the `Handler` contract and its invariants.

## Reference

- [Commands](reference/README.md) — the flat command surface, and a file per topic.
- [The trigger model](trigger-model.md) — the two grammars a charter is written in, and every
  field of a rule.
- [Starting a verse](reference/up.md) · [entities](reference/entities.md) ·
  [the store](reference/store.md) · [tasks](reference/tasks.md) ·
  [runners](reference/runners.md) · [resolvers](reference/resolvers.md) ·
  [ios](reference/io.md) · [the ios verse ships](reference/iod.md) ·
  [connect and scopes](reference/connect.md) · [representation](reference/representation.md)
- [Examples](reference/examples/README.md) — a runner, a coding runner, and an io to copy
  and change.

## Elsewhere

- [Kubernetes deploy](https://github.com/signadot/verse/blob/main/deploy/README.md) — build the image and run verse on a cluster.
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
