# Task control

This page is the contract behind `verse up -runner <url>` and a runner named on a spawn. It is
the [seam](glossary.md#seam) between verse and where a task runs. It builds on
[external tasks](external-tasks.md), where it is the attached shape, and it makes concrete what
that page calls "wrapping and enforcing the verse invariants".

In short, the task-control interface is the `runnerd` HTTP contract behind a base URL, made
resumable.

- Verse is its client. Verse mirrors it into the durable `verse.task.instance` entity, the
  [instance record](glossary.md#instance-record).
- The local supervisor is one built-in implementation of the same URL contract. It has no
  privileged shortcut.
- Anyone else plugs in a URL, completely outside verse: k8s, AWS Lambda, a service for work done
  by people, or a custom runtime.

[What is built](#what-is-built), at the end, lists the parts that are not built.

## The two names

This interface governs how one [task](glossary.md#task) runs. It is not a general control
policy. Nothing here decides anything about the engine, its charter, or how a
[firing](glossary.md#firing) passes its gates.

Each of the two settings has its own name, and neither name is "policy". What runs a task is its
[runner](glossary.md#runner). How much it may do without asking is its
[vector](glossary.md#vector).

| meaning | interface | wire | flag |
|---|---|---|---|
| what runs a task | `task.Runner`, returning a `task.Handle` | `runner` on a `verse.task.instance` and on `verse status` | `verse up -runner <url>` |
| how much it may do unasked | `task.Vector` | `vector` on a launch spec | `verse task vector <id>` |

A policy layer over the engine is a plausible thing to want later. A name "policy" here would
collide with it.

## What the interface is

Verse defines a task as a contract, not as "an LLM loop":

1. A [driver](glossary.md#driver) proposes an action.
2. An [executor](glossary.md#executor) performs it.
3. A vector gates each effect at a [boundary](glossary.md#boundary).
4. A read plane streams events.
5. A control plane steers the task at boundaries.
6. Verse mirrors the whole task to a `verse.task.instance` entity, and the task ends with a
   terminal.

Task control is the durable form of the in-process `Supervisor`, which enforces that contract.

In process, verse enforces the contract by interception. The loop cannot `Execute` an action
until `Supervisor.Check` returns proceed. `Supervisor.Check` derives the `Effect`, calls
`Vector.Evaluate`, and calls `Resolver.Resolve`. Verse is trusted because it runs the loop
itself.

Outside the process, verse cannot intercept every action of a remote worker.
Enforcement becomes four things: authorize, checkpoint, attest, and CAS-enforce invariants.

- Verse authorizes the runner: the identity on its entity is the
  [worker](glossary.md#worker) of record, granted the task and nothing else.
- Verse records what the worker reports, as audited attestations.
- Verse CAS-enforces the structural invariants on the entity: terminal-once, writes by the
  worker of record only, and gate-before-the-effect.

Verse relays a change of vector to the runner. The worker honours it or does not.
Nothing outside the process derives an effect or evaluates a vector.

So there is one control contract with two modes of enforcement. What each mode guarantees
differs, with the mechanism:

| | who performs the step | what holds |
|---|---|---|
| the local supervisor | verse's own executor | **enforced** — the action cannot run until the vector clears it |
| a spawned worker on k8s, lambda or a host | the worker | **attested** — verse holds the worker's account of what it did |

Gates, the resolver, boundaries and the terminal are the same in both modes. The vector is
the part that does not travel.

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
| a *policy change* | a change of vector | It applies to later actions and does not preempt. |
| *work the task produced for itself* | the result of a child | It folds in at the hinge. It never overtakes an action that is already proposed. |

Preemption is for people.

Suppose you change a running task to `auto`. That means "stop asking me about actions like
this". It does not mean "approve the action already in front of me". If the change dissolved the
pending question, a policy change would approve one specific action that nobody looked at.

A task-control backend is a reconciler over the `verse.task.instance` resource. It is not itself
a gated task. Governing a task must not require another governed task, or the regress never
stops.

A backend is the dual of the [source](glossary.md#source) of an [io](io.md). A source reflects
external state *into* the [store](glossary.md#store). A task-control backend runs and governs
*work* on a substrate. Both reconcile a resource, and both must be idempotent. Only one of them
is a controller, and it is the backend (see
[io.md](io.md#a-source-is-not-a-controller-and-cannot-be)). k8s is on both sides:

- `k8s.*` as a source that reflects the cluster in;
- k8s as a task-control backend that runs tasks as Jobs out.

## Idempotency ⇒ the restart discipline (`verse down; up`)

A backend is a reconciler. So it must be level-triggered, and verse must be able to rebuild it
from the durable entity. The same `Observe:Since` and `webhook:reconcile` law holds here as
everywhere in verse. Two rules follow.

**No control state of a task lives only in memory.** Pending gates, the worker of record, the control
fields and the event cursor are all on the `verse.task.instance` entity. A restart reads them
again.

**`verse up` reconciles, and a resumed task is not an orphan.**

| Task | What `up` does |
|---|---|
| an in-process agent whose runtime died | orphans it, because the loop is gone |
| a task on a runner, such as a k8s Job still running | resumes it. Verse attaches again to the bound runner, and reconciles from the current status. |

The work on a runner went on in the world while verse was down. Verse resumes where the
work outlived the daemon, and orphans only what died with it.

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

## Everything through the base URL — no privileged local path

The engine is a client of a bound base URL. It spawns there, subscribes to the event stream, and
mirrors the stream into the `verse.task.instance` entity. Durability is in verse, and not in the
runner.

Each spawn binds its runner. The binding is `runner: <name>` in the charter or spec, and names
an installed runner, as `-driver` names a driver. There is no in-memory shortcut:
`verse task cancel|say` and `verse flow suspend -task` go through the same dispatch as for a
remote task.

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
GET    /tasks                      every task the runner hosts, as snapshots
GET    /tasks/{id}                 level truth: current snapshot
GET    /tasks/{id}/events?since=N  edge stream (SSE): replay cursor>N, then live
POST   /tasks               the launch (below)             spawn → {id}
       201 built it · 200 it was already running · 400 the body does not parse, or the runner will not host it
POST   /tasks/{id}/resolve  {id, choice, by, say?}         answer a gate: choice is proceed | deny, or an option the record names
       204 applied · 404 no such decision, or it is already answered
POST   /tasks/{id}/say      {instruction, by?}             say to it (re-prompt)
       204 applied · 410 it has ended, and hears nothing
POST   /tasks/{id}/cancel   {reason?}                      end it, and say why
POST   /tasks/{id}/pause                                   the operator's hold (docs/control.md)
POST   /tasks/{id}/resume
PUT    /tasks/{id}/vector   {vector, by?}                  slide autonomy
```

A control call that was applied answers `204`. A task the runner does not host answers `404`.
A resolve without a `choice` answers `400`, and the gate stays open.

### The bodies

Each body is one Go type: a request or an answer of the runner's own in package `runner`, and
a task's snapshot and events in package `task` (the launch and the vector are still there too).
verse uses them when it sends and the stock runner (`runner/runnerd`) when it reads. Each is in
the schema under its name (`verse schema`), which is where a runner in another language reads
it.

| Route | Type | Schema name |
|---|---|---|
| `POST /tasks` | `task.SpawnRequest` | `verse-runner-spawn` |
| `POST /tasks`, the answer | `runner.SpawnResponse` | `verse-runner-spawned` |
| `POST /tasks/{id}/say` | `runner.SayRequest` | `verse-runner-say` |
| `POST /tasks/{id}/cancel` | `runner.CancelRequest` | `verse-runner-cancel` |
| `POST /tasks/{id}/resolve` | `runner.ResolveRequest` | `verse-runner-resolve` |
| `PUT /tasks/{id}/vector` | `task.VectorRequest` | `verse-runner-vector` |
| `GET /tasks`, `GET /tasks/{id}` | `task.Snapshot` | `verse-task-snapshot` |
| `GET /tasks/{id}/events` | `task.Event` | `verse-task-event` |

**The launch.** Every field is optional. A field that is absent means the runner's own default.

| Field | What it is |
|---|---|
| `id` | the task's id. verse mints it, so that it matches the task's record. A runner sent none mints its own. |
| `prompt` | the one job |
| `driver` | which driver runs it: `llm`, `scripted`, `calls`, or one the runner declares |
| `frame` | the standing instructions the prompt is read in |
| `tools` | the tools offered. Absent is all of them. A name no driver knows fails the spawn. |
| `retain` | how many events to keep for a reader that resumes |
| `converse` | the run is a conversation: when the driver goes quiet, the person gets the turn as a question, and the run ends when they end it |
| `virtualKey` | the model credential the task runs under: the key itself. It carries a secret. |
| `credential` | the token verse signed for the task when its rule's gate passed ([gates](reference/gates.md#a-credential-for-what-a-gate-let-through)). It carries a secret. The runner hands it to the task as a file named by `VERSE_CREDENTIAL_FILE`. |
| `answer` | the answer contract, a tony match pattern. A runner that cannot enforce one refuses the spawn. |
| `calls` | the work itself, for the `calls` driver |
| `vector` | the gate vector to launch under, as resolved values |
| `grant` | the hard envelope, as resolved values. Absent is unbounded. |
| `environment` | the name of the workspace. The runner resolves it, and refuses a name it does not have. |
| `occurrence` | which run this is: the store revision it is about. The runner says it back on the snapshot. |
| `provenance` | `{principal?, subject?, chain?}`: on whose account, and about what |

Two facts about a launch stay in verse and are not sent: the scope the task was spawned into,
and the resolver its questions are held out to.

**A spawn is idempotent by id while the task runs.** A second spawn of a live id answers `200`
with the task that is running. A spawn of an id whose task has ended is a new run.

### The event envelope and the cursor

Every event carries a timestamp and a cursor. The cursor is per task, gapless and monotonic. It
is a plain sequence number that the runner assigns, and it need not be a global logd commit.

```json
{ "cursor": 42, "task": "review@demo.pr.1", "at": "…",
  "kind": "exec", "tool": "run_tests", "text": "2 failed" }
```

The timestamp `at` is not built: an event carries a cursor and no time (see
[what is built](#what-is-built)).

Verse keeps its high-water cursor on the entity. On a restart, it knows where to resume.

### The canonical kinds → what verse mirrors

| `kind` | key fields | entity projection |
|---|---|---|
| `boundary` | `seq` | a safe point where control lands |
| `say` | `text, by` | an utterance: `by` empty = the agent's narration → `say` preview; `by` set = a say-to folded in → journal only |
| `exec` | `tool, text, err` | `lastAction`, `lastOutput` + journal |
| `pending` | `id, gate, mode, tool, reason, options?, deadline?` | `pending {id, gate}` — verse routes it to a Resolver; `options` (each `{id, need}`) and `deadline` (`{within, default}`) shape the question as a charter gate's would, defaulting to proceed or deny at one each |
| `decision` | `id, gate, mode, choice, proceed, by, reason` | clears `pending`; records the resolution — `choice` is the option, `proceed` the same as yes or no |
| `child` | `child, status` | flow-tracking (sub-task spawn/join) |
| `vector` | `by` | records an autonomy slide (the new vector is observable) |
| `status` | `text: running\|paused\|resumed\|done\|failed\|cancelled`, `result?`, `err?` | `status`, `paused`; on terminal `result` (CAS-once) and `err`, why it ended, which the status alone never says |

That table is the durable contract. It is `mirror.Reflect`, plus `vector`, minus the
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
attempts, and publishes `observed: false` on the entity while it is disconnected.

An index that looks confident while it is only stale is worse than one that says it is stale.

The terminal is a `status` event: `done|failed|cancelled` plus `result`. Verse CAS-writes it
once. `?since=<terminal cursor>` replays it and closes. The `/events` of a finished task is a
finite record that a reader can replay. So a verse that restarts *after* completion still learns
the outcome. A reader that has seen the terminal should stop connecting again, because there is
nothing left to connect to.

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
pending  @40 {id:G, gate:"deploy", mode:ask, tool:merge}   → verse mirrors pending[G]
   … verse routes G to a Resolver, gets a verdict …
POST /resolve {id:G, choice:proceed, by:"user:scott@example.com", say:["squash first"]}   → 204
decision @41 {id:G, choice:proceed, proceed:true, by:"user:scott@example.com"}   → verse clears pending[G]
say      @42 {by:"user:scott@example.com", text:"squash first"}                  → folded in
exec     @43 {tool:merge, text:"merged"}                    → worker proceeded
```

Suppose verse restarts between @40 and @41. Verse replays `pending@40`, routes it again, and
POSTs `resolve` again. If the first answer landed, the runner refuses the second as already
answered, and the replay carries `decision@41`. Verse dedups by cursor. In either case there is
one decision: no gate is lost, and nothing merges twice.

## Invariants verse CAS-enforces on the entity

Three things hold on the `verse.task.instance` entity, even when a runner or a worker misbehaves:

- terminal-once (the status is monotonic);
- writes by the worker of record only;
- gate-before-the-effect.

For a remote worker, gate-before-the-effect is an attestation that verse timestamps and
audits. Verse cannot intercept the merge itself. Autonomy outside verse is
coarser by nature, and this page says so.

## What a 3rd-party implementer must satisfy

For each task, an implementation must:

- assign gapless cursors per task;
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
  live. Verse keeps its high-water cursor on the entity.
- `client.HTTP(baseURL)` drives a remote verse over the verse-facing contract.
- The reconcile at startup resumes a task where the work outlived the daemon, and does not orphan
  it.
- Every operator verb works on local and remote tasks alike.

Not built, though the sections above describe them:

- A timestamp `at` on an event. An event carries a cursor and no time.

One larger track is outside this page: a k8s runner that spawns Jobs, alongside `k8s.*` as an
[io](io.md).
