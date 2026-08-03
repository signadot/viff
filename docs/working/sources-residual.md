# External sources: what is left

Status: residual. [Sources](../sources.md) landed and its working note and plan went away with
it; this is what they named and the landing did not build. Nothing here is a looseness inside
something that runs — those are recorded where they run — except where it says so.

## Dial-in, for the source verse cannot reach

The one designed thing that is not built. A `dial-in` registration is **refused at install**,
with a message saying it is unbuilt rather than unwanted, because one that installed and then
never dialled would sit in a listing looking installed — the one state an operator cannot debug
from the outside.

It matters more than a residual usually does. A source sits next to its truth, and truth lives
in the places that are hardest to dial: a repo on a laptop, a system inside a corp network.
That is most of the interesting cases, not a corner.

**The contract does not change. Only where the `net.Conn` comes from does.** That is the whole
of the design, and it is what stops this becoming a second contract in which a source declares
truth unasked — the push design arriving through the back door with the reconcile loop
duplicated after all. verse still dials `GET /reflect`; it just dials it down a connection the
source opened. `source.HTTP` already takes the transport that makes this one line on each side.

Two candidates, and one is in the house. **xap** (`libconnect/common/xap`) is a full-duplex
multiplexer with **named endpoints** and symmetric `Dial`/`Listen` on both sides of one
underlying conn, written for exactly this — its own doc says it "should work well for SaaS's
interacting with customer agents", and it is what `signadot local connect` already runs on.
Reverse HTTP is the alternative, as an inbox long-poll (the broker's shape) or an HTTP/2 tunnel
with the roles swapped.

**xap, because it changes one line on each side.** Named endpoints carry both legs — verse's
reflect calls and the source's hints — on one connection without inventing a message envelope
to tell them apart, and both ends keep `net/http` exactly as the url case has it:

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

The inbox long-poll is the one to argue against explicitly, because the broker is the in-tree
precedent and it is what somebody will reach for. It turns a request and its response into a
correlated pair that has to be matched up by hand, with a timeout policy and an id space and a
retry rule — all of which HTTP already has and none of which a reflect needs. The broker pays
that price because a worker *claims* work and may go away holding it; a reflect is a plain call
that either answers or does not.

**The nonce is the grant**, as it already is for a broker's worker leg: `verse source add
git:laptop --dial-in` mints one, the source is deployed with it, and the connection it opens is
bound to that registration rather than to whatever the source says it is.

Two things move, both somewhere better. The install probe has nothing to talk to at install
time, so a `--dial-in` registration is the "unreachable, installed with a warning" case by
construction — and **the probe moves to the first connection**, where a descriptor that
disagrees with the registration refuses the *connection* rather than the install. That is the
check landing where the answer exists. And the hint stops needing a route on the operator API
at all: it is a `Dial(ctx, "hint")` on the connection already open.

The same inversion is the better answer to [the broker's own
residual](runners-residual.md) — several workers behind one name, and verse unable to tell a
queuing runner from a synchronous one — but that is a different note's business.

The test is the one the transport already runs, through a third transport: same `source.Git`,
same `Reflection`, no ingress.

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
