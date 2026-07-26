# The controlPolicy: a base-URL contract for running and governing processes

Status: design. Builds on `docs/external-processes.md` (Design D — detached-core-first)
and makes its "controlPolicy wrapping and enforcing the verse invariants" concrete.

**In one line:** a controlPolicy is the `processd` HTTP contract — made **resumable** and
**claim-aware** — behind a **base URL**. verse is its *client* and mirrors it into the
durable `process:instance` entity; the local supervisor is just one built-in
implementation of the same URL contract with no privileged shortcut; and anyone else
(k8s, AWS Lambda, a human-workflow service, a custom runtime) plugs in a URL, completely
outside verse.

## What a controlPolicy is

verse already defines a process not as "an LLM loop" but as a **contract** (driver
proposes, executor performs, effects are gated by a vector at boundaries, a read plane
streams events, a control plane steers at boundaries, the whole thing is mirrored to a
`process:instance` entity and ends with a terminal). A controlPolicy is the durable
generalization of the in-process **`Supervisor`** that enforces that contract:

- **In-process, enforcement is interception** — the loop cannot `Execute` an action until
  `Supervisor.Check` (derive `Effect` → `Vector.Evaluate` → `Resolver.Resolve`) returns
  proceed. verse is trusted by construction.
- **Out-of-process, enforcement becomes authorize + checkpoint + attest + CAS-invariants** —
  verse can't intercept a remote/human's every action, so it **authorizes the claim** (who
  may drive), gates the **checkpoints** the worker reports (the same vector, applied to
  *reported* effects), records reported effects as audited **attestations**, and
  CAS-enforces the structural invariants on the entity (terminal-once, claim-holder-only
  writes, gate-before-the-effect).

So it is **one control contract with three enforcement modes**: the local supervisor
(interception), a spawned worker on k8s/lambda/a host (attestation), and a human (surfaced
attestation). Same vector, gates, resolver, boundaries, terminal — only the mechanism
differs. Takeover falls out for free: `cancel` the agent, a human re-claims (CAS claim
transfer + identity), reads the journal (durable handoff), and continues on the same
subject under the same policy.

A controlPolicy is a **reconciler/controller over the `process:instance` resource**, not a
gated process itself — which stops the regress (governing a process must not require
another governed process). It is the **dual of a Source** (`docs/sources.md`): a Source is
a controller that reflects external state *into* the verse; a controlPolicy is a controller
that runs and governs *work* on a substrate. Both are docd-controller-shaped, both
reconcile a resource, both must be idempotent. And **k8s sits on both sides** — `k8s:*` as
a Source (reflect the cluster in) and k8s as a controlPolicy backend (run processes as
Jobs out).

## Idempotency ⇒ the restart discipline (`verse down; up`)

Because a controlPolicy is a reconciler, it must be **level-triggered and reconstructable
from the durable entity** — the same `Observe:Since` / `webhook:reconcile` law verse runs
everywhere. Two rules follow:

1. **No process control-state lives only in memory.** Pending gates, the claim, control
   fields, the event cursor — all in the `process:instance` entity. A restart re-reads them.
2. **`verse up` reconciles; resume ≠ orphan.** An **in-process** agent whose runtime died →
   orphan (the loop is gone). A **detached/claimed** process — a human still merging, a k8s
   Job still running — **resume**: the work continued in the world while verse was down, so
   on up verse re-attaches to its bound policy and reconciles from current status. This
   refines today's `ReconcileOrphans` into "resume where the work outlived the daemon."

## Everything through the base URL — no privileged local path

verse (the engine) is a **client of a bound base URL**. It spawns there, subscribes to the
event stream, and **mirrors the stream into the `process:instance` entity** — so durability
lives in verse, not the policy. Which policy runs a given process is a **per-spawn binding**
(`policy: <url>` in the charter/spec, like today's `--driver`), and there is deliberately **no
in-memory shortcut** — `verse process cancel/say/pause` go through the same dispatch as a
remote one, which is what forces the contract to be complete and keeps local ≡ remote.

Crucially, verse can **mint** that URL on its own **broker face** rather than requiring every
policy to stand up a service (see "Hosting"): the built-in local supervisor lives at the
well-known `<broker>/broker/local` (an in-process handler), and dynamically registered brokers
get `<broker>/broker/<nonce>`. So "no privileged local path" costs no separate loopback
server — local is simply the broker at a fixed nonce.

## The verse-facing contract

Two endpoints, which are `Observe : Since` once more, plus out-of-band control:

```
GET    /processes/{id}                 level truth: current snapshot
GET    /processes/{id}/events?since=N  edge stream (SSE): replay cursor>N, then live
POST   /processes/{id}/resolve  {id, proceed, by, say?, key}     answer a gate
POST   /processes/{id}/say      {text, by, key}                  say to it (re-prompt)
POST   /processes/{id}/cancel   {by, key}
POST   /processes/{id}/pause    {by}
POST   /processes/{id}/resume   {by}
PUT    /processes/{id}/vector   {vector, by}                     slide autonomy
POST   /processes                {task, subject, vector, claim?, policy-config}  spawn → {id}
```

### The event envelope and the cursor

Every event carries a **per-process, gapless, monotonic cursor** (a plain seq assigned by
the policy — it need not be a global logd commit) and a timestamp:

```json
{ "cursor": 42, "process": "review@demo:pr:1", "at": "…",
  "kind": "exec", "tool": "run_tests", "text": "2 failed" }
```

verse persists its high-water cursor **on the entity**, so on restart it knows exactly
where to resume.

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
| `status` | `text: running\|paused\|resumed\|done\|failed\|cancelled`, `result?` | `status`, `paused`; on terminal `result` (**CAS-once**) |

That table *is* the durable contract — it is today's `mirror.Reflect`, plus `claim`/`vector`,
minus the live-only `gap` marker (unneeded; see resync below).

### `since` semantics and the two restarts

- **N ≥ current** → nothing to replay; stream live.
- **N within the retained window** → replay `cursor > N` in order, then live. This is the
  **verse-restart** path: verse reconnects with its last mirrored cursor, the still-running
  policy replays the gap, the worker noticed nothing.
- **N below the policy's low-watermark** (compaction, or the **policy-restart** path where it
  lost its log) → the policy answers a single `{"kind":"resync","cursor":C}` frame; verse
  re-reads `GET /processes/{id}` (level truth), adopts cursor `C`, and streams from there.
  Fine-grained journal between N and C may be lost, but current *state* is exact.

So: **resync-to-snapshot is the universal safety net** whenever a cursor can't be served —
verse restarts, policy restarts, slow consumers, compaction. No `gap` marker, because verse
always pulls *from its cursor* rather than trusting a live tail.

The **terminal** is a `status` event (`done|failed|cancelled` + `result`) verse
CAS-writes once; `?since=<terminal cursor>` replays it and closes. A finished process's
`/events` is a finite, replayable record — a verse that restarts *after* completion still
learns the outcome.

### The control⇄events interlock

Control ingress is a distinct, out-of-band `POST` (never a store write — the
`deployment-and-steering.md` rule), but **its application is emitted as an event on the one
ordered stream**. The POST is the *request*; the event is the *truth*.

1. **The stream is truth; the POST return is just `202 Accepted`.** verse never assumes
   control took hold until the corresponding event appears at a new cursor. This dissolves
   races: a `cancel` POSTed after completion simply never yields a `cancelled` event; a
   `resolve` for an already-timed-out gate comes back as a `decision` marked stale — a
   recorded no-op, not a silent drop. The POST does **not** block on the effect (a human's
   next boundary may be hours away).
2. **One ordered stream interleaves reports and control-effects by cursor** — a single total
   order verse replays; nothing else to reconcile.
3. **Priority is ordering.** "Steering outranks observation" means the policy drains control
   at the boundary before the next action, so a control-effect event precedes the action it
   gated in cursor order.
4. **Idempotent under retry via `key`.** Control POSTs carry an idempotency key echoed on the
   resulting event; a retried `say`/`cancel` yields at most one event; verse also dedups
   by cursor.

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
with the same `key`; the policy re-surfaces `decision@41`; verse dedups by cursor. Replay +
idempotent re-POST converge — no lost gate, no double-merge.

## Invariants verse CAS-enforces on the entity

Regardless of a misbehaving policy or worker, on the `process:instance` entity: **terminal-once**
(monotonic status), **claim-holder-only writes**, and **gate-before-the-effect** — which for
a detached/human worker is an *attestation* verse timestamps and audits (it can't intercept
the merge itself). External autonomy is coarser by nature; that is stated, not hidden.

## Brokering: firewalled workers and humans, without inverting verse

A worker that can't accept inbound connections (a laptop agent, a pod in a private cluster,
a person) doesn't force verse to invert. The controlPolicy service (the "broker") is the one
reachable rendezvous; **both verse and the worker are clients that dial out to it**:

```
 verse ──POST/GET(events)/POST(control)──▶  broker (base URL)  ◀──claim/inbox/report── worker
 mirror → process:instance (durable truth)   per-proc relay log        dials OUT (NAT-friendly)
```

The broker has two faces: the **verse-facing** contract above (verse is a plain client), and
a **worker-facing** dial-in protocol it owns (claim → inbox long-poll for spec+control →
report). Its whole job is to glue a process's two halves: worker reports become the events
verse streams; verse control becomes items the worker pulls; a checkpoint the worker reports
becomes an `EvPending` verse routes to a resolver whose verdict flows back down the worker's
inbox. verse's spawner does the *same three things* — spawn, stream, control — whether behind
the URL is the in-process loop, a reachable k8s runner, or a broker relaying to a laptop.
Reachability is implementation detail behind the URL.

The two legs carry independent, resumable cursors (one on the verse leg, two on the worker leg
— see "The worker-facing dial-in protocol" below), each with the same replay/resync discipline;
the broker keeps a bounded per-process log to serve them, but the **entity remains verse's
durable system of record** — the broker is a relay, which is what lets a 3rd party run a cheap
one.

The **human is just a broker whose worker is a person**: the inbox long-poll is "check
notifications / run `verse task`", `report` is their progress `say`, pushed control is a
notification or PR comment. That is why the human and the NAT'd agent are the *same*
mechanism — and why designing the human case first gets the agent for free.

## Hosting: verse's broker face (`/broker/<nonce>`)

A base URL doesn't have to be a *new service*. verse can **host** a broker the way docd hosts
a controller: docd forwards a subtree's ops to a registered controller under a mount; verse
forwards a process's worker + engine ops to a registered controlPolicy under `/broker/<nonce>`
(the same capability-URL trick `connect` uses for `dir-content-<nonce>`). A policy binding is
then uniformly a URL — `<broker>/broker/local`, `<broker>/broker/<nonce>`, or an external URL —
and verse consumes the verse-facing contract from it (an in-process call or a forwarded one for
a hosted broker; HTTP for an external one).

**Two listeners, one deployment.** `verse up` exposes:

- the **operator API** on `--addr` — CLI, entities, gates, process control, and broker
  *registration* (`POST /broker` → mint a nonce). Trusted / internal.
- the **broker face** on a **sibling port** — serves `/broker/<nonce>/...`, the worker-facing
  dial-in (`claim`/`inbox`/`report`), nonce-guarded and reachable by workers.

The sibling port keeps the operator↔worker trust boundary crisp: sharing verse's deployment for
brokering does **not** expose the operator API to the workers — a worker only ever reaches its
nonce'd path, never the entity/gate surface.

**The built-in local supervisor lives at the well-known `/broker/local`** (a fixed nonce, an
in-process handler), so a default spawn binds to it with zero config and the binding is stable
across restarts. **Dynamically registered brokers get a random nonce** (the capability): an
in-process plugin, or — the docd-controller inversion — a **dial-in registrant** that connects
*out* to verse's registration, gets a nonce, and has its worker+engine ops forwarded to it. So
a runtime that can only dial out (a firewalled pod, a vendor behind NAT) still hosts a broker
without a face of its own.

**Lifecycle** follows the daemon-reaping + restart discipline already in place: `up` binds both
listeners and re-mints `/broker/local`; brokers register at runtime; `down` reaps them; `up`
reconciles each non-terminal process from the policy binding on its entity — re-mint the local,
re-attach an external, orphan only a dead in-process handler.

**The two topologies compose.** A hosted nonce-broker can relay out to an external runtime: a
firewalled k8s pod dials `<broker>/broker/<nonce>`, and verse's registered handler drives the
Job — the pod needs no face, and neither does the vendor. A 3rd party who wants their own front
door still binds an external URL directly.

## What a 3rd-party implementer must satisfy

Per process: assign gapless per-process cursors; serve `GET /processes/{id}` as current
truth; serve `GET /events?since=` with replay-then-live and a `resync` frame below its
watermark; emit the kinds above; make every control POST idempotent (by `key`) and echo its
application as an ordered event; make the terminal a stable, replayable last event. Nothing
about *how* it runs the work leaks through — which is what makes local, k8s, lambda, and a
NAT'd broker interchangeable behind the URL.

## The worker-facing dial-in protocol

The verse-facing contract stands on its own — local, k8s, and lambda policies need nothing
below. But a policy that runs work on a party which **can't accept inbound connections** (a
laptop agent, a pod in a private cluster, a person) needs a way for that worker to dial *out*
and still be driven. The worker leg is **the verse leg inverted**: verse pulls reports and
pushes control; the worker pushes reports and pulls control. Both only ever dial out; the
broker is the one reachable rendezvous. This is the *standard* worker protocol — a "write-once"
worker codes against it and runs under any broker; a broker may also offer a private optimized
transport (WebSocket/gRPC) with this HTTP form as the interop baseline.

Three endpoints, worker as client:

```
POST /processes/{id}/claim   {identity}                      → {spec, cCursor, rCursor}
GET  /processes/{id}/inbox?since={cCursor}   (long-poll/SSE)  → control directed at the worker
POST /processes/{id}/report  {from: rCursor, events:[…]}      → the worker's events
```

- **`inbox`** is the dual of verse's `/events`: a resumable stream of control *directed at the
  worker* — `spec` (once), `say {text, by}`, `resolve {gateId, proceed, say?}`,
  `cancel|pause|resume`, `vector`. Each item carries a control cursor; the worker resumes with
  `?since=cCursor`.
- **`report`** is the dual of verse's control POSTs: the worker appends `pending`, its own
  `say`, `exec`, `status` transitions, `boundary`. Each gets a report cursor; `from: rCursor`
  makes a re-POST idempotent (the broker appends only past its high-water and re-acks).

### The two cursors on this leg

The verse leg has **one** cursor (verse's read position on the event log). The worker leg has
**two**, because the worker both reads and writes:

- **`cCursor`** — its position in the **inbox** (control pulled). Reconnect → `inbox?since=cCursor`.
- **`rCursor`** — its high-water in what it has **reported** (and the broker acked). Reconnect →
  `report {from: rCursor}`.

Each is independently resumable, so there are **three resume points — verse's read, the worker's
read, the worker's write — over one durable truth** (the `process:instance` entity). A worker
that sleeps and reconnects resumes both of its cursors without verse noticing; a verse that
restarts re-reads `/events` without the worker noticing. All three use the same
replay-then-live-with-resync-below-watermark discipline.

### How the broker maps the worker leg onto the verse leg

The broker keeps **one ordered event log per process** (what `/events` streams) and sequences
into it from **two interleaved, cursored sources**:

1. **Worker reports** (`POST /report`) → sequenced into the log; verse's next `/events?since`
   delivers them. A report at `rCursor=R` becomes a merged-log entry at a broker-assigned
   `cursor=V`; the axes relate only through the broker's sequencer.
2. **Control-origin events** → when verse POSTs control, the broker both records the ordered
   event *and* relays the item to the worker's inbox (a new `cCursor`). Same fact, two readers.

Who authors an effect event follows the interlock rule (truth = actually applied):

- **Operator-authored** (`decision` from `/resolve`, `vector` from set-vector, an operator
  `say`) — the broker emits the event **on the POST** (the operator/resolver acted) and relays
  the item for the worker to *act on*. No duplication: the `say` is the operator speaking; the
  worker's resulting action is a separate `exec`.
- **Worker-confirmed** (`cancelled`/`paused`/`resumed`) — the broker only **relays**
  `cancel`/`pause`; the effect event is the worker's **report** when it stops at its boundary.
  Until then verse sees "cancel requested" in the snapshot; `status:cancelled` lands on
  confirmation — the coarse latency stated honestly.

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

`POST /processes/{id}/claim {identity}` is a CAS on the claim; on success it returns the `spec`
and resume cursors. Claimed-by-another → `409` unless a **takeover**: revoke the prior claim
(its inbox gets a `revoked` item so a still-connected old worker stops), the new identity
claims, reads the **journal** (already durable — the handoff context), and continues on the
same subject. The claim is mirrored on the entity, so who-holds-it is observable — the
human-takes-over-a-runaway-agent story as a plain claim transfer. A **pool** variant,
`POST /claim {identity, kinds}`, long-polls for the next eligible unclaimed process and
CAS-assigns one, so N workers drain a queue without stepping on each other.

The **human is this protocol, human-paced**: `claim` = "I'll take it" (CLI/bot), `inbox` pull =
"check notifications / run `verse task`", `report` = progress `say` + checkpoints. Identical
three endpoints, a person's cadence — which is why the NAT'd agent and the human come out the
*same*, and why designing the human case first gets the agent for free.
