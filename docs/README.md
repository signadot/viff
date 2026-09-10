# verse documentation

Everything here describes what verse does. Design notes for work that is not built live in
[roadmap/](roadmap/README.md).

Four documents stand on their own, and the rest are deep-dives on one subject each.

- [Overview](overview.md) — the architecture, the model, and the decisions that hold it up.
  Read this first.
- [Tutorial](tutorial.md) — five sections you drive: entities, tasks, and the charters that
  relate them. Each has a script that seeds it in one command.
- [Reference](reference/README.md) — the flat command surface, and a file per topic for the
  detail the tutorial skips.
- [Seams](seams.md) — the nine seams that can be a *network*, and which side opens each one.
  This is the view across the subjects below rather than one of them: what may be remote, what
  may sit behind a firewall, and why the same payload travels in both directions.

## By subject

Each subject pairs the part of the tutorial that walks it with the notes that explain it. Walk
first to see the thing move; read first to know why it is shaped that way. Where a subject has
unbuilt work, the last row points into the roadmap.

### Entities and the store

Everything coordinates through one observable, cursored entity store. Its change model is what
makes the reactive loop converge.

| | |
|---|---|
| walk | [§2 entities](tutorial.md#2-entities) |
| read | [reference/entities.md](reference/entities.md) — refs, merging writes, conditional writes, selection, watching |
| | [reference/store.md](reference/store.md) — what the store guarantees, and the entity grain the engine reacts at |
| | [Change events](change-events.md) — why `Op` is `set`/`delete`, and what the entity grain costs |
| ahead | [The store session](working/store-session.md) — verse's own law turned on verse: a connection to reconcile rather than one to establish, and readiness as a fact about it |
| | [Named persistent channels](roadmap/channel-observe.md) — an idea, not yet a design |

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
| ahead | [Firing residual](working/firing-residual.md) — the clock a ticker would be, and what `verse data --rm` should mean |
| | [Goals: what is left](working/goals-residual.md) — why there are no goals, and three shapes a better definition might take |
| | [Dynamic charter](roadmap/dynamic-charter.md) — a charter authored per change, where the goal idea is headed |

### Tasks and autonomy

A task is a governed unit of agentic work. How it *runs* is a seam, so the same verbs
drive a local agent, a remote one, and a human.

| | |
|---|---|
| walk | [§3 tasks](tutorial.md#3-tasks) · [attached-resolver.sh](https://github.com/signadot/verse/blob/main/scripts/attached-resolver.sh) — five acts of supervision, one command each |
| read | [reference/tasks.md](reference/tasks.md) — the whole surface: drivers and vectors, gates, the mirror, steering, and executing one by hand |
| | [Task control](task-control.md) — the base-URL contract and the dial-in protocol |
| | [Attached resolvers](attached-resolvers.md) — a service that answers gates: the two doors, the layers, and whose answer counts |
| | [reference/resolvers.md](reference/resolvers.md) — the binding, the two entities, and the routes a resolver lives on |
| | [An automatic supervisor](auto-supervisor.md) — a worked charter where an agent decides, and promotes what is not its to decide |
| | [Runners](runners.md) — where work runs: installing one, naming it on a spawn, and what removing one does to the work on it |
| | [reference/runners.md](reference/runners.md) — the runner surface: the entity, the verbs, the routes, and what a task records |
| | [reference/examples/runner](reference/examples/runner/main.go) — a runner you can copy and change; it compiles with the tree |
| | [reference/examples/coderunner](reference/examples/coderunner/main.go) — a runner that codes: a workspace of git checkouts per task, a contained process per task, and a push as a capability the gate releases a credential for rather than one canned act |
| | [reference/examples/source](reference/examples/source/main.go) — a source you can copy and change; it compiles with the tree |
| | [External tasks](external-tasks.md) — how the seam was chosen |
| ahead | [Attached resolvers: what is left](working/attached-resolvers-residual.md) — the drivers, the motion fork, and a `by` nobody verifies |
| | [Runners: what is left](working/runners-residual.md) — how control is applied is convention rather than contract, and whether `local` should be nameable |
| | [A runner descriptor](working/runner-descriptor.md) — `GET /runner`: how control is applied, and isolation as ordered classes verse compares and does not interpret |
| | [comment-drift, and the merge gate](working/comment-drift-drive.md) — what the drive built, what it took, and what it found |
| | [The same drive on example/app](working/signadot/README.md) — a protected master, so the gate becomes a pull request the train keeps current |
| | [A driver that proposes a list of calls](working/calls-driver-plan.md) — taking the LLM out of work with one right answer, without taking away the supervision |
| | [Nothing bounds a run](working/task-budget.md) — a review that concluded and would not stop, an operator's word ignored, and why the fix is not a prompt |
| | [Config: a field that says where it reads itself from](working/config-secrets.md) — one question this repo answers twice, and why `!env` must not reach a charter |
| | [The coding runner: what is left](working/coding-runner-residual.md) — deployed now, so what `principal: runner` actually contains is the live gap, plus the delete no destination can express |
| | [Attribution](working/attribution.md) — an identity as a parameter at install and an argument at launch; why entity writes having no author is the one defect underneath several |
| | [Derive: what is left](working/derive-residual.md) — the fan-out landed; what it deliberately does not do, and why a sweep is not a longer loop |
| | [verse-dev: a coding pipeline](working/verse-dev-coderunner.md) — the drive: the walls predicted before the first run, and what running it found |
| | [An address is as deep as the tree](working/entity-tree.md) — why entity-hood is a lens rather than a stored fact, and the order the change landed in |
| | [Authority](roadmap/authority.md) · [expose](roadmap/expose.md) — where permission comes from, and what a task can see |

### Steering

Observation can lag; steering cannot. An intervention that queues behind a backlog is an
intervention defeated.

| | |
|---|---|
| walk | [§3 steering](tutorial.md#steering) |
| read | [Deployment and steering](deployment-and-steering.md) — the two planes, and why a hold costs a cursor rather than a buffer |

### Federation: bringing the outside in

External state — your working tree, a git repo, a cluster — is reflected into the slice of the
verse that owns it, through a docd controller.

| | |
|---|---|
| walk | [reference/connect.md](reference/connect.md) — federating a tree, and scopes |
| read | [verse connect and docd](working/connect.md) — the mechanics, through docd's two faces |
| | [Content federation](content-federation.md) — how `connect` federates a tree: the index/warehouse split, and what a task reads through |
| | [Writing a controller](controllers.md) — the `Handler` contract and its invariants |
| | [Sources](sources.md) — reflecting git, Slack and Linear: a source you deploy, hint as hint, reconcile as truth |
| | [reference/sources.md](reference/sources.md) — the source surface: the entity, ownership globs, the verbs, the routes, and the contract a source serves |
| | [reference/sourced.md](reference/sourced.md) — the five verse ships: git (and mirrors), git-issue, Slack, Linear, GitHub — flags, credentials, and what each reflects |
| | [reference/examples/source](reference/examples/source/main.go) — a source you can copy and change; it compiles with the tree |
| ahead | [Sources: what is left](working/sources-residual.md) — dial-in for the source verse cannot reach, and the levers a resync has not spent |
| | [GitHub: what is left](working/gh-source-residual.md) — the kinds no repository can own, a rename nothing defends against, and what the budget measured |
| | [Shared ownership](working/shared-ownership.md) — an entity with more than one writer: the claim that is a path (built), and the charter half that is not |
| | [expose](roadmap/expose.md) · [Scope lifecycle](roadmap/scope-lifecycle.md) — a named durable surface (with the write half), and the lifetime of the isolation it lives in |

## What is built, and what is not

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
