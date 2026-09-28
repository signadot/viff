# Examples

These are working programs that you can copy and change. They compile with the rest of the
tree.

| Example | What it is | The command that runs it |
|---|---|---|
| [runner/](runner/main.go) | a verse [runner](../../glossary.md#runner): the smallest thing that serves the task-control contract. The two functions that a drive changes are marked. | `go run ./docs/reference/examples/runner -addr :9800` |
| [coderunner/](coderunner/main.go) | a runner that codes. Each task gets a workspace of git checkouts and a contained process. A push is a gated capability, and the runner hands out a credential for it. | `go run ./docs/reference/examples/coderunner -config docs/reference/examples/coderunner/coderunner.tony -addr :9802` |
| [worker/](worker/main.go) | a dial-in [worker](../../glossary.md#worker). It claims work from a [broker](../../glossary.md#broker) and does it with a local `claude`, so nothing has to be able to reach it. | `go run ./docs/reference/examples/worker -verse localhost:9797 -as "runner:$USER"` |
| [io/](io/main.go) | a verse [io](../../glossary.md#io): the smallest thing that serves the reflection contract. The one function that a drive changes is marked. | `go run ./docs/reference/examples/io -addr :9801 -repo verse=/path/to/a/repo` |

The two runners are two steps, and not one program that tries to be both. `runner/` is the
contract at its smallest. `coderunner/` is what a real runner does with the contract.

`worker/` is a different kind of program. It is not a smaller runner. It is an inverted one.
Verse reaches a runner, and a worker reaches verse. The network decides which one you want.
The work does not decide it.

## Running the runner

```sh
go run ./docs/reference/examples/runner -addr :9800
```

Then run these commands against a verse:

```sh
verse runner add gpu http://localhost:9800
verse task run -runner verse.runners.gpu "fetch the data; retrain; report"
verse ps -runner verse.runners.gpu
```

The runner has a placeholder driver. The driver splits the prompt on semicolons and does each
part. You can watch a task run from start to end, and then replace the two functions with your
own work.

The `verse runner add` above prints `reach: a runner`. That line is the install probe. It
confirms that the program at the URL serves the contract.

## Running the worker

The worker needs a verse whose tasks are brokered. A worker claims work. Verse does not hand
the work to it.

```sh
verse up -broker -broker-addr :9090                                    # one terminal
go run ./docs/reference/examples/worker -verse localhost:9797 -as "runner:$USER"   # another
verse task run -runner broker "summarise what changed in cmd/verse this week"   # a third
```

The task moves through three statuses:

| Status | When |
|---|---|
| `unclaimed` | until the worker takes the task |
| `running` | while the worker does it |
| `done` | at the end. The agent's answer is the task's `result`. |

While the task runs, `verse task attach <id>` shows the agent's output line by line.

The worker is not configured with a broker URL. Each task carries the `dialURL` through which
a worker claims it. So the same binary works against any verse. When the broker moves, every
worker follows it.

The worker takes two more flags:

| Flag | What it does |
|---|---|
| `-agent` | names the program that does the work. `/bin/echo` is a useful stand-in while you connect the parts. |
| `-once` | takes a single pass and exits |

The credential never passes through verse. The agent runs on this machine, with the
permissions that the owner of this machine gave it. For this reason a laptop is a reasonable
place to run a task.

## Running the coderunner

```sh
go run ./docs/reference/examples/coderunner \
  -config docs/reference/examples/coderunner/coderunner.tony -addr :9802
```

The config of the coderunner declares **environments**. An
[environment](../../glossary.md#environment) holds:

- a set of repositories. Each has a base, and may have a place where its result goes.
- the driver that runs the work.
- the tools that exist.
- what the task may reach.

A spawn names one environment:

```sh
verse runner add coderunner http://localhost:9802 -identity coderunner    # its reports are written as runner:coderunner
verse task run -runner verse.runners.coderunner -environment local -as "$USER@example.com" "add a test for firstLine"
```

### A workspace per task

Each task gets its own working directory. The directory holds **every repository that the
environment names**. Each repository is cloned from a shared bare mirror, and all of them are
on the same branch, `verse/auto/<task>`:

```
<workspace>/verse/         ← being changed;  destinations: auto → refs/heads/verse/auto/$task
<workspace>/tony-format/   ← there to be read; no destinations declared
```

`push` is **per repository**. So a repository with no destination stays read-only.

### A process per task

The runner does not host a task. The runner prepares the workspace and starts
`coderunner -task` inside the containment. Then it **forwards the task-control contract to the
child process**. These are the same routes that verse calls on the runner. So spawn, attach,
gate answers, steering, cancel and changes of the vector all cross a process boundary, and the
runner translates nothing.

The child process inherits a socket as a file descriptor. It does not reach the socket by path.

### A push is a capability with named destinations

A repository declares **destinations**. A destination has a name and a ref. A task names a
destination, and never a ref. `auto` is where the task's own work lands, and `push:` means
`auto`. Every other destination is a flow that the environment chose to allow:

```tony
credentials:
- name: gh-verse
  env: GITHUB_TOKEN     # the NAME of a variable; this file carries no secret
repos:
- name: verse
  repo: "https://github.com/signadot/verse"
  base: main
  destinations:
  - name: auto
    ref: "refs/heads/verse/auto/$sha/$task"
  - name: main
    ref: "refs/heads/main"
    credential: gh-verse
```

A credential is **supplied by the operator and never minted**. The credentials are tokens that
a person made. The config maps grants to secrets that already exist. What a credential permits
is what the operator gave it. A destination that names no credential uses the runner's own
token, from `-token-env`.

The closing delivery is one of these pushes. It is the **last action of the task's own loop**.
The task's vector gates it, the [observation plane](../../glossary.md#observation-plane) shows
it, and it happens before the terminal status. The agent can propose other pushes with the
`push` tool. Both kinds are ordinary actions, so a [charter](../../glossary.md#charter) decides
them in the same way:

```tony
vector:
  default: auto
  gates:
  # anything leaving at all
  - name: delivery
    mode: ask
    match:
      verbs: [push]
  # or just the one that matters, by the ref it will actually touch
  - name: to-main
    mode: ask
    match:
      resources:
      - kind: gitref
        pattern: "refs/heads/main"
```

**Approval releases the credential**, and nothing else does. The task holds no git credential
until a gate says yes about one destination. Then the task holds the credential that this
destination names, and no other. If the gate refuses, the task never has the ability to push.

### Who enforces what

Four questions have four different answers:

| | enforced by |
|---|---|
| **whether** — may this happen at all | the vector, at the gate. Without approval there is no credential. |
| **which secret** — what is released | the environment's destination → credential mapping. |
| **where** — which refs a credential may touch | the forge: token scope, and repository rules about who may push what. |
| **how** — fast-forward only | **the receiving end.** `receive.denyNonFastForwards`, or the forge's equivalent rule. |

**verse does not compete with git server configuration.** So `--ff-only` is an instruction in
a prompt, and a rule at the far end enforces it. If a model ignores the instruction and tries
to merge in another way, the remote refuses the push.

A refusal by the remote is an answer, and not a failure. Examples are "not a fast-forward", a
protected branch, and a ruleset. When the remote refuses:

1. The task completes and reports the refusal.
2. The driver plans again.
3. The work moves to `held`. Verse does not retry it against a rule that refuses it every
   time.

This is the ordinary outcome of a merge proposal when main moves while somebody answers the
gate.

One setting works in the other direction, and you must configure it on purpose.
`receive.denyDeletes` blocks a sweep. A destination that retires a branch needs a remote that
permits deletes on that ref pattern.

### The environment is the unit of narrowing

A task may reach what its environment declares, and nothing more. That is the whole
containment. It is not a rule that each charter must remember. Here are two environments over
one repository, from the deployment:

| | `verse-fix` | `verse-merge` |
|---|---|---|
| destinations | `auto` | `main`, `retire` (a delete) |
| `push` in `tools:` | no | yes |

`tools:` is a floor. A spawn can narrow it and cannot widen it. So a task in `verse-fix`
cannot ask for a destination at all. Its closing delivery to `auto` comes from the finisher,
and the agent cannot aim it. A charter that does not point at `verse-merge` cannot reach main,
whatever its vector says.

**A task here need not be an agent.** This runner builds `driver: calls` as well as `llm` and
the programs under `agents:`. A call list gets the same extras: the same `push`, the same
finisher and the same deriver. So a charter that knows the actions writes them down, and pays
for no model at all:

```tony
spawn:
  environment: verse-merge
  vector: auto
  calls:
  - tool: run_bash
    input:
      command: "git -C verse fetch origin $branch && git -C verse merge --ff-only FETCH_HEAD"
  - tool: push
    input: {destination: main}
```

Nothing downstream can tell which driver proposed the push. The push derives the same effect,
meets the same gate, and appears in the same way. For the driver itself, see
[tasks.md](../tasks.md#the-drivers). This example comes from the charter
`deploy/charters/verse/repo/verse/comment-drift.tony`.

The egress allowlist is the one place where **reachable** and **authorised** differ. An
environment with destinations needs a route to the forge, because the task does the pushing.
The allowlist is per network namespace, and the model's shell shares that namespace. So the
shell can reach `github.com`, but it can do nothing there. The credential is kept out of
`env:`, which is the allowlist of variables that a subprocess inherits. Verse refuses a
missing route when it reads the config. It does not wait to find the problem at the end of a
run.

The commit carries what verse told the runner:

```
Verse-Task: run-8f30…
Verse-Principal: user:walter@example.com
Verse-Subject: demo.pr.1
Verse-Chain: review
```

When the push lands under a shared bot identity, these lines are the only link back into verse
that someone outside can check.

### Work waits in the mirror

Before the runner tries any push, it stages the commit in its own mirror:

| Ref | When |
|---|---|
| `refs/pending/<task>/<repo>/<target>` | a push was approved |
| `refs/held/<task>/<repo>` | the push was refused, or the task did not reach it |

The task does the pushing, so the remote says whether a push landed. The sweep asks the remote
at three times: when the task ends, at startup, and on the next spawn against that repository.

| What the remote says | What the sweep does |
|---|---|
| the ref landed | drops it |
| the ref did not land | retries it |
| the remote refuses it | moves it to `held`. Verse never retries a held ref. |

For this reason the workspace can be thrown away. The runner removes it, unless something
could not be staged. `GET /runner` lists what is waiting, with the command to fetch each item:

```sh
curl -s localhost:9802/runner        # `undelivered:` — each with a pasteable git fetch
```

### Containment, refused rather than degraded

An environment declares what the task may reach. The runner builds that containment, or it
**refuses the launch**.

| Flag | What it provides |
|---|---|
| `-confine host` | almost nothing. It is what a laptop can honestly provide. |
| `-confine bwrap` | a private filesystem and an unshared network, on Linux |

An environment that lists `egress:` destinations asks for `network.egress: restricted`. The
runner builds this by unsharing the network and binding in a socket to a proxy that applies
the allowlist. So it cannot run under `host`. The runner says so. It does not run the task
with a proxy that the task is free to ignore.

`GET /runner` reports the containment that **this deployment** can build, per leaf.

## Running the io

```sh
go run ./docs/reference/examples/io -addr :9801 -repo verse=/path/to/a/repo
```

The io serves two routes and nothing else:

| Route | What it says |
|---|---|
| `GET /io` | what the io is |
| `GET /reflect` | what the io sees |

So you can look at exactly what verse would be told:

```sh
curl localhost:9801/io
curl "localhost:9801/reflect?at=git.verse"
```

As shipped, the io reflects real git repositories. So the code you copy works before you
change anything. One function is marked for you to replace. It reads your system's truth and
returns it. The function is never handed a [store](../../glossary.md#store), and it cannot
write. Verse converges the store with your answer.

## Why it is this way

### Programs, not fenced blocks

The examples compile with the tree. An example in a fenced block goes out of date the first
time a signature changes, and nobody notices.

### A list of repositories

A workspace holds a list of repositories, and not one repository, because the work usually
spans several. Examples are a change to a service and the library it uses, or a generator and
the code it generates. Side by side under one root, they are one task. Otherwise they are two
tasks that somebody has to coordinate.

### A process per task

Containment is applied by running a program with exec. A loop that runs in the runner's own
process is a loop that no sandbox reaches. With that design, the driver that is easier to
supervise would also be the one that is less contained. The boundary is drawn once, around the
whole task. That puts `run_bash` inside the boundary. It also keeps the runner's git
credential outside, as a property of the boundary and not of a list of allowed environment
variables.

The child process reaches the socket as a file descriptor and not by path. If the agent could
dial the control plane, the agent could answer its own gates.

### Credentials are never minted

A system that can create credentials is a system whose compromise creates credentials. So the
runner only maps grants to secrets that already exist.

### A credential, not an operation

The channel releases a credential. It does not carry an operation that the runner performs.
One operation looks smaller, but it is not. The count of operations in the protocol stays at
one, while the number of git operations behind it grows:

- a merge is a fetch, a merge and a push.
- a retire is a delete.
- a tag is another operation.

Each one is a new parser, a new authorization decision, and a new thing to audit. When the
runner releases a credential, it no longer needs to know which git operation the task does. So
a new flow is one line of config, and the protocol does not change.

### Verse and git server configuration

The rules of a repository about its own history do not belong to verse. People push to these
repositories too. The receiver enforces an invariant for everyone who pushes. Verse could
enforce it only for the actors that go through verse. That set is smaller, and it is not the
set that causes concern.

A refusal from the remote is an authority that verse deliberately does not hold. The remote
uses it. So verse treats the refusal as an answer, and does not retry.

### One environment for each purpose

The two environments could be one environment with two destinations and one more tool. The
cost would then move to the charters. Charters that share an environment would each have to
narrow their own `tools:`. That is a rule that somebody forgets, and not a property that
something enforces.

### A call list instead of an agent

A call list is cheap, because nothing downstream can tell it from an agent. A charter that
knows its actions pays for no model.

### The io does not write

The io is never handed a store. Verse, not the io, converges the store with the io's answer.
This keeps a source short enough to be worth writing.
