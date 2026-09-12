# Task control

The contract behind `verse up --runner <url>` and `verse up --broker`. Walk it first in
[reference/tasks.md](reference/tasks.md#executing-one-by-hand), where a human executes a task by
dialing in; this is the seam that makes that work. It builds on
[external tasks](external-tasks.md), where it is design D, and makes its "wrapping and enforcing
the verse invariants" concrete.

In one line: the task-control interface is the `taskd` HTTP contract, made resumable and
claim-aware, behind a base URL. Verse is its client and mirrors it into the durable
`verse.task.instance` entity. The local supervisor is one built-in implementation of the same URL
contract with no privileged shortcut, and anyone else — k8s, AWS Lambda, a human-workflow service,
a custom runtime — plugs in a URL, completely outside verse.

See [what is built](#what-is-built) at the end for the parts that are not.

## The two names

This interface governs how one *task* runs. It is not a general control policy: nothing here has
an opinion about the engine, its charter, or how a firing is gated. Keeping those distinct matters,
because a policy layer over the engine is a plausible thing to want later and would collide
head-on.

So the two dials each carry their own name, and neither is "policy". What runs a task is its
*runner*, and the autonomy dial is its *vector*.

| meaning | interface | wire | flag |
|---|---|---|---|
| what runs a task | `task.Runner`, returning a `task.Handle` | `runner` on a `verse.task.instance` and on `verse status` | `verse up --runner <url>` |
| how much it may do unasked | `task.Vector` | `vector` on a launch spec | `verse task vector <id>` |

## What the interface is

Verse defines a task not as "an LLM loop" but as a contract: a driver proposes, an executor
performs, effects are gated by a vector at boundaries, a read plane streams events, a control plane
steers at boundaries, and the whole thing is mirrored to a `verse.task.instance` entity and ends
with a terminal. Task control is the durable generalization of the in-process `Supervisor` that
enforces that contract.

In-process, enforcement is interception. The loop cannot `Execute` an action until
`Supervisor.Check` — derive `Effect`, `Vector.Evaluate`, `Resolver.Resolve` — returns proceed, and
verse is trusted by construction.

Out-of-task, enforcement becomes authorize, checkpoint, attest and CAS-invariants. Verse cannot
intercept a remote or human worker's every action, so it authorizes the claim, gates the
checkpoints the worker reports under the same vector applied to *reported* effects, records those
effects as audited attestations, and CAS-enforces the structural invariants on the entity:
terminal-once, claim-holder-only writes, gate-before-the-effect.

So it is one control contract with three enforcement modes: the local supervisor by interception,
a spawned worker on k8s, lambda or a host by attestation, and a human by surfaced attestation. Same
vector, gates, resolver, boundaries and terminal; only the mechanism differs. Takeover falls out for
free: `cancel` the agent, a human re-claims by CAS claim transfer plus identity, reads the journal,
and continues on the same subject under the same runner.

A task-control backend is a reconciler over the `verse.task.instance` resource rather than a gated
task itself, which stops the regress: governing a task must not require another governed task. It
is the dual of a [Source](sources.md). A Source reflects external state *into* the verse; a
task-control backend runs and governs *work* on a substrate. Both reconcile a resource and both
must be idempotent, but only one of them is a controller, and this is the one
(see [sources.md](sources.md#a-source-is-not-a-controller-and-cannot-be)). k8s sits on both sides:
`k8s:*` as a Source reflecting the cluster in, and k8s as a task-control backend running tasks as
Jobs out.

## Idempotency ⇒ the restart discipline (`verse down; up`)

Because it is a reconciler, a backend must be level-triggered and reconstructable from the durable
entity, under the same `Observe:Since` and `webhook:reconcile` law verse runs everywhere. Two rules
follow.

No task control-state lives only in memory. Pending gates, the claim, control fields and the event
cursor all sit on the `verse.task.instance` entity, and a restart re-reads them.

`verse up` reconciles, and resume is not orphan. An in-process agent whose runtime died is orphaned,
because the loop is gone. A detached or claimed task — a human still merging, a k8s Job still
running — resumes: the work continued in the world while verse was down, so on `up` verse
re-attaches to its bound runner and reconciles from current status. Resume where the work outlived
the daemon; orphan only what died with it.

Open questions follow their asker rather than being judged on their own. A resumed task keeps its
pending gates on the docket under the same ids, and answering one still reaches the worker. An
orphaned task's go with it: settled `orphaned`, off the docket, still readable, because a question
listed as answerable when nothing will read the answer asks a person to decide into a void.

A gate a *firing* raised is settled unconditionally, because what waited on it was a goroutine
in the verse. If its rule still matches, the reinstalled charter replays and poses the same
question under the same id, and it is answerable again within milliseconds — which is what a
deterministic gate id is for. If the rule no longer matches, the question was about a world
that has moved on.

## Everything through the base URL — no privileged local path

The engine is a client of a bound base URL. It spawns there, subscribes to the event stream, and
mirrors the stream into the `verse.task.instance` entity, so durability lives in verse rather than
the runner. Which runner runs a given task is a per-spawn binding — `runner: <url>` in the charter
or spec, like `--driver` — and there is deliberately no in-memory shortcut: `verse task
cancel/say/pause` go through the same dispatch as a remote one, which forces the contract to be
complete and keeps local equivalent to remote.

Verse can mint that URL on its own broker face rather than requiring every runner to stand up a
service (see "Hosting"). The built-in local supervisor lives at the well-known
`<broker>/broker/local`, an in-process handler, and dynamically registered brokers get
`<broker>/broker/<nonce>`. So "no privileged local path" costs no separate loopback server: local
is the broker at a fixed nonce.

## The verse-facing contract

Two endpoints, which are `Observe : Since` once more, plus out-of-band control.

Every control call reports which of three things became of it: it was applied, the far side refused
it, or the far side was never reached. Only the third is worth retrying, and a caller that cannot
tell them apart either retries what already happened or abandons what never arrived. The
distinction is carried in the status, and across the Go seam as `task.ErrUnreachable` and friends.

```
GET    /tasks/{id}                 level truth: current snapshot
GET    /tasks/{id}/events?since=N  edge stream (SSE): replay cursor>N, then live
POST   /tasks/{id}/resolve  {id, choice, by, say?, key}      answer a gate: choice is proceed | deny, or an option the record names
       204 applied · 404 no such decision · 409 already answered · 502 runner unreachable
POST   /tasks/{id}/say      {text, by, key}                  say to it (re-prompt)
POST   /tasks/{id}/cancel   {by, key}
POST   /tasks/{id}/pause    {by}
POST   /tasks/{id}/resume   {by}
PUT    /tasks/{id}/vector   {vector, by}                     slide autonomy
POST   /tasks                {id?, prompt, subject, vector, claim?, runner-config}  spawn → {id}
                               (idempotent by id: 201 built it, 200 it was already running)
```

### The event envelope and the cursor

Every event carries a timestamp and a per-task, gapless, monotonic cursor: a plain sequence number
assigned by the runner, which need not be a global logd commit.

```json
{ "cursor": 42, "task": "review@demo.pr.1", "at": "…",
  "kind": "exec", "tool": "run_tests", "text": "2 failed" }
```

Verse persists its high-water cursor on the entity, so on restart it knows where to resume.

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

That table is the durable contract: `mirror.Reflect`, plus `claim` and `vector`, minus the `gap`
marker, which says something about the *reading* rather than about the task.

### `since` semantics and the two restarts

With N at or above the current cursor there is nothing to replay, so the stream goes live.

With N inside the retained window, the runner replays `cursor > N` in order and then goes
live. This is the verse-restart path: verse reconnects with its last mirrored cursor, the
still-running runner replays the gap, and the worker noticed nothing.

With N below the runner's low-watermark — compaction, or the runner-restart path where it lost its
log — the runner answers a single `{"kind":"gap","cursor":C}` frame. The reader re-reads
`GET /tasks/{id}` for level truth, adopts cursor `C`, and streams from there. Fine-grained journal
between N and C may be lost, but current state is exact.

Resync-to-snapshot is the universal safety net whenever a cursor cannot be served: verse restarts,
runner restarts, slow consumers, compaction. The same `gap` frame carries both reasons a reader can
be behind, a cursor below the window and events dropped because the reader could not keep up.

`?since=` means this on every runner. `GET /tasks/{id}/events` hands the cursor to whichever
runtime holds the work — the local one, a broker, a remote taskd — so a client that resumes gets
the frames after its cursor and no others, wherever the task runs.

A resume that falls below the window also replays the gates still open below it. The gap says a
reader missed something; this says what. It matters because a resolver is a client of this endpoint
and nothing else, so without it a reconnecting resolver learns it is behind and cannot see the
question it exists to answer. Only in the gap case: inside the window the replay already carries
them.

**A stream that ends without a terminal did not end because the task did.** The terminal is always
the last frame, so its absence means the connection broke, and the answer is to resume from the
last cursor rather than to conclude anything about the work. `mirror.Reflect` does exactly that,
backing off between attempts and publishing `observed: false` on the entity while it is
disconnected, because an index that reads as confident while it is merely stale is worse than one
that admits it.

The terminal is a `status` event — `done|failed|cancelled` plus `result` — that verse CAS-writes
once, and `?since=<terminal cursor>` replays it and closes. A finished task's `/events` is a
finite, replayable record, so a verse that restarts *after* completion still learns the outcome. A
reader that has seen the terminal should stop reconnecting, because there is nothing left to
reconnect to.

The terminal is the one frame a reader never loses. Everything else may be dropped to a
subscriber that cannot keep up — the loop is never stalled by an observer — and the `gap`
marker rides the next event to say so. Nothing rides after the terminal, so it is delivered
out of a slot reserved for it, and it reaches a reader that missed everything before it.

### The control⇄events interlock

Control ingress is a distinct, out-of-band `POST`, never a store write — the rule from
[deployment and steering](deployment-and-steering.md) — but its application is emitted as an event
on the one ordered stream. The POST is the request; the event is the truth.

The POST return is `202 Accepted`, and verse never assumes control took hold until the
corresponding event appears at a new cursor. That dissolves races: a `cancel` POSTed after
completion never yields a `cancelled` event, and a `resolve` for an already-timed-out gate comes
back as a `decision` marked stale, a recorded no-op rather than a silent drop. The POST does not
block on the effect, since a human's next boundary may be hours away.

One ordered stream interleaves reports and control-effects by cursor, giving a single total
order verse replays with nothing else to reconcile. Priority is that ordering: "steering
outranks observation" means the runner drains control at the boundary before the next action,
so a control-effect event precedes the action it gated in cursor order.

Control POSTs carry an idempotency `key` echoed on the resulting event, so a retried `say` or
`cancel` yields at most one event; verse also dedups by cursor.

The gate lifecycle, end to end, is entirely on the stream and fully resumable (a say-to is a
`say` event with `by` set — the operator's side of the conversation):

```
pending  @40 {id:G, gate:"deploy", mode:ask, tool:merge}   → verse mirrors pending[G]
   … verse routes G to a Resolver, gets a verdict …
POST /resolve {id:G, proceed:true, by:"human:scott", say:"squash first", key}   → 202
decision @41 {id:G, proceed:true, by:"human:scott"}        → verse clears pending[G]
say      @42 {by:"human:scott", text:"squash first"}        → folded in
exec     @43 {tool:merge, text:"merged"}                    → worker proceeded
```

Restart between @40 and @41 → verse replays `pending@40`, re-routes, re-POSTs `resolve`
with the same `key`; the runner re-surfaces `decision@41`; verse dedups by cursor. Replay +
idempotent re-POST converge — no lost gate, no double-merge.

## Invariants verse CAS-enforces on the entity

Regardless of a misbehaving runner or worker, three things hold on the `verse.task.instance`
entity: terminal-once (monotonic status), claim-holder-only writes, and
gate-before-the-effect — which for a detached or human worker is an attestation verse
timestamps and audits, since it cannot intercept the merge itself. External autonomy is
coarser by nature; that is stated, not hidden.

## Brokering: firewalled workers and humans, without inverting verse

A worker that cannot accept inbound connections — a laptop agent, a pod in a private cluster, a
person — does not force verse to invert. The broker, itself a backend, is the one reachable
rendezvous, and both verse and the worker dial out to it:

```
 verse ──POST/GET(events)/POST(control)──▶  broker (base URL)  ◀──claim/inbox/report── worker
 mirror → task:instance (durable truth)   per-task relay log       dials OUT (NAT-friendly)
```

The broker has two faces: the verse-facing contract above, where verse is a plain client, and a
worker-facing dial-in protocol it owns — claim, then inbox long-poll for spec and control, then
report. Its job is to glue a task's two halves. Worker reports become the events verse streams,
verse control becomes items the worker pulls, and a checkpoint the worker reports becomes an
`EvPending` verse routes to a resolver whose verdict flows back down the worker's inbox. Verse's
spawner does the same three things — spawn, stream, control — whether behind the URL is the
in-process loop, a reachable k8s runner, or a broker relaying to a laptop. Reachability is an
implementation detail behind the URL.

The two legs carry independent, resumable cursors — one on the verse leg, two on the worker leg,
see [the worker-facing dial-in protocol](#the-worker-facing-dial-in-protocol) below — each with the
same replay and resync discipline. The broker keeps a bounded per-task log to serve them, but the
entity remains verse's durable system of record. The broker is a relay, which is what lets a third
party run a cheap one.

The human is a broker whose worker is a person. The inbox long-poll is "check notifications, run
`verse task inbox`", `report` is their progress `say`, and pushed control is a notification or a PR
comment. That is why the human and the NAT'd agent are the same mechanism, and why designing the
human case first gets the agent for free.

## Hosting: verse's broker face (`/broker/<nonce>`)

A base URL does not have to be a new service. Verse can host a broker the way docd hosts a
controller: docd forwards a subtree's ops to a registered controller under a mount, and verse
forwards a task's worker and engine ops to a registered backend under `/broker/<nonce>`, the same
capability-URL trick [`connect`](working/connect.md) uses for `dir-content-<nonce>`. A runner
binding is then uniformly a URL: `<broker>/broker/local`, `<broker>/broker/<nonce>`, or an external
one. Verse consumes the verse-facing contract from it — as an in-process or forwarded call for a
hosted broker, over HTTP for an external one.

Two listeners, one deployment. `verse up` exposes the operator API on `--addr` — CLI,
entities, gates, task control, and broker registration (`POST /broker` mints a nonce),
trusted and internal — and the broker face on a sibling port, serving `/broker/<nonce>/...`
and the worker-facing dial-in (`claim`, `inbox`, `report`), nonce-guarded and reachable by
workers.

The sibling port keeps the operator-to-worker trust boundary crisp. Sharing verse's deployment for
brokering does not expose the operator API to the workers, because a worker only ever reaches its
nonce'd path, never the entity or gate surface.

The built-in local supervisor lives at the well-known `/broker/local`, a fixed nonce and an
in-process handler, so a default spawn binds to it with no config and the binding is stable across
restarts. Dynamically registered brokers get a random nonce, which is the capability: an in-process
plugin, or, in the docd-controller inversion, a dial-in registrant that connects out to verse's
registration, gets a nonce, and has its worker and engine ops forwarded to it. So a runtime that can
only dial out — a firewalled pod, a vendor behind NAT — still hosts a broker without a face of its
own.

Lifecycle follows the daemon-reaping and restart discipline already in place: `up` binds both
listeners and re-mints `/broker/local`, brokers register at runtime, `down` reaps them, and
`up` reconciles each non-terminal task from the runner binding on its entity — re-minting
the local, re-attaching an external, orphaning only a dead in-process handler.

The two topologies compose. A hosted nonce-broker can relay out to an external runtime: a
firewalled k8s pod dials `<broker>/broker/<nonce>`, and verse's registered handler drives the
Job, so the pod needs no face and neither does the vendor. A third party who wants their own
front door still binds an external URL directly.

## What a 3rd-party implementer must satisfy

Per task: assign gapless per-task cursors; serve `GET /tasks/{id}` as current truth; serve
`GET /events?since=` with replay-then-live and a `gap` frame below its watermark; emit the kinds
above; make every control POST idempotent by `key` and echo its application as an ordered event;
make the terminal a stable, replayable last event. Nothing about *how* it runs the work leaks
through, which is what makes local, k8s, lambda and a NAT'd broker interchangeable behind the
URL.

## The worker-facing dial-in protocol

The verse-facing contract stands on its own, and local, k8s and lambda policies need nothing below.
But a runner working through a party that cannot accept inbound connections — a laptop agent, a pod
in a private cluster, a person — needs a way for that worker to dial out and still be driven. The
worker leg is the verse leg inverted: verse pulls reports and pushes control, the worker pushes
reports and pulls control. Both only ever dial out, and the broker is the one reachable rendezvous.
This is the standard worker protocol, so a write-once worker codes against it and runs under any
broker. A broker may also offer a private optimized transport over WebSocket or gRPC, with this
HTTP form as the interop baseline.

Three endpoints, worker as client:

```
POST /tasks/{id}/claim   {identity}                      → {spec, cCursor, rCursor}
GET  /tasks/{id}/inbox?since={cCursor}   (long-poll/SSE)  → control directed at the worker
POST /tasks/{id}/report  {events:[…], key?, epoch}        → {rCursor, applied}
```

`inbox` is the dual of verse's `/events`: a resumable stream of control directed at the worker —
`spec` once, `say {text, by}`, `resolve {decision, choice, proceed, say?, by}`, `cancel|pause|resume`, `vector`.
Each item carries a control cursor, and the worker resumes with `?since=cCursor`.

An answer is a decision when the worker has taken it, not when it was queued. `resolve` closes the
gate at once — a second answerer is told somebody was here first, because two answers to one
question is a race with one winner however slowly the answer travels — and the `decision` event is
emitted when the worker's inbox poll acknowledges the item carrying it. The acknowledging poll is
the one that asks for what comes *after* the item rather than the one that returns it: receiving is
not taking, since that response is exactly what a lost one looks like. Until then the question is
answered-and-undelivered, and if the claim expires first the answer was never delivered at all.

A claim is a lease. It survives `broker.ClaimLease` without the holder being heard from, and
the renewal is the work itself — every inbox poll and every report says the holder is alive,
so a worker that talks to the broker at all is already keeping it. An expired claim is
released when somebody next asks for the work, and the silent holder is fenced: if it comes
back it comes back as a stranger. The task is not made terminal, because a broker that
cannot hear a worker knows nothing about whether the work is running — only that nobody may
claim to speak for it.

This is the only place in the task-control seams where a clock decides anything. One clock,
the broker's, comparing two readings of its own; timestamps written elsewhere are records and
never inputs.

`cCursor` is what this worker last acknowledged, not where the broker has got to: a poll for
`since=N` is the acknowledgement of everything at or below N, because a worker could only ask
for what came after having taken what came before. So a reconnecting worker is handed the
place it actually reached, and control issued during its outage is still waiting for it.

A worker that asks to go backwards is served from where it got to rather than from where it
asked. The broker knows what this claim acknowledged, so a worker that lost its cursor does
not have to remember — what it has not taken is still waiting, and what it has is not handed
back to be applied twice. A takeover resets that mark: what a different worker acknowledged
says nothing about what this one has done, and an acknowledgement was never a promise that
the work was carried out.

What remains the worker's own half is the window between receiving control and acknowledging
it. A worker that pulled an item, acted on it, and died before its next poll will be handed
that item again — so control a worker cannot afford to apply twice should be applied
idempotently by the worker, which is the only party that can.

`key` makes a report retryable. A worker whose response was lost cannot know whether its events
landed, and without one its only choices are to drop what it observed or to report it twice, one
deploy becoming two events at two cursors, afterwards indistinguishable from the work having been
done twice. A repeat of a key already applied is answered with what the first one was answered
with, not applied again. It is optional, and necessarily so: only the worker knows a retry is a
retry, and one that sends no key gets a fresh report every time.

`applied` is how many of the reported events were taken, which is not always all of them:
nothing follows a terminal, so a batch whose terminal is not its last entry has the remainder
dropped. A worker computes its retry from this rather than assuming a 200 means everything
landed.

`report` is the dual of verse's control POSTs: the worker appends `pending`, its own `say`,
`exec`, `status` transitions, and `boundary`. Each gets a report cursor, and `from: rCursor`
makes a re-POST idempotent — the broker appends only past its high-water and re-acks.

### The two cursors on this leg

The verse leg has one cursor, verse's read position on the event log. The worker leg has two,
because the worker both reads and writes: `cCursor` is its position in the inbox (control
pulled), so it reconnects with `inbox?since=cCursor`, and `rCursor` is its high-water in what
it has reported and the broker acked, so it reconnects with `report {from: rCursor}`.

Each is independently resumable, so there are three resume points — verse's read, the worker's
read, the worker's write — over one durable truth, the `verse.task.instance` entity. A worker that
sleeps and reconnects resumes both of its cursors without verse noticing, and a verse that restarts
re-reads `/events` without the worker noticing. All three use the same
replay-then-live-with-resync-below-watermark discipline.

### How the broker maps the worker leg onto the verse leg

The broker keeps one ordered event log per task — what `/events` streams — and sequences
into it from two interleaved, cursored sources.

Worker reports (`POST /report`) are sequenced into the log, and verse's next `/events?since`
delivers them. A report at `rCursor=R` becomes a merged-log entry at a broker-assigned
`cursor=V`; the axes relate only through the broker's sequencer.

Control-origin events arrive when verse POSTs control: the broker both records the ordered
event and relays the item to the worker's inbox at a new `cCursor`. One fact, two readers.

Who authors an effect event follows the interlock rule: truth is what was applied.
Operator-authored events — a `decision` from `/resolve`, a `vector` from set-vector, an operator
`say` — are emitted by the broker on the POST, because the operator or resolver acted, and the item
is relayed for the worker to act on. There is no duplication: the `say` is the operator speaking,
and the worker's resulting action is a separate `exec`.

Worker-confirmed events — `cancelled`, `paused`, `resumed` — work the other way. The broker
only relays `cancel` and `pause`; the effect event is the worker's report when it stops at its
boundary. Until then verse sees "cancel requested" in the snapshot, and `status:cancelled`
lands on confirmation. That coarse latency is stated rather than hidden.

A gate round-trips across both legs:

```
worker POST /report pending@R7      → broker seq → events@V40 → verse mirrors pending[G]
verse routes, resolves, POST /resolve {G, proceed, say:"squash first"}
  broker: events@V41 decision{by:human:scott}      (verse: cleared)
          events@V42 say{by:human:scott,"squash first"}
          inbox@C3   resolve{G, proceed, say:"squash first"}   (relayed to worker)
worker GET /inbox?since=C2 → C3 → proceeds → POST /report exec@R8  (→ events@V43)
```

### Claim, identity, takeover

The identity on a claim is the principal the worker acts as from then on: verse lists it as a
runner, grants it `read-write` and `control` on the task and on this run, and the mirror writes
every report as it ([perms.md](perms.md)). It holds nothing on the questions the task raises.

`POST /tasks/{id}/claim {identity}` is a CAS on the claim, and on success it returns the `spec` and
resume cursors. Claimed by another gives `409` unless this is a takeover: the prior claim is
revoked, its inbox getting a `revoked` item so a still-connected old worker stops, and the new
identity claims, reads the journal — already durable, and the handoff context — and continues on
the same subject. The claim is mirrored on the entity, so who holds it is observable, which makes a
human taking over a runaway agent a plain claim transfer. A pool variant,
`POST /claim {identity, kinds}`, long-polls for the next eligible unclaimed task and CAS-assigns
one, so N workers drain a queue without stepping on each other.

The human is this protocol at human pace: `claim` is "I'll take it" from a CLI or bot, the `inbox`
pull is "check notifications", and `report` is progress `say` plus checkpoints. Three identical
endpoints at a person's cadence.

## What is built

Everything above is built except where noted here. The seam itself, `task.Runner` and
`task.Handle`, carries every task, so the engine holds a handle and never a concrete `*Task`.
Events carry a per-task cursor with a bounded ring, `GET /events?since=N` replays and then goes
live, and verse persists its high-water cursor on the entity. `runner.HTTP(baseURL)` drives a
remote verse over the verse-facing contract. Startup reconcile resumes rather than orphans where
the work outlived the daemon. The dial-in broker is hosted under `/broker/<nonce>`, publishes each
task's `dialURL` on its entity, and is guarded by the terminal-once latch and the claim fence.
`verse task do` is the executor vantage, and every operator verb works across local, remote and
brokered tasks alike.

Not built: the inbox long-poll, so a worker polls. Two larger tracks sit outside this document:
a human surfacer and identity-projected inbox, with `owner`, `claimant` and `resolver` stamps,
and a k8s runner that spawns Jobs behind the broker, alongside `k8s:*` as a
[Source](controllers.md).
