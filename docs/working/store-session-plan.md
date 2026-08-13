# The store session: implementation plan

Status: steps 1 and 3 are built; 2, 4 and 5 are not. The argument is
[store-session.md](store-session.md); this file is the order of work and what each step proves.

**Step 1 answered the question this plan opened with, and the answer was not on the list.** The
plan named three candidates for why the write plane did not recover, all of them about the
session. All three were wrong: the session recovers from a clean close in 4ms, from a dark route
in 15s, and from a peer replaced over the same volume in 4ms. What made the outage permanent was
a store that got slower with every commit it had ever taken (upstream, filed as tony-format
`ps8kfs9dh12kr777fnn0`) and, in verse, a watch re-establishment with no budget holding the
session mutex — so every writer queued on a lock, spent its deadline there, and reported a
timeout for a request that never reached the wire.

The steps below are kept as written, with what happened recorded under each. The record is the
point: three plausible diagnoses, argued from real code, none of them right, and one test that
settled it in 45 seconds.

## Order of work

Five steps. 1 decides the shape of 3; 2 and 4 are independent of both and worth doing whatever 1
says. Each leaves the tree green.

### 1. Reproduce it, and name the layer

A test that kills the store under a live client and asserts recovery. `entitytest` already starts
a logd this process controls, which is what makes this cheap: start one, open a store, write, stop
logd, write (expect failure), start logd on the same address, write again — and assert the last
write SUCCEEDS.

That test fails today, in whichever way the outage did, and its failure mode names the layer. Run
it two ways, because they are different failures wearing one costume: **stop logd cleanly**, so
the peer sends a FIN and the read-pump learns immediately, and **pause the process** (`SIGSTOP`,
or a proxy that drops packets), so nothing arrives and only a deadline can notice. The first
exercises teardown; the second exercises the heartbeat. A fix built against the clean case and
shipped against the half-open one is the failure mode this step exists to avoid — and the outage
was the half-open kind, since a rolled pod's socket does not always get to say goodbye.

Assert the same recovery from a READ as well as a write, since the `/status` evidence says the
wedge was not write-specific.

**Proves:** which of the three it is, with a failing test to hang the fix on. If — and this is a
real possibility worth writing down before it happens — the test PASSES both ways, then the
outage was something else entirely (a scope's engine, the reflector's own context, a goroutine
leaked in versed) and the rest of this plan is premature. Finding that out is worth one test.

**DONE, and it was the possibility written down here.** `entity/reconnect_test.go`, four cases
rather than two: closed cleanly (4ms), gone dark (15s, bounded by the session heartbeat), peer
replaced over the same volume (4ms, data intact) — all three passing on the first run, which
retired every candidate this plan listed. The fourth case is the one none of them covered: a
store with A WATCH OPEN, the one thing versed has that a bare store does not. That failed exactly
as staging did — one write attempt in a 20-second window, `context deadline exceeded`, after
connectivity was fine again — and the goroutine dump named the holder: `connectLocked`, parked in
the hello read, session mutex in hand.

Two things earned their keep. The dark-route case, because a clean close recovers in 4ms and
would have hidden the starvation entirely; and dumping stacks instead of reasoning about the
second failure, which is how the 30s wire timeout was found after a bounded context did not fix
it.

### 2. Readiness says what it means

`/status` reports `Store.Rev()`, and `Rev()` opens a watch when none is live, so the probe is a
store round trip today — by accident. That accident is why the pod correctly refused traffic; a
refactor making `Rev()` read a cached field would silently turn readiness into "the HTTP server
is listening", and this outage would present as a healthy verse writing nothing.

Make it deliberate: a cheap bounded read against the store's own root, reported as a field, and a
non-200 when it fails. Independent of step 3 — a verse that cannot serve should say so whether or
not it can recover.

**Proves:** with the store down, `/status` fails within its own timeout rather than hanging past
the probe's; with the store back, it succeeds. And the negative worth asserting: a `/status` that
answers 200 while a write would time out is the bug being removed, so the test is written against
the store's reachability rather than against the handler's.

### 3. Recovery, in whichever layer step 1 names

**DONE, and none of the shapes below is what it took.** Step 1 named verse, and the fix is two
bounds rather than a retry: `sessionBudget` (10s) is now the single budget for any session call,
`establish()` puts the four unbounded watch opens inside it, and `WireTimeout: sessionBudget` on
the session bounds the handshake read that no caller's context can reach. `establish` bounds the
retry LOOP; the wire timeout bounds each ATTEMPT; either alone leaves a hold that outlasts every
caller's deadline. The starvation test recovers 5s after the route heals.

The retryability analysis below was not needed and is kept because it stays true: nothing retries
a write, so nothing had to decide whether a write may be applied twice. That question returns the
day someone adds a retry, and the answer is already written down.

The shapes considered before step 1 reported, none of which were built:

- If verse must retry: a `withSession` helper wrapping the ops, classifying **unreachable**
  (redial, bounded backoff, one retry) apart from **refused** (a CAS precondition, a compacted
  replay, a malformed patch — return it). That cut already exists on the runner side as
  `task.ErrUnreachable`.
- If `watchCancel != nil` is standing in for "the root watch is delivering" while it re-establishes,
  it stops being a fact about a goroutine having been started and becomes a fact about a watch
  delivering — the same mistake as the per-instance watermark, one field over.
- If it is upstream — `connected` surviving a dead socket, or a heartbeat that cannot notice one —
  a defect on tony-format carrying step 1's reproduction, and a bound here meanwhile so a wedged
  session fails fast instead of at every caller's deadline.

**Which writes may be retried is decided here, not discovered.** A deadline is an absence of an
answer, so a retried write may be a second application:

- `Modify` and `CommitIf` carry a precondition — a retry either applies once or fails its CAS
  and re-reads. Safe.
- `ReplaceFrom` / `ReplaceAt` are the single-writer path writing a whole value; rewriting the
  same value is inert. Safe.
- A bare `Write` with a caller's value is idempotent only if the caller says so. Audit the call
  sites (there are few, and nearly all are one of the two above); anything left gets an explicit
  opt-in rather than a silent retry.

**Proved:** step 1's test passes. The exactly-once test is not needed and was not written —
there is no retry to double-apply. It becomes required the moment one is added.

### 4. Boot is the steady state's first iteration

`cmd/verse/up.go` waits for docd with its own deadline, its own log line and its own failure
mode. Delete it. An unreachable store at second zero is the same condition as one that vanished
at 14:26, and a system that handles the second needs no special case for the first.

This is the step that makes the whole thing worth doing rather than merely correct: node
eviction, a docd rollout at 3am, a partition and a cold boot stop being four situations with
four behaviours.

**Proves:** `verse up` against no docd comes up, is unready, and becomes ready when docd
arrives — with no deadline and nothing special about the first attempt. The smoke test covers
the ordinary boot; this needs the inverted one.

### 5. Drive it on staging

Two runs, both of which happened by accident already: roll docd and versed together (the
original trigger), and kill docd under a live verse mid-review. The second is the interesting
one — a task is in flight, its mirror is writing, and the question is whether the run survives
the store going away or fails cleanly enough to re-fire.

**Proves:** the thing this exists for. And it names the residual, since a task whose mirror
could not write for thirty seconds is a case nobody has looked at yet.

## Not in this plan

- **Deploy ordering.** It fixes the case somebody foresaw and none of the others, and puts
  knowledge of the store's lifecycle in a Makefile, which is the last place a person looks when
  a pod is unready. Step 1 settled this by accident: ordering would not have helped either
  outage, since neither was about which process started first.
- **A store that buffers writes while disconnected.** This one is not a scoping call, it is a
  property being defended. **verse returning "written" means docd returned written, which means
  logd returned written.** One word, one meaning, all the way down, and no ambiguity anywhere in
  the chain about what a caller has been told.

  A buffer replaces that with "accepted, and it will land later, probably", and everything built
  on the first meaning stops being true at once. A CAS precondition is a claim about a revision
  that now does not exist yet. A commit returns before it has a commit. A read after a write can
  answer either way and both are correct. Nobody downstream — the engine, a source, a person at
  `verse entity get` — can say what the store holds, because the answer depends on a queue they
  cannot see.

  The retry in step 3 is careful to be the opposite of this: it is bounded, it happens BEFORE the
  caller is answered, and its answer is still the store's own. And a write that genuinely did not
  land already has a right answer — the firing failed, say so, and let the level-triggered rule
  fire again. Level-triggering exists precisely so that a lost write costs a retry rather than a
  guarantee.
