# Build a runner and an io

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
| A, in Go | a `Builder`, a driver and an executor, on the package `task/taskd` | `taskd` serves every route of the contract |
| B, in any language | an HTTP server | nothing. You serve every route yourself. |

The sample, [examples/runner/main.go](../reference/examples/runner/main.go), is way A. It
compiles with the tree.

### Way A: the smallest program

The lines of the sample that matter are below. The file has the rest.

```go
func main() {
	// …
	d := taskd.New(build)
	// …
	if err := http.ListenAndServe(*addr, d.Handler()); err != nil {
		log.Fatalf("runner: %v", err)
	}
}

func build(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, reg)
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
| `taskd.New(build)` | makes a daemon that hosts tasks. The daemon calls `build` once for each spawn. |
| `d.Handler()` | the HTTP routes of the contract |
| `build` | a `taskd.Builder`. It makes one task from one `task.Launch`. The launch carries what the spawn said: the prompt, the driver, the frame, the tools, the vector and the grant. |
| `task.NewSupervisor` | decides what happens at a gate of the task. With `task.Auto` every action runs. With `task.Ask` every action raises a gate, which a person answers with `verse gate`. |
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
| `GET /tasks` | none | `200` and a list of `verse-instance`, one for each task the runner hosts |
| `GET /tasks/{id}` | none | `200` and one `verse-instance` |
| `GET /tasks/{id}/events?since=N` | none | the event stream of the task, as server-sent events. Each event holds one frame, a `verse-frame`. |
| `POST /tasks` | `verse-runner-spawn` | `201` and a `verse-runner-spawned` when the runner built the task. `200` and the same body when the task was already running. `400` when the body does not parse, or when the runner will not host the task. |
| `POST /tasks/{id}/resolve` | `verse-resolve` | `204` when applied. `404` when the task has no such open question. |
| `POST /tasks/{id}/say` | `verse-runner-say` | `204` when applied |
| `POST /tasks/{id}/cancel` | `verse-runner-cancel`, or no body | `204` when applied |
| `POST /tasks/{id}/pause` | none | `204` when applied |
| `POST /tasks/{id}/resume` | none | `204` when applied |
| `PUT /tasks/{id}/vector` | `verse-runner-vector` | `204` when applied |

Every route with an `{id}` answers `404` for a task that the runner does not host.

`verse schema <name>` prints one type.

```sh
verse schema verse-runner-spawn
```

```
verse-runner-spawn:
  answer: .[tony-base:ir]
  calls: .[array(verse-call)]
  converse: .[bool]
  driver: .[string]
  environment: .[string]
  frame: .[string]
  grant: .[nullable(verse-grant)]
  id: .[string]
  occurrence: .[int]
  prompt: .[string]
  provenance: .[nullable(verse-runner-provenance)]
  retain: .[int]
  tools: .[array(string)]
  vector: .[nullable(verse-vector)]
  virtualKey: .[string]
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
verse task run -runner sample -wait "fetch the data; train the model"
```

```
at: verse.runners.sample
reach: "a runner"
```

```
at: verse.runners.sample
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

On way A, `taskd` does the first five for you. The other four are yours on both ways.

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
6. **`virtualKey` is the model credential itself.** In the `spawn` of a rule the field
   holds an address. In the launch that a runner receives, it holds the key. Use the key
   for that task only, and do not write it to a log.
7. **The runner enforces an answer contract, or it refuses the spawn.** `answer` is the
   shape that the last message of the task must have
   ([the answer contract](../reference/action.md#the-answer-contract)).
8. **The runner refuses a spawn that names what it does not have.** The name is an
   `environment`, or a tool in `tools`.
9. **A failure sets `err`, and not `result`.** `result` is the answer of a task that ended
   with `done`.

The sample does not do 7 and 8. Its `build` reads the id and the prompt of the launch and
no other field. Add the refusals before you deploy a copy of it.

Verse has no conformance test that a new runner can run against itself. There is one
exception, for Go. The package `task/tasktest` tests a Go type that produces a
`task.Instance`. It tests four properties.

- The snapshot reports the driver.
- A failure is an `err`.
- A completion is a `result`.
- The reason of a gate reaches the snapshot.

## Write an io

An io is a program between verse and one external system, such as a git repository. You
deploy an io and you install it. An io has two halves.

| Half | What it does | Which side opens the connection |
|---|---|---|
| in | reflects the external system into a place in the store | verse |
| out | acts on the external system | the io |

The io owns its place and is the only writer of it. This recipe builds the in half, which
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
| `Root` | the root of the io: the first segment of every address the io writes. The sample returns `git`. |
| `Reflect` | a `Reflection`: what the external system holds at this moment. A zero `ref` asks for everything the io owns. A `ref` with segments asks for one place, and an answer with everything is correct for it too. |

The sample delegates to `verseio.Git`, which reads a git repository on disk. It also has a
third method, `Owns`, which returns the claims of the io: what only this io may write. To reflect a system of your
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
| `GET /io` | `{root, owns}`: what the program is. `verse io add` and `verse io check` ask it. |
| `GET /reflect[?at=<address>]` | a `Reflection`. With no `at`, the answer is for everything the io owns. `iod` answers `500` when `Reflect` returns an error. |

Verse asks for a full reflection when you install the io, and again every minute. The io
may also send verse a **hint**, which says which addresses went stale. Verse reflects
those addresses again at once.

```
POST /io/{name}/hint?at=<address>
```

`{name}` is the name the io was installed under. The parameter is `at`, and it may repeat.
A hint carries no data, and verse answers `204`. A hint with no `at` asks for a full
reflection. Verse ignores a parameter of another name, so a hint with `?ref=` is a full
reflection too.

### Install it

Start the sample in a terminal of its own. `-repo verse=.` reflects the repository in the
current directory under the name `verse`.

```sh
go run ./docs/reference/examples/io -addr 127.0.0.1:9811 -repo verse=.
```

Then install the io, check it, and read what it reflected.

```sh
verse io add sample http://127.0.0.1:9811 -root git
verse io check sample
verse data git verse head
```

```
at: verse.io.sample
owns:
- at: git.verse
reach: "a source"
```

```
at: verse.io.sample
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

[Ios](../reference/io.md) has every command, and
[the contract an io serves](../reference/io.md#the-contract-an-io-serves) has the form of a
reflection on the wire.

### What an io must do that the compiler does not check

1. **`Covers` claims no more than the answer is complete for.** Verse removes every stored
   entity under a cover that the answer does not hold. If the io sees half of a system, it
   covers that half.
2. **A read that failed is an error.** It is never an empty answer. An empty answer with
   covers tells verse that the external system is empty, and verse removes what was there.
3. **One io for one place in one scope.** The install compares the claim of the io with the
   claim of every installed io in the same scope. It refuses an install that it cannot prove
   separate, with `409` and both claims in the message.
4. **The root you name is the root the io says.** Verse refuses
   `verse io add … -root github` for the sample with `400`, because the sample says `git`.
5. **A reflection passes `Reflection.Check`.** Verse runs the check before it writes
   anything, and it refuses the whole answer for one fault.

`Reflection.Check` refuses these.

| What the reflection holds | Why verse refuses it |
|---|---|
| a cover with no path | the empty path is the root, so the cover claims everything |
| a cover whose first segment is `*` or an operator | the cover claims every place |
| a cover whose first segment is not the root of the io | an io may not remove what another io wrote |
| a cover with an `op` | a cover selects stored entities, and they have no `op` |
| an entity under `verse` | no io reflects the records that verse keeps about itself |
| an entity outside the root of the io | the io does not own that place |
| an entity whose address is the root alone | a reflection writes into the place of the io, and not over it |

### The out half

The out half is a *sink*, and it works in the other direction. Somebody writes an input,
which is a node with a `to` field, under a place that the io owns. The io reads the input,
makes the external system match it, and writes what happened beside it, at
`<name>~status`. In Go, a sink is a `Sink` with one method, `Deliver`. Use a sink for an effect that an edit can correct, such
as a message or a comment. Use a task for an effect that nothing can take back, such as a
push. [The out half](../io.md#the-out-half-verse-asks-the-io-does-it) describes the model, and
[the reference](../reference/io.md#the-out-half) describes the registration and its grants.
