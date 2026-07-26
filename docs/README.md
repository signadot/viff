# verse documentation

Everything here describes what verse does today. Design notes for work that is not built
live in [roadmap/](roadmap/README.md).

Three docs stand on their own, and the rest are deep-dives on one subject each.

- [Overview](overview.md) — the architecture, the model, and the load-bearing decisions.
  Read this first.
- [Tutorial](tutorial.md) — zero to a running, steerable, self-driving flow, in ten sections.
- [Reference](reference.md) — the command surface, the config layers, and what verse reads
  and writes.

## By subject

Each subject below pairs the part of the tutorial that walks it with the notes that explain
it. Walk first if you want to see the thing move; read first if you want to know why it is
shaped that way. Where a subject has unbuilt work, the last row points into the roadmap.

### Charters and triggers

A charter is a list of rules: when this changes and the gates resolve, do this. It is the
core of the model, and almost everything else is something a rule can reach.

| | |
|---|---|
| walk | [§2 install a charter and drive it](tutorial.md#2-install-a-charter-and-drive-it) · [§6 gates the group decides](tutorial.md#6-gates-the-group-decides--whether-and-who) · [§7 goals](tutorial.md#7-goals--give-a-flow-a-destination) |
| read | [The trigger model](trigger-model.md) — the two grammars, and every field of a rule |
| | [Completion hooks](completion-hooks.md) — why `onDone` is a field and not a second trigger |

### Processes and autonomy

A process is a governed unit of agentic work. How it *runs* is a seam, so the same verbs
drive a local agent, a remote one, and a human.

| | |
|---|---|
| walk | [§3 processes](tutorial.md#3-processes) · [§5 human-executed processes](tutorial.md#5-human-executed-processes-dial-in) |
| read | [The controlPolicy](control-policy.md) — the base-URL contract and the dial-in protocol |
| | [External processes](external-processes.md) — how the seam was chosen |
| ahead | [Authority](roadmap/authority.md) · [expose](roadmap/expose.md) — where permission comes from, and what a process can see |

### Steering

Observation can lag. Steering cannot — an intervention that queues behind a backlog is an
intervention defeated.

| | |
|---|---|
| walk | [§4 steer flows](tutorial.md#4-steer-flows) |
| read | [Deployment and steering](deployment-and-steering.md) — the two planes, and why a hold costs a cursor rather than a buffer |

### The store

Everything coordinates through one observable, cursored entity store. Its change model is
what makes the reactive loop converge.

| | |
|---|---|
| walk | [§10 what this is and isn't yet](tutorial.md#10-what-this-is-and-isnt-yet) |
| read | [Change events](change-events.md) — why `Op` is `set`/`delete`, and what the entity grain costs |
| | [Entity values](entity-values.md) — why a payload is a tony node rather than a Go map |
| ahead | [Named persistent channels](roadmap/channel-observe.md) — an idea, not yet a design |

### Federation: bringing the outside in

External state — your working tree, a git repo, a cluster — is reflected into the slice of
the verse that owns it, through a docd controller.

| | |
|---|---|
| walk | [§8 federate your local working tree](tutorial.md#8-federate-your-local-working-tree) · [§9 scopes](tutorial.md#9-scopes) |
| read | [verse connect and docd](connect.md) — the mechanics, through docd's two faces |
| | [Content federation](content-federation.md) — why `connect` replaced `--root` |
| | [Writing a controller](controllers.md) — the `Handler` contract and its invariants |
| | [Sources](sources.md) — reflecting git, GitHub and the rest; webhook as hint, reconcile as truth |
| ahead | [Write-back](roadmap/write-back.md) · [Scope lifecycle](roadmap/scope-lifecycle.md) — the write half, and the lifetime of the isolation it happens in |

## What is built, and what isn't

Every doc in this directory describes something that runs, and says so where a corner of it
does not. Every doc in [roadmap/](roadmap/README.md) describes something that does not exist
yet. That is the only distinction the two directories carry, and it is checkable: if a note's
subject has no code, it belongs in the roadmap.

## Elsewhere

- [Kubernetes deploy](../deploy/README.md) — build the image and run verse on a cluster.
- [tony](https://signadot.github.io/tony-format/) — the document language a charter is
  written in, and [docd](https://signadot.github.io/tony-format/docd/), the store behind a
  durable verse.
- [go-tony](https://pkg.go.dev/github.com/signadot/tony-format/go-tony) on pkg.go.dev —
  including [`ir/kpath`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath)
  (the addressing a process chain uses),
  [`mergeop`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) (the patch
  and match operators), and
  [`system/libctl`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/system/libctl)
  (the controller `Handler`).
