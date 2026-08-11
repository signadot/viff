# Examples

Working programs you can copy and change. They compile with the tree, which is the point: an
example in a fenced block goes stale the first time a signature moves and nobody finds out.

| | |
|---|---|
| [runner/](runner/main.go) | a verse runner: the smallest thing that serves the task-control contract, with the two functions a drive changes marked |
| [coderunner/](coderunner/main.go) | a runner that codes: a workspace of git checkouts per task, an LLM loop or an external program in it, and the runner — not the agent — pushing the result |
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
somewhere its result goes, plus which driver runs the work — and a spawn names one:

```sh
verse runner add pool:code http://localhost:9802
verse task run --runner runners:pool:code --environment local --as "$USER" "add a test for firstLine"
```

Each task gets its own working directory holding **every repository the environment names**,
each cloned off a shared bare mirror, all on the same `agent/<task>` branch, removed when
the task ends (`-keep` holds onto it):

```
<workspace>/verse/         ← being changed;  push: refs/heads/agent/$task
<workspace>/tony-format/   ← there to be read; no push declared
```

That is a list rather than one repo because the work usually is: a change to a service and the
library it consumes, a generator and the code it generates. Side by side under one root, they
are one task rather than two that have to be coordinated — and `push` being **per repository**
is what keeps a reference tree read-only.

Where a repository declares a `push`, the **runner** commits and pushes it when the task
finishes — the task never does, because the whole point is that it cannot reach the credential.
The commit carries what verse told the runner:

```
Verse-Task: run-8f30…
Verse-Principal: scott
Verse-Subject: demo:pr:1
Verse-Chain: review
```

which is the only externally checkable link back into the verse when the push lands under a
shared bot identity.

`GET /runner` says what this runner is, what drivers it hosts and what containment it can
build. It is a stub — nothing consumes it yet — but the containment it reports is true: this
example provides only the weakest rung of each class, and **refuses** any environment asking
for more rather than running the work less contained than it said.

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
