# Build a runner and an iod

This page has two recipes for a developer who extends verse: a runner and an io.

Each recipe needs a verse on your own machine, started as
[Before you start](../build.md#before-you-start) says.

## Write a runner

A runner is a program that runs tasks for verse. Verse reaches a runner at a URL, and
drives every task on it over HTTP. The routes and their bodies are the *contract* of a
runner.

### Two ways to write one

| Way | What you write | What you get |
|---|---|---|
| A, in Go | a `Builder`, a driver and an executor, on the package `runner/runnerd` | `runnerd` serves every route of the contract |
| B, in any language | an HTTP server | nothing. You serve every route yourself. |

The sample, [examples/runner/main.go](../reference/examples/runner/main.go), is way A. It
compiles with the tree.

### Way A: the smallest program

The lines of the sample that matter are below. The file has the rest.

```go
func main() {
	// …
	d := runnerd.NewInProcessHandler(build)
	d.Describe(runner.Description{Offers: []runner.Offer{{
		Agent: "sample",
		With:  map[string]runner.Accepts{"network": runner.AcceptsOneOf(runner.NetworkOpen)},
	}}})
	if err := http.ListenAndServe(*addr, d.HTTPHandler()); err != nil {
		log.Fatalf("runner: %v", err)
	}
}

func build(l runner.Launch, reg *task.Registry) *task.Task {
	sup := task.NewSupervisor(l.Policy, reg)
	return task.NewTask(l.ID, sup, &planDriver{prompt: l.Prompt}, &echoExecutor{})
}

func (d *planDriver) Next(ctx context.Context, prev task.StepResult) (task.ToolUse, bool, error) {
	// …
}

func (echoExecutor) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	// …
}
```

| Part | What it does |
|---|---|
| `runnerd.NewInProcessHandler(build)` | makes a task handler that runs tasks in this program's process, with no sandbox. It calls `build` once for each spawn. (`runnerd verse` uses a `runnerd.SubprocessHandler` instead, which runs each task in a sandboxed process of its own.) |
| `d.Describe(…)` | what the runner offers, which `GET /describe` answers: one agent, `sample`. Each offer says which networks it builds under `with.network`. The sample runs its tasks unconfined, so it builds only `open`. Verse does not install a runner that offers nothing, or whose offer leaves out its networks. |
| `d.HTTPHandler()` | the HTTP routes of the contract, and `GET /describe` |
| `build` | a `runnerd.Builder`. It makes one task from one `runner.Launch`. The launch carries what the spawn said: the prompt, the doer and what it works with, and the policy. |
| `task.NewSupervisor` | decides what happens before each action of the task. `l.Policy` is the launch's policy: a verb no row names is refused, the first row that matches a step denies it, asks about it or allows it, and the last row decides the calls of named verbs no row above matched. A person answers an ask with `verse question`. An action that is no step (`task.StepOf`), such as the sample's `do`, asks nobody. |
| `Next` | the driver. It returns the next action and `true`, or `false` when the work is done. An error fails the task. |
| `Execute` | the executor. It carries out one action. The driver receives its `StepResult` as `prev` at the next call. |

You change the driver and the executor. The driver of the sample splits the prompt at each
`;`, and its executor prints each part.

A `Builder` returns no error. To refuse a launch, return
`task.NewFailed(l.ID, reg, "<reason>")`, which is a task that fails at once with that reason.

### Way B: the routes

`{id}` is the id of a task. The body types are names in the schema of verse.

| Method and path | Body | What to answer |
|---|---|---|
| `GET /describe` | none | `200` and what the runner offers: its agents and whether it runs scripts, what each takes under `with` (the networks it builds among them), and the systems each intercepts, with the ceiling over each. Verse checks a spawn against it. |
| `GET /tasks` | none | `200` and a list of `verse-task-snapshot`, one for each task the runner hosts |
| `GET /tasks/{id}` | none | `200` and one `verse-task-snapshot` |
| `GET /tasks/{id}/events?since=N` | none | the event stream of the task, as server-sent events. Each holds one event, a `verse-task-event`. |
| `POST /tasks` | `verse-runner-launch` | `201` and the task's `verse-task-snapshot` when the runner built the task. `200` and the same when the task was already running. `400` when the body does not parse, or when the runner will not host the task, with the reason. |
| `POST /tasks/{id}/resolve` | `verse-runner-resolve` | `204` when applied. `404` when the task has no such open question. |
| `POST /tasks/{id}/say` | `verse-runner-say` | `204` when applied. `410` when the task has ended, since nothing said to it would be heard. |
| `POST /tasks/{id}/cancel` | `verse-runner-cancel`, or no body | `204` when applied |
| `POST /tasks/{id}/pause` | none | `204` when applied |
| `POST /tasks/{id}/resume` | none | `204` when applied |
| `PUT /tasks/{id}/policy` | `verse-runner-policy` | `204` when applied |

Every route with an `{id}` answers `404` for a task that the runner does not host.

`verse schema <name>` prints one type.

```sh
verse schema verse-runner-launch
```

```
verse-runner-launch:
  answer: .[tony-base:ir]
  converse: .[bool]
  credential: .[string]
  doer: !verse-runner-doer null        # {agent | script, with: {git, git-issue, network, env, model, effort}, annotations}
  id: .[string]
  occurrence: .[int]
  policy: !irtype {}                   # the task's policy, its rows {name, match, then}; absent gives the task no verb
  prompt: .[string]
  provenance: !verse-runner-provenance null
  timeout: .[string]
```

Verse sends every body as tony, with `Content-Type: application/tony`. A tony parser reads
JSON, and a JSON parser does not read tony. So a runner in another language needs a tony
parser. Verse reads an answer in the format that the `Content-Type` of the answer names,
and JSON is one of them. Verse reads the frames of an event stream as tony, unless the
response names another format in the header `X-Verse-Format`.

[The verse-facing contract](../task-control.md#the-verse-facing-contract) describes the routes,
and [the bodies](../task-control.md#the-bodies) describes each field of a launch.

### Install it and run a task

Start the sample in a terminal of its own.

```sh
go run ./docs/reference/examples/runner -addr 127.0.0.1:9810
```

Then install the runner, check it, and run a task on it.

```sh
verse runner add sample http://127.0.0.1:9810
verse runner check sample
verse task run -runner sample -agent sample -wait "fetch the data; train the model"
```

```
at: verse.runners.sample
offers:
- agent: sample
  with:
    network:
    - open
reach: "a runner"
```

```
at: verse.runners.sample
offers:
- agent: sample
  with:
    network:
    - open
reach: "a runner"
url: http://127.0.0.1:9810
```

```
run-37716c60f6ce
run-37716c60f6ce done
```

The terminal of the sample prints the two steps:

```
runner: fetch the data
runner: train the model
```

`verse runner add` asks the URL for `GET /tasks` before it installs. `reach` is the answer:
`a runner`, `not a runner` or `unreachable`. [Runners](../reference/runners.md) has every
command and what each refuses.

### What a runner must do that the compiler does not check

On way A, `runnerd` does the first five for you. The other four are yours on both ways.

1. **A spawn is idempotent by id while the task runs.** A second spawn of a live id answers
   `200` with the task that runs, and starts nothing. A spawn of an id whose task has ended
   is a new run.
2. **The runner refuses a body that does not parse**, with `400`. A runner that spawns
   from such a body runs a task with no prompt.
3. **The terminal status is the last frame of the event stream.** The runner closes the
   stream after it. Verse reads a stream that ends with no terminal status as a broken
   connection.
4. **`?since=N` replays, and then goes live.** The runner sends every retained frame with a
   cursor above N, in order, and then the live frames. When N is below the retained frames,
   the runner sends one `gap` frame with the current cursor first.
   [`since` semantics](../task-control.md#since-semantics-and-the-two-restarts) has the detail.
5. **A control call says what became of it.** It answers `204` when the runner applied it,
   and `404` for a task that the runner does not host.
6. **The doer's annotation `verse.portkey/virtual-key` is the model credential itself.** In
   the `spawn` of a rule, `virtualKey` holds an address. In the launch that a runner receives,
   the annotation holds what the address resolved to. Use it for that task only, and do not
   write it to a log.
7. **`credential` is a token for the task, and the runner hands it over as a file.** Verse
   signed it for this task when the rule's gate passed
   ([a credential for what a gate let through](../reference/gates.md#a-credential-for-what-a-gate-let-through)).
   Write it to a file outside the task's workspace, readable by the task's user alone, and set
   `VERSE_CREDENTIAL_FILE` in the task's shell to the file's path. Do not put the token in the
   environment or in a log, and remove the file when the task ends. A runner that drops the
   field runs the task without its credential, and the task's promotion is refused.
8. **The runner enforces an answer contract, or it refuses the spawn.** `answer` is the
   shape that the last message of the task must have
   ([the answer contract](../reference/action.md#the-answer-contract)).
8. **The runner refuses a spawn that names what it does not have.** An agent it does not
   offer, a model or effort that agent does not take, a repository on a forge it has none
   on, a policy row that opens what its ceilings close, a row on a system it does not hold, a
   network it does not build. `GET /describe` says what it has.
9. **A failure sets `err`, and not `result`.** `result` is the answer of a task that ended
   with `done`.

The sample does not do 7 and 8. Its `build` reads the id, the prompt and the policy of the
launch and no other field. Add the refusals before you deploy a copy of it.

Verse has no conformance test that a new runner can run against itself. There is one
exception, for Go. The package `task/tasktest` tests a Go type that produces a
`task.Snapshot`. It tests four properties.

- The snapshot reports the doer.
- A failure is an `err`.
- A completion is a `result`.
- The reason of a gate reaches the snapshot.

## Write an iod

An iod is a program between verse and one external system, such as a git repository. You
deploy an iod and you install it. An iod has two halves.

| Half | What it does | Which side opens the connection |
|---|---|---|
| in | reflects the external system into a place in the store | verse |
| out | acts on the external system | the iod |

The iod owns its place and is the only writer of it. This recipe builds the in half, which
is a *source*: the Go interface `Source`.

### The smallest source

The sample is [examples/io/main.go](../reference/examples/io/main.go). These are its two
methods.

```go
type demo struct{ git *verseio.Git }

func (d *demo) Root() string { return d.git.Root() }

func (d *demo) Reflect(ctx context.Context, ref entity.Ref) (verseio.Reflection, error) {
	return d.git.Reflect(ctx, ref)
}
```

| Method | What it returns |
|---|---|
| `Root` | the root of the iod: the first segment of every address the iod writes. The sample returns `git`. |
| `Reflect` | a `Reflection`: what the external system holds at this moment. A zero `ref` asks for everything the iod owns. A `ref` with segments asks for one place, and an answer with everything is correct for it too. |

The sample delegates to `verseio.Git`, which reads a git repository on disk. It also has a
third method, `Owns`, which returns the claims of the iod: what only this iod may write. To reflect a system of your
own, write the body of `Reflect`. The sample holds this sketch.

```go
func (d *demo) Reflect(ctx context.Context, ref entity.Ref) (verseio.Reflection, error) {
    issues, err := d.api.ListIssues(ctx, project)
    if err != nil {
        return verseio.Reflection{}, err          // an error, NEVER an empty reflection
    }
    cover := verseio.Cover("linear", "issue", "*")
    cover.Predicate = entity.Obj{"project": project}.Payload() // say what you are complete FOR
    rn := verseio.Reflection{Covers: []entity.Pattern{cover}}
    for _, i := range issues {
        rn.Add(entity.RefAt("linear", "issue", i.ID),
            entity.Obj{"project": project, "title": i.Title, "stage": i.State})
    }
    return rn, nil
}
```

A `Reflection` has two fields.

| Field | What it holds |
|---|---|
| `Entities` | the entities that the external system holds |
| `Covers` | the covers: the patterns under which the answer is complete. Verse removes a stored entity that a cover reaches and that `Entities` does not hold. |

A source has no access to the store. Verse compares the answer with the store and writes
the difference.

### Serve it

`iod.New(src).Handler()`, from the package `io/iod`, serves the two routes that verse
dials.

| Route | What it answers |
|---|---|
| `GET /io` | `{root, owns}`: what the program is. `verse iod add` and `verse iod check` ask it. |
| `GET /reflect[?at=<address>]` | a `Reflection`. With no `at`, the answer is for everything the iod owns. `iod` answers `500` when `Reflect` returns an error. |

Verse asks for a full reflection when you install the iod, and again every minute. The iod
may also send verse a **hint**, which says which addresses went stale. Verse reflects
those addresses again at once.

```
POST /io/{name}/hint?at=<address>
```

`{name}` is the name the iod was installed under. The parameter is `at`, and it may repeat.
A hint carries no data, and verse answers `204`. A hint with no `at` asks for a full
reflection. Verse ignores a parameter of another name, so a hint with `?ref=` is a full
reflection too.

### Install it

Start the sample in a terminal of its own. `-repo verse=.` reflects the repository in the
current directory under the name `verse`.

```sh
go run ./docs/reference/examples/io -addr 127.0.0.1:9811 -repo verse=.
```

Then install the iod, check it, and read what it reflected.

```sh
verse iod add sample http://127.0.0.1:9811 -root git
verse iod check sample
verse data git verse head
```

```
at: verse.iod.sample
owns:
- at: git.verse
reach: "a source"
```

```
at: verse.iod.sample
reach: "a source"
says:
  owns:
  - at: git.verse
  root: git
url: http://127.0.0.1:9811
```

```
dirty: true
ref: verse/build-on
repo: verse
sha: 10fe682272161438fd6b4872832315bffed3af1d
```

Your output names your own branch and commit. `verse data git verse ref` prints the
branches. A hint is one request:

```sh
curl -i -X POST "http://$VERSE_ADDR/io/sample/hint?at=git.verse.head"
```

```
HTTP/1.1 204 No Content
…
```

[Iods](../reference/io.md) has every command, and
[the contract an iod serves](../reference/io.md#the-contract-an-iod-serves) has the form of a
reflection on the wire.

### What an iod must do that the compiler does not check

1. **`Covers` claims no more than the answer is complete for.** Verse removes every stored
   entity under a cover that the answer does not hold. If the iod sees half of a system, it
   covers that half.
2. **A read that failed is an error.** It is never an empty answer. An empty answer with
   covers tells verse that the external system is empty, and verse removes what was there.
3. **One iod for one place in one scope.** The install compares the claim of the iod with the
   claim of every installed iod in the same scope. It refuses an install that it cannot prove
   separate, with `409` and both claims in the message.
4. **The root you name is the root the iod says.** Verse refuses
   `verse iod add … -root github` for the sample with `400`, because the sample says `git`.
5. **A reflection passes `Reflection.Check`.** Verse runs the check before it writes
   anything, and it refuses the whole answer for one fault.

`Reflection.Check` refuses these.

| What the reflection holds | Why verse refuses it |
|---|---|
| a cover with no path | the empty path is the root, so the cover claims everything |
| a cover whose first segment is `*` or an operator | the cover claims every place |
| a cover whose first segment is not the root of the iod | an iod may not remove what another iod wrote |
| a cover with an `op` | a cover selects stored entities, and they have no `op` |
| an entity under `verse` | no iod reflects the records that verse keeps about itself |
| an entity outside the root of the iod | the iod does not own that place |
| an entity whose address is the root alone | a reflection writes into the place of the iod, and not over it |

### The out half

The out half is a *sink*, and it works in the other direction. Somebody writes an input,
which is a node with a `to` field, under a place that the iod owns. The iod reads the input,
makes the external system match it, and writes what happened beside it, at
`<name>~status`. In Go, a sink is a `Sink` with one method, `Deliver`. Use a sink for an effect that an edit can correct, such
as a message or a comment. Use a task for an effect that nothing can take back, such as a
push. [The out half](../io.md#the-out-half-verse-asks-the-iod-does-it) describes the model, and
[the reference](../reference/io.md#the-out-half) describes the registration and its grants.
