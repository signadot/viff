# controlPolicy — implementation plan

The incremental build-out of `docs/control-policy.md`. `processd` already implements ~90%
of the verse-facing contract, so this mostly *formalizes a seam that half-exists*. Each
phase ships on its own, local keeps working throughout, and remote → detached → dial-in
come online in order. Guiding decision: **the engine's local dispatch stays a Go call**
(`localPolicy` in-process; HTTP only for remote).

## Phase 1 — the `ControlPolicy`/`ProcHandle` seam ✅ (0abf19a)

Put every process behind a handle so the engine holds `ControlPolicy`/`ProcHandle`, not
`*Process`. `process/policy.go` (the interfaces), `trigger` (`flows` → `flowProc{id,
handle}`; control drives handles), `mirror.Reflect(store, id, handle, subject)`, and
`versed.localPolicy{processd}` (the `/broker/local` handler). Behavior-neutral: full suite
green, demo flow + connect validators unchanged.

## Phase 2 — resumable events (the cursor backbone) ⏳

Give each `process.Event` a **per-process monotonic cursor**; retain a bounded ring in the
process bus; `GET /events?since=N` replays `>N` then goes live, with a `resync` frame below
the low-watermark; `GET /processes/{id}` returns current state + cursor. Persist verse's
high-water cursor on the `process:instance` entity (via `mirror`).

**Deliverable/test:** drop and re-subscribe a process's event stream from a stored cursor
with no loss/dup. (Local restart still *orphans* — the loop is a goroutine — so this pays
off for detached; but the cursor is now in the contract everywhere.)

## Phase 3 — the HTTP base-URL face + a *remote* policy ✅ (57993df, 3cab4ab)

`controlpolicy.HTTP(baseURL)` — an `httpPolicy` + `remoteHandle` over the wire (spawn,
`GET /events?since` SSE, control POSTs, snapshot Result); `processd` spawn accepts an
`{id}`. `verse up --policy <url>` binds it; `verse up --broker-addr <addr>` serves the
broker face (`/broker/local`) on a sibling port. Proven end-to-end (two verses): A runs
its demo review process on B via `--policy`, mirrors B's cursored events, and `reviewed`
fires — `demo:pr:1` reaches "reviewed" with the work hosted on B. (`verse up` binds
per-server today; per-spawn `policy:` and the auto-`/broker/local` default are follow-ons.)

## Phase 4 — resume-not-orphan reconcile ✅ (cfaaa3d)

`ControlPolicy` gained `Attach(id)` (re-attach) + `Ref()` (policy id on the entity);
`mirror` records the policy alongside the cursor; `versed.reconcileProcesses` (replacing
the orphan-only reconcile) RESUMEs a remote-hosted non-terminal process (re-attach +
re-mirror, replay recovering a terminal reached while down) and ORPHANs a local-dead one.
Proven: a stuck-"running" entity tagged with a remote policy converges to "completed"
after re-attach. Phase 4b (9e265ba) added `Engine.Adopt` so a resumed process is
re-tracked for control (flow cancel/say/pause), not merely observed. **Remaining
follow-ons:** a health-check so a vanished remote orphans rather than stranding
"running", claim/identity + terminal-once CAS.

## Phase 5 — the worker-facing dial-in broker ✅

A broker `ControlPolicy` (`broker/`): `WorkerHandler()` serves
`/processes/{id}/{claim,inbox,report}` (the two cursors) to dial-in workers, while the
verse-side `handle` presents the ordinary `ProcHandle` contract to the engine — Subscribe
replays+streams the merged log; control (`Cancel`/`Steer`/…) enqueues inbox items the
worker pulls (a `say` also records its effect on the log, per the interlock). Both parties
are clients of the broker; only the broker is reachable, so a firewalled worker or a human
works without verse inverting.

**Deliverable/test:** ✅ `broker.TestBrokerDialInLoop` — a mock dial-in worker claims a
process, reports events that appear on the engine's `Subscribe()` channel, and a verse-side
`Cancel()` surfaces on the worker's next `GET /inbox`. Proves firewalled + human.

Hosting ✅ (6ef590e): `versed.HostBroker` mounts a worker handler under `/broker/<nonce>`
(an unguessable capability; `/broker/local` is the well-known supervisor), prefix stripped.

Safety CAS ✅: **terminal-once** (a latch — the first terminal writer wins; post-terminal
reports are frozen no-ops that echo the final status, so a lost-ack retry succeeds) +
**claim-takeover** (a per-claim `epoch` fencing token gates `report`/`inbox`; a stale
generation is `409 fenced`). Decided **Fork-1(b)** — `Cancel` is verse-authoritative: it
latches `cancelled` immediately (a dead worker still terminates) and a late worker
`completed` loses to the latch — and **Fork-2(a)** — same-identity reconnect always
re-claims (bumping the epoch to fence its own zombie); a different identity takes over only
after `Revoke` releases the claim (operator-authorized, never race-driven).

Publish-on-entity ✅: a hosted broker's worker-facing base (`<face>/broker/<nonce>`,
composed by `versed.UseBroker`) is published on `process:instance` as `dialURL` via
`mirror` (type-asserting the handle's `DialURL()`), closing the mint→publish→dial loop so a
worker can be *told* where to dial in rather than pre-configured. Distinct from `policy`
(verse-facing resume/orphan): an in-process broker is ephemeral, so `Ref()` stays a label.

Worker CLI ✅ — folded into `verse process` as two vantages rather than a separate verb:
operator (`list`/`get`/`cancel`/`say`/`resolve`, now policy-agnostic — brokered routes to the
handle) and executor (`do complete|fail|note|gate`, the worker leg). All self-resolve the
broker from `process get <id>`'s `dialURL` (no URL typed). `--as` is required (attributable
claims). `process inbox` is an unfenced operator peek at queued worker control. The **gate
loop** is wired: `do gate` raises a gate up to versed (reports `EvPending`), versed governance
resolves it (`process resolve` → the broker handle's `Resolve`), and a `resolve` inbox item
relays the outcome back so the executor (`--wait`) unblocks — the executor never resolves its
own gate. Proven end-to-end (demo review): a dial-in worker drives a brokered process to
completed/cancelled and raises+clears a gate, advancing the flow.

`attach` ✅ — the last unify: versed bridges a brokered process's events to SSE by
subscribing the policy handle (replay the merged log, then live), local still streams from
the runtime. So every `verse process` verb now works whether a process runs local or behind
a broker.

Remote union ✅ — `httpPolicy` now implements the lister interface (`ListProcesses`/
`ProcessSnapshot`, a thin bounded client over the remote's `GET /processes`), so `process
list`/`get` span **local + broker + remote**, each row tagged with its policy. Proven with
two verses: A's demo review runs on B via `--policy` and shows in A's `process list` tagged
with B's URL. (A dead remote yields nothing, bounded by a 3s timeout, rather than hanging.)

`process run` honors the policy ✅ — versed's `POST /processes` now goes through the same
seam as a trigger spawn (`s.policy.Spawn` + `mirror.Reflect`), so an ad-hoc run lands on the
configured broker/remote/local and is mirrored as a `process:instance` entity like any other
process (it was neither before). Proven: local run --wait completes + is mirrored; a broker
run lands on the broker and an executor `do complete`s it.

**Remaining follow-ons:** richer governance for a raised gate (vote/review/self vs today's
approve/deny); long-poll the inbox; a lease/TTL takeover (Fork-2(b)) once the health-check
drives `Revoke`.
**Needs more thought (deferred):** capability-scoping of `dialURL` — publishing on a
broadly-readable entity means any operator-reader can dial in as the worker; per-assignee
grants belong with the ownership/inbox track, not the minimal CLI.

## Deferred (own tracks)

- **Human surfacer + inbox** — the ownership/inbox thread (`owner`/`claimant`/`resolver`
  stamps, the identity-projection inbox). Back-burnered.
- **k8s policy** — spawn Jobs behind a Phase-5 broker + a k8s spawner; plus `k8s:*` as a
  Source (`docs/controllers.md`).

## Shape

Phases 1–2 are low-risk foundations; **Phase 3 is the pivotal proof** (engine as a client
of a URL); 4 adds durability; 5 adds the dial-in. The refactor to eyeball was Phase 1's
`engine.flows` → policy-mediated control (done); everything downstream assumes it.
