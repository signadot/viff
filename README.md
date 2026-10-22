# verse

Verse is a small runtime. It governs software delivery work across systems that you do not
own, and you choose how much of that work agents do without asking.

You install a *charter*. A charter is a list of rules, and each rule has an event, a condition
and an action. Verse keeps a shared store of *entities*, which anyone may observe. When an
entity changes, verse finds the rules that match and does the work with agents.

Every reaction passes a *gate*. A gate has one of three modes:

| Mode | What happens |
|---|---|
| `auto` | the action runs |
| `ask` | a person or an agent must approve first |
| `park` | the action is held |

You set the mode for each action. The agents that do the work stay observable, and you can
steer them while they run.

```
entity change ──▶ trigger matches ──▶ gate resolves ──▶ action fires
     ▲                                                       │
     └───────────── actions write entities ◀────────────────┘
```

## No orchestrator

Verse has no orchestrator. This follows from what verse can do. It is not a preference.

Verse works with things it does not own: a repository, a chat workspace, a person's time.
Verse can *observe* them, and it can *request* that they change. It cannot stop them from
changing.

An orchestrator keeps a position in a plan. That position is a second statement about the
state of those things, and it is wrong as soon as one of them changes between two steps. So
verse keeps no plan. It reacts to what it last observed. This is why the loop in the diagram
goes through the store, and not through a scheduler.

The flow of work is therefore not written down anywhere. Verse *derives* it when you ask:
`verse flow show <subject>` builds the flow for one subject from three sources.

| Source | What it says |
|---|---|
| the charter | what may still happen |
| the mirror | what has already happened |
| the live registry | what is running now |

You write the rules, and each rule's spawn says what its task may reach, its environment, and
what it does without asking, its autonomy.

## Quick start

You need Go 1.27.1 or later.

```sh
go build -o verse ./cmd/verse
export PATH="$PWD:$PATH"          # so that `verse` is this build
export VERSE_ADDR=localhost:9797
verse up                          # engine + task runtime + store, in one process

./scripts/seed-demo.sh            # in another terminal: a charter, entities, a waiting gate
```

`verse up` starts the store too, as two child processes (logd and docd), so the store is
durable from the first run. `up` finds the `o` binary that hosts them by itself. It uses
`-o-bin` if you give it, else a matching version on `PATH`, else one it installs into
`~/.verse/bin` ([the reference](docs/reference/up.md#the-o-binary)). So `up` works in any
directory, and not only in a checkout.

You need no configuration to start. Toward a verse on your own machine the CLI acts as
`user:<login>@localhost`, where `<login>` is your login name.

`scripts/seed-demo.sh` fills the empty verse. It writes two rules and a few entities. When it
ends, one reaction has already happened and one gate is waiting for you. Then it prints what
to type next.

You can do the same by hand: `verse charter -a …`, `verse data … --put …`, `verse question`,
`verse question approve …`.

Two more scripts seed worlds to read and to react to:

- `scripts/seed-entities.sh` writes entities to select over, for the data page of the
  [tutorial](docs/tutorial/README.md).
- `scripts/charter-shapes.sh` installs the shapes a rule can have, one command each, for
  [the tutorial's charters pages](docs/tutorial/charters.md).

The tutorial has four pages, one for each part of verse: data, questions, tasks and charters.

## What's here

- `entity/` — the store of entities. It is one tree, and a kinded path addresses each node.
  The store can be watched, and every commit has a revision. It is durable, and a *scope* is a
  copy-on-write view of it. It runs over logd and docd. Every write names who made it. A node
  belongs to whoever created it, and the store refuses a write that the node's permissions do
  not allow ([permissions](docs/perms.md)).
- `trigger/` — the rule engine. A rule matches when all of its conditions hold. All the gates
  of a rule are asked at the same time, and all must pass. An action is data, not code. A new
  rule acts on what already stands in the store. The engine watches only the subtrees that
  its rules read.
- `task/` — a *task* is a unit of work that verse governs and that you can steer. An agent or
  a shell command does it.
- `runner/` — where a task runs. There are two ways: local, in the verse process; and remote,
  on another verse over HTTP. The engine drives both through one interface, so every
  `verse task` command works with each. `runner/runnerd/` hosts a runner, embedded in verse
  or served over HTTP, and `runner/runnerd/verserunner/` is the runner verse ships.
- `mirror/` — writes each running task into the store, as a `verse.task.instance` entity. So
  to observe or steer a task, you observe an entity.
- `resolver/` — a service that answers asks for whoever grants it. A resolver is installed. It
  is not written in a charter, because it lasts longer than one rule, and because it belongs to
  whoever runs it. Its `match` says which asks it watches; a person or a place's owner on record
  grants it to answer for them, and its answer is then theirs. To consult somebody it raises an
  onward ask, whose answer comes back as its own.
- `io/` and `cmd/iod/` — the connection to an external system. An *iod* has two halves: it
  reflects state in, and it acts out. You deploy an iod and install it (`verse iod add`). It
  owns a place in the store, such as `git`, and rules react to that place. Verse ships five
  (`iod git|git-issue|slack|linear|github`). To write another, copy the sample program and
  write one function.
- `versed/` and `cmd/verse/` — the HTTP server and the `verse` CLI. Each command is named in
  the singular, and with no argument it lists.

  | The CLI | Commands |
  |---|---|
  | governs | `data`, `charter`, `schedule`, `task` (with the `do` verbs for an executor), `flow`, `resolver`, `runner`, `io`, `perms`, `group` |
  | answers with | `question` |
  | reads | `status`, `watch`, `provider`, `schema` |
  | runs | `up`, `down` |

- `deploy/` and `Makefile` — build the image locally and run verse on Kubernetes, from a
  checked-in `o build`.

## Docs

[docs/](docs/README.md) is the index. It groups the pages by who reads them: somebody
running a verse, writing a charter, building a runner or an iod, or changing verse itself. It
describes what runs today, and so does the site mkdocs builds from it. Start with:

- [Overview](docs/overview.md) — the architecture, the model, the seams, and the decisions
  the design depends on. Read this first.
- [Tutorial](docs/tutorial/README.md) — four pages that you run yourself: data, questions,
  tasks, and the charters that put them together.
- [Charter reference](docs/trigger-model.md) — the model of a rule, and the two grammars a
  charter is written in. It links to the pages that say what each field means.
- [Seams](docs/seams.md) — the nine places that can be a network, and which side opens
  each: what may run remotely, what may sit behind a firewall, and what each costs.
- [Reference](docs/reference/README.md) — the commands, the config layers, and a page for
  each topic: [starting a verse](docs/reference/up.md),
  [entities](docs/reference/entities.md), [tasks](docs/reference/tasks.md),
  [resolvers](docs/reference/resolvers.md), [runners](docs/reference/runners.md),
  [iods](docs/reference/io.md) and [the five that ship](docs/reference/iod.md),
  [scopes](docs/reference/scopes.md), [MCP](docs/reference/mcp.md),
  [the store](docs/reference/store.md), [representation](docs/reference/representation.md).
- [Kubernetes deploy](deploy/README.md) — build the image (`make docker`) and run verse on a
  cluster.
  
## Deployment

- Deployment of shared instance `I` gets an annotated git tag `deploy/<I>.N`. 
  The annotation for the tag mentions the components which are deployed.
- Deployment tags the main branch in normal operation, but may tag off main
  to work around a deployment issue.  If it tags off-main, the tag annotation names the
  nearest main commit.
- main stays ff-only

