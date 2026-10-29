# Task control

This page is the contract behind `verse up -runner <url>` and a runner named on a spawn. It is
the [seam](glossary.md#seam) between verse and where a task runs. It builds on
[external tasks](external-tasks.md), where it is the attached shape, and it makes concrete what
that page calls "wrapping and enforcing the verse invariants".

In short, the task-control interface is the `runnerd` HTTP contract behind a base URL, made
resumable.

- Verse is its client. Verse mirrors it into the durable record at `verse.task.instance.<id>`,
  the [instance record](glossary.md#instance-record).
- The local supervisor is one built-in implementation of the same URL contract. It has no
  privileged shortcut.
- Anyone else plugs in a URL, completely outside verse: k8s, AWS Lambda, a service for work done
  by people, or a custom runtime.

[What is built](#what-is-built), at the end, lists the parts that are not built.

## The two names

This interface governs how one [task](glossary.md#task) runs. It is not a general control
policy. Nothing here decides anything about the engine, its charter, or how a
[firing](glossary.md#firing) passes its gates.

Each of the settings has its own name, and none of them is "policy". What runs a task is its
[runner](glossary.md#runner). What it may reach at all is its
[environment](glossary.md#environment), fixed when it starts. How much of that it may do without
asking is its [autonomy](glossary.md#autonomy): the rows that name the steps it refuses, asks
about or allows.

| meaning | interface | wire | flag |
|---|---|---|---|
| what runs a task | `task.Runner`, returning a `task.Handle` | `runner` on a `verse.task.instance` and on `verse status` | `verse up -runner <url>` |
| what it may reach | `systems.Environment` | `environment` on a launch | fixed for the run |
| how much it may do unasked | `task.Autonomy` | `autonomy` on a launch | `verse task autonomy <id>` |

A policy layer over the engine is a plausible thing to want later. A name "policy" here would
collide with it.

## What the interface is

Verse defines a task as a contract, not as "an LLM loop":

1. An agent's driver, or a script's call, proposes an action.
2. An [executor](glossary.md#executor) performs it.
3. Each [step](glossary.md#step) is decided at a [boundary](glossary.md#boundary): refused if
   it is outside the environment, then by the autonomy.
4. A read plane streams events.
5. A control plane steers the task at boundaries.
6. Verse mirrors the whole task to its record at `verse.task.instance.<id>`, and the task ends
   with a terminal.

Task control is the durable form of the in-process `Supervisor`, which enforces that contract.

In process, verse enforces the contract by interception. The loop cannot `Execute` an action
until `Supervisor.Check` returns proceed. `Supervisor.Check` derives the step, refuses it
if the environment does not give it, asks the autonomy which row decides it, and for an ask calls `Resolver.Resolve`. Verse is trusted because it runs the loop
itself.

On a [runner](glossary.md#runner), the same supervisor runs in the task's process, and the
runner holds the line around it. Each verb that crosses a credential (a push, a pull request, a
tracker's or Slack's act) is performed by the runner's own process, which decides it again by
the task's environment, its current autonomy and the runner's own ceiling before anything moves, and fills the facts only
it knows (a push's `exists` and `own`). An act a row asks about is performed on an answer the
runner itself relayed to the task, about that call, once per act; the task's process holds the
model, and its word that a question was answered is not taken. A change of autonomy goes to the runner first, which
refuses a row reaching nothing in the task's environment; what the task's process says of it is
not what holds the line, since that is the process the autonomy constrains.
Verse holds the spawn to the runner's offer at install, and its record is what the runner
reports: every call, the row that decided it, and whoever answered.

So there is one control contract, and what a row guarantees depends on who performs its step:

| the step | who performs it | what a row is |
|---|---|---|
| a verb the runner performs (`git.push`, `github.create-pr`, a tracker's, Slack's) | the runner, with its credential | **a bound**: refused there is prevented |
| a verb the runner stops inside the run (`files.write`, `commands.run`, `agent.call-sub-agent`) | the agent, as it proposed it | **a prompt** over what the agent proposes, until a seam below the task makes it a bound ([inside](systems/inside.md)) |

Rows, the resolver, boundaries and the terminal are the same wherever the task runs.

## The loop is a state machine

In process, the loop is a pure transition, `step(state, input) → (state, []effect)`
(`task/machine.go`). The shell around it does the I/O. You need to know two things about it
without reading it.

**The contract is five properties** (`task/contract.go`):

1. A task reaches a terminal at most once, and emits nothing after it.
2. A task does not terminate while a child is outstanding.
3. Every child that starts ends exactly once, with an outcome.
4. The task proposes the closing action at most once per quiescence, and never while the input
   is open.
5. Every wait names what discharges it.

The first four are safety properties. Tests check them by enumerating interleavings of the
transition. The fifth is liveness, and an argument supports it instead. No enumeration can show
that an event arrives.

**Inputs have a precedence, and the precedence is a policy, not a taxonomy.**

| Input | Examples | What it does |
|---|---|---|
| an *intervention* | cancel, pause, resume, say | An operator redirects this run. It preempts, and it preempts a pending gate too. |
| an *autonomy change* | a new autonomy | It applies to later steps and does not preempt. |
| *work the task produced for itself* | the result of a child | It folds in at the hinge. It never overtakes an action that is already proposed. |

Preemption is for people.

Suppose you change a running task's autonomy to allow by default. That means "stop asking me
about steps like this". It does not mean "approve the step already in front of me". If the change
dissolved the pending question, an autonomy change would approve one specific action that nobody
looked at.

A task-control backend is a reconciler over the task's record at `verse.task.instance.<id>`.
It is not itself a gated task. Governing a task must not require another governed task, or the
regress never stops.

A backend is the dual of the [source](glossary.md#source) of an [iod](io.md). A source reflects
external state *into* the [store](glossary.md#store). A task-control backend runs and governs
*work* on a substrate. Both reconcile a resource, and both must be idempotent. Only one of them
is a controller, and it is the backend (see
[io.md](io.md#a-source-is-not-a-docd-controller-and-cannot-be)). k8s is on both sides:

- `k8s.*` as a source that reflects the cluster in;
- k8s as a task-control backend that runs tasks as Jobs out.

## Idempotency ⇒ the restart discipline (`verse down; up`)

A backend is a reconciler. So it must be level-triggered, and verse must be able to rebuild it
from the durable record. The same `Observe:Since` and `webhook:reconcile` law holds here as
everywhere in verse. Two rules follow.

**No control state of a task lives only in memory.** Pending gates, the worker of record, the control
fields and the event cursor are all on the task's record. A restart reads them again.

**`verse up` reconciles, and a resumed task is not an orphan.**

| Task | What `up` does |
|---|---|
| an in-process agent whose runtime died | orphans it, because the loop is gone |
| a task on a runner, such as a k8s Job still running | resumes it. Verse attaches again to the bound runner, and reconciles from the current status. |

The work on a runner went on in the world while verse was down. Verse resumes where the
work outlived the daemon, and orphans only what died with it.

The reconcile reads baseline's tasks. A task that a [scope](glossary.md#scope) started is
neither resumed nor orphaned at a restart: its record keeps the status it had.

An open question follows the task that asks it.

- A resumed task keeps its pending gates on the [docket](glossary.md#docket), under the same
  ids. An answer to one still reaches the worker.
- The questions of an orphaned task end with it. Verse settles them `orphaned`. They leave the
  docket, and you can still read them.

A question listed as answerable, when nothing will read the answer, asks a person to decide for
nobody.

Verse settles a gate that a *firing* raised, whatever its state. What waited on it was a
goroutine in the verse process.

- If the [rule](glossary.md#rule) still matches, the reinstalled charter replays. It poses the
  same question under the same id, and the question is answerable again within milliseconds.
  This is why a gate id is deterministic.
- If the rule no longer matches, the question was about a world that has changed.

## Everything through the base URL: no privileged local path

The engine is a client of a bound base URL. It spawns there, subscribes to the event stream, and
mirrors the stream into the task's record. Durability is in verse, and not in the
runner.

Each spawn binds its runner. The binding is `runner: <name>` in the charter or spec, and names
an installed runner, as `agent.name` names an agent the runner offers. There is no in-memory
shortcut: `verse task cancel|say` and `verse flow suspend -task` go through the same dispatch as
for a remote task.

The dispatch is per [scope](glossary.md#scope). With `-scope`, it reaches a task that the scope
spawned. It refuses a task that the scope inherited, and names the scope that holds it (see
[what a scope owns](reference/scopes.md#what-a-scope-owns)).

One dispatch for every task forces the contract to be complete. It keeps local tasks equivalent
to remote tasks.

The built-in local supervisor is reached through the same interface, in process. `local` is the
meaning of naming no runner.

## The verse-facing contract

This contract has two endpoints, which are `Observe : Since` again, and control out of band.

Every control call reports which of three things happened to it:

1. it was applied;
2. the far side refused it;
3. the far side was never reached.

Only the third is worth a retry. A caller that cannot tell them apart either retries what already
happened, or gives up on what never arrived. The status carries the distinction. Across the Go
seam, `task.ErrUnreachable` and the errors like it carry it.

```
GET    /describe                   what the runner offers: its agents, scripts, and what each takes
       Verse-Runner-Token: <its workload token>, to a verse the runner verified as its own (docs/runners.md, "Who the runner is")
GET    /ready                      200 as soon as the runner serves, for a readiness probe; whether its verse has heard it is the registration's to say
GET    /tasks                      every task the runner hosts, as snapshots
GET    /tasks/{id}                 level truth: current snapshot
GET    /tasks/{id}/events?since=N  edge stream (SSE): replay cursor>N, then live
POST   /tasks               the launch (below)             launch → {id}
       201 built it · 200 it was already running · 400 the body does not parse, or the runner will not host it
POST   /tasks/{id}/resolve  {id, choice, by, say?}         answer a gate: choice is proceed | deny, or an option the record names
       204 applied · 404 no such decision, or it is already answered
POST   /tasks/{id}/say      {instruction, by?, ask?}       say to it (re-prompt); ask is the ask the words are
       204 applied · 410 it has ended, and hears nothing · 422 it takes no words (a script)
POST   /tasks/{id}/cancel   {reason?, by?}                 end it, and say why
POST   /tasks/{id}/pause    {by?}                          the operator's hold (docs/control.md)
POST   /tasks/{id}/resume   {by?}
PUT    /tasks/{id}/autonomy {autonomy, by?}                replace the autonomy
       204 applied · 400 a row it cannot decide · 410 it has ended
```

A control call that was applied answers `204`. A task the runner does not host answers `404`.
A resolve without a `choice` answers `400`, and the gate stays open.

### The bodies

Each body is one Go type: the launch and the requests of the runner's own in package `runner`,
and a task's snapshot, events and autonomy in package `task`. Verse uses them when it sends
and the stock runner (`runner/runnerd`) when it reads. Each is in the schema under its name
(`verse schema`), which is where a runner in another language reads it.

| Route | Type | Schema name |
|---|---|---|
| `GET /describe` | `runner.Description` | its parts: `verse-runner-agent-offer`, `verse-runner-setting` and `verse-runner-system`, a ceiling and `builds` being blocks in an environment's form |
| `POST /tasks` | `runner.Launch` | `verse-runner-launch`, with `verse-runner-agent` and `verse-runner-provenance`; the environment is blocks per system (systems.Environment), and the autonomy the task's own rows (task.Autonomy) |
| `POST /tasks/{id}/say` | `runner.SayRequest` | `verse-runner-say` |
| `POST /tasks/{id}/cancel` | `runner.CancelRequest` | `verse-runner-cancel` |
| `POST /tasks/{id}/pause`, `…/resume` | `runner.PauseRequest`, `runner.ResumeRequest` | `verse-runner-pause`, `verse-runner-resume` |
| `POST /tasks/{id}/resolve` | `runner.ResolveRequest` | `verse-runner-resolve` |
| `PUT /tasks/{id}/autonomy` | `runner.AutonomyRequest` | `verse-runner-autonomy` |
| `GET /tasks`, `GET /tasks/{id}` | `task.Snapshot` | `verse-task-snapshot` |
| `GET /tasks/{id}/events` | `task.Event` | `verse-task-event` |

**The launch.** It is the spawn as the charter wrote it, with verse's references resolved, and
the runner reads it as it is. A runner checks it strictly: an
unknown key, both or neither of a prompt and a script, an environment or a setting its offer does
not take is refused with `400` and the reason. A refused launch is a failed spawn, never a task
run without what it asked for.

| Field | What it is |
|---|---|
| `id` | the task's id. Verse mints it, so that it matches the task's record. |
| `prompt` or `script` | the one job: a prompt, for an agent, or a script, which does its own work. Exactly one. |
| `agent` | the agent a prompt is for, written flat: `{name: <agent>, <setting>: …, annotations: {…}}`. Absent, or with no name, the runner's default, its first agent offer. Each setting is one the offer declares; `annotations` are verse's or the runner's to write, such as `verse.portkey/virtual-key`, and the runner refuses one it does not know. A script takes none. |
| `environment` | what the run may reach: `vars` for its shell, and `systems`, a block per system or repository (`git`, `git-issue`, `network`, …) giving the verbs and their shapes. A step outside it is refused before any row is read. Fixed when the run starts, and within the runner's ceilings. |
| `autonomy` | what happens to each step within the environment: rows, each a `match` on a step's system, verb and fields and a `then` of `deny`, `ask` or `allow`, the last with no match. An ask's shape rides whole (`need`, `answerers`, `within`, `default`, `labels`); the runner reads the first three and never `default` or `labels`, which are verse's ([a row's question](reference/tasks.md#a-rows-question)). Absent asks about every step. |
| `answer` | the answer contract, a tony match pattern. A runner that cannot enforce one refuses the launch; a script takes none. |
| `timeout` | how long the task may run, as a duration. Time spent waiting for an answer does not count; time paused does. |
| `converse` | the run is a conversation: when the agent goes quiet, the person gets the turn as a question, and the run ends when they end it. Only for an offer whose `converse` is true. |
| `occurrence` | which run this is: the store revision it is about. The runner says it back on the snapshot. |
| `provenance` | `{principal?, subject?, chain?}`: on whose account, and about what. A record, never authority. |
| `credential` | the token verse signed for the task when its rule's gate passed ([gates](reference/gates.md#a-credential-for-what-a-gate-let-through)). It carries a secret. The runner hands it to the task as a file named by `VERSE_CREDENTIAL_FILE`, and never says it back. |

A row's ask rides whole, but what silence settles on and its `labels` are verse's to act on,
and a runner reads neither. What stays in verse and is not sent is the scope the task was
spawned into, whom it acts for, and whom its questions naming nobody are held out to. The
runner says which row matched; verse adds the rest when it poses the question.

**A launch is idempotent by id while the task runs.** A second launch of a live id answers `200`
with the task that is running. A launch of an id whose task has ended is a new run.

### The event envelope and the cursor

Every event carries a timestamp and a cursor. The cursor is per task, gapless and monotonic. It
is a plain sequence number that the runner assigns, and it need not be a global logd commit.

```json
{ "cursor": 42, "task": "review@demo.pr.1", "at": "…",
  "kind": "exec", "tool": "run_tests", "text": "2 failed" }
```

The timestamp `at` is not built: an event carries a cursor and no time (see
[what is built](#what-is-built)).

Verse keeps its high-water cursor on the task's record. On a restart, it knows where to resume.

### The canonical kinds → what verse mirrors

| `kind` | key fields | what the record gets |
|---|---|---|
| `boundary` | `seq` | a safe point where control lands |
| `say` | `text, by, id?` | an utterance: `by` empty = the agent's narration → `say` preview; `by` set = a say-to folded in → journal only; `id` is the ask the words were, which settles as `heard` |
| `exec` | `tool, step?, fields?, text, err` | `lastAction`, `lastOutput` + journal; `step` and `fields` name the call, as its `decision` did |
| `pending` | `id, gate, step, fields, tool, action, text, options?, deadline?, answerers?` | `pending {id, gate}` — verse routes it to a Resolver; `options` (each `{id, need}`) and `deadline` (`{within}`) shape the question as a charter gate's would, defaulting to proceed or deny at one each |
| `decision` | `id, gate, step?, fields?, choice, proceed, by, text` | clears `pending`; records the resolution — `choice` is the option, `proceed` the same as yes or no; `gate` is the autonomy's row that decided the call, whether it allowed, denied or asked, or `environment` for a step outside it |
| `child` | `child, status` | flow-tracking (sub-task spawn/join) |
| `autonomy` | `text, by` | records a change of autonomy: `text` is the autonomy in one line, `2 row(s), default ask` |
| `status` | `text: running\|paused\|done\|failed\|cancelled`, `by?`, `err?` | `status`, `paused`; on terminal `result` (CAS-once), read from `GET /tasks/{id}`, and `err`, why it ended, which the status alone never says |

That table is the durable contract. It is `mirror.Reflect`, plus `autonomy`, minus the
`gap` marker. The `gap` marker says something about the *reading*, and not about the task.

### `since` semantics and the two restarts

What the runner does depends on where N is.

| N | What the runner does |
|---|---|
| at or above the current cursor | nothing to replay: the stream goes live |
| inside the kept window | replays `cursor > N` in order, then goes live |
| below the low-watermark of the runner | answers a single `{"kind":"gap","cursor":C}` frame |

N inside the window is the path of a verse restart. Verse connects again with its last mirrored
cursor. The runner is still running, and replays the gap. The worker notices nothing.

N below the low-watermark comes from compaction, or from a runner restart in which the runner
lost its log. After a `gap` frame, the reader reads `GET /tasks/{id}` again for level truth,
adopts cursor `C`, and streams from there. The detailed journal between N and C may be lost,
but the current state is exact.

A resync to the snapshot is the general safety net whenever a runner cannot serve a cursor:

- a verse restart;
- a runner restart;
- a slow consumer;
- compaction.

The same `gap` frame carries both reasons that a reader can be behind: a cursor below the window,
and events dropped because the reader could not keep up.

`?since=` means the same on every runner. `GET /tasks/{id}/events` hands the cursor to the
runtime that holds the work: the local one or a remote runnerd. A client that resumes
gets the frames after its cursor and no others, wherever the task runs.

A resume below the window also replays the gates still open below it. The gap says that a reader
missed something, and this replay says what it missed. The replay happens only in the gap case.
Inside the window, the ordinary replay already carries those gates.

An answering service is a client of this endpoint and of nothing else. Without this replay, an
answerer that connects again learns that it is behind, and cannot see the question it exists to
answer.

**A stream that ends without a terminal did not end because the task ended.** The terminal is
always the last frame. If it is absent, the connection broke. Resume from the last cursor, and
conclude nothing about the work. `mirror.Reflect` does exactly that. It backs off between
attempts, and publishes `observed: false` on the record while it is disconnected.

An index that looks confident while it is only stale is worse than one that says it is stale.

The terminal is a `status` event: `done|failed|cancelled`, and `err` where it failed. Verse
reads the result from the snapshot, and CAS-writes both once. `?since=<terminal cursor>`
replays the terminal and closes. The `/events` of a finished task is a finite record that a
reader can replay. So a verse that restarts *after* completion still learns the outcome. A
reader that has seen the terminal should stop connecting again, because there is nothing left
to connect to.

The terminal is the one frame that a reader never loses. The runner may drop any other frame for
a subscriber that cannot keep up, because an observer never stalls the loop. The `gap` marker
then comes with the next event to say so. Nothing comes after the terminal, so the runner
delivers it from a slot kept for it. It reaches a reader that missed everything before it.

### The control⇄events interlock

Control arrives as a distinct `POST`, out of band, and never as a write to the store. That is
the rule of [deployment and steering](deployment-and-steering.md). The runner emits the
application of the control as an event on the one ordered stream. **The POST is the request,
and the event is the truth.**

The POST returns `204 No Content`. Verse never assumes that control took hold until the
matching event appears at a new cursor. This removes races:

- A `cancel` POSTed after completion never yields a `cancelled` event.
- A `resolve` for a gate that already timed out comes back as a `decision` marked stale. It is a
  recorded no-op, and not a silent drop.

The POST does not wait for the effect. The next boundary of a remote worker may be far away.

One ordered stream interleaves reports and control effects by cursor. That gives a single total
order, which verse replays with nothing else to reconcile. Priority is this ordering. "Steering
outranks observation" means that the runner drains control at the boundary before the next
action. So a control-effect event comes before the action it gated, in cursor order.

A control POST carries no idempotency key.

| Control | Sent again |
|---|---|
| `cancel` of a task that has ended | changes nothing |
| an answer to a question that is already answered | refused `404` |
| `say` | said twice |

Verse dedups events by cursor.

The lifecycle of a gate, from end to end, is all on the stream, and a reader can resume it. A
say-to is a `say` event with `by` set: it is the operator's side of the conversation.

```
pending  @40 {id:G, gate:"deploy", step:"git.push", fields:{ref:"refs/heads/main"}, tool:push}   → verse mirrors pending[G]
   … verse routes G to a Resolver, gets a verdict …
POST /resolve {id:G, choice:proceed, by:"user:you@example.com", say:["squash first"]}   → 204
decision @41 {id:G, choice:proceed, proceed:true, by:"user:you@example.com"}   → verse clears pending[G]
say      @42 {by:"user:you@example.com", text:"squash first"}                  → folded in
exec     @43 {tool:push, text:"pushed"}                     → worker proceeded
```

Suppose verse restarts between @40 and @41. Verse replays `pending@40`, routes it again, and
POSTs `resolve` again. If the first answer landed, the runner refuses the second as already
answered, and the replay carries `decision@41`. Verse dedups by cursor. In either case there is
one decision: no gate is lost, and nothing pushes twice.

## Invariants verse CAS-enforces on the record

Three things hold on the task's record, even when a runner or a worker misbehaves:

- terminal-once (the status is monotonic);
- writes by the worker of record only;
- gate-before-the-effect.

For a remote worker, gate-before-the-effect is an attestation that verse timestamps and
audits. Verse cannot intercept the push itself. A policy outside verse is
coarser by nature, and this page says so.

## What a 3rd-party implementer must satisfy

For each task, an implementation must:

- assign gapless cursors per task;
- serve `GET /describe` with what it offers, and refuse a launch outside it;
- where it is registered with a `subject`, verify its verse's bearer on every call but a bare
  `GET /describe`, which is public, and answer the describe with its own workload token in
  `Verse-Runner-Token` to the verse it verified, and to nobody else
  ([who the runner is](runners.md#who-the-runner-is)); announce itself at start with
  `POST /runners/hello` and that token, in a loop until verse answers 2xx, again after a `401` at
  verse's door and never after a `403`; and serve `GET /ready` as 200 as soon as it serves;
- where it offers the [verse system](systems/verse.md), carry each of a task's verse calls to
  verse with its own token and `Verse-Task: <the task's id>`, while the task runs; verse admits
  the call as the task's owner ([a task carried by its runner](reference/perms.md#a-task-carried-by-its-runner)).
  A runner that reaches no verse does not offer `verse`, and refuses a launch that names it;
- serve `GET /tasks/{id}` as current truth;
- serve `GET /events?since=` with replay-then-live, and a `gap` frame below its watermark;
- emit the kinds above;
- read the bodies as [the contract](#the-bodies) declares them, and refuse a body that does not
  parse;
- echo the application of every control POST as an ordered event;
- make the terminal a stable last event that a reader can replay.

Nothing about *how* it runs the work shows through. For this reason local, k8s and lambda can
replace one another behind the URL.

## What is built

Everything above is built, except what this section names.

- The seam itself, `task.Runner` and `task.Handle`, carries every task. The engine holds a
  handle, and never a concrete `*Task`.
- Events carry a per-task cursor with a bounded ring. `GET /events?since=N` replays and then goes
  live. Verse keeps its high-water cursor on the task's record.
- `client.HTTP(baseURL)` drives a remote runner over the verse-facing contract.
- The reconcile at startup resumes a task where the work outlived the daemon, and does not orphan
  it.
- Every operator verb works on local and remote tasks alike.

Not built, though the sections above describe them:

- A timestamp `at` on an event. An event carries a cursor and no time.

One larger track is outside this page: a k8s runner that spawns Jobs, alongside `k8s.*` as an
[iod](io.md).
