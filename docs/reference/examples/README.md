# Examples

These are working programs that you can copy and change. They compile with the rest of the
tree.

| Example | What it is | The command that runs it |
|---|---|---|
| [runner/](runner/main.go) | a verse [runner](../../glossary.md#runner): the smallest thing that serves the task-control contract. The two functions that a drive changes are marked. | `go run ./docs/reference/examples/runner -addr :9800` |
| [coderunner/](coderunner/main.go) | a runner that codes: the verse runner (`runnerd verse`, package `runner/runnerd/verserunner`) with environments configured, under the coderunner's name and flags. Each task gets a workspace of git checkouts and a contained process. A push is a gated capability the runner performs from its mirror with its own token; the task never holds one. | `go run ./docs/reference/examples/coderunner -config docs/reference/examples/coderunner/coderunner.tony -addr :9802` |
| [io/](io/main.go) | a verse [io](../../glossary.md#io): the smallest thing that serves the reflection contract. The one function that a drive changes is marked. | `go run ./docs/reference/examples/io -addr :9801 -repo verse=/path/to/a/repo` |

The two runners are two steps, and not one program that tries to be both. `runner/` is the
contract at its smallest. `coderunner/` is what a real runner does with the contract.

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

## Running the coderunner

```sh
go run ./docs/reference/examples/coderunner \
  -config docs/reference/examples/coderunner/coderunner.tony -addr :9802
```

The config of the coderunner declares **environments**. An
[environment](../../glossary.md#environment) holds the deployment's half of a run:

- the driver that runs the work.
- the tools that exist.
- what the task may reach.

A spawn names one environment:

```sh
verse runner add coderunner http://localhost:9802 -identity coderunner    # its reports are written as runner:coderunner
verse task run -runner verse.runners.coderunner -environment local -repos repos.tony -as "$USER@example.com" "add a test for firstLine"
```

### A workspace per task

Each task gets its own working directory. The directory holds **every repository that the
spawn names** in its `repos:`, each under the name part of its `repo`. Each repository is cloned
from a shared bare mirror, and all of them are on the same branch, `verse/auto/<task>`:

```
<workspace>/verse/         ← being changed;  {repo: signadot/verse, access: write, push: {new: ["verse/auto/*"]}}
<workspace>/tony-format/   ← there to be read; {repo: signadot/tony-format}
```

What may be pushed is **per repository**. So a repository that is not `access: write`, with its
`push` allowance, stays read-only. A spawn with no `repos:` gets an empty directory.

The workspace is package [`runner/runnerd/workspace`](../../../runner/runnerd/workspace): the mirrors, the clone off
them, the work staged on its way out, and the rules a delivery is checked against. The coderunner
is one runner that uses it.

**`fetch` is the read in the other direction.** A checkout sees upstream as it was at spawn: its
`mirror/*` refs point where the runner's mirror did. An agent whose base may have moved, that
must fast-forward a branch, or that wants a sibling's delivery, calls `fetch` with the
repository. The runner fetches its mirror under the credential it already holds, links the new
objects into the checkout and moves `mirror/*`; the agent's branch and tree are untouched, and
what it does with a newer `mirror/main` is its own git. Nothing crosses the hop but the list of
refs that moved, so a private repository fetches as a public one does. It derives a `network`
read of the repository's host, and the vector decides.

### A process per task

The runner does not host a task. The runner prepares the workspace and starts
`coderunner -task` inside the containment. Then it **forwards the task-control contract to the
child process**. These are the same routes that verse calls on the runner. So spawn, attach,
gate answers, steering, cancel and changes of the vector all cross a process boundary, and the
runner translates nothing. The process, its socket and the forwarding are runnerd's
`SubprocessHandler`, the same for every runner built on it; the coderunner is its `Preparer`,
and what it readies for each task is the workspace, the containment, the egress and the
delivery channel.

The child process inherits a socket as a file descriptor. It does not reach the socket by path.

### A push is a capability within an allowance

The spawn says which **branches** a task may push to, per repository. `new` are patterns for
branches the run may create; a branch one matches is the run's own, and may be pushed again,
rewritten and deleted. `existing` are branches that already exist, by name; a push to one
fast-forwards it, and only `force: true` lets it be rewritten. A task names a branch, and the
runner checks the name against this before a credential moves; a branch outside it is refused
with the allowance in words, before anything is gated:

```tony
spawn:
  environment: verse-merge
  repos:
  - repo: signadot/verse
    from: main
    access: write
    push:
      new: ["verse/auto/*"]
      existing: [main]
```

The runner checks what the spawn asks before any work is set up: an environment it has, a network
its sandbox can build, repositories it can name, a driver it runs. A launch it will not host is
refused with the reason, rather than started and failed.

The credential a push is made with is the runner's own token, from `-git-token-env`. It is
**supplied by the operator and never minted**: a token a person made, with whatever the forge
let it do.

**Delivery is the agent's decision.** An agent that has the `push` tool commits what it means
to deliver and calls `push`, naming the branch, or naming none to push its own branch
`verse/auto/<task>` under that name; what it did not push does not leave, and it may push as
many times as the work needs. Only commits are delivered, and every one of them must end with
the provenance trailers (`Verse-Task`, `Verse-Principal`, `Verse-Subject`, `Verse-Chain`) that
the brief names. A commit without them is refused with what is missing, and the agent re-commits:
the runner never rewrites the agent's commits, so the agent knows the SHAs it delivered. A
program under `agents:` has no tools, so nothing it does leaves the workspace; what it did not
push is held in the runner's mirror. The checkout carries a git identity, so `git commit` needs
none from the agent: a person's task commits as `u@h (verse <driver>)` at
`u+verse-<driver>-runner@h`, an address that says in the forge's view what the Verse-Principal
trailer says in the message; a charter firing commits as the runner. Either way the push is an
ordinary action: the task's vector gates it, the
[observation plane](../../glossary.md#observation-plane) shows it, and a
[charter](../../glossary.md#charter) decides it in the same way:

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

**A pull request is a tool too.** `create-pr` opens one from a branch the task pushed into a base, with the title and body the task writes, and finds the open one rather than opening a second; `update-pr` changes the open one. Each derives a `create` or a `write` on a `pull` resource named `owner/name:base`, so a charter gates proposing into `main` apart from pushing. They are offered for a repository at GitHub that the task may push to. The runner makes the request with its own token, so the environment's egress needs no route to the forge for these either.

**Approval is what makes the runner push**, and nothing else does. The task holds no git
credential at any point: it asks, and the runner pushes from its mirror with its own token once
a gate says yes about that one push. If the gate refuses, nothing moves.

### Who enforces what

Four questions have four different answers:

| | enforced by |
|---|---|
| **whether** — may this happen at all | the vector, at the gate. Without approval there is no credential. |
| **which branches** — what a task may name | the repository's `push` allowance, checked by the runner before the credential moves. |
| **where** — which refs a credential may touch | the forge: token scope, and repository rules about who may push what. |
| **how** — fast-forward only | the allowance, which forces only what the spawn said, and **the receiving end**: `receive.denyNonFastForwards`, or the forge's equivalent rule. |

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
`receive.denyDeletes` blocks a sweep. A run that deletes its charter's stale branches needs a
remote that permits deletes under that pattern.

### The environment is the unit of narrowing

A task may reach what its environment declares, and nothing more. That is the whole
containment. It is not a rule that each charter must remember. Here are two environments over
one repository, from the deployment, and the spawns the comment-drift charters point at them:

| | `verse-fix` (drift.tony) | `verse-merge` (merge.tony) |
|---|---|---|
| the spawn's `push` | `new: ["verse/auto/*"]` | `existing: [main]`, `new: ["verse/auto/*"]` (deletable) |
| `push` in `tools:` | yes | yes |
| `fetch` in `tools:` | yes | yes |
| `run_bash` in `tools:` | yes, with `go` | yes, with `git` only |

`tools:` is the set a spawn gets when it names none. What a task may push is the
charter's, on its spawn, and the record shows it: a fix spawn permits its own branch and nothing
else, and a reader of merge.tony sees `main` named where it is reached. An environment that
leaves `push` out of its floor cannot deliver at all: the agent decides delivery, and an agent
without the tool decides nothing.

**A task here need not be an agent.** This runner builds `driver: calls` as well as `llm` and
the programs under `agents:`. A call list gets the same extras: the same `push` and the same
deriver, and a list that wants to deliver writes it. So a charter that knows the actions writes
them down, and pays for no model at all:

```tony
spawn:
  environment: verse-merge
  vector: auto
  calls:
  - tool: run_bash
    input:
      command: "git -C verse fetch origin $branch && git -C verse merge --ff-only FETCH_HEAD"
  - tool: push
    input: {branch: main}
```

Nothing downstream can tell which driver proposed the push. The push derives the same effect,
meets the same gate, and appears in the same way. For the driver itself, see
[tasks.md](../tasks.md#the-drivers). This example comes from the charter
`.verse/charters/comment-drift/merge.tony`.

**The task has no route to the forge.** The runner pushes from its own mirror, with its own
token, and opens pull requests with it, so an environment's egress allowlist never needs
`github.com`. The task's shell shares the task's network namespace, and `git push` or `gh` from
it has nowhere to go. The credential is never in the task process at all.

The commit carries what verse told the runner:

```
Verse-Task: run-8f30…
Verse-Principal: user:walter@example.com
Verse-Subject: demo.pr.1
Verse-Chain: review
```

An author line can be set by anyone with the checkout; these lines are the link back into verse
that someone outside can check.

### Work waits in the mirror

Before the runner tries any push, it stages the commit in its own mirror:

| Ref | When |
|---|---|
| `refs/pending/<task>/<repo>` | a push was approved |
| `refs/pending-force/<task>/<repo>` | a push that may rewrite its target was approved |
| `refs/held/<task>/<repo>` | the push was refused, or the task did not reach it |

The runner pushes from the mirror, and the remote says whether a push landed. The sweep asks the
remote at three times: when the task ends, at startup, and on the next spawn against that
repository.

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

An environment declares what the task may reach. The runner's sandbox builds that, or it
**refuses the launch**.

| Flag | What it provides |
|---|---|
| `-confine host` | almost nothing. It is what a laptop can honestly provide. |
| `-confine bwrap` | a private filesystem, and a network the environment chooses, on Linux |

An environment that lists `egress:` hosts asks for a **restricted network**. The sandbox
builds this by unsharing the network and binding in a socket to a proxy that applies the
allowlist (runnerd's egress). So it cannot run under `host`. The runner says so. It does not run
the task with a proxy that the task is free to ignore.

`GET /runner` reports the sandbox **this deployment** runs tasks in, and the networks it builds.

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

### The runner performs, the task asks

The channel carries three acts the runner performs: push a branch or delete one, open or update
a pull request, fetch. The vocabulary is closed, because the spawn's allowance is: what a run
may do to a repository is push to a branch it may push to, and a pull request from one. So the
runner can hold the credential and never hand it over. The task names the branch, the runner
checks it, stages the commit, and pushes from its own mirror. A merge is still the task's own
git in its own checkout; only the push that lands it is the runner's.

### Verse and git server configuration

The rules of a repository about its own history do not belong to verse. People push to these
repositories too. The receiver enforces an invariant for everyone who pushes. Verse could
enforce it only for the actors that go through verse. That set is smaller, and it is not the
set that causes concern.

A refusal from the remote is an authority that verse deliberately does not hold. The remote
uses it. So verse treats the refusal as an answer, and does not retry.

### One environment for each purpose

The two environments could be one environment with one wider allowance and one more tool. The
cost would then move to the charters. Charters that share an environment would each have to
narrow their own `tools:`. That is a rule that somebody forgets, and not a property that
something enforces.

### A call list instead of an agent

A call list is cheap, because nothing downstream can tell it from an agent. A charter that
knows its actions pays for no model.

### The io does not write

The io is never handed a store. Verse, not the io, converges the store with the io's answer.
This keeps a source short enough to be worth writing.
