# verse documentation

Everything here describes what verse does today. Design notes for work that is not built
live in [roadmap/](roadmap/README.md).

Four things stand on their own, and the rest are deep-dives on one subject each.

- [Overview](overview.md) — the architecture, the model, and the load-bearing decisions.
  Read this first.
- [Tutorial](tutorial.md) — five sections you drive: entities, tasks, and the charters
  that relate them. Each has a script that seeds it in one command.
- [Reference](reference/README.md) — the flat command surface, and a file per topic for the
  detail the tutorial skips.
- [Seams](seams.md) — the nine seams that can be a *network*, and which side opens each one.
  The view across the subjects below rather than one of them: what may be remote, what may sit
  behind a firewall, and why the same payload travels in both directions.

## By subject

Each subject below pairs the part of the tutorial that walks it with the notes that explain
it. Walk first if you want to see the thing move; read first if you want to know why it is
shaped that way. Where a subject has unbuilt work, the last row points into the roadmap.

### Entities and the store

Everything coordinates through one observable, cursored entity store. Its change model is
what makes the reactive loop converge.

| | |
|---|---|
| walk | [§2 entities](tutorial.md#2-entities) |
| read | [reference/entities.md](reference/entities.md) — refs, merging writes, conditional writes, selection, watching |
| | [reference/store.md](reference/store.md) — what the store guarantees, and the entity grain the engine reacts at |
| | [Change events](change-events.md) — why `Op` is `set`/`delete`, and what the entity grain costs |
| ahead | [Named persistent channels](roadmap/channel-observe.md) — an idea, not yet a design |

### Charters and triggers

A charter is a list of rules: when this changes and the gates resolve, do this. It is the
core of the model, and almost everything else is something a rule can reach.

| | |
|---|---|
| walk | [§4 charters](tutorial.md#4-charters--relating-entities-to-tasks) · [§5 gates and the docket](tutorial.md#5-gates-and-the-docket) |
| read | [The trigger model](trigger-model.md) — the two grammars, and every field of a rule |
| | [Convergence](convergence.md) — what ends a firing, what is checked at install, and what a read per commit costs |
| | [The cycle throttle](cycle-throttle.md) — how a runaway across rules is caught at runtime, and why a paced cycle is not one |
| | [Strata](strata.md) — a declared level ordering, `derive`, the write that is not the subject, and where a path read inside one may reach |
| | [Provider selection](provider-selection.md) — how a doer is chosen, and why it may only narrow what the spawn granted |
| | [reference/goals.md](reference/goals.md) — a target over the reactive substrate (being redesigned) |
| ahead | [Firing residual](working/firing-residual.md) — the clock a ticker would be, and what `verse entity rm` should mean |
| | [Goals: what is left](working/goals-residual.md) — every use case fell short of the current definition, and three shapes a better one might take |
| | [Dynamic charter](roadmap/dynamic-charter.md) — where goals are headed |

### Tasks and autonomy

A task is a governed unit of agentic work. How it *runs* is a seam, so the same verbs
drive a local agent, a remote one, and a human.

| | |
|---|---|
| walk | [§3 tasks](tutorial.md#3-tasks) · [attached-resolver.sh](../scripts/attached-resolver.sh) — five acts of supervision, one command each |
| read | [reference/tasks.md](reference/tasks.md) — the whole surface: drivers and vectors, gates, the mirror, steering, and executing one by hand |
| | [Task control](task-control.md) — the base-URL contract and the dial-in protocol |
| | [Attached resolvers](attached-resolvers.md) — a service that answers gates: the two doors, the layers, and whose answer counts |
| | [reference/resolvers.md](reference/resolvers.md) — the binding, the two entities, and the routes a resolver lives on |
| | [An automatic supervisor](auto-supervisor.md) — a worked charter where an agent decides, and promotes what is not its to decide |
| | [Runners](runners.md) — where work runs: installing one, naming it on a spawn, and what removing one does to the work on it |
| | [reference/runners.md](reference/runners.md) — the runner surface: the entity, the verbs, the routes, and what a task records |
| | [reference/examples/runner](reference/examples/runner/main.go) — a runner you can copy and change; it compiles with the tree |
| | [reference/examples/source](reference/examples/source/main.go) — a source you can copy and change; it compiles with the tree |
| | [External tasks](external-tasks.md) — how the seam was chosen |
| ahead | [Attached resolvers: what is left](working/attached-resolvers-residual.md) — the drivers, the motion fork, and a `by` nobody verifies |
| | [Runners: what is left](working/runners-residual.md) — how control is applied is convention rather than contract, and whether `local` should be nameable |
| | [Authority](roadmap/authority.md) · [expose](roadmap/expose.md) — where permission comes from, and what a task can see |

### Steering

Observation can lag. Steering cannot — an intervention that queues behind a backlog is an
intervention defeated.

| | |
|---|---|
| walk | [§3 steering](tutorial.md#steering) |
| read | [Deployment and steering](deployment-and-steering.md) — the two planes, and why a hold costs a cursor rather than a buffer |

### Federation: bringing the outside in

External state — your working tree, a git repo, a cluster — is reflected into the slice of
the verse that owns it, through a docd controller.

| | |
|---|---|
| walk | [reference/connect.md](reference/connect.md) — federating a tree, and scopes |
| read | [verse connect and docd](working/connect.md) — the mechanics, through docd's two faces |
| | [Content federation](content-federation.md) — why `connect` replaced `--root` |
| | [Writing a controller](controllers.md) — the `Handler` contract and its invariants |
| | [Sources](sources.md) — reflecting git, Slack and Linear: a source you deploy, hint as hint, reconcile as truth |
| | [reference/sources.md](reference/sources.md) — the source surface: the entity, ownership globs, the verbs, the routes, and the contract a source serves |
| | [reference/sourced.md](reference/sourced.md) — the three verse ships: git (and mirrors), Slack, Linear — flags, credentials, and what each reflects |
| | [reference/examples/source](reference/examples/source/main.go) — a source you can copy and change; it compiles with the tree |
| ahead | [Sources: what is left](working/sources-residual.md) — dial-in for the source verse cannot reach, and what a many-to-many relation does to an ownership claim |
| | [A GitHub source](working/gh-source.md) · [plan](working/gh-source-plan.md) — issues, discussions, PRs and Actions: covering a live set and windowing a settled one, and a second entity no address can spell |
| | [Shared ownership](working/shared-ownership.md) — an entity with more than one writer: a claim that is a path, and sharing as single-writership one level down |
| | [expose](roadmap/expose.md) · [Scope lifecycle](roadmap/scope-lifecycle.md) — a named durable surface (with the write half), and the lifetime of the isolation it lives in |

## What is built, and what isn't

Every doc in this directory describes something that runs, and says so where a corner of it
does not. A gap inside something that runs is recorded where it runs — the note stays here
and names the missing corner.

Design notes for work that is *not* built live in one of two places below, and the line
between them is editorial rather than factual:

- [roadmap/](roadmap/README.md) is a synthesized whole. It is read front to back, so its
  notes agree with each other, cross-reference rather than repeat, and are ordered by what
  unblocks what. Adding to it is an act of synthesis, not filing.
- [working/](working/README.md) is everything else worth keeping in the repo to iterate on:
  ideas, half-designs, an argument that has not finished arriving. No coherence is owed —
  between two working notes, or between a working note and the roadmap.

So there are two independent questions, not one. *Does it run?* decides `docs/` versus the
other two. *Does it cohere with the rest of the plan yet?* decides `working/` versus
`roadmap/`. A note is promoted by being written into the roadmap's argument — often merged
into an existing note — which is why promotion is rarely a `git mv`.

## Elsewhere

- [Kubernetes deploy](../deploy/README.md) — build the image and run verse on a cluster.
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
