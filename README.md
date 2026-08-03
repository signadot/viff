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

./scripts/seed-demo.sh            # in another terminal: a charter, entities, a waiting gate
```

`up` brings up logd and docd as children, so the store is durable from the first run — it
resolves the `o` that hosts them itself (`--o-bin`, else a version-matched one on `PATH`,
else installed into `~/.verse/bin`; see [the reference](docs/reference/up.md#the-o-binary)), so
`up` works wherever you run it, not only in a checkout.

`scripts/seed-demo.sh` fills the empty verse: two rules, a handful of entities, one auto
reaction that has already happened and one gate waiting for you — then prints what to type
next. Or do it by hand: `verse trigger add …`, `verse entity put …`, `verse gates`,
`verse gate approve …`. Two more scripts seed the [tutorial](docs/tutorial.md)'s sections:
`scripts/seed-entities.sh` (a world to select over) and `scripts/charter-shapes.sh` (the
shapes a rule comes in, one command each). The tutorial walks the lot in five sections —
entities, processes, and the charters that relate them.

## What's here

- `entity/` — the entity-verse: a `<system, kind, id>` store, observable and
  revision-cursored, durable and scope-aware with copy-on-write, over logd/docd. One
  conditional write addresses a whole entity or one field inside it.
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
- `source/` — reflecting external state *in*: a Source is a program you deploy and install
  (`verse source add`), reflecting an owned slice like `git:*` that triggers react to. `verse
  connect` federates your working tree the same way, into a scope.
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
- [Tutorial](docs/tutorial.md) — hands-on, in five sections: entities, processes, and the
  charters that relate them, each with a script that seeds it.
- [Trigger model](docs/trigger-model.md) — the two grammars a charter is written in, and
  what every field of a rule means.
- [Reference](docs/reference/README.md) — the command surface, the config layers, and a
  per-topic deep end: [starting a verse](docs/reference/up.md),
  [entities](docs/reference/entities.md), [processes](docs/reference/processes.md),
  [connect and scopes](docs/reference/connect.md), [goals](docs/reference/goals.md),
  [the store](docs/reference/store.md), [representation](docs/reference/representation.md).
- [Kubernetes deploy](deploy/README.md) — build the image (`make docker`) and run verse on a
  cluster.
