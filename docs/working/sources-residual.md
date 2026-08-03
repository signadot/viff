# External sources: what is left

Status: residual. [Sources](../sources.md) landed and its working note and plan went away with
it; this is what they named and the landing did not build. Nothing here is a looseness inside
something that runs — those are recorded where they run — except where it says so.

## Dial-in, for the source verse cannot reach

The one designed thing that is not built, and **the mechanism is not decided**. A `dial-in`
registration is refused at install, with a message saying it is unbuilt rather than unwanted,
because one that installed and then never dialled would sit in a listing looking installed —
the one state an operator cannot debug from the outside.

It matters more than a residual usually does. A source sits next to its truth, and truth lives
in the places that are hardest to dial: a repo on a laptop, a system inside a corp network.
That is most of the interesting cases, not a corner.

Before building any of this, note the answer that needs nothing: **Tailscale, cloudflared,
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

**How much code each costs is not symmetric.** Three of the four candidates keep `source.HTTP`
and change only where the `net.Conn` comes from — it already takes an `http.RoundTripper`, so
verse's side is a `DialContext` and the source keeps serving `sourced.New(src).Handler()`. The
fourth is not a transport at all: it replaces the call, so it is a second implementation of
`source.Source` on verse's side and *no verse client library at all* on the source's. That
asymmetry is the whole argument, and it runs in opposite directions for the two parties.

### 1. xap — the current design

**xap** (`libconnect/common/xap`) is a full-duplex multiplexer with named endpoints and
symmetric `Dial`/`Listen` on both sides of one underlying conn, written for exactly this — its
own doc says it "should work well for SaaS's interacting with customer agents", and it is what
`signadot local connect` already runs on. Named endpoints carry both legs on one connection
without inventing a message envelope to tell them apart, and both ends keep `net/http` exactly
as the url case has it:

```go
// the source, once, at startup. Outbound only: no ingress, no port, no url.
x, _ := xap.Dial(ctx, "tcp", verseAddr)
ln, _ := x.Listen(ctx, "reflect")     // verse dials this label back down the same conn
go http.Serve(ln, sourced.New(src))   // the SAME handler the url case serves
```

```go
// verse, per dialed-in registration. The same client the url case builds.
tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
    return x.Dial(ctx, "reflect")
}}
src := source.HTTP("git", "http://source", tr)   // the system comes from the registration
```

It satisfies both invariants for free, because it is HTTP: the status line is what tells a
failure from an empty answer, and a source cannot answer unasked because there is no request.

Two costs, and neither is technical:

- **libconnect is a private repo.** Open-sourcing it is agreed in principle and is work. A
  plain `require` is the call if xap wins — `GOPRIVATE` is already `github.com/signadot/*`, so
  nothing changes for anyone building verse today. **The bill comes due all at once the day
  verse goes public**, when it stops building for anyone outside signadot. That is the trigger
  to revisit, not the language question below.
- **Vendoring is not the answer.** It does not touch the language problem at all — a vendored
  Go multiplexer helps a Python author exactly as much as a private one — it makes verse the
  maintainer of a copy that will drift from the original, and it has to be undone the day xap
  opens. It solves "can verse build", which `GOPRIVATE` already solves.
- **Go only.** A source behind NAT can only be written in Go, because xap is the only way in
  and there is no other implementation of it. That is the real restriction, and it is why the
  candidates below exist.

The usual objection to a private dependency is bus factor, and it does not apply here: the
author is in the building and uses it.

### 2. A docd controller mount — rejected

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

### 3. The broker's attach — does not fit

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

### 4. An entity channel

verse writes its asks as entities at `sources:ext:<nonce>/asks`; the source **watches** that
over `/watch` and writes reflections back at `sources:ext:<nonce>/reflect` with `POST /entity`.
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

**Does a non-Go source behind NAT actually want in?** If yes, candidate 4 is the answer and the
dependency question dissolves. If it stays hypothetical, 1 is less machinery and touches no
log.

**What does candidate 4's log cost measure?** If a reflection body per source per resync is
small against what a verse already writes, it is the better answer on every axis and worth
building even for the Go case.

**When does verse go public?** That is when a private `require` stops being free.

The test, whichever wins, is the one the transport already runs, through a third transport:
same `source.Git`, same `Reflection`, no ingress.

## The claim that is still unproved

**Ownership as an address works when a source's ids encode ownership positionally**, which
git's do only because `source/git.go` prefixes every id with the repo. That is a property of
that code, not a law.

A source reflecting `linear:issue:ENG-123` whose ownership is "project ENG" cannot spell that
as an address at all, and will hit `--allow-overlap` on its second registration. **If the first
non-git source does, this decision is the one to revisit** — it is evidence the vocabulary is
wrong, not that the operator should keep passing the flag. `Covers` stayed an `entity.Pattern`
throughout, so the road back to predicates is open, and a check that later becomes more
permissive breaks nothing installed under the strict one.

The cheap test — a second git source on another host — exercises the transport and says nothing
about the vocabulary, so it does not count.

The reason the predicate form was not taken: an install compares two declarations with no
entities in hand, and proving predicates apart syntactically has traps. A null in a predicate is
a *presence* test, so the obvious "same field, different constants" rule proves `{repo: null}`
disjoint from `{repo: verse}` when the second entails the first — a wrong refusal from a check
whose whole job is to be conservative.

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

## Deferred by argument, not by effort

These are in [sources.md](../sources.md)'s own "what a Source does not do", and are repeated
here only so that a reader of this file has the whole list: no write-back (the outbound executor
path), no paging (narrow `Covers` instead; a paged reflect before a stream, ever), no authority
on the install probe or the hint route.
