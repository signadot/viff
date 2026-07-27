# verse

A small runtime for emergent SDLC flows on a dial of agentic autonomy. You install a
*charter* of event-condition-action rules; the runtime reacts to changes in a shared,
observable entity-verse and does the work with governed agents. Every reaction passes a
gate — *auto*, *ask* (a human or agent approves), or *park* — so you set how much runs on
its own, action by action, and the agents doing the work stay observable and steerable.
Nobody authors the flow: it emerges from triggers reacting to state that other actions
write, and you decide how much autonomy it runs with.

```
entity change ──▶ trigger matches ──▶ gate resolves ──▶ action fires
     ▲                                                       │
     └───────────── actions write entities ◀────────────────┘
```

## Quick start

```sh
go build -o verse ./cmd/verse     # or: make bin
export VERSE_ADDR=localhost:9797
./verse up                        # engine + process runtime + store, in one process
```

`up` is docd-backed by default — durable, and it brings up logd and docd as children. Add
`--store memory` for a zero-dependency, no-persistence run. In another terminal, install a
charter and drive it: `verse trigger add …`, `verse entity put …`, `verse gates`,
`verse gate approve …`. The [tutorial](docs/tutorial.md) walks it end to end — charters,
gates, processes, steering, human-executed dial-in processes, and federating your local
working tree.

## What's here

- `entity/` — the entity-verse: a `<system, kind, id>` store, observable and
  revision-cursored. Durable and scope-aware, with copy-on-write, via logd/docd by default;
  an in-memory store (`--store memory`) drops in behind the same interface.
- `trigger/` — the ECA engine: conjunctive matches, conjunctive gates posed concurrently,
  actions-as-data, level-triggered catch-up via a cursor, and per-subtree watches with no
  root watch.
- `process/` and `process/processd/` — governed, steerable units of work, scripted or LLM
  agents, behind an HTTP control and read plane.
- `runner/` and `broker/` — how a process *runs* is a seam: local (in-process),
  remote (another verse over HTTP), or a dial-in broker where a firewalled worker or a human
  executes it. The engine drives all three through one handle, so every `verse process` verb
  works whichever it is.
- `mirror/` — reflects a running process into the verse as a `process:instance` entity, so
  "observe or steer another process" is just "observe an entity."
- `source/` — reflecting external state *in*: `verse connect` federates your working tree,
  and a multi-repo git Source reflects repo state (`git:*`) that triggers react to.
- `versed/` and `cmd/verse/` — one HTTP surface and one `verse` CLI: `up`, `down`, `watch`,
  `entity`, `gate`, `trigger`, `process` including the `do` executor verbs, `flow`, and
  `connect`.
- `deploy/` and `Makefile` — a checked-in `o build` and a local-build image pipeline to run
  verse on Kubernetes.

## Docs

[docs/](docs/README.md) is the index: it pairs each subject — the store, triggers,
processes, steering, federation — with the tutorial section that walks it and the notes that
explain it. Everything under `docs/` describes what runs today; design notes for work that
is not built live in [docs/roadmap/](docs/roadmap/README.md). Start with:

- [Overview](docs/overview.md) — the architecture, the model, the seams, and the
  load-bearing decisions. Read this first.
- [Tutorial](docs/tutorial.md) — hands-on, zero to a running, steerable flow, plus dial-in
  processes and local-tree federation.
- [Trigger model](docs/trigger-model.md) — the two grammars a charter is written in, and
  what every field of a rule means.
- [Reference](docs/reference.md) — the command surface, the config layers, and what verse
  reads and writes.
- [Kubernetes deploy](deploy/README.md) — build the image (`make docker`) and run verse on a
  cluster.
