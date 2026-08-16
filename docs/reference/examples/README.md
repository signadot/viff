# Examples

Working programs you can copy and change. They compile with the tree, which is the point: an
example in a fenced block goes stale the first time a signature moves and nobody finds out.

| | |
|---|---|
| [runner/](runner/main.go) | a verse runner: the smallest thing that serves the task-control contract, with the two functions a drive changes marked |
| [coderunner/](coderunner/main.go) | a runner that codes: a workspace of git checkouts per task, a contained process per task, and a push as a gated capability the runner hands a credential for |
| [source/](source/main.go) | a verse source: the smallest thing that serves the reflection contract, with the one function a drive changes marked |

The two runners are a ladder rather than one program trying to be both: `runner/` is the
contract at its smallest, `coderunner/` is what a real one does with it.

## Running the runner

```sh
go run ./docs/reference/examples/runner -addr :9800
```

Then, against a verse:

```sh
verse runner add pool:gpu http://localhost:9800
verse task run --runner verse:runners:pool:gpu "fetch the data; retrain; report"
verse ps --runner verse:runners:pool:gpu
```

It is a placeholder driver — it splits the prompt on semicolons and does each part — so you can
watch a task run end to end before replacing the two functions with the work you actually
have. The `verse runner add` above will print `reach: a runner`, which is the install probe
confirming that what is at that url really serves the contract.

## Running the coderunner

```sh
go run ./docs/reference/examples/coderunner \
  -config docs/reference/examples/coderunner/coderunner.tony -addr :9802
```

Its config declares **environments** — a set of repositories, each with a base and optionally
somewhere its result goes, plus which driver runs the work, which tools exist, and what the task
may reach — and a spawn names one:

```sh
verse runner add pool:code http://localhost:9802
verse task run --runner verse:runners:pool:code --environment local --as "$USER" "add a test for firstLine"
```

### A workspace per task

Each task gets its own working directory holding **every repository the environment names**, each
cloned off a shared bare mirror, all on the same `verse/auto/<task>` branch:

```
<workspace>/verse/         ← being changed;  destinations: auto → refs/heads/verse/auto/$task
<workspace>/tony-format/   ← there to be read; no destinations declared
```

That is a list rather than one repo because the work usually is: a change to a service and the
library it consumes, a generator and the code it generates. Side by side under one root, they are
one task rather than two that have to be coordinated — and `push` being **per repository** is what
keeps a reference tree read-only.

### A process per task

The runner hosts no task. It prepares the workspace, starts `coderunner -task` inside the
containment, and **forwards the task-control contract to it** — the same routes verse calls on the
runner. So spawn, attach, gate answers, steering, cancel and vector changes all cross a process
boundary and nothing is translated.

The reason is that containment is applied by exec'ing something. A loop running in the runner's own
process is a loop no sandbox reaches, which made the *more* supervisable driver the *less* contained
one. Drawing the boundary once, around the whole task, is what puts `run_bash` inside it — and it
keeps the runner's git credential outside, as a property of the boundary rather than of an env-var
allowlist.

The child is reached over a socket it inherits as a descriptor rather than by path, because a
control plane the agent can dial is an agent that can answer its own gates.

### A push is a capability with named destinations

A repository declares **destinations** — a name and a ref — and a task names one of them, never a
ref. `auto` is where a task's own work lands and is what `push:` spells; anything else is a flow
the environment chose to allow:

```tony
credentials:
- {name: gh-verse, env: GITHUB_TOKEN}     # the NAME of a variable; this file carries no secret
repos:
- name: verse
  repo: "https://github.com/signadot/verse"
  base: main
  destinations:
  - {name: auto, ref: "refs/heads/verse/auto/$sha/$task"}
  - {name: main, ref: "refs/heads/main", credential: gh-verse}
```

A credential is **operator-supplied and never minted**. These are hand-made tokens: a system that
can create credentials is a system whose compromise creates credentials, so this maps grants to
secrets that already exist and what one permits is whatever the operator gave it. A destination
naming none falls back to the runner's own `-token-env` token.

The closing delivery is one of those pushes — the **last action of the task's own loop**, gated by
its vector, narrated on its read plane, done before the terminal status — and the agent can propose
others with the `push` tool. Both are ordinary actions, so a charter decides them the same way:

```tony
vector:
  default: auto
  gates:
  # anything leaving at all
  - {name: delivery, mode: ask, match: {verbs: [push]}}
  # or just the one that matters, by the ref it will actually touch
  - {name: to-main, mode: ask, match: {resources: [{kind: gitref, pattern: "refs/heads/main"}]}}
```

**Approval is what releases the credential**, and there is no other path to one: the task holds no
git credential until a gate says yes about a particular destination, and then it holds the one that
destination names and no other. A refused gate means the ability to push never existed.

That is also why the channel carries a credential rather than an operation. One op that the runner
performs looks smaller and is not: the op count stays at one while the *operation* count grows
behind it — a merge is fetch, merge, push; a retire is a delete; a tag is another — and each is a
new parser, a new authorization decision, and a new thing to audit. Releasing a credential is the
point at which the runner stops needing to know what git operation is being done, so a new flow is
a line of config and costs the protocol nothing.

### Who enforces what

Easy to assume and expensive to get wrong, so it is worth splitting out:

| | enforced by |
|---|---|
| **whether** — may this happen at all | the vector, at the gate. Load-bearing: no approval, no credential. |
| **which secret** — what is released | the environment's destination → credential mapping. |
| **where** — which refs a credential may touch | the forge: token scope, and repository rules about who may push what. |
| **how** — fast-forward only | **the receiving end.** `receive.denyNonFastForwards`, or the forge's equivalent rule. |

**verse does not compete with git server configuration.** A repository's rules about its own
history are not verse's to hold: humans push to these repositories too, and an invariant enforced
by the receiver applies to all of them, while one enforced by verse applies only to the actors
that went through verse — the smaller set, and the one that was never the worry. So `--ff-only` is
an instruction in a prompt backed by a rule at the far end, and a model that ignores the
instruction gets refused by the remote rather than merging some other way.

Which makes a REMOTE's refusal — not a fast-forward, a protected branch, a ruleset — an answer
rather than a failure: an authority verse deliberately does not hold, exercising it. The task
completes and says so, the driver re-plans, and the work moves to `held` rather than being retried
against a rule that will refuse it every time. That matters because it is the *ordinary* outcome
of a merge proposal, main having moved while somebody answered the gate.

One interaction to configure deliberately in the other direction: `receive.denyDeletes` blocks a
sweep. A destination that exists to retire a branch needs a remote that permits deletes on that
ref pattern.

### The environment is the unit of narrowing

What a task may reach is what its environment declares, and that is the whole containment — not a
rule each charter remembers. Two environments over one repository, from the deployment:

| | `verse-fix` | `verse-merge` |
|---|---|---|
| destinations | `auto` | `main`, `retire` (a delete) |
| `push` in `tools:` | no | yes |

`tools:` is a FLOOR — a spawn narrows it and cannot widen it — so a task in `verse-fix` cannot ask
for a destination at all, and its closing delivery to `auto` is the finisher's rather than
something the agent can aim. A charter that has not been pointed at `verse-merge` cannot reach
main however its vector is set.

Both could have been one environment with two destinations and one more tool. The cost would have
been paid elsewhere: charters that share an environment would each have had to narrow their own
`tools:`, which is a rule somebody forgets rather than a property anything holds.

**A task here need not be an agent.** This runner builds `driver: calls` as well as `llm` and the
programs under `agents:`, and a call list gets the same extras — the same `push`, the same
finisher, the same deriver — so a charter that knows the actions writes them and pays for no model
at all:

```tony
spawn:
  environment: verse-merge
  vector: auto
  calls:
  - tool: run_bash
    input: {command: "git -C verse fetch origin $branch && git -C verse merge --ff-only FETCH_HEAD"}
  - tool: push
    input: {destination: main}
```

Nothing downstream can tell which driver proposed the push: it derives the same effect, meets the
same gate, and is narrated the same way. That is what makes it cheap — see
[tasks.md](../tasks.md#the-drivers) for the driver itself, and `docs/working/comment-drift.tony` for
the charter this example is taken from, where it replaced an agent that spent five minutes
searching the filesystem when its branch was deleted underneath it.

The egress allowlist is the one place **reachable** and **authorised** come apart. An environment
with destinations needs a route to the forge — the task does the pushing — and the allowlist is
per network namespace, which the model's shell shares. So the shell can reach `github.com` and can
do nothing there: the credential is on the other side of `env:`, the allowlist a subprocess
inherits. A missing route is refused when the config is read rather than discovered at the end of
a run.

The commit carries what verse told the runner:

```
Verse-Task: run-8f30…
Verse-Principal: scott
Verse-Subject: demo:pr:1
Verse-Chain: review
```

which is the only externally checkable link back into the verse when the push lands under a shared
bot identity.

### Work waits in the mirror

Before any push is attempted the commit is staged in the runner's own mirror, under
`refs/pending/<task>/<repo>/<target>` when a push was approved and `refs/held/<task>/<repo>` when
it was refused or the task never got that far. The task does the pushing, so whether it landed is
the remote's to say: the sweep asks it — when the task ends, at startup, and on the next spawn
against that repository — and a ref that landed is dropped, one that did not is retried, and one
the remote refuses moves to `held`, which is never retried.

That is what makes the workspace disposable: it is removed unless something could not be staged.
`GET /runner` lists what is waiting, with the command to fetch it:

```sh
curl -s localhost:9802/runner        # `undelivered:` — each with a pasteable git fetch
```

### Containment, refused rather than degraded

An environment declares what the task may reach; the runner builds it or **refuses the launch**.
`-confine host` claims almost nothing and is what a laptop can honestly provide; `-confine bwrap`
reaches a private filesystem and an unshared network on Linux. An environment listing `egress:`
destinations is asking for `network.egress: restricted`, which is built by unsharing the network
and binding in a socket to an allowlisting proxy — so it cannot run under `host`, and says so
rather than running the task with a proxy it is free to ignore.

`GET /runner` reports the containment **this deployment** can build, per leaf.

## Running the source

```sh
go run ./docs/reference/examples/source -addr :9801 -repo verse=/path/to/a/repo
```

It serves two routes and nothing else — `GET /source` says what it is, `GET /reflect` says
what it sees — so you can look at exactly what verse would be told:

```sh
curl localhost:9801/source
curl "localhost:9801/reflect?ref=git:head:verse"
```

As shipped it reflects real git repositories, so what you copy already works before you change
anything. One function is marked to replace: read your system's truth and return it. It is
never handed a store and cannot write — converging the store with your answer stays on verse's
side, which is what keeps a source short enough to be worth writing.
