# Task control

This page is the contract behind `verse up -runner <url>` and `verse up -broker`. Walk through it
first in [reference/tasks.md](reference/tasks.md#executing-one-by-hand), where a person dials in
and executes a task by hand. This page is the [seam](glossary.md#seam) that makes that work. It
builds on [external tasks](external-tasks.md), where it is design D, and it makes concrete what
that page calls "wrapping and enforcing the verse invariants".

In short, the task-control interface is the `taskd` HTTP contract behind a base URL, made
resumable and aware of claims.

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

Outside the process, verse cannot intercept every action of a remote worker or of a person.
Enforcement becomes four things: authorize, checkpoint, attest, and CAS-enforce invariants.

- Verse authorizes the [claim](glossary.md#claim-of-a-worker).
- Verse records what the [worker](glossary.md#worker) reports, as audited attestations.
- Verse CAS-enforces the structural invariants on the entity: terminal-once, writes by the claim
  holder only, and gate-before-the-effect.

Verse relays a change of vector to the inbox of the worker. The worker honours it or does not.
Nothing outside the process derives an effect or evaluates a vector. The broker records no
envelope that it cannot check.

So there is one control contract with three modes of enforcement. What each mode guarantees
differs, with the mechanism:

| | who performs the step | what holds |
|---|---|---|
| the local supervisor | verse's own executor | **enforced** — the action cannot run until the vector clears it |
| a spawned worker on k8s, lambda or a host | the worker | **attested** — verse holds the worker's account of what it did |
| a human, by surfaced attestation | a person | **attested**, and the person is answerable for the account |

Gates, the resolver, boundaries and the terminal are the same in all three modes. The vector is
the part that does not travel.

Takeover needs nothing extra. You `cancel` the agent. A person claims the task again, by a CAS
transfer of the claim with an identity. The person reads the journal and continues on the same
subject under the same runner.

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

**No control state of a task lives only in memory.** Pending gates, the claim, the control
fields and the event cursor are all on the `verse.task.instance` entity. A restart reads them
again.

**`verse up` reconciles, and a resumed task is not an orphan.**

| Task | What `up` does |
|---|---|
| an in-process agent whose runtime died | orphans it, because the loop is gone |
| a detached or claimed task, such as a person still merging or a k8s Job still running | resumes it. Verse attaches again to the bound runner, and reconciles from the current status. |

The work of a detached task went on in the world while verse was down. Verse resumes where the
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
[what a scope owns](reference/connect.md#what-a-scope-owns)).

One dispatch for every task forces the contract to be complete. It keeps local tasks equivalent
to remote tasks.

Verse can mint that URL on its own broker face, so a runner need not stand up a service (see
"Hosting").

| Runner | URL | What serves it |
|---|---|---|
| the built-in local supervisor | the well-known `<broker>/broker/local` | an in-process handler |
| a broker registered at runtime | `<broker>/broker/<nonce>` | the registered backend |

So "no privileged local path" needs no separate loopback server. Local is the broker at a fixed
nonce.

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

### The bodies

Each body is one Go type in package `task`, used by verse when it sends and by the stock runner
(`task/taskd`) when it reads. Each is in the schema under its name (`verse schema`), which is
where a runner in another language reads it.

| Route | Type | Schema name |
|---|---|---|
| `POST /tasks` | `task.SpawnRequest` | `verse-runner-spawn` |
| `POST /tasks`, the answer | `task.Spawned` | `verse-runner-spawned` |
| `POST /tasks/{id}/say` | `task.SayRequest` | `verse-runner-say` |
| `POST /tasks/{id}/cancel` | `task.CancelRequest` | `verse-runner-cancel` |
| `PUT /tasks/{id}/vector` | `task.VectorRequest` | `verse-runner-vector` |
| `GET /tasks/{id}` | `task.Instance` | `verse-instance` |

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
| `virtualKey` | the model credential the task runs under: the key itself. It is the one field that carries a secret. |
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
| `claim` | `by, identity, at` | `claim {by, identity, at}` (CAS; authorizes the driver) |
| `boundary` | `seq` | a safe point where control lands |
| `say` | `text, by` | an utterance: `by` empty = the agent's narration → `say` preview; `by` set = a say-to folded in → journal only |
| `exec` | `tool, text, err` | `lastAction`, `lastOutput` + journal |
| `pending` | `id, gate, mode, tool, reason, options?, deadline?` | `pending {id, gate}` — verse routes it to a Resolver; `options` (each `{id, need}`) and `deadline` (`{within, default}`) shape the question as a charter gate's would, defaulting to proceed or deny at one each |
| `decision` | `id, gate, mode, choice, proceed, by, reason` | clears `pending`; records the resolution — `choice` is the option, `proceed` the same as yes or no |
| `child` | `child, status` | flow-tracking (sub-task spawn/join) |
| `vector` | `by` | records an autonomy slide (the new vector is observable) |
| `status` | `text: running\|paused\|resumed\|done\|failed\|cancelled`, `result?` | `status`, `paused`; on terminal `result` (CAS-once) |

The `claim` kind is not built. Verse writes a claim on the record of the task, and not as an
event on the stream (see [what is built](#what-is-built)).

That table is the durable contract. It is `mirror.Reflect`, plus `claim` and `vector`, minus the
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
runtime that holds the work: the local one, a broker, or a remote taskd. A client that resumes
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

The POST does not wait for the effect. The next boundary of a person may be hours away.

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
- writes by the claim holder only;
- gate-before-the-effect.

For a detached worker or a person, gate-before-the-effect is an attestation that verse
timestamps and audits. Verse cannot intercept the merge itself. Autonomy outside verse is
coarser by nature, and this page says so.

## Brokering: firewalled workers and humans, without inverting verse

Some workers cannot accept inbound connections: an agent on a laptop, a pod in a private cluster,
a person. Such a worker does not force verse to invert. The [broker](glossary.md#broker) is
itself a backend, and it is the one reachable meeting point. Verse and the worker both dial out
to it:

```
 verse ──POST/GET(events)/POST(control)──▶  broker (base URL)  ◀──claim/inbox/report── worker
 mirror → task.instance (durable truth)   per-task relay log       dials OUT (NAT-friendly)
```

The broker has two faces:

| Face | Who is the client | What it serves |
|---|---|---|
| the verse-facing contract, above | verse, as a plain client | spawn, events, control |
| a worker-facing dial-in protocol that the broker owns | the worker | claim, then an inbox long-poll for the spec and control, then report |

The inbox long-poll is not built: a worker polls (see [what is built](#what-is-built)).

The job of the broker is to join the two halves of a task:

- The reports of the worker become the events that verse streams.
- The control of verse becomes items that the worker pulls.
- A checkpoint that the worker reports becomes an `EvPending`. Verse routes it to a resolver, and
  the verdict goes back down the [inbox](glossary.md#inbox) of the worker.

The spawner of verse does the same three things: spawn, stream and control. It does them
whatever is behind the URL: the in-process loop, a reachable k8s runner, or a broker that relays
to a laptop.
Reachability is a detail of the implementation behind the URL.

The two legs have independent cursors that a reader can resume. The verse leg has one, and the
worker leg has two (see [the worker-facing dial-in protocol](#the-worker-facing-dial-in-protocol)
below). Each follows the same discipline of replay and resync. The broker keeps a bounded log per
task to serve them. The entity stays the durable system of record of verse. The broker is a
relay, so a third party can run a cheap one.

A person is a worker of a broker. For a person:

- the inbox long-poll is "check notifications, run `verse task inbox`";
- `report` is the person's progress `say`;
- pushed control is a notification or a PR comment.

So a person and an agent behind NAT use the same mechanism. A design for the person first gives
the agent at no extra cost.

## Hosting: verse's broker face (`/broker/<nonce>`)

A base URL need not be a new service. Verse can host a broker in the way docd hosts a
controller.

| Host | What it forwards | To what | Under |
|---|---|---|---|
| docd | the ops of a subtree | a registered controller | a mount |
| verse | the worker ops and engine ops of a task | a registered backend | `/broker/<nonce>` |

This is the same capability-URL method that `connect` uses for `dir-content-<nonce>`. A runner
binding is then always a URL: `<broker>/broker/local`, `<broker>/broker/<nonce>`, or an external
one. Verse consumes the verse-facing contract from it. For a hosted broker the call is in process
or forwarded. For an external broker the call is HTTP.

One deployment has two listeners.

| Listener | What it serves | Trust |
|---|---|---|
| the operator API, on `-addr` | the CLI, entities, gates, task control, and broker registration (`POST /broker` mints a nonce) | trusted and internal |
| the broker face, on a sibling port | `/broker/<nonce>/...` and the worker-facing dial-in (`claim`, `inbox`, `report`) | guarded by the nonce, and reachable by workers |

`POST /broker` is not built: a verse hosts the broker it was started with (see
[what is built](#what-is-built)).

The sibling port keeps a clear trust boundary between operators and workers. Brokering shares
the deployment of verse, but it does not expose the operator API to workers. A worker only
reaches its own path under a nonce, and never the surface of entities or gates.

The built-in local supervisor is at the well-known `/broker/local`. It has a fixed nonce and an
in-process handler. So a default spawn binds to it with no config, and the binding stays the
same across restarts.

A broker registered at runtime gets a random nonce, and the nonce is the capability. It may be
an in-process plugin. It may also be a registrant that dials in, as in the docd-controller
inversion. Such a registrant connects out to the registration of verse and gets a nonce. Verse
then forwards the worker ops and engine ops to it. So a runtime that can only dial out, such as a pod behind a
firewall or a vendor behind NAT, still hosts a broker without a face of its own.

The lifecycle follows the existing discipline of reaping daemons and restarting:

1. `up` binds both listeners and mints `/broker/local` again.
2. Brokers register at runtime.
3. `down` reaps them.
4. `up` reconciles each task that is not terminal, from the runner binding on its entity. It
   mints the local broker again, attaches an external one again, and orphans only an in-process
   handler that died.

The two topologies combine. A hosted nonce-broker can relay out to an external runtime. A k8s
pod behind a firewall dials `<broker>/broker/<nonce>`, and the handler that verse registered
drives the Job. Neither the pod nor the vendor needs a face. A third party that wants its own
front door still binds an external URL directly.

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

Nothing about *how* it runs the work shows through. For this reason local, k8s, lambda and a
broker behind NAT can replace one another behind the URL.

## The worker-facing dial-in protocol

The verse-facing contract stands on its own. The local, k8s and lambda backends need nothing in
this section.

A runner may work through a party that cannot accept inbound connections: an agent on a laptop,
a pod in a private cluster, a person. That worker needs a way to dial out and still be driven.

The worker leg is the verse leg inverted:

| Leg | Reports | Control |
|---|---|---|
| verse | verse pulls them | verse pushes it |
| worker | the worker pushes them | the worker pulls it |

Both sides only dial out, and the broker is the one reachable meeting point. This is the standard
worker protocol, so a worker written once runs under any broker. A broker may also offer a
private, faster transport over WebSocket or gRPC. This HTTP form is the baseline for interop.

Three endpoints, with the worker as the client:

```
POST /tasks/{id}/claim   {identity}                      → {spec, cCursor, rCursor}
GET  /tasks/{id}/inbox?since={cCursor}   (long-poll/SSE)  → control directed at the worker
POST /tasks/{id}/report  {events:[…], key?, epoch}        → {rCursor, applied}
```

The long-poll on `inbox` is not built: a worker polls (see [what is built](#what-is-built)).

`inbox` is the dual of verse's `/events`. It is a stream of control directed at the worker, and
the worker can resume it. It carries these items:

- `spec`, once;
- `say {text, by}`;
- `resolve {decision, choice, proceed, say?, by}`;
- `cancel|pause|resume`;
- `vector`.

Each item carries a control cursor, and the worker resumes with `?since=cCursor`.

**An answer is a decision when the worker has taken it, not when the broker queued it.**

- `resolve` closes the gate at once. A second answerer is told that somebody answered first.
  Two answers to one question are a race with one winner, however slowly the answer travels.
- The broker emits the `decision` event when the inbox poll of the worker acknowledges the item
  that carries it.
- The acknowledging poll is the one that asks for what comes *after* the item. It is not the poll
  that returns the item. A received response is not proof that the worker took the item, because
  a lost response looks the same.
- Until the acknowledgement, the question is answered and not delivered. If the claim expires
  first, the answer was never delivered at all.

**A claim is a lease.** It survives for `broker.ClaimLease` without the broker hearing from the
holder. The work itself renews the lease: every inbox poll and every report says that the holder
is alive. So a worker that talks to the broker at all already keeps its lease.

The broker releases an expired claim when somebody next asks for the work, and it fences the
silent holder. If the holder comes back, it comes back as a stranger. The broker does not make
the task terminal. A broker that cannot hear a worker knows nothing about whether the work is
running. It knows only that nobody may claim to speak for the work.

This is the only place in the task-control seams where a clock decides anything. There is one
clock, the broker's, and the broker compares two readings of its own clock. A timestamp written
elsewhere is a record, and never an input.

`cCursor` is what this worker last acknowledged. It is not how far the broker has got. A poll for
`since=N` acknowledges everything at or below N, because a worker asks for what comes next only
after it has taken what came before. So a worker that connects again gets the place it actually
reached, and control issued while it was away is still waiting for it.

A worker that asks to go backwards gets items from where it got to, and not from where it asked.
The broker knows what this claim acknowledged, so a worker that lost its cursor need not remember
it. What the worker has not taken is still waiting. What it has taken is not handed back to be
applied twice.

A takeover resets that mark. What a different worker acknowledged says nothing about what this
worker has done. An acknowledgement is not a promise that the work was done.

One window stays the worker's own: the time between receiving control and acknowledging it. A
worker may pull an item, act on it, and die before its next poll. The broker then hands it that
item again. So the worker should apply idempotently any control that it cannot afford to apply
twice. The worker is the only party that can.

**`key` makes a report retryable.** A worker whose response was lost cannot know whether its
events landed. Without a key it has two choices, and both are bad:

- drop what it observed;
- report it twice. One deploy then becomes two events at two cursors, and afterwards nobody can
  tell it from work done twice.

The broker answers a repeat of a key already applied with the answer to the first, and does not
apply it again. The key is optional, and must be. Only the worker knows that a retry is a retry.
A worker that sends no key gets a new report every time.

**`applied` is how many of the reported events the broker took.** It is not always all of them.
Nothing follows a terminal, so if a batch has its terminal before its last entry, the broker
drops the rest. A worker computes its retry from `applied`. It does not assume that a 200 means
everything landed.

`report` is the dual of the control POSTs of verse. The worker appends:

- `pending`;
- its own `say`;
- `exec`;
- `status` transitions;
- `boundary`.

Each gets a report cursor. `from: rCursor` makes a re-POST idempotent: the broker appends only
what is past its high-water, and acknowledges again.

`report {from: rCursor}` is not built. A report carries `epoch`, `events`, and optionally `key`
and `result` (see [what is built](#what-is-built)).

### The two cursors on this leg

The verse leg has one cursor: the read position of verse on the event log. The worker leg has
two, because the worker both reads and writes.

| Cursor | What it is | How the worker connects again |
|---|---|---|
| `cCursor` | its position in the inbox, the control it pulled | `inbox?since=cCursor` |
| `rCursor` | its high-water in what it reported and the broker acknowledged | `report {from: rCursor}` |

Each cursor resumes independently. So there are three resume points over one durable truth, the
`verse.task.instance` entity:

1. the read of verse;
2. the read of the worker;
3. the write of the worker.

A worker that sleeps and connects again resumes both its cursors, and verse does not notice. A
verse that restarts reads `/events` again, and the worker does not notice. All three follow the
same discipline: replay, then live, with a resync below the watermark.

### How the broker maps the worker leg onto the verse leg

The broker keeps one ordered event log for each task. `/events` streams this log. The broker
sequences two interleaved sources into it, each with a cursor.

**Worker reports.** The broker sequences each `POST /report` into the log, and the next
`/events?since` of verse delivers it. A report at `rCursor=R` becomes an entry of the merged log
at a `cursor=V` that the broker assigns. The two axes relate only through the sequencer of the
broker.

**Control-origin events.** These arrive when verse POSTs control. The broker records the ordered
event, and relays the item to the inbox of the worker at a new `cCursor`. One fact has two
readers.

Who writes an effect event follows the interlock rule: the truth is what was applied.

| Event | Who emits it | When |
|---|---|---|
| operator-authored: a `decision` from `/resolve`, a `vector` from set-vector, an operator `say` | the broker | on the POST, because the operator or the answerer acted. The broker relays the item for the worker to act on. |
| worker-confirmed: `cancelled`, `paused`, `resumed` | the worker, in a report | when it stops at its boundary. The broker only relays `cancel` and `pause`. |

There is no duplication. The `say` is the operator speaking, and the action that the worker then
takes is a separate `exec`.

Until the worker confirms, verse sees "cancel requested" in the snapshot. `status:cancelled`
lands on confirmation. This latency is coarse, and this page says so.

A gate goes across both legs and back:

```
worker POST /report pending@R7      → broker seq → events@V40 → verse mirrors pending[G]
verse routes, resolves, POST /resolve {G, proceed, say:"squash first"}
  broker: events@V41 decision{by:human:scott}      (verse: cleared)
          events@V42 say{by:human:scott,"squash first"}
          inbox@C3   resolve{G, proceed, say:"squash first"}   (relayed to worker)
worker GET /inbox?since=C2 → C3 → proceeds → POST /report exec@R8  (→ events@V43)
```

### Claim, identity, takeover

The identity on a claim is the [principal](glossary.md#principal) that the worker acts as from
then on. Verse lists it as a runner. Verse grants it `read-write` and `control` on the task and on
this run. The mirror writes every report as that principal ([perms.md](perms.md)). The identity
holds nothing on the questions that the task raises.

`POST /tasks/{id}/claim {identity}` is a CAS on the claim. On success, it returns the `spec` and
the resume cursors. A task claimed by another worker gives `409`, unless this is a takeover.

In a takeover:

1. The broker revokes the prior claim. Its inbox gets a `revoked` item, so an old worker that is
   still connected stops.
2. The new identity claims.
3. The new identity reads the journal, which is already durable and is the context of the
   handoff.
4. It continues on the same subject.

Verse mirrors the claim on the entity, so anyone can see who holds it. A person who takes over a
runaway agent does a plain transfer of the claim.

A pool variant, `POST /claim {identity, kinds}`, long-polls for the next eligible unclaimed task
and CAS-assigns one. N workers can then drain a queue without getting in each other's way.

Takeover by a `revoked` item and the pool claim are not built. A worker claims one task by its
id. A displaced worker learns that it was displaced from a `409` on its next call (see
[what is built](#what-is-built)).

A person uses this protocol at the pace of a person:

| Endpoint | For a person |
|---|---|
| `claim` | "I'll take it", from a CLI or a bot |
| the `inbox` pull | "check notifications" |
| `report` | a progress `say`, and checkpoints |

The three endpoints are the same. Only the pace differs.

## What is built

Everything above is built, except what this section names.

- The seam itself, `task.Runner` and `task.Handle`, carries every task. The engine holds a
  handle, and never a concrete `*Task`.
- Events carry a per-task cursor with a bounded ring. `GET /events?since=N` replays and then goes
  live. Verse keeps its high-water cursor on the entity.
- `runner.HTTP(baseURL)` drives a remote verse over the verse-facing contract.
- The reconcile at startup resumes a task where the work outlived the daemon, and does not orphan
  it.
- The dial-in broker is hosted under `/broker/<nonce>`. It publishes the `dialURL` of each task
  on the entity of the task. The terminal-once latch and the claim fence guard it.
- `verse task do` is the view of the executor. Every operator verb works on local, remote and
  brokered tasks alike.

Not built, though the sections above describe them:

- The inbox long-poll. A worker polls.
- A timestamp `at` on an event. An event carries a cursor and no time.
- A `claim` event kind. A claim is written on the task's record and is not an event on the
  stream.
- `POST /broker`, which would register a broker and mint its nonce. A verse hosts the broker it
  was started with (`verse up -broker`).
- Takeover by a `revoked` inbox item, and a pool claim `POST /claim`. A worker claims one task
  by its id, and a worker that has been displaced learns it from a `409` on its next call.
- `report {from: rCursor}`. A report carries `epoch`, `events`, and optionally `key` and
  `result`.

Two larger tracks are outside this page:

- a human surfacer and an inbox projected by identity, with `owner`, `claimant` and `resolver`
  stamps;
- a k8s runner that spawns Jobs behind the broker, alongside `k8s.*` as a
  [Source](controllers.md).
