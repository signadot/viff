# Build a runner and an iod

This page has two recipes for a developer who extends viff: a runner and an iod.

Each recipe needs a viff on your own machine, started as
[Before you start](../build.md#before-you-start) says.

## Write a runner

A runner is a program that runs tasks for viff. Viff reaches a runner at a URL, and
drives every task on it over HTTP. The routes and their bodies are the *contract* of a
runner.

### Two ways to write one

| Way | What you write | What you get |
|---|---|---|
| A, in Go | a `Builder`, a driver and an executor, on the package `pkg/runner/runnerd` | `runnerd` serves every route of the contract |
| B, in any language | an HTTP server | nothing. You serve every route yourself. |

The sample, [examples/runner/main.go](../reference/examples/runner/main.go), is way A. It
compiles with the tree.

### Way A: the smallest program

The lines of the sample that matter are below. The file has the rest.

```go
func main() {
	// …
	d := runnerd.NewInProcessHandler(build)
	d.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{{
		Agent: &runner.AgentOffer{Name: "sample"},
		Systems: map[string]runner.System{"network": {
			Builds: systems.Ceiling{{Attributes: map[string][]string{"mode": {runner.NetworkOpen}}}},
		}},
	}}})
	guard, err := runnerd.NewGuard(runnerd.Viff{URL: *viffURL}, *tokenFile)
	// …
	go guard.Announce(context.Background(), log.Printf)
	if err := http.ListenAndServe(*addr, guard.Wrap(d.HTTPHandler())); err != nil {
		log.Fatalf("runner: %v", err)
	}
}

func build(l runner.Launch, reg *task.Registry) *task.Task {
	sup := task.NewSupervisor(l.Environment, l.Autonomy, reg)
	sup.SetCeilings(map[string]systems.Ceiling{"commands": systems.Open()})
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
| `runnerd.NewGuard(…)`, `guard.Wrap`, `guard.Announce` | proves the runner to the one viff it serves, with the token in `-token-file`: it answers the token on `GET /describe` to that viff, takes only that viff's tokens on every other route, and tells the viff it is here. The registration names the token by its hash (`viff runner add -token-file`). A runner is never taken at its word, so a runner without one cannot be registered. |
| `runnerd.NewInProcessHandler(build)` | makes a task handler that runs tasks in this program's process, with no sandbox. It calls `build` once for each spawn. (`runnerd` uses a `runnerd.SubprocessHandler` instead, which runs each task in a sandboxed process of its own.) |
| `d.Describe(…)` | what the runner offers, which `GET /describe` answers, in `version: 2`: one agent, `sample`, taking no settings. Each offer says which networks it builds, under its network's `builds`. The sample runs its tasks unconfined, so it builds only `open`. Viff does not install a runner that offers nothing, or whose offer leaves out what network it builds. |
| `d.HTTPHandler()` | the HTTP routes of the contract, and `GET /describe` |
| `build` | a `runnerd.Builder`. It makes one task from one `runner.Launch`. The launch carries what the spawn said: the prompt or the script, the agent and its settings, the environment, and the autonomy. |
| `task.NewSupervisor` | decides what happens before each action of the task. A step outside `l.Environment` is refused. Within it, `l.Autonomy`'s first row that matches the step denies it, asks about it or allows it, and the last row decides what no row above matched. A person answers an ask with `viff question`. An action that is no step (`task.StepOf`), such as the sample's `do`, asks nobody. `SetCeilings` gives the runner's own line over each system, read after the autonomy: a system the runner states no ceiling for is closed, so a runner says what it leaves open (`systems.Open()`), as the sample does for `commands`. |
| `Next` | the driver. It returns the next action and `true`, or `false` when the work is done. An error fails the task. |
| `Execute` | the executor. It carries out one action. The driver receives its `StepResult` as `prev` at the next call. |

You change the driver and the executor. The driver of the sample splits the prompt at each
`;`, and its executor prints each part.

A `Builder` returns no error. To refuse a launch, return
`task.NewFailed(l.ID, reg, "<reason>")`, which is a task that fails at once with that reason.

### Way B: the routes

`{id}` is the id of a task. The body types are names in the schema of viff.

| Method and path | Body | What to answer |
|---|---|---|
| `GET /describe` | none | `200` and what the runner offers: its agents, with the settings each takes, and whether it runs scripts, and the systems each holds, with what the runner builds of each (`builds`, the network's modes) and the ceiling over each. Viff checks a spawn against it. |
| `GET /tasks` | none | `200` and a list of `viff-task-snapshot`, one for each task the runner hosts |
| `GET /tasks/{id}` | none | `200` and one `viff-task-snapshot` |
| `GET /tasks/{id}/events?since=N` | none | the event stream of the task, as server-sent events. Each holds one event, a `viff-task-event`. Answered at once, and each one answered renews [the task's lease](../runners.md#a-tasks-lease). |
| `POST /tasks` | `viff-runner-launch` | `201` and the task's `viff-task-snapshot` when the runner built the task. `200` and the same when the task was already running. `400` when the body does not parse, or when the runner will not host the task, with the reason. |
| `POST /tasks/{id}/resolve` | `viff-runner-resolve` | `204` when applied. `404` when the task has no such open question. |
| `POST /tasks/{id}/say` | `viff-runner-say` | `204` when applied. `410` when the task has ended, since nothing said to it would be heard. |
| `POST /tasks/{id}/cancel` | `viff-runner-cancel`, or no body | `204` when applied |
| `POST /tasks/{id}/pause` | none | `204` when applied |
| `POST /tasks/{id}/resume` | none | `204` when applied |
| `PUT /tasks/{id}/autonomy` | `viff-runner-autonomy` | `204` when applied |

Every route with an `{id}` answers `404` for a task that the runner does not host.

`viff schema <name>` prints one type.

```sh
viff schema viff-runner-launch
```

```
viff-runner-launch:
  agent: .[nullable(viff-runner-agent)]   # {name, <setting>: …, annotations}; viff always names it for a prompt
  answer: .[tony-base:ir]
  autonomy: !irtype {}                 # the rows {name, match, then}; absent asks about every step
  converse: .[bool]
  credential: .[string]
  environment: !irtype {}              # {vars, systems}: what the task may reach
  id: .[string]
  occurrence: .[int]
  person: .[string]                    # reserved; refused
  prompt: .[string]                    # a prompt, or
  provenance: !viff-runner-provenance null
  script: .[string]                    # a script
  timeout: .[string]
```

Viff sends every body as tony, with `Content-Type: application/tony`. A tony parser reads
JSON, and a JSON parser does not read tony. So a runner in another language needs a tony
parser. Viff reads an answer in the format that the `Content-Type` of the answer names,
and JSON is one of them. Viff reads the frames of an event stream as tony, unless the
response names another format in the header `X-Viff-Format`.

[The viff-facing contract](../task-control.md#the-viff-facing-contract) describes the routes,
and [the bodies](../task-control.md#the-bodies) describes each field of a launch.

### Install it and run a task

Start the sample in a terminal of its own, with a static token it proves itself with.
`-viff-url` names the viff it serves, `http://localhost:9797` by default, spelled as `viff up`
prints its issuer: a runner told `127.0.0.1` refuses every token a `localhost` viff signs.

```sh
openssl rand -hex 32 > runner.token
go run ./docs/reference/examples/runner -addr 127.0.0.1:9810 -token-file runner.token
```

Then install the runner, check it, and run a task on it.

```sh
viff runner add sample http://127.0.0.1:9810 -token-file runner.token
viff runner check sample
echo '{agent: {name: sample}, prompt: "fetch the data; train the model", environment: {systems: {network: {mode: open}}}}' |
  viff task run -f - -wait
```

```
at: mirror.runners.sample
offers:
- agent:
    name: sample
  systems:
    network:
      builds:
        mode:
        - open
reach: "a runner"
```

```
at: mirror.runners.sample
offers:
- agent:
    name: sample
  systems:
    network:
      builds:
        mode:
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

`viff runner add` asks the URL for `GET /tasks` before it installs. `reach` is the answer:
`a runner`, `not a runner` or `unreachable`. [Runners](../reference/runners.md) has every
command and what each refuses.

### What a runner must do that the compiler does not check

On way A, `runnerd` does the first five for you, and its guard the tenth. The others are yours
on both ways.

1. **A spawn is idempotent by id while the task runs.** A second spawn of a live id answers
   `200` with the task that runs, and starts nothing. A spawn of an id whose task has ended
   is a new run.
2. **The runner refuses a body that does not parse**, with `400`. A runner that spawns
   from such a body runs a task with no prompt.
3. **The terminal status is the last frame of the event stream.** The runner closes the
   stream after it. Viff reads a stream that ends with no terminal status as a broken
   connection.
4. **`?since=N` replays, and then goes live.** The runner sends every retained frame with a
   cursor above N, in order, and then the live frames. When N is below the retained frames,
   the runner sends one `gap` frame with the current cursor first.
   [`since` semantics](../task-control.md#since-semantics-and-the-two-restarts) has the detail.
5. **A control call says what became of it.** It answers `204` when the runner applied it,
   and `404` for a task that the runner does not host.
6. **The agent's annotation `viff.portkey/virtual-key` is the model credential itself.** In
   the `spawn` of a rule, `virtualKey` holds an address. In the launch that a runner receives,
   the annotation holds what the address resolved to. Use it for that task only, and do not
   write it to a log.
7. **`credential` is a token for the task, and the runner hands it over as a file.** Viff
   signed it for this task when the rule's gate passed
   ([a credential for what a gate let through](../reference/gates.md#a-credential-for-what-a-gate-let-through)).
   Write it to a file outside the task's workspace, readable by the task's user alone, and set
   `VIFF_CREDENTIAL_FILE` in the task's shell to the file's path. Do not put the token in the
   environment or in a log, and remove the file when the task ends. A runner that drops the
   field runs the task without its credential, and the task's promotion is refused.
8. **The runner enforces an answer contract, or it refuses the spawn.** `answer` is the
   shape that the last message of the task must have
   ([the answer contract](../reference/action.md#the-answer-contract)).
8. **The runner refuses a spawn that names what it does not have.** An agent it does not
   offer, a setting that agent does not take, an environment beyond its ceilings or what it
   builds (a repository on a forge it has none on, a system it does not hold, a network it
   does not build), and one missing a step its doer requires. `GET /describe` says what it has.
9. **A failure sets `err`, and not `result`.** `result` is the answer of a task that ended
   with `done`.
10. **The runner proves itself to its viff.** On `GET /describe` from its viff it answers its
   own token in `Viff-Runner-Token`; on every other route it takes only a bearer its viff
   signed for it; and its requests to viff carry its token. Viff refuses a registration that
   names no `subject`, so a runner that cannot prove itself is never registered
   ([who the runner is](../runners.md#who-the-runner-is)).

The sample does not do 7 and 8. Its `build` reads the id, the prompt, the environment and the autonomy of
the launch and no other field. Add the refusals before you deploy a copy of it.

Viff has no conformance test that a new runner can run against itself. There is one
exception, for Go. The package `pkg/task/tasktest` tests a Go type that produces a
`task.Snapshot`. It tests four properties.

- The snapshot reports the agent.
- A failure is an `err`.
- A completion is a `result`.
- The reason of a gate reaches the snapshot.

## Write an iod

An iod is a program between viff and one external system, such as a git repository. You
deploy an iod and you install it. An iod has two halves.

| Half | What it does | Which side opens the connection |
|---|---|---|
| in | reflects the external system into a place in the store | viff |
| out | acts on the external system | the iod |

The iod owns its place and is the only writer of it. This recipe builds the in half, which
is a *source*: the Go interface `Source`.

### The smallest source

The sample is [examples/io/main.go](../reference/examples/io/main.go). These are its two
methods.

```go
type demo struct{ git *viffio.Git }

func (d *demo) Root() string { return d.git.Root() }

func (d *demo) Reflect(ctx context.Context, ref entity.Ref) (viffio.Reflection, error) {
	return d.git.Reflect(ctx, ref)
}
```

| Method | What it returns |
|---|---|
| `Root` | the root of the iod: the first segment of every address the iod writes. The sample returns `git`. |
| `Reflect` | a `Reflection`: what the external system holds at this moment. A zero `ref` asks for everything the iod owns. A `ref` with segments asks for one place, and an answer with everything is correct for it too. |

The sample delegates to `viffio.Git`, which reads a git repository on disk. It also has a
third method, `Owns`, which returns the claims of the iod: what only this iod may write. To reflect a system of your
own, write the body of `Reflect`. The sample holds this sketch.

```go
func (d *demo) Reflect(ctx context.Context, ref entity.Ref) (viffio.Reflection, error) {
    issues, err := d.api.ListIssues(ctx, project)
    if err != nil {
        return viffio.Reflection{}, err          // an error, NEVER an empty reflection
    }
    cover := viffio.Cover("linear", "issue", "*")
    cover.Predicate = entity.Obj{"project": project}.Payload() // say what you are complete FOR
    rn := viffio.Reflection{Covers: []entity.Pattern{cover}}
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
| `Covers` | the covers: the patterns under which the answer is complete. Viff removes a stored entity that a cover reaches and that `Entities` does not hold. |

A source has no access to the store. Viff compares the answer with the store and writes
the difference.

### Serve it

`iod.New(src).Handler()`, from the package `pkg/io/iod`, serves the two routes that viff
dials.

| Route | What it answers |
|---|---|
| `GET /io` | `{root, owns}`: what the program is. `viff iod add` and `viff iod check` ask it. |
| `GET /reflect[?at=<address>]` | a `Reflection`. With no `at`, the answer is for everything the iod owns. `iod` answers `500` when `Reflect` returns an error. |

Viff asks for a full reflection when you install the iod, and again every minute. The iod
may also send viff a **hint**, which says which addresses went stale. Viff reflects
those addresses again at once.

```
POST /iods/{name}/hint?at=<address>
```

`{name}` is the name the iod was installed under. The parameter is `at`, and it may repeat.
A hint carries no data, and viff answers `204`. A hint with no `at` asks for a full
reflection. Viff ignores a parameter of another name, so a hint with `?ref=` is a full
reflection too.

### Install it

Start the sample in a terminal of its own. `-repo viff=.` reflects the repository in the
current directory under the name `viff`.

```sh
go run ./docs/reference/examples/io -addr 127.0.0.1:9811 -repo viff=.
```

Then install the iod, check it, and read what it reflected.

```sh
viff iod add sample http://127.0.0.1:9811 -root git
viff iod check sample
viff data git viff head
```

```
at: mirror.iods.sample
owns:
- at: git.viff
reach: "a source"
```

```
at: mirror.iods.sample
reach: "a source"
says:
  owns:
  - at: git.viff
  root: git
url: http://127.0.0.1:9811
```

```
dirty: true
ref: viff/build-on
repo: viff
sha: 10fe682272161438fd6b4872832315bffed3af1d
```

Your output names your own branch and commit. `viff data git viff ref` prints the
branches. A hint is one request:

```sh
curl -i -X POST "http://$VIFF_ADDR/io/sample/hint?at=git.viff.head"
```

```
HTTP/1.1 204 No Content
…
```

[Iods](../reference/io.md) has every command, and
[the contract an iod serves](../reference/io.md#the-contract-an-iod-serves) has the form of a
reflection on the wire.

### What an iod must do that the compiler does not check

1. **`Covers` claims no more than the answer is complete for.** Viff removes every stored
   entity under a cover that the answer does not hold. If the iod sees half of a system, it
   covers that half.
2. **A read that failed is an error.** It is never an empty answer. An empty answer with
   covers tells viff that the external system is empty, and viff removes what was there.
3. **One iod for one place in one scope.** The install compares the claim of the iod with the
   claim of every installed iod in the same scope. It refuses an install that it cannot prove
   separate, with `409` and both claims in the message.
4. **The root you name is the root the iod says.** Viff refuses
   `viff iod add … -root github` for the sample with `400`, because the sample says `git`.
5. **A reflection passes `Reflection.Check`.** Viff runs the check before it writes
   anything, and it refuses the whole answer for one fault.

`Reflection.Check` refuses these.

| What the reflection holds | Why viff refuses it |
|---|---|
| a cover with no path | the empty path is the root, so the cover claims everything |
| a cover whose first segment is `*` or an operator | the cover claims every place |
| a cover whose first segment is not the root of the iod | an iod may not remove what another iod wrote |
| a cover with an `op` | a cover selects stored entities, and they have no `op` |
| an entity under `viff` | no iod reflects the records that viff keeps about itself |
| an entity outside the root of the iod | the iod does not own that place |
| an entity whose address is the root alone | a reflection writes into the place of the iod, and not over it |

### The out half

The out half is a *sink*, and it works in the other direction. Somebody writes an input,
which is a node with a `to` field, under a place that the iod owns. The iod reads the input,
makes the external system match it, and writes what happened beside it, at
`<name>~status`. In Go, a sink is a `Sink` with one method, `Deliver`. Use a sink for an effect that an edit can correct, such
as a message or a comment. Use a task for an effect that nothing can take back, such as a
push. [The out half](../io.md#the-out-half-viff-asks-the-iod-does-it) describes the model, and
[the reference](../reference/io.md#the-out-half) describes the registration and its grants.
