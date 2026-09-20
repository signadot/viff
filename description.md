# residual: sources — dial-in, and the gaps inside what runs

# External sources: what is left

Status: residual of a landed subject. [Sources](../sources.md) landed and its working note and
plan went away with it; this is what they named and the landing did not build.

It is about the **seam**, not about any one connector. Two things that were open here have since
been answered elsewhere and are not repeated: the ownership finding below produced
a design whose first phase is built ([done.md](done.md)), and the GitHub connector
has a residual of its own in [gh-source-residual.md](gh-source-residual.md). What is left here is
dial-in, the levers a resync has not spent, and the gaps inside what runs.

## Dial-in, for the source verse cannot reach

The one designed thing that is not built, and **the mechanism is not decided**. A `dial-in`
registration is refused at install, with a message saying it is unbuilt rather than unwanted,
because one that installed and then never dialled would sit in a listing looking installed —
the one state an operator cannot debug from the outside.

It matters more than a residual usually does. A source sits next to its truth, and truth lives
in the places that are hardest to dial: a repo on a laptop, a system inside a corp network.
That is most of the interesting cases, not a corner.

Before building any of this, note the answer that needs nothing: **cloudflared,
ngrok or a jump host turns a laptop repo back into the plain `url:` case**, with no verse code
and no language restriction. For a good deal of what "behind NAT" means in practice that is the
honest answer, and it should be tried before a protocol is written for it.

### What has to hold, whatever wins

**verse asks; the source answers.** A source that declared truth unasked would be the push
design arriving through the back door, with the reconcile loop duplicated into every source —
which is the thing the seam exists to prevent.

**A failure must be distinguishable from an empty answer.** An empty `Reflection` carrying
covers is a legitimate instruction to tombstone everything under them, so any transport that
cannot tell "I could not answer" from "the answer is nothing" will, sooner or later, delete a
slice because a source was down. This is the criterion that decides most of the candidates
below, and `TestAFailedReflectIsAnErrorAndNotAnEmptyOne` is what holds it for the url case.

**How much code each costs is not symmetric.** Two of the three candidates keep `source.HTTP`
and change only where the `net.Conn` comes from — it already takes an `http.RoundTripper`, so
verse's side is a `DialContext` and the source keeps serving `sourced.New(src).Handler()`. The
third is not a transport at all: it replaces the call, so it is a second implementation of
`source.Source` on verse's side and *no verse client library at all* on the source's. That
asymmetry is the whole argument, and it runs in opposite directions for the two parties.

### 1. A docd controller mount — rejected

`verse connect` already proves that a process behind NAT can answer reads over a connection it
opened: it runs `libctl.RunController` against docd's *mount* face and serves file bytes back
down that connection, with nobody ever dialling the laptop. A `Reflection` is a tony document,
which is what a `libctl.Handler.Match` returns, so the shape looks like a fit — and it would
even be consistent with the rule sources.md states, since a reflection document is bytes
somebody asks for while the entities verse writes from it are facts something reacts to.

**It fails the second invariant, and that is decisive.** On a mount, absence and emptiness are
the same read: `source.Content.Match` returns `ir.Null()` for the mount root, for a missing
file and for a path that escapes, and a controller that is not running gives nothing at all.
There is nowhere to put "I could not answer". That is not merely theoretical here —
`LogdStore.Get` already carries the `isNotFound` string-match hack because libctl renders
logd's typed not-found code into an opaque string, so error fidelity through that path is
known lossy.

Three smaller ones, recorded so the idea is not re-proposed: the ref parameter has to be
smuggled into path segments; mounts are baseline-only (connect gets per-scope behaviour by
putting a nonce *in the ref*); and it would make docd part of a source's contract, which
sources.md currently says it is not.

### 2. The broker's attach — does not fit

The in-tree dial-in precedent for *runners*: a worker connects out to `claim` / `inbox` /
`report` (`broker/broker.go`), and both parties are clients while only the broker is reachable.

It carries a great deal that a reflect has no use for. `claim` hands out an **epoch** that
fences later writes; `inbox` renews a lease, refuses a stale claim with 409, treats the poll
cursor as an acknowledgement with a high-water mark that survives re-claim, and settles
undelivered answers on the poll that asks past them; `report` latches a terminal exactly once.
Every one of those exists because **a worker claims work and may go away holding it**. A
reflect is a plain call that either answers or does not. Piggybacking means inheriting concepts
that do not apply, or building a second simpler inbox beside the first — a new protocol either
way, just one that looks familiar.

And the property it would be piggybacked *for* is the one piece missing: **the inbox is a
short poll**, and the long-poll is still owed (the roadmap records it).

What does reuse cleanly, whichever candidate wins: **nonce-as-grant and the `/broker/<nonce>`
hosting pattern**. Knowing the url is the grant, and the connection is bound to the
registration rather than to whatever the source says it is.

### 3. An entity channel

verse writes its asks as entities at `verse.sources.ext.<nonce>.asks`; the source **watches** that
over `/watch` and writes reflections back at `verse.sources.ext.<nonce>.reflect` with `POST /entity`.
Both legs are outbound from the source, both are surfaces verse already serves, and no new
protocol is invented anywhere.

It is the only candidate that **deletes the language restriction**: a NAT'd source needs an
HTTP client and nothing else, so it is the same effort as any other verse client, in any
language. It also gets correlation and replay from the store — `?since=<rev>` is the acked
cursor the broker had to hand-build — and it uses verse's own address rather than docd's.

Three costs, sharpest first:

- **The durable log pays for every reflection body, at every pass.** A repo with 500 refs is a
  reflection document of 500 entities written into the log as one payload, then converged into
  500 entities — and the ask happens every resync whether anything changed or not. That is the
  churn the design spent a rule avoiding (`lastReflect` writes only when a pass wrote). The
  source cannot answer only-on-change without keeping a diff and a cursor, which is the "source
  stays dumb" property gone. Mitigations to weigh: a long resync for dial-in sources leaning on
  hints for latency, or answering into a throwaway scope dropped after converge. **Measure this
  before choosing it.**
- **The nonce stops being a capability.** For the broker it works because `/broker/<nonce>` is
  the only way in. Here the surface is the general entity API, so anyone who can write entities
  can write another source's reflect path or watch its asks. That is the authority gap the
  design already defers, but the nonce becomes a *name* rather than a grant.
- **The law goes from structural to policed.** Over HTTP a source cannot answer unasked,
  because there is no request. Here an unasked write is expressible, so verse must ignore a
  reflection that does not name an outstanding ask — a check rather than a guarantee.

One detail for whoever builds it: `entity.Pattern` has no id dimension (`queryPattern`), so a
source watching for its own asks filters on a payload predicate — `?if={nonce: …}` — with the
nonce in the payload rather than only in the ref.

### What would decide it

**Does a non-Go source behind NAT actually want in?** If yes, candidate 3 is the answer.

**What does candidate 3’s log cost measure?** If a reflection body per source per resync is
small against what a verse already writes, it is the better answer on every axis and worth
building even for the Go case.

The test, whichever wins, is the one the transport already runs, through a third transport:
same `source.Git`, same `Reflection`, no ingress.

Landed; described in [sources.md](../sources.md#five-of-them-and-what-the-ones-after-git-cost)
and [done.md](done.md).

## Gaps inside what runs

Recorded here rather than in [sources.md](../sources.md) because each is a thing to fix, not a
thing to know.

**A source installed with `--no-probe` claims nothing, and then every later install in its
system needs `--allow-overlap`.** A registration with no `owns` can be proved apart from
nothing, which is correct and is also a way for one careless install to make a whole system
awkward. The fix, if it becomes annoying: `--no-probe` requires an explicit `owns`.

**Scoped registrations are not restored on restart.** `entity.Store` has `Scope` and
`DeleteScope` and nothing that lists, and a scoped store reads baseline layered underneath, so
nothing can find them. Survivable because a scope is created on demand by whatever owns it, and
that thing can install again — but a store that could enumerate scopes would close it, and
would close the same gap for scoped charters.

**`Git.InstallHook` has no caller.** The hook is written and tested and points at the right
route, but nothing installs it: `verse source add --dir` does not offer to, because writing into
somebody's repository is a side effect an install should be asked for rather than assume. A
flag would settle it.

**A `dir` registration is not portable**, and a restart restores it whether or not the
directory is still there. The reflector reports the failure into `lastError`, which is the right
behaviour; nothing has confirmed it end to end.

**A deployed source is not registered by deploying it.** `deploy/{git,slack,linear,github}-source`
brings the connector up; `verse source add` is still a verb somebody runs afterwards. That is
deliberate — an install carries an ownership claim verse proves against what is already
standing, and a manifest is not where that conversation happens — but it does mean a fresh
cluster is two steps, and the registration NAME has to match the hint url baked into the pod
(`<x>SourceName`). A mismatch shows up as verse 404ing every hint, which is at least loud.

**A thread stops being reflected when its parent ages out.** The Slack window is over
`conversations.history`, and a reply does not bump its parent there, so a long-running thread on
an old message falls out of the window with it. Raising `-depth` postpones it rather than fixing
it; the fix, if it matters, is a second window over recently-active threads, which needs a
cursor the source deliberately does not keep.

**`source.Throttle` has no visibility.** A source answering from its cache is indistinguishable
from an external system where nothing is happening — the same shape as the wedged-source problem
`lastPass` exists for, one level down. Nothing reports it; a `/source` descriptor field saying
"my floor is 2m" would, and would also let `verse source` explain why a hint took two minutes.
`Descriptor` is still `{system, owns}`, so there is nowhere for it to go yet. The GitHub source
adds a second invisible answer of the same kind — a pass answered entirely from 304s — which
argues the field should say what a source's *floor* is rather than what its throttle is.

**And it protects the external API, not the store**, which turns out to be the half that hurt
first. A throttled pass still converges: `Reflector.converge` calls `current()`, which Lists the
whole system slice out of the store to answer "what changed", so the store cost of a resync is
paid whether the answer is cached, unchanged, or new.

Landed; described in [sources.md](../sources.md#ownership--single-writer--no-cas).

**What is left of the levers**, now that the urgent one is spent:

- **A pass that changes nothing still Lists everything.** A `current()` that could ask the store
  for a slice's *revision* rather than its contents would make the steady state — most passes,
  most of the time — one cheap question instead of a full read. This is the one with a customer
  now: the GitHub source made the *external* read of a quiet repository free with conditional
  requests, and against a repository with four thousand open items the List is what is left
  ([gh-source-residual.md](gh-source-residual.md#the-cost-that-is-verses-not-githubs)).
- **Narrower slices.** `git.<repo>.ref.*` includes GitHub's `refs/pull/*`, which is 3000 of those 3377
  entities and which no SDLC flow reacts to. A `-refs heads,tags` flag on `sourced git` would
  cut the slice by an order of magnitude; it still does not exist. This is now about the store
  staying small rather than about a pass finishing.
- **A resync interval that is not a constant.** Still true that one minute suits ten refs and
  not three thousand — but no longer urgent, since a pass now finishes well inside it.

And what the numbers no longer say: verse converges at ~290 entities/second against a local
logd, while a bare unconditional patch runs at ~1700. The remaining gap is verse's own — the
List, the comparison, the single-goroutine loop — and nothing has looked at it, because at 12
seconds for a whole repository nothing is asking.

**And versed still has no liveness probe**, and the premise of that decision has moved: `/healthz`
now answers from this process alone, so the endpoint it was waiting on exists and the argument is
reopened in `deploy/source/versed-deployment.yaml`.

## Deferred by argument, not by effort

These are in [sources.md](../sources.md)'s own "what a Source does not do", and are repeated
here only so that a reader of this file has the whole list: no write-back (the outbound executor
path), no paging (narrow `Covers` instead; a paged reflect before a stream, ever), no authority
on the install probe or the hint route.
