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
repos:
- name: verse
  repo: "https://github.com/signadot/verse"
  base: main
  destinations:
  - {name: auto, ref: "refs/heads/verse/auto/$sha/$task"}
  - {name: main, ref: "refs/heads/main", credential: gh-verse}
```

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
destination names and no other. A refused gate means the ability to push never existed. A push the
REMOTE refuses — not a fast-forward, a protected branch, a ruleset — is an answer rather than a
failure, because that is an authority verse deliberately does not hold, exercising it.

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
