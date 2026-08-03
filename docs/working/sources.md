# External sources: a source you deploy

Status: design, with steps 1 and 2 built (the `Reflect`/converge split, and the HTTP
transport with its sample; see the step list at the end).
It argues one change to [sources](../sources.md) —
that a Source should be a program somebody deploys and installs, the way a
[runner](../runners.md) is — and one correction, that the substrate both sources.md and
[controllers](../controllers.md) name for it is now the wrong one.

What exists to build on. In main, `source/reflector.go` (the `Source` interface, the
`Reflector`, and the converge the Reflector now owns) and `source/git.go` (a multi-repo git
Source) are a library with no caller outside its tests.

**The wiring this note was written against is not in the repo, and step 3 has to be read
accordingly.** It described a `sources` worktree carrying `verse source add <dir>`, a durable
`source:<system>:<id>` registration, a reflector under the server's lifetime and `POST
/hooks/{system}`. None of it is reachable: branch `sources` is an ancestor of `main` carrying
unrelated commits, no history under any ref holds a `cmd/verse/source.go` or a `/sources`
route, there is no stash, and no registered worktree has a caller of `NewGit` or
`NewReflector` outside `source/`'s own tests. Its registration, restart and removal *reasoning*
still survives — it is what step 3 below is — but the code does not, so step 3 is a build
rather than a re-seam.

## The substrate named in the published docs is wrong

Both published notes end at the same place. sources.md: "a verse `Source` = a `libctl.Handler`
(+ its internal poll/webhook reconcile) run under `RunController`". controllers.md: "Two things
are built as controllers today: the content controller and, ahead, every Source." That was
right when it was written and is not right now, and the tree already holds the proof.

Mounts moved to a sibling root. `entity.MountRoot` (`entity/logdstore.go:58`) is disjoint from
the entity root because **a mount is process-driven, so it has no history**, and a
history-bearing watch must never span one. Two consequences follow directly from the code:
`refPath` renders a mount-owned slice under `MountRoot`, and `WatchSubtree`
(`entity/logdstore.go:687`) renders *only* under `s.root`. So a controller-served slice is
readable by its ordinary ref and **invisible to every watch there is** — no change events, no
`Since` replay, and therefore no trigger, ever.

A Source exists so that triggers fire on external change. A controller-mounted Source would
reflect a repo perfectly and fire nothing. **So a Source is not a controller**, and its
reflected facts are ordinary log-backed writes.

The precedent is already in the tree, already tested, and was learned the same way. `verse
connect` federates a working tree as two slices: content served on demand by a controller mount,
and status written log-backed — the latter, in `entity/logdstore.go`'s own words, "is what keeps
the trigger that matches it watchable", pinned by `TestFederationKindsAreMountOwned`. The rule
that split obeys generalizes exactly: **bytes somebody asks for can be a mount; facts something
reacts to must be writes.** A Source is entirely the second kind.

The obvious escape does not exist. Mounting a Source under `DefaultRoot` instead would put it
back where watches could see it — and re-create precisely what `MountRoot` was introduced to
fix: a composed root watch that silently drops `FromCommit` and returns a successful-looking
empty replay, plus a mount that stalls ~5s on the overlapping root watch and then force-ends it.
That is not a trade, it is the bug.

Both published notes now carry the correction rather than a deletion — [sources](../sources.md)
records the version of itself that was wrong, and [controllers](../controllers.md) opens with
the question of whether to write one at all. The controller path itself is untouched: it is
right for content federation, which is what it was built for.

## The seam that has to move is the store

`Source.Reconcile(ctx, store, ref)` hands the connector the store. That is the one thing that
cannot cross a process boundary — and not for the reason it looks like. Shipping a store client
is trivial, and `POST /entity` already exists, so an external program could write `git:*` today
with nothing new built. The problem is what `git.go` *does* with the store, which is the part
that must not be re-implemented once per source:

- **skip unchanged**, or every periodic resync re-fires every trigger over the slice. The tree
  has already paid for getting this wrong once, with a measurement attached: comparing nodes
  with `reflect.DeepEqual` made a re-established watch re-emit an entire slice as changed —
  "twenty entities delivered forty events — and every trigger over it would have fired again for
  nothing".
- **tombstone what is gone**, scoped to the slice the pass covered, so one repo's reconcile
  never deletes another's refs.
- **never tombstone history** — commits fall out of the bounded tip window and stay.

None of that is knowledge about git. All of it is knowledge about the entity-verse, and a
connector author who has to hold it will get it wrong in a way nothing notices until a charter
fires two hundred times. So the interface splits, and the store stays on verse's side:

```go
// Reflect reads external truth for the slice `ref` names — a zero ref is everything this
// source owns — and returns it. It does not write, and it is not handed anything that could.
type Source interface {
    System() string
    Reflect(ctx context.Context, ref entity.Ref) (Reflection, error)
}

type Reflection struct {
    Covers   []entity.Pattern // what this payload is COMPLETE for: the tombstone authority
    Entities []entity.Entity
}
```

`Covers` is the load-bearing field and it is not new information — it is what
`converge(store, repo, desired)` already scopes by, said out loud instead of held in a
parameter. An entity under a covered pattern and absent from `Entities` is tombstoned; an
entity in `Entities` under no covered pattern is written and never tombstoned. Git's "keep
history" then stops being a branch in verse's code and becomes a declaration in the source's
answer: it covers `git:ref:*` and `git:head:*` for the repo, and does not cover `git:commit:*`.

`entity.Pattern` is the charter's own selector — system, kind, and a tony predicate over the
payload — and `Pattern.Select` already lists what satisfies one. The git source's
per-repo scoping is `{system: git, kind: ref, value: {repo: verse}}`, which is the predicate it
already writes into every payload. One grammar, no second vocabulary for ownership. (`value:`
rather than the `fields:` written here at first: that spelling was retired from the charter
grammar, and the whole point is to use the charter's.)

## Which way the wire runs: verse dials

**Pull.** The registration carries a url, verse's Reflector dials `GET /reflect?ref=…`, and the
source answers with a `Reflection`. Three reasons, in the order they matter:

*The hint law keeps one implementation.* The worktree's own hook handler already refuses to
parse deliveries itself, on the grounds that the law — map the delivery to refs, schedule a
reconcile, write nothing — "is the Reflector's to keep, and a second implementation here is
exactly how that law would come to be broken in one of two places". A source that pushed its
own reflections would move the reconcile loop into every source, which is that same duplication
one level up and across a network, where a divergence is least recoverable.

*The source stays dumb.* Pulled, a source is a pure function of external truth: no ticker, no
backoff, no retry policy, no diff, no cursor, no CAS. That is the property that makes the sample
program short enough to copy, and it is the same property that makes the sample runner short.

*It is the runner shape.* A url on an entity, dialed by verse, probed at install — which is
what makes "externally installable" mean the same thing twice rather than two things.

**The hint reverses, and stays a hint.** The source owns delivery and parsing, because the
source is what deployed the git hook or holds the webhook secret; it then tells verse `POST
/sources/<system>/<name>/hint?ref=…`, and verse pulls. A hint carrying entities would be the law
broken exactly where it costs most. It also pays a second time: the hint route can stay
unauthenticated in a wide-open dogfood verse, because a delivery that carries no data can cost
at most a reconcile of a slice its sender does not get to choose the contents of.

*Refs as query parameters, where this note first wrote a `{refs: […]}` body.* The
[plan](sources-plan.md) changed it, and the sender is the argument: a `/bin/sh` git hook running
`curl` is the case to keep to one line, a handful of refs fit in a url, and a route that must
stay cheap enough to leave unauthenticated should not also be negotiating a content type.

## Dialing in, for the source verse cannot reach

A source sits next to its truth, and truth lives in the places that are hardest to dial: a repo
on a laptop, a system inside a corp network. That case is not a residual to note — it is most of
the interesting ones, so it gets designed now.

**The contract does not change. Only where the `net.Conn` comes from does.** That is the whole
of the design, and it is what stops this becoming a second contract in which a source declares
truth unasked — the push design arriving through the back door with the reconcile loop
duplicated after all. verse still dials `GET /reflect`; it just dials it down a connection the
source opened.

Two candidates, and one of them is in the house. **xap** (`libconnect/common/xap`) is a
full-duplex multiplexer with **named endpoints** and symmetric
`Dial`/`Listen` on both sides of one underlying conn, written for exactly this — its own doc
says it "should work well for SaaS's interacting with customer agents", and it is what
`signadot local connect` already runs on. Reverse HTTP is the alternative in two forms: an
inbox long-poll (the broker's shape — the source polls verse for work and POSTs answers back)
or an HTTP/2 tunnel with the roles swapped.

**xap, and the reason is that it changes one line on each side.** Named endpoints carry both
legs — verse's reflect calls and the source's hints — on one connection without inventing a
message envelope to tell them apart, and both ends keep `net/http` exactly as the url case has
it:

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
bound to that registration rather than to whatever the source says it is. Ownership is then
checked against the registration, which is the same check the url case makes at install.

Two things move, both to somewhere better. The install probe has nothing to talk to at install
time, so a `--dial-in` registration is the "unreachable, installed with a warning" case by
construction — and **the probe moves to the first connection**, where a descriptor that
disagrees with the registration refuses the *connection* rather than the install. That is the
check landing where the answer exists. And the hint stops needing a route on the operator API
at all: it is a `Dial(ctx, "hint")` on the connection already open.

The same inversion is the better answer to [the broker's own
residual](runners-residual.md) — several workers behind one name, and verse unable to
tell a queuing runner from a synchronous one — but that is a different note's business.

## Installing one

```sh
verse source add git:laptop http://build-host:9801
verse sources
verse source show sources:git:laptop
```

```tony
name:   laptop
system: git
url:    "http://build-host:9801"   # or `dial-in: <nonce>`, or `dir:` for the built-in
owns:   ["git:*:verse/*", "git:head:verse"]   # ref globs; see below
scope:  ""                         # empty is baseline; see below
```

Addressed `sources:<system>:<name>`, where the system is the slice reflected — unlike a runner's
kind, which is the operator's own category, because here the first segment is already answering
a question that matters to verse.

**Adding one probes it**, with `GET /source` returning a descriptor `{system, owns}`. The three
outcomes are the runner's three and for the runner's reasons — *a source* (it answered and
decoded), *not a source* (it answered and is not that: refused), *unreachable* (installed, with
a warning, because a manifest landing before its pod is the ordinary state of a source deployed
alongside the entity that names it). `--no-probe` installs anyway.

Two checks have no runner analogue, and both are the thing an external source loses that an
in-process one had by construction:

- **The descriptor's system must be the one the operator named.** A `github` registration
  pointed at a git source is the mistake this catches at the moment somebody is present.
- **Ownership must be provably disjoint from every standing registration in the same scope**, or
  the install is refused. Single-writer is what makes a reflected slice need no CAS; in-process
  it held because there was one `Git` value, and across deployments nothing holds it but this
  check. Disjointness is *proved*, not assumed, and it refuses what it cannot prove — including
  a registration owning `git:*:*` outright, since that one claims the lot. `--allow-overlap` is
  the way past it, with the cost stated rather than discovered: two writers on one slice, each
  tombstoning what the other reflected. *In the same scope* is load-bearing and is the next
  section: a scoped source shadowing a baseline one is the feature, not a collision.

  **An ownership claim is a ref glob, not a `Pattern`**, and this note first said the opposite.
  It proposed `{system: git, value: {repo: verse}}` on the grounds that ownership should reuse
  the charter's selector rather than invent a vocabulary. What that missed is that the two
  questions are asked in different places: converge evaluates `Covers` against stored entities
  whose payloads it can read, while an install compares two *declarations* with no entities in
  hand. **Addresses compare statically; payload predicates do not.** Proving `{repo: verse}` and
  `{repo: kernel}` apart means reasoning about tony matches syntactically, which is both harder
  than it looks — a null in a predicate is a PRESENCE test, so a naive "same field, different
  constants" rule proves `{repo: null}` disjoint from `{repo: verse}` when the second entails
  the first — and unavailable to a source whose ownership is not a constant field at all.

  So `owns` is spelled the way every other address in verse is, with `*` allowed:
  `git:*:verse/*`. An id glob is a **literal prefix and an optional trailing `*`**, deliberately
  and only that: prefix-disjointness is a string comparison, while general glob disjointness is
  automata intersection, and the whole reason for retreating from predicates was to stop proving
  things the hard way. Two claims are disjoint when any segment is — two literal systems that
  differ, two literal kinds that differ, or two id prefixes neither of which prefixes the other.

  Git's claim is two entries per repo rather than one, and the reason is worth stating because
  the shortcut is tempting: refs and commits are `<repo>/<name>`, but a head is the bare
  `<repo>`, so the claim is `git:*:verse/*` **and** `git:head:verse`. The single glob `verse*`
  would cover both — and would also claim `verse-extras/main`, which is not a false alarm but a
  real overclaim, and one that would make two perfectly disjoint repos refuse to install
  alongside each other.

  This is the cheaper of two guesses about an unproved vocabulary, and it is wrong in a knowable
  direction: it works when a source's ids encode ownership positionally, which git's do because
  `git.go` prefixes every id with the repo. A source reflecting `linear:issue:ENG-123` whose
  ownership is "project ENG" cannot spell that as an address, and has an obvious predicate. If
  that is the first source to arrive, this decision is the one to revisit — and `Covers` stays a
  `Pattern` throughout, so the predicate grammar is still there to return to.

**Three ways to reach a source, one contract.** A registration carries exactly one of `url`
(verse dials), `dial-in` (the source dials, above), or `dir` (the in-process git source the
worktree built). They differ only in where the `net.Conn` comes from; `Reflect` is the same call
down all three. This is not the runner's "a runner with no url is refused", and deliberately:
there, absence already meant something — `local` — so a url-less entity would have been
ambiguous. Here the field present says which it is, and refusing `dir` would delete a working,
tested, zero-deployment path to buy a symmetry nobody reads.

## Removing one, and the fork the worktree does not have yet

The worktree tombstones everything the reflection wrote, on a good argument: a slice nobody
reflects any more is a slice nothing can correct, and a charter would go on believing in a
branch of a repo verse has stopped watching.

It is right about the common case and wrong about one that will happen. Moving a repo to another
host is a remove and an add of the same physical truth, and tombstone-always makes that fire
every branch-deleted trigger in the verse and then every branch-created one behind it — a
bookkeeping change destroying and re-creating live facts, which is the runner's rename case
arriving in a different costume. So the same resolution: **removal with a non-empty reflected
slice is refused until told which**, `--forget` (tombstone it; the reflection is over) or
`--keep` (leave the last known state, now frozen and quietly ageing). A removal with nothing
reflected needs no flag, there being nothing to be wrong about.

## What verse records, and what it refuses to

`lastReflect` and `lastError` go on the registration, so a source that has wedged — alive,
answering, reflecting nothing new — is visible as staleness rather than as silence.

That is not the runner's rule inverted. A runner probe is an *opinion about a third party's
wellness*, which is why the store never holds one: it can be stale in the direction that costs.
`lastReflect` is a fact about verse's own last write, which is the poll cursor sources.md already
calls dogfood. The distinction survives the SAT check: a stale `lastReflect` cannot say a source
is well when it is not — it says the opposite, and the older it gets the louder.

**Except that it was asked to be two clocks, and the [plan](sources-plan.md) splits it.** A
cursor moves only when something changed, so on a quiet repo a healthy source and a wedged one
carry the same `lastReflect` and the paragraph above is wrong about what it catches. A stamp
that moved every pass would catch the wedge — and write to `sources:*` once per poll interval,
so anything watching that slice fires forever on a verse where nothing is happening. So the two
clocks separate along the line runners already drew: `lastReflect` and `lastError` are STORED,
because they are facts about verse's own actions and change rarely; when a pass last *completed*
is REPORTED on `GET /sources` from the live reflector and never written down, because that is an
opinion about now.

## One source, two transports

`source.Git` behind `sourced.New(…)` serving the contract *is* the sample external source, and
the same value behind verse's own reflector is the built-in. That is `processd`'s trick, and
`processd` says why it matters: verse's "own in-process runner is the same handler — so there is
no privileged local path for this to differ from." Here it also means the reference source is
not a toy — `docs/reference/examples/source/main.go` with one function marked to change, and the
change being "read your system's truth and return it".

## Scoped sources

A source registers into a scope, and the obvious-sounding alternative — that a reflection is a
fact about the outside world, so every scope should read the same one — is wrong. `verse
connect` already refutes it: it reflects a working tree into a per-session scope, which is
precisely a scoped source built as a special case. The general form is the one that matters
most, and it is the sandbox: **my checkout's branches shadow baseline's view of the same repo,
for my triggers, without touching anyone else's.** A verse whose reflections are all shared has
no way to say "in here, the repo looks like this", which is the question a sandbox exists to
answer.

Nothing new is needed to build it. `Store.Scope(name)` gives the scoped store the Reflector
writes through, a scope's engine already exists and is started lazily
(`Server.scopeEngine` → `Engine.ObserveScoped`), and copy-on-write already has the semantics
this wants: reads see the scope overlaid on baseline, writes land in the scope
(`TestLogdStore_ScopeIsolation`). So the registration carries a `scope`, and a scoped
reflection fires that scope's triggers and nobody else's.

**The registration lives in the scope it reflects into**, which is what makes the lifetime
free: `DeleteScope` drops the overlay and takes the registration and everything reflected with
it, so a scoped source has no deregistration path to forget and cannot outlive the sandbox it
was for. Restart restores per-scope registrations the way baseline ones are restored, from the
scope they are in.

Shadowing is the feature, and it is why the ownership check is scoped. Two sources in one scope
must still be provably disjoint. A scoped source claiming what a baseline source also claims is
not a collision at all — it is the overlay doing its job, and refusing it would refuse the
motivating case.

**A scope can delete what it does not own, and this was the open question.** A scoped converge
tombstones what external truth no longer has, and inside a scope most of what it finds standing
is inherited from baseline — so if a scoped delete could only remove scope-local entities, a
scoped source could not say "that branch does not exist in this sandbox" and every scope would
be stuck with baseline's set as a floor. It can. Measured and now pinned by
`TestLogdStore_ScopeHidesAnInheritedEntity`: a scoped delete of an inherited entity hides it
from the scope's `Get` *and* its `List` (the face converge reads), leaves baseline holding it,
is undone by a scoped write, and is dropped with the overlay on `DeleteScope`. The event half
holds too — `TestScopedWatchSeesADeleteOfAnInheritedEntity`: a scoped subtree watch, which is
what a per-scope engine runs, seeds inherited entities and reports the hide as a `Delete`, so
the scope's triggers fire on it exactly as they would for an entity the scope wrote itself.
Read-through is not one-way, and the overlay is a full view rather than an append.

Two things to get right, then:

- **Writes and tombstones land in the scope, never in baseline.** It follows from holding a
  scoped store, which is exactly why it needs a test: a converge handed the baseline store by
  accident corrupts what everyone reads, and nothing in the reflection would say so.
- **`Covers` is evaluated against the scope's overlaid read**, so a baseline entity inherited
  into the scope and falling under a covered pattern *will* be tombstoned in the scope — that is
  the property above, doing its job. It is the honest meaning of "this scope's git is this
  checkout", and it is the reason `Covers` should be no wider than what the source actually
  knows: a scoped source that covers more than it can see makes the rest disappear for its own
  triggers.

Baseline stays the default and the common case: one shared reflection of a repo everything reads
is what most of a deployment wants, and `scope:` empty is what it gets.

## What this does not do

**No write-back.** A source reflects; acting on the external system stays the outbound executor
path ([the write half of expose](../roadmap/expose.md#the-write-half)).

**No paging.** One reflect answer is one payload in memory. A source of ten thousand issues
narrows its `Covers` and reflects a slice at a time, which is the same level-triggered pass at a
finer grain. If that ever stops being enough the answer is a paged reflect, not a stream — a
stream would put ordering back into a contract whose whole point is that it has none.

**No authority.** verse dials a url an entity-writer chose and accepts hints from anyone, which
is the same edge the runner install probe already opens, and lands with
[authority](../roadmap/authority.md) rather than before it.

**Nothing new about credentials.** They belong to the source process, which is a gain and worth
saying: a GitHub source's webhook arrives from the internet and needs an ingress and a signature
check, and once the source is a deployed program that is *its* problem to solve where it already
has to be solved, instead of a public face verse has to grow.

## What to build, and what would falsify it

0. ~~Find out what a scoped tombstone does.~~ **Done**, before the rest, because two claims
   depended on it: `TestLogdStore_ScopeHidesAnInheritedEntity` and
   `TestScopedWatchSeesADeleteOfAnInheritedEntity` in package `entity`. The answer is the one
   scoped sources need, so nothing below has to route around the store.
1. ~~**Split `Reconcile` into `Reflect` + converge**~~ **Done.** `git.go` kept its plumbing and
   lost `converge`; `git_test.go` survived a one-line edit (it drives `Reflector.Reconcile`
   now). Three things the design did not predict, each pinned in `source/converge_test.go`:

   - **Both of git's special cases dissolved into `Covers`, as hoped, and so did a third
     nobody had named.** `converge` used to skip re-writing a commit because commits are
     immutable and skip tombstoning one because history is kept; the first is now just payload
     equality and the second is git declining to cover `git:commit:*`. No git-shaped branch
     survives in verse.
   - **A write has to REPLACE, not merge, and that was a latent bug.** `Store.Write` merges, so
     a field the source stopped reporting used to stay standing — and since the
     unchanged-check compares against what the source *said*, the entity then differed on every
     pass forever, firing its triggers each time. A detached HEAD does exactly this (`ref`
     vanishes from the head payload). Converge goes through `entity.Modify`, which is the
     tree's own replace primitive, and pays its extra read only for an entity that changed.
   - **The store absorbs a no-op patch**, so the obvious test for "skip unchanged" — assert no
     events — passes whether or not converge skips anything. It was measuring logd's
     idempotence. The law is about what converge ISSUES, so the test counts writes through a
     wrapping store; all four laws are mutation-checked.

   One check the design did not have and now does: a cover with **no system** is refused before
   anything is written. An empty dimension in an `entity.Pattern` is a *wildcard*, so such a
   cover does not claim nothing — it claims everything the pass looked at, and tombstones
   whatever the answer did not carry.

   `Webhook` came off the `Source` interface, since an external source cannot be handed an
   `*http.Request`; it is an optional `Hinter` the in-process git source still satisfies, and
   step 3's hint route retires it.
2. ~~**`source.HTTP` + `sourced` + the sample.**~~ **Done.** `source/http.go` (client, probe,
   descriptor), `source/sourced` (two routes, no state), and
   `docs/reference/examples/source/main.go`, which serves `source.Git` so the sample works
   before you change the one function it marks. Four things worth recording:

   - **`entity.Pattern` carries the wire, rather than a twin in `source`.** Tagging it needed
     the `//tony:schemagen=verse-pattern` directive — without it codegen silently skips a type
     however it is tagged, which is what made the twin look necessary at first. The predicate
     is spelled `value:`, the charter's own name for it, not the `fields:` this note's example
     used: `trigger.Match` retired that spelling.
   - **`source.HTTP` takes the system**, rather than discovering it. `Source.System()` cannot
     fail and is asked on every converge; discovering it costs a call that can. The
     registration names it (`sources:<system>:<name>`), and whether the far side AGREES is the
     separate question the install probe asks once, with an operator present.
   - **A failed reflect is an error, never an empty Reflection** — the sharpest bug this design
     admits, since an empty answer with covers means "tombstone all of it". Pinned end to end:
     a reconcile over a failing source reports the failure and leaves the slice standing.
   - **The Go round-trip test does not pin the wire.** Both ends use the same struct, so a
     renamed tony tag renames symmetrically and nothing notices — the first mutation check
     passed with `covers` renamed. The field NAMES are the contract a source written in
     another language depends on, so a second test reads the bytes; both mutations fail it.

   `Owns` on the descriptor is built here rather than in step 3: it is an optional `Owner`
   interface, and git's answer is one kind-less `{system: git, value: {repo: <id>}}` per repo —
   coarser than a Covers on purpose, since a second source must not write a repo's commits
   either.
3. **The registration**: entity, probe, the two extra checks, the hint route, the removal fork,
   and `scope` — decomposed into seven steps in [the plan](sources-plan.md), which is also
   where the two decisions above were forced out of this note. Restart, durability and
   tombstoning were to have been the worktree's; since that worktree is not here, they are
   built.
4. **Dial-in over xap**, which is a `DialContext` on verse's side and a `Listen` on the
   source's. The test is the same one step 2 runs, through a third transport: same
   `source.Git`, same `Reflection`, no ingress.

Two claims were load-bearing and unproved when this was written. **One is now measured**:
converge inside a scope behaves as the store says it does — `TestAScopedReflectionHidesAnInheritedEntity`
runs a real reflection on top of the primitive, and a scoped pass hides a branch inherited from
baseline while leaving baseline holding it.

**The other stands, and moved.** It was "is `Covers` as a list of `entity.Pattern`s expressive
enough for a source that is not git" — and the answer for *ownership* turned out to be that
`Covers` was the wrong shape for it entirely: an install compares declarations with no entities
in hand, so ownership became a ref glob and `Covers` stayed a `Pattern` for the pass. The
untested claim is now about that glob: it works when a source's ids encode ownership
positionally, which git's do only because `git.go` prefixes every id with the repo. The honest
test is still the same one — a system whose truth is not content-addressed and whose ownership
does not fall where the address does, `linear:issue:ENG-123` owned by project ENG. A second git
source on another host exercises the transport and says nothing about the vocabulary.
