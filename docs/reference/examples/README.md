# Examples

Working programs you can copy and change. They compile with the tree, which is the point: an
example in a fenced block goes stale the first time a signature moves and nobody finds out.

| | |
|---|---|
| [runner/](runner/main.go) | a verse runner: the smallest thing that serves the task-control contract, with the two functions a drive changes marked |
| [coderunner/](coderunner/main.go) | a runner that codes: a workspace of git checkouts per task, a contained process per task, and delivery as a gated action of the task that the runner performs |
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
verse task run --runner runners:pool:gpu "fetch the data; retrain; report"
verse ps --runner runners:pool:gpu
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
verse task run --runner runners:pool:code --environment local --as "$USER" "add a test for firstLine"
```

### A workspace per task

Each task gets its own working directory holding **every repository the environment names**, each
cloned off a shared bare mirror, all on the same `verse/auto/<task>` branch:

```
<workspace>/verse/         ← being changed;  push: refs/heads/verse/auto/$task
<workspace>/tony-format/   ← there to be read; no push declared
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

### Delivery is an action of the task

Where a repository declares a `push`, the work leaves as the **last action of the task's own loop**
— gated by its vector, narrated on its read plane, and done before the terminal status:

```tony
vector:
  default: auto
  gates:
  - {name: delivery, mode: ask, match: {verbs: [push]}}
```

The **runner** performs it, because the task cannot reach the credential; the task asks over a
channel that carries one operation and no argument that changes where anything goes. A refused gate
means nothing is pushed. A failed push fails the task.

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
`refs/pending/<task>/<repo>` when its delivery was approved and `refs/held/<task>/<repo>` when it
was refused or the task never got that far. The staged copy is dropped once the far side has it, so
what remains is exactly the work this runner holds that the world does not — and `pending` is
retried on the next spawn against that repository, while `held` never is.

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
