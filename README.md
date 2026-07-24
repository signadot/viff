# verse

A small runtime for **emergent SDLC flows on a dial of agentic autonomy**. You
install a *charter* of event-condition-action rules; the runtime reacts to changes
in a shared, observable **entity-verse** and does the work with governed **agents**.
Every reaction passes a **gate** — *auto*, *ask* (a human or agent approves), or
*park* — so you set how much runs on its own, action by action, and the agents doing
the work stay observable and steerable. Nobody authors the flow — it emerges from
triggers reacting to state that other actions write; you decide how much autonomy it
runs with.

```
entity change ──▶ trigger matches ──▶ gate resolves ──▶ action fires
     ▲                                                       │
     └───────────── actions write entities ◀────────────────┘
```

## Quick start

```sh
go build -o verse ./cmd/verse     # or: make bin
export VERSE_ADDR=localhost:9797
./verse up --demo                 # engine + process runtime + store, in one process
```

`up` is **docd-backed by default** (durable; it brings up logd+docd as children); add
`--store memory` for a zero-dependency, no-persistence run. In another terminal, drive the
seeded demo (`verse entity put …`, `verse gate list`, `verse gate resolve …`). The
**[tutorial](docs/tutorial.md)** walks it end to end — charters, gates, processes, steering,
human-executed (dial-in) processes, and federating your local working tree.

## What's here

- **`entity/`** — the entity-verse: a `<system, kind, id>` store, observable and
  revision-cursored. Durable and scope-aware (copy-on-write) via **logd/docd** by default;
  an in-memory store (`--store memory`) drops in behind the same interface.
- **`trigger/`** — the ECA engine: conjunctive matches, conjunctive gates (posed
  concurrently), actions-as-data, level-triggered catch-up via a cursor, per-subtree watches
  (no root watch).
- **`process/`** + **`process/processd/`** — governed, steerable units of work (scripted or
  LLM agents), behind an HTTP control + read plane.
- **`controlpolicy/`** + **`broker/`** — how a process *runs* is a seam: **local**
  (in-process), **remote** (another verse over HTTP), or a **dial-in broker** where a
  firewalled worker or a human executes it. The engine drives all three through one handle,
  so every `verse process` verb works whichever it is.
- **`mirror/`** — reflects a running process into the verse as a `process:instance` entity,
  so "observe/steer another process" is just "observe an entity."
- **`source/`** — reflecting external state *in*: `verse connect` federates your working
  tree, and a (multi-repo) **git Source** reflects repo state (`git:*`) that triggers react
  to.
- **`versed/`** + **`cmd/verse/`** — one HTTP surface and one `verse` CLI (`up`, `down`,
  `watch`, `entity`, `gate`, `trigger`, `process` incl. the `do` executor verbs, `flow`,
  `connect`).
- **`deploy/`** + **`Makefile`** — a checked-in `o build` and a local-build image pipeline to
  run verse on Kubernetes.

## Docs

- **[Overview](docs/overview.md)** — *read this first*: the architecture, the model, the
  seams, and the load-bearing decisions.
- **[Tutorial](docs/tutorial.md)** — hands-on, zero to a running, steerable flow (plus
  dial-in processes and local-tree federation).
- **[controlPolicy](docs/control-policy.md)** — the base-URL contract for running and
  governing a process: local, remote, a dial-in broker, or a human.
- **[Deployment & steering](docs/deployment-and-steering.md)** — the planes, "steering
  outranks observation," and why a hold costs a cursor, not a buffer.
- **[verse connect & docd](docs/connect.md)** — federating a working tree through docd's two
  faces.
- **[Sources](docs/sources.md)** — reflecting external systems (git, GitHub, …) via
  webhook-as-hint + poll-reconcile, git first.
- **[Kubernetes deploy](deploy/README.md)** — build the image (`make docker`) and run verse
  on a cluster.
- Also: [change events](docs/change-events.md) · [content federation](docs/content-federation.md)
  · [writing a controller](docs/controllers.md) · [write-back](docs/write-back.md)
  · [external processes](docs/external-processes.md) · [entity values](docs/entity-values.md).
