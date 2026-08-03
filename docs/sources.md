# Sources: reflecting external systems into the verse

A Source reflects an external system — a git repo, GitHub, Linear — into the slice of the
entity-verse it owns (`git:*`, `github:*`, …), and is the single writer of that slice. Triggers
and agents *read* the reflected entities and react. A source only reflects: acting back on the
external system is the outbound path, not this one.

For the surface — the entity, the verbs, the routes, the contract a source serves — see
[reference/sources.md](reference/sources.md). To build one, copy
[examples/source](reference/examples/source/main.go), which is a real program that compiles
with the tree.

```sh
verse source add git:laptop http://build-host:9801
verse sources
```

## The core law: a hint is a hint, reconcile is the truth

External systems offer two integration modes, and they are the same split verse already runs
internally:

> webhook : poll-reconcile :: Observe : Since

A webhook is a low-latency hint ("PR 42 changed") that may be missed, duplicated, or arrive out
of order. A poll-reconcile is the authoritative, level-triggered pass that reads external truth
and converges the owned slice. So:

> **A hint never writes entities.** It schedules a reconcile of the affected ref(s); the
> reconcile reads the source of truth and writes to converge.

That one rule buys idempotency, self-healing, and order/duplicate independence for free — you
converge against the system's real state, not a flaky delivery. Translating a webhook payload
straight into entity writes is the same mistake as trusting `Observe` without `Since`. A
periodic full reconcile is the net that catches every missed hint.

**verse does not parse deliveries at all.** The source owns its own — it deployed the git hook,
it holds the webhook secret — and tells verse which refs went stale. A second implementation of
"map a delivery to refs" per source is exactly how the law comes to be broken in one of two
places, and across a network a divergence is least recoverable. It pays twice: a delivery
carrying no data can cost at most a reconcile of a slice its sender does not get to choose the
contents of, so the hint route can stay unauthenticated in a wide-open dogfood verse.

## Ownership = single writer = no CAS

A Source owns `github:*` and is its only writer, so within that slice there is no lost-update
problem — which is the escape hatch for the parked CAS finding: reflected slices need no
compare-and-swap because they have one writer. (CAS still matters where several triggers write
a *shared subject*, not a reflected slice.)

In process that held because there was one `Git` value. Across deployments nothing holds it but
the install check below.

## The seam: a source answers, verse converges

```go
// A Source is NOT handed a store. It reads external truth and answers with it; converging
// the store with that answer is the Reflector's job.
type Source interface {
    System() string
    Reflect(ctx context.Context, ref entity.Ref) (Reflection, error)
}

type Reflection struct {
    Covers   []entity.Pattern // what this answer is COMPLETE for: the tombstone authority
    Entities []entity.Entity
}
```

The store cannot cross a process boundary — but that is not the reason it is on this side of
the seam. Shipping a store client is trivial and `POST /entity` already exists, so an external
program could write `git:*` today. The reason is what converge *does*, which must not be
re-implemented once per source:

- **skip unchanged**, or every periodic resync re-fires every trigger over the slice;
- **tombstone what is gone**, scoped to what the pass covered, so one repo's reconcile never
  deletes another's refs;
- **never tombstone what the answer did not claim**.

None of that is knowledge about git. All of it is knowledge about the entity-verse, and a
connector author holding it will get it wrong in a way nothing notices until a charter fires
two hundred times.

`Covers` is the load-bearing field, and it is not new information — it is the scoping a
reconcile always had, said out loud instead of held in a parameter. An entity under a covered
pattern and absent from `Entities` is tombstoned; an entity in `Entities` under no covered
pattern is written and never tombstoned. Git's "keep history" is then a declaration in the
answer rather than a branch in verse's code: it covers `git:ref:*` and `git:head:*` for a repo
and does not cover `git:commit:*`, so a commit that has fallen out of the bounded tip window is
simply not mentioned, and stays.

A write **replaces** rather than merges, because the answer is what the entity IS: a field the
source stopped reporting is gone. Merging would leave the stale field standing — and since the
unchanged-check compares against what the source said, the entity would then differ on every
pass forever, firing its triggers each time.

## A source you deploy

`source/sourced` serves the contract as two routes — `GET /source` for a descriptor, `GET
/reflect` for a `Reflection` — and `source.HTTP` is the client verse drives it with. That is
`processd`'s trick, for `processd`'s reason: `source.Git` behind `sourced` *is* the sample
external source, and the same value behind verse's own reflector is the built-in, so there is
no privileged local path for a deployed one to differ from.

**Three ways to reach one, one contract.** A registration carries exactly one of `url` (verse
dials), `dir` (the in-process git source), or `dial-in` (the source dials — designed, not
built; see [what is left](working/sources-residual.md)). They differ only in where the
`net.Conn` comes from; `Reflect` is the same call down all three. This is deliberately not the
runner's "a runner with no url is refused": there, absence already meant `local`, so a url-less
entity was ambiguous. Here the field present says which it is.

**verse dials; the hint reverses.** A pushed reflection would move the reconcile loop into
every source, which is the duplication above one level up and across a network. Pulled, a
source is a pure function of external truth: no ticker, no backoff, no retry policy, no diff,
no cursor, no CAS — which is what keeps the sample program short enough to copy.

One rule the transport must not break: **a failed reflect is an error, never an empty
Reflection.** An empty answer carrying covers is a legitimate instruction to tombstone
everything under them, so a status nobody read or a body nobody could decode, returned as the
zero value, would spell a broken source as an emptied external system — and converge would
believe it.

## Installing one, and the two checks

Adding a source **probes it**, with the runner's three outcomes and for the runner's reasons —
*a source* (it answered and decoded), *not a source* (it answered and is not that: refused),
*unreachable* (installed, with a warning, because a manifest landing before its pod is the
ordinary state of a source deployed alongside the entity that names it).

Two checks have no runner analogue, and both are what an external source loses that an
in-process one had by construction:

- **The descriptor's system must be the one the operator named.** A `github` registration
  pointed at a git source is unrecoverable later: every reflection would be written into the
  wrong slice by a source that thinks it is right.
- **Ownership must be provably disjoint from every standing registration in the same scope**,
  or the install is refused, with both claims in the message — verse could not prove them
  apart, so the operator is the one who can say. `--allow-overlap` is the way past, with the
  cost stated rather than discovered: two writers on one slice, each tombstoning what the other
  reflected.

**An ownership claim is an address, not a payload predicate**, and the reason is where the
question is asked. Converge evaluates `Covers` against stored entities whose payloads it can
read; an install compares two *declarations* with nothing in hand. Addresses compare
statically; predicates do not. So `owns` is a ref with `*` allowed — `git:*:verse/*` — with id
globs restricted to a literal prefix and a trailing `*`, which makes disjointness a string
comparison where general glob disjointness would be automata intersection. Two claims are
disjoint when any segment is; a `*` segment is disjoint from nothing, so `git:*:*` claims the
lot and can be proved apart from nothing in its own system.

## Removing one, and the fork

Tombstoning what a source wrote is right about the common case — a slice nobody reflects any
more is a slice nothing can correct, and a charter would go on believing in a branch of a repo
verse has stopped watching. It is wrong about one that will happen: **moving a repo to another
host is a remove and an add of the same physical truth**, and tombstoning it fires every
branch-deleted trigger in the verse and then every branch-created one behind it — a bookkeeping
change destroying and re-creating live facts, which is the runner's rename case in a different
costume.

So removal with a non-empty reflected slice is **refused until told which**: `--forget`
(the reflection is over) or `--keep` (the same truth is moving; leave the last known state, now
frozen and quietly ageing). A removal with nothing reflected needs no flag, there being nothing
to be wrong about. What `--forget` may delete is bounded by `owns` and nothing else: it can only
tombstone what the install proved this source was the single writer of.

## What verse records, and what it refuses to

`lastReflect` and `lastError` go on the registration. `lastError` is written on **transition**,
so a source failing every minute writes once and the write that clears it is the one that says
it recovered. `lastReflect` is written only when a pass actually **wrote** something — it is
the poll cursor, and stamping it every pass would put a write on `sources:*` once per resync
interval forever, so anything watching that slice would fire on a verse where nothing is
happening.

That cursor cannot catch a source that has *wedged* — alive, answering, reflecting nothing new
— because on a quiet repo a healthy source looks identical. So **when a pass last completed is
reported and never stored**, read live from the reflector on `verse sources`. That is the
runner's rule about opinions of wellness: a reachability fact that is written down can be stale
in the direction that costs.

A clean shutdown is not a failure and is not recorded as one. Cancelling a reflector fails the
pass in flight; written down, every source would come back from a clean stop carrying it, and a
listing after a restart would show a verse full of broken sources that are all fine.

## Scoped sources

A source registers into a scope, and the obvious-sounding alternative — a reflection is a fact
about the outside world, so every scope should read the same one — is wrong. `verse connect`
already refutes it: it reflects a working tree into a per-session scope, which is a scoped
source built as a special case. The general form is the sandbox: **my checkout's branches
shadow baseline's view of the same repo, for my triggers, without touching anyone else's.** A
verse whose reflections are all shared has no way to say "in here, the repo looks like this",
which is the question a sandbox exists to answer.

Shadowing is the feature, which is why the ownership check is scoped: two sources in one scope
must still be provably disjoint, while a scoped source claiming what a baseline source also
claims is the overlay doing its job. **The registration lives in the scope it reflects into**,
so `DeleteScope` drops the overlay and takes the registration, everything reflected, and the
reflector with it — a scoped source has no deregistration path to forget and cannot outlive the
sandbox it was for.

**A scope can hide what it does not own**, and this was the open question. A scoped converge
tombstones what external truth no longer has, and inside a scope most of what it finds standing
is inherited from baseline — so if a scoped delete could only remove scope-local entities, a
scoped source could not say "that branch does not exist in this sandbox", and every scope would
be stuck with baseline's set as a floor. It can: the tombstone hides the inherited entity from
the scope's reads and its watches, leaves baseline holding it, and is dropped with the overlay.

Baseline stays the default: one shared reflection everything reads is what most of a deployment
wants, and an empty `scope` is what it gets.

## Git: the exemplar

A git repo fits the pattern more cleanly than the SaaS ones, needs no credentials or network,
and is SDLC-native.

- The "webhook" is a git hook — `reference-transaction` locally, `post-receive` on a bare repo
  — curling the registration's hint route with the ref that names the repo. No signing, no
  network, and nothing in the delivery but which slice went stale (`Git.InstallHook`).
- Reflection is **cursorless**, because git is content-addressed: `git for-each-ref` yields a
  `ref→sha` map, and comparing shas against the stored entities *is* the level-triggered diff —
  no since-token, no missed-event window.
- It is **multi-repo native**: a repo is registered under an id, every entity carries it, and a
  pass is per-repo, so one repo's reconcile never tombstones another's refs.
- It reflects git-*aware* state — branch, HEAD, dirty, ahead/behind — which is what SDLC flows
  react to ("commit landed on main", "branch deleted", "tag created"), not "a byte changed". An
  index, not a warehouse: a process needing a diff follows the ref into the real repo.

## A Source is not a controller, and cannot be

An earlier version of this note ended with the opposite: that a `Source` *is* a `libctl.Handler`
run under `RunController`, mounted at its system's subtree. That was written when controller
mounts still rendered under the entity root, and it is now wrong. It is recorded rather than
deleted because the mistake is an easy one to make twice — the shape really does fit, right up
until you ask what fires.

**A mounted slice fires no triggers.** Mounts render under `entity.MountRoot`, a top-level
segment disjoint from the entity root, because a mount is process-driven and so has no history,
and a history-bearing watch must never span one. `LogdStore.WatchSubtree` renders only under the
entity root. So a controller-served slice is readable by its ordinary ref and invisible to every
watch there is: no change events, no `Since` replay, nothing a charter can react to. A git
Source mounted this way would reflect a repo perfectly and fire nothing, which is the entire
point of it. Mounting under the entity root instead is not a trade — it re-creates the composed
root watch that silently drops `FromCommit`, which is what `MountRoot` exists to prevent. See
[writing a controller](controllers.md) for both halves of that rule.

The line it draws is clean, and `connect` sits on both sides of it: **bytes somebody asks for
may be a mount; facts something reacts to must be ordinary writes.** A Source is entirely the
second kind, and stays a verse-level interface with nothing underneath it.

## What a Source does not do

**No write-back.** A source reflects; acting on the external system stays the outbound executor
path ([the write half of expose](roadmap/expose.md#the-write-half)).

**No paging.** One reflect answer is one payload in memory. A source of ten thousand issues
narrows its `Covers` and reflects a slice at a time, which is the same level-triggered pass at a
finer grain. If that stops being enough the answer is a paged reflect, not a stream — a stream
would put ordering back into a contract whose whole point is that it has none.

**No authority.** verse dials a url an entity-writer chose and accepts hints from anyone, which
is the same edge the runner install probe already opens, and lands with
[authority](roadmap/authority.md) rather than before it.

**Nothing new about credentials**, which is a gain worth saying: a GitHub source's webhook
arrives from the internet and needs an ingress and a signature check, and once the source is a
deployed program that is *its* problem to solve where it already has to be solved, instead of a
public face verse has to grow.

What is designed and not built — dial-in, for the source verse cannot reach — is in
[what is left](working/sources-residual.md).
