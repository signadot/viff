# Process control: a base-URL contract for running and governing processes

The contract behind `verse up --runner <url>` and `verse up --broker`. Walk it first in
[reference/processes.md](reference/processes.md#executing-one-by-hand), where a human executes a
process by dialing in; this is the seam that makes that work. It builds on
[external processes](external-processes.md) (design D, detached-core-first) and makes its
"wrapping and enforcing the verse invariants" concrete.

In one line: the process-control interface is the `processd` HTTP contract, made resumable
and claim-aware, behind a base URL. verse is its client and mirrors it into the durable
`process:instance` entity; the local supervisor is one built-in implementation of the same
URL contract with no privileged shortcut; and anyone else (k8s, AWS Lambda, a human-workflow
service, a custom runtime) plugs in a URL, completely outside verse.

Most of what follows is built — see [what is built](#what-is-built) at the end for the parts
that are not.

## A note on the name

This interface governs how one *process* runs. It is not a general control policy — nothing
here has an opinion about the engine, its charter, or how a firing is gated. Keeping those
distinct matters, because a policy layer over the engine is a plausible thing to want later
and would collide head-on.

So the two dials each carry their own name, and neither of them is "policy": what runs a
process is its *runner*, and the autonomy dial is its *vector*.

| meaning | interface | wire | flag |
|---|---|---|---|
| what runs a process | `process.Runner`, returning a `process.Handle` | `runner` on a `process:instance` and on `verse status` | `verse up --runner <url>` |
| how much it may do unasked | `process.Vector` | `vector` on a launch spec | `verse process vector <id>` |

## What the interface is

verse defines a process not as "an LLM loop" but as a contract: a driver proposes, an
executor performs, effects are gated by a vector at boundaries, a read plane streams events,
a control plane steers at boundaries, and the whole thing is mirrored to a `process:instance`
entity and ends with a terminal. Process control is the durable generalization of the
in-process `Supervisor` that enforces that contract.

In-process, enforcement is interception: the loop cannot `Execute` an action until
`Supervisor.Check` (derive `Effect` → `Vector.Evaluate` → `Resolver.Resolve`) returns
proceed. verse is trusted by construction.

Out-of-process, enforcement becomes authorize, checkpoint, attest, and CAS-invariants. verse
cannot intercept a remote or human worker's every action, so it authorizes the claim (who may
drive), gates the checkpoints the worker reports (the same vector, applied to *reported*
effects), records reported effects as audited attestations, and CAS-enforces the structural
invariants on the entity — terminal-once, claim-holder-only writes, gate-before-the-effect.

So it is one control contract with three enforcement modes: the local supervisor
(interception), a spawned worker on k8s, lambda, or a host (attestation), and a human
(surfaced attestation). Same vector, gates, resolver, boundaries, terminal — only the
mechanism differs. Takeover falls out for free: `cancel` the agent, a human re-claims (CAS
claim transfer plus identity), reads the journal (durable handoff), and continues on the same
subject under the same runner.

A process-control backend is a reconciler over the `process:instance` resource, not a gated process
itself — which stops the regress, since governing a process must not require another governed
process. It is the dual of a [Source](sources.md): a Source is a controller that reflects
external state *into* the verse; a process-control backend is a controller that runs and governs *work*
on a substrate. Both are docd-controller-shaped, both reconcile a resource, both must be
idempotent. And k8s sits on both sides — `k8s:*` as a Source reflecting the cluster in, and
k8s as a process-control backend running processes as Jobs out.

## Idempotency ⇒ the restart discipline (`verse down; up`)

Because it is a reconciler, a backend must be level-triggered and reconstructable from
the durable entity — the same `Observe:Since` / `webhook:reconcile` law verse runs everywhere.
Two rules follow.

No process control-state lives only in memory. Pending gates, the claim, control fields, the
event cursor all sit on the `process:instance` entity, and a restart re-reads them.

`verse up` reconciles, and resume is not orphan. An in-process agent whose runtime died is
orphaned, because the loop is gone. A detached or claimed process — a human still merging, a
k8s Job still running — resumes: the work continued in the world while verse was down, so on
`up` verse re-attaches to its bound runner and reconciles from current status. Startup
reconcile is therefore "resume where the work outlived the daemon; orphan only what died
with it."

Open questions follow their asker rather than being judged on their own. A resumed process
keeps its pending gates on the docket, with the same ids, and answering one still reaches the
worker. An orphaned process's go with it — settled `orphaned`, off the docket, still readable,
because a question listed as answerable when nothing will read the answer asks a person to
decide into a void.

A gate a *firing* raised is settled unconditionally, because what waited on it was a goroutine
in the verse. If its rule still matches, the reinstalled charter replays and poses the same
question under the same id, and it is answerable again within milliseconds — which is what a
deterministic gate id is for. If the rule no longer matches, the question was about a world
that has moved on.

## Everything through the base URL — no privileged local path

The engine is a client of a bound base URL. It spawns there, subscribes to the event stream,
and mirrors the stream into the `process:instance` entity — so durability lives in verse, not
the runner. Which runner runs a given process is a per-spawn binding (`runner: <url>` in the
charter or spec, like `--driver`), and there is deliberately no in-memory shortcut:
`verse process cancel/say/pause` go through the same dispatch as a remote one, which is what
forces the contract to be complete and keeps local equivalent to remote.

Crucially, verse can mint that URL on its own broker face rather than requiring every
runner to stand up a service (see "Hosting"): the built-in local supervisor lives at the
well-known `<broker>/broker/local` (an in-process handler), and dynamically registered brokers
get `<broker>/broker/<nonce>`. So "no privileged local path" costs no separate loopback
server — local is simply the broker at a fixed nonce.

## The verse-facing contract

Two endpoints, which are `Observe : Since` once more, plus out-of-band control.

Every control call reports which of three things became of it: it was applied, the far side
REFUSED it, or the far side was never reached. Only the third is worth retrying, and a caller
that cannot tell them apart either retries what already happened or abandons what never
arrived — so the distinction is carried in the status, and across the Go seam as
`process.ErrUnreachable` and friends.

```
GET    /processes/{id}                 level truth: current snapshot
GET    /processes/{id}/events?since=N  edge stream (SSE): replay cursor>N, then live
POST   /processes/{id}/resolve  {id, approve, by, say?, key}     answer a gate
       204 applied · 404 no such decision · 409 already answered · 502 runner unreachable
POST   /processes/{id}/say      {text, by, key}                  say to it (re-prompt)
POST   /processes/{id}/cancel   {by, key}
POST   /processes/{id}/pause    {by}
POST   /processes/{id}/resume   {by}
PUT    /processes/{id}/vector   {vector, by}                     slide autonomy
POST   /processes                {id?, task, subject, vector, claim?, runner-config}  spawn → {id}
                               (idempotent by id: 201 built it, 200 it was already running)
```

### The event envelope and the cursor

Every event carries a per-process, gapless, monotonic cursor — a plain sequence number
assigned by the runner, which need not be a global logd commit — and a timestamp:

```json
{ "cursor": 42, "process": "review@demo:pr:1", "at": "…",
  "kind": "exec", "tool": "run_tests", "text": "2 failed" }
```

verse persists its high-water cursor on the entity, so on restart it knows exactly where to
resume.

### The canonical kinds → what verse mirrors

| `kind` | key fields | entity projection |
|---|---|---|
| `claim` | `by, identity, at` | `claim {by, identity, at}` (CAS; authorizes the driver) |
| `boundary` | `seq` | a safe point where control lands |
| `say` | `text, by` | an utterance: `by` empty = the agent's narration → `say` preview; `by` set = a say-to folded in → journal only |
| `exec` | `tool, text, err` | `lastAction`, `lastOutput` + journal |
| `pending` | `id, gate, mode, tool, reason` | `pending {id, gate}` — verse routes it to a Resolver |
| `decision` | `id, gate, mode, proceed, by, reason` | clears `pending`; records the resolution |
| `child` | `child, status` | flow-tracking (sub-process spawn/join) |
| `vector` | `by` | records an autonomy slide (the new vector is observable) |
| `status` | `text: running\|paused\|resumed\|done\|failed\|cancelled`, `result?` | `status`, `paused`; on terminal `result` (CAS-once) |

That table is the durable contract — it is `mirror.Reflect`, plus `claim`/`vector`, minus the
`gap` marker, which says something about the *reading* rather than about the process.

### `since` semantics and the two restarts

With N at or above the current cursor there is nothing to replay, so the stream goes live.

With N inside the retained window, the runner replays `cursor > N` in order and then goes
live. This is the verse-restart path: verse reconnects with its last mirrored cursor, the
still-running runner replays the gap, and the worker noticed nothing.

With N below the runner's low-watermark — compaction, or the runner-restart path where it lost
its log — the runner answers a single `{"kind":"gap","cursor":C}` frame; the reader re-reads
`GET /processes/{id}` for level truth, adopts cursor `C`, and streams from there. Fine-grained
journal between N and C may be lost, but current state is exact.

Resync-to-snapshot is therefore the universal safety net whenever a cursor cannot be served —
verse restarts, runner restarts, slow consumers, compaction. The same `gap` frame carries both
reasons a reader can be behind: a cursor below the window, and events dropped because the
reader could not keep up.

`?since=` means this on every runner. The unified `GET /processes/{id}/events` hands the cursor
to whichever runtime holds the work — the local one, a broker, a remote processd — so a client
that resumes gets the frames after its cursor and no others, wherever the process happens to
run.

The terminal is a `status` event (`done|failed|cancelled` plus `result`) that verse CAS-writes
once; `?since=<terminal cursor>` replays it and closes. A finished process's `/events` is a
finite, replayable record, so a verse that restarts *after* completion still learns the
outcome — and a reader that has seen the terminal should stop reconnecting, because there is
nothing left to reconnect to.

The terminal is the one frame a reader never loses. Everything else may be dropped to a
subscriber that cannot keep up — the loop is never stalled by an observer — and the `gap`
marker rides the next event to say so. Nothing rides after the terminal, so it is delivered
out of a slot reserved for it, and it reaches a reader that missed everything before it.

### The control⇄events interlock

Control ingress is a distinct, out-of-band `POST`, never a store write — the rule from
[deployment and steering](deployment-and-steering.md) — but its application is emitted as an
event on the one ordered stream. The POST is the request; the event is the truth.

The stream is truth and the POST return is just `202 Accepted`. verse never assumes control
took hold until the corresponding event appears at a new cursor. This dissolves races: a
`cancel` POSTed after completion simply never yields a `cancelled` event, and a `resolve` for
an already-timed-out gate comes back as a `decision` marked stale — a recorded no-op, not a
silent drop. The POST does not block on the effect, since a human's next boundary may be hours
away.

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

Regardless of a misbehaving runner or worker, three things hold on the `process:instance`
entity: terminal-once (monotonic status), claim-holder-only writes, and
gate-before-the-effect — which for a detached or human worker is an attestation verse
timestamps and audits, since it cannot intercept the merge itself. External autonomy is
coarser by nature; that is stated, not hidden.

## Brokering: firewalled workers and humans, without inverting verse

A worker that cannot accept inbound connections — a laptop agent, a pod in a private cluster,
a person — does not force verse to invert. The broker, itself a backend, is the one
reachable rendezvous, and both verse and the worker are clients that dial out to it:

```
 verse ──POST/GET(events)/POST(control)──▶  broker (base URL)  ◀──claim/inbox/report── worker
 mirror → process:instance (durable truth)   per-proc relay log        dials OUT (NAT-friendly)
```

The broker has two faces: the verse-facing contract above, where verse is a plain client, and
a worker-facing dial-in protocol it owns — claim, then inbox long-poll for spec and control,
then report. Its whole job is to glue a process's two halves: worker reports become the events
verse streams, verse control becomes items the worker pulls, and a checkpoint the worker
reports becomes an `EvPending` verse routes to a resolver whose verdict flows back down the
worker's inbox. verse's spawner does the same three things — spawn, stream, control — whether
behind the URL is the in-process loop, a reachable k8s runner, or a broker relaying to a
laptop. Reachability is an implementation detail behind the URL.

The two legs carry independent, resumable cursors (one on the verse leg, two on the worker
leg — see [the worker-facing dial-in protocol](#the-worker-facing-dial-in-protocol) below),
each with the same replay and resync discipline. The broker keeps a bounded per-process log to
serve them, but the entity remains verse's durable system of record: the broker is a relay,
which is what lets a third party run a cheap one.

The human is just a broker whose worker is a person. The inbox long-poll is "check
notifications, run `verse process inbox`", `report` is their progress `say`, and pushed control
is a notification or a PR comment. That is why the human and the NAT'd agent are the same
mechanism, and why designing the human case first gets the agent for free.

## Hosting: verse's broker face (`/broker/<nonce>`)

A base URL does not have to be a new service. verse can host a broker the way docd hosts a
controller: docd forwards a subtree's ops to a registered controller under a mount, and verse
forwards a process's worker and engine ops to a registered backend under
`/broker/<nonce>` — the same capability-URL trick [`connect`](working/connect.md) uses for
`dir-content-<nonce>`. A runner binding is then uniformly a URL: `<broker>/broker/local`,
`<broker>/broker/<nonce>`, or an external one. verse consumes the verse-facing contract from
it, as an in-process call or a forwarded one for a hosted broker, over HTTP for an external
one.

Two listeners, one deployment. `verse up` exposes the operator API on `--addr` — CLI,
entities, gates, process control, and broker registration (`POST /broker` mints a nonce),
trusted and internal — and the broker face on a sibling port, serving `/broker/<nonce>/...`
and the worker-facing dial-in (`claim`, `inbox`, `report`), nonce-guarded and reachable by
workers.

The sibling port keeps the operator-to-worker trust boundary crisp: sharing verse's deployment
for brokering does not expose the operator API to the workers, because a worker only ever
reaches its nonce'd path, never the entity or gate surface.

The built-in local supervisor lives at the well-known `/broker/local` — a fixed nonce and an
in-process handler — so a default spawn binds to it with zero config and the binding is stable
across restarts. Dynamically registered brokers get a random nonce, which is the capability:
an in-process plugin, or, in the docd-controller inversion, a dial-in registrant that connects
out to verse's registration, gets a nonce, and has its worker and engine ops forwarded to it.
So a runtime that can only dial out — a firewalled pod, a vendor behind NAT — still hosts a
broker without a face of its own.

Lifecycle follows the daemon-reaping and restart discipline already in place: `up` binds both
listeners and re-mints `/broker/local`, brokers register at runtime, `down` reaps them, and
`up` reconciles each non-terminal process from the runner binding on its entity — re-minting
the local, re-attaching an external, orphaning only a dead in-process handler.

The two topologies compose. A hosted nonce-broker can relay out to an external runtime: a
firewalled k8s pod dials `<broker>/broker/<nonce>`, and verse's registered handler drives the
Job, so the pod needs no face and neither does the vendor. A third party who wants their own
front door still binds an external URL directly.

## What a 3rd-party implementer must satisfy

Per process: assign gapless per-process cursors; serve `GET /processes/{id}` as current
truth; serve `GET /events?since=` with replay-then-live and a `gap` frame below its
watermark; emit the kinds above; make every control POST idempotent (by `key`) and echo its
application as an ordered event; make the terminal a stable, replayable last event. Nothing
about *how* it runs the work leaks through — which is what makes local, k8s, lambda, and a
NAT'd broker interchangeable behind the URL.

## The worker-facing dial-in protocol

The verse-facing contract stands on its own; local, k8s, and lambda policies need nothing
below. But a runner that runs work on a party which cannot accept inbound connections — a
laptop agent, a pod in a private cluster, a person — needs a way for that worker to dial out
and still be driven. The worker leg is the verse leg inverted: verse pulls reports and pushes
control, the worker pushes reports and pulls control. Both only ever dial out, and the broker
is the one reachable rendezvous. This is the standard worker protocol, so a write-once worker
codes against it and runs under any broker; a broker may also offer a private optimized
transport over WebSocket or gRPC, with this HTTP form as the interop baseline.

Three endpoints, worker as client:

```
POST /processes/{id}/claim   {identity}                      → {spec, cCursor, rCursor}
GET  /processes/{id}/inbox?since={cCursor}   (long-poll/SSE)  → control directed at the worker
POST /processes/{id}/report  {from: rCursor, events:[…]}      → {rCursor, applied}
```

`inbox` is the dual of verse's `/events`: a resumable stream of control directed at the
worker — `spec` (once), `say {text, by}`, `resolve {gateId, proceed, say?}`,
`cancel|pause|resume`, `vector`. Each item carries a control cursor, and the worker resumes
with `?since=cCursor`.

`cCursor` is what this worker last ACKNOWLEDGED, not where the broker has got to: a poll for
`since=N` is the acknowledgement of everything at or below N, because a worker could only ask
for what came after having taken what came before. So a reconnecting worker is handed the
place it actually reached, and control issued during its outage is still waiting for it.

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
read, the worker's write — over one durable truth, the `process:instance` entity. A worker that
sleeps and reconnects resumes both of its cursors without verse noticing; a verse that restarts
re-reads `/events` without the worker noticing. All three use the same
replay-then-live-with-resync-below-watermark discipline.

### How the broker maps the worker leg onto the verse leg

The broker keeps one ordered event log per process — what `/events` streams — and sequences
into it from two interleaved, cursored sources.

Worker reports (`POST /report`) are sequenced into the log, and verse's next `/events?since`
delivers them. A report at `rCursor=R` becomes a merged-log entry at a broker-assigned
`cursor=V`; the axes relate only through the broker's sequencer.

Control-origin events arrive when verse POSTs control: the broker both records the ordered
event and relays the item to the worker's inbox at a new `cCursor`. One fact, two readers.

Who authors an effect event follows the interlock rule, that truth is what was actually
applied. Operator-authored events — a `decision` from `/resolve`, a `vector` from set-vector,
an operator `say` — are emitted by the broker on the POST, because the operator or resolver
acted, and the item is relayed for the worker to act on. There is no duplication: the `say` is
the operator speaking, and the worker's resulting action is a separate `exec`.

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

`POST /processes/{id}/claim {identity}` is a CAS on the claim, and on success it returns the
`spec` and resume cursors. Claimed by another gives `409` unless this is a takeover: the prior
claim is revoked (its inbox gets a `revoked` item so a still-connected old worker stops), the
new identity claims, reads the journal — already durable, and the handoff context — and
continues on the same subject. The claim is mirrored on the entity, so who holds it is
observable, which makes the human-takes-over-a-runaway-agent story a plain claim transfer. A
pool variant, `POST /claim {identity, kinds}`, long-polls for the next eligible unclaimed
process and CAS-assigns one, so N workers drain a queue without stepping on each other.

The human is this protocol at human pace: `claim` is "I'll take it" from a CLI or bot, the
`inbox` pull is "check notifications", and `report` is progress `say` plus checkpoints.
Identical three endpoints, a person's cadence — which is why the NAT'd agent and the human come
out the same, and why designing the human case first gets the agent for free.

## What is built

Everything above is built except where noted here. The seam itself
(`process.Runner`/`process.Handle`) carries every process, so the engine holds a handle and
never a concrete `*Process`. Events carry a per-process cursor with a bounded ring, `GET /events?since=N`
replays and then goes live, and verse persists its high-water cursor on the entity.
`runner.HTTP(baseURL)` drives a remote verse over the verse-facing contract. Startup
reconcile resumes rather than orphans where the work outlived the daemon. The dial-in broker
is hosted under `/broker/<nonce>`, publishes each process's `dialURL` on its entity, and is
guarded by the terminal-once latch and the claim fence. `verse process do` is the executor
vantage; every operator verb works across local, remote, and brokered processes alike.

Not built: the inbox long-poll (the worker polls today). Two larger tracks sit outside this
document — a human surfacer and identity-projected inbox, with `owner`/`claimant`/`resolver`
stamps, and a k8s runner that spawns Jobs behind the broker, alongside `k8s:*` as a
[Source](controllers.md).
