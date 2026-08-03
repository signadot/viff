# Sources: reflecting external systems into the verse

Status: built as a library, not yet reachable from the CLI. The `Source` interface, its
`Reflector` and the converge the Reflector owns are in `source/reflector.go`, and
`source/git.go` is a multi-repo git Source — `Reflect`, `Webhook`, and
`InstallHook`/`RemoveHook`, with tests. What is missing is the wiring: no command registers a
repo or starts a reflector, so `NewGit` has no caller outside its test. Reading this as a
design sketch is the one mistake to avoid; the design landed.

Local-tree federation is built too, but as a special case rather than through the `Source`
below. `verse connect` federates the working tree under a per-session scope: it *writes*
scoped metadata (`local:dir-status-<scope>:*`) to docd, and runs a read-only content
controller (`source/content.go`, a `libctl.Handler` under `RunController`) that serves file
bytes on demand at `local:dir-content-<scope>:*`. Metadata is a scoped write rather than a
mounted Handler because docd mounts are not scope-specific; see
[content federation](content-federation.md).

## What a Source is

A Source reflects an external system — a git repo, GitHub, Linear, Notion — into
the slice of the entity-verse it owns (its system: `git:*`, `github:*`, …). It is
the single writer for that slice. Triggers and agents *read* the reflected entities
and react; the Source only reflects. Acting back *on* the external system (comment on
a PR, move an issue, push a tag) is a separate, outbound concern (see below).

verse already reflects two systems. `connect` federates the local tree (scoped
`local:dir-status-<scope>:*` metadata writes + a `local:dir-content-<scope>:*` content
controller), and `mirror` reflects processes (`process:instance:*`, event-based). A
Source generalizes the *remote* case — webhook + poll-reconcile against an external
truth. The half of `connect` it resembles is the *status* half: log-backed writes that
something reacts to, not the content controller beside them (see below, and
[content federation](content-federation.md)).

## The core law: a webhook is a hint, reconcile is the truth

External systems offer two integration modes, and they are the same split verse
already runs internally:

> webhook : poll-reconcile :: Observe : Since

A webhook is a low-latency hint ("PR 42 changed") that may be missed, duplicated,
or arrive out of order. A poll-reconcile is the authoritative, level-triggered
pass that reads external truth and converges the owned slice. So the load-bearing
rule:

> A webhook never writes entities. It triggers a reconcile of the affected ref(s);
> the reconcile reads the external source of truth and writes to converge.

That one rule buys idempotency, self-healing, and order/duplicate independence for
free — you converge against the system's real state, not a flaky delivery.
Translating a webhook payload straight into entity writes is the same mistake as
trusting `Observe` without `Since`. A periodic full reconcile is the safety net that
catches every missed hook.

## Ownership = single writer = no CAS for reflected data

A Source owns `github:*` and is its only writer, so within that slice there is no
lost-update problem — which is the escape hatch for the parked CAS finding: reflected
slices don't need compare-and-swap because they have one writer. (CAS still matters
where multiple triggers write a *shared subject*, not a reflected slice.)

## The interface (sketch)

```go
// A Source reflects an external system into the entity-verse slice it owns. It is NOT
// handed a store: it reads external truth and answers with it, and converging the
// store with that answer is the Reflector's job.
type Source interface {
    System() string // the owned slice: <System>:*

    // Reflect reads external truth for the slice `ref` names — a zero ref is
    // everything this source owns — and returns it. It does not write.
    Reflect(ctx context.Context, ref entity.Ref) (Reflection, error)
}

type Reflection struct {
    Covers   []entity.Pattern // what this answer is COMPLETE for: the tombstone authority
    Entities []entity.Entity
}
```

`Covers` is the load-bearing field. An entity under a covered pattern and absent from
`Entities` is tombstoned; an entity in `Entities` under no covered pattern is written and
never tombstoned — which is how git says "keep history": it covers `git:ref:*` and
`git:head:*` for a repo and does not cover `git:commit:*`, so a commit that has fallen out
of the bounded tip window is simply not mentioned, and stays.

Plus a `Reflector` (the informer shape) which owns the store side of the seam: an initial
full `Reconcile`, a periodic one as the resync net, and targeted ones kicked by hints.
Converge lives there, and there only, because the three laws it keeps — skip what is
unchanged, tombstone only what the answer covers, never tombstone what it does not — are
knowledge about the entity-verse rather than about git or GitHub, and a connector author
who had to hold them would get them wrong in a way nothing notices until a charter fires
two hundred times.

A source that receives its own delivery implements the optional `Hinter`
(`Webhook(*http.Request) ([]entity.Ref, error)`), which the Reflector serves at
`/hooks/<system>` and which schedules a reconcile and writes nothing. It is off the `Source`
interface because a source reached over a url cannot be handed an `*http.Request` at all: it
parses its own deliveries and hints verse with the refs.

Reflection is mechanical connector code, not an agent — judgment lives in the triggers
and agent-processes that react to the reflected entities.

Inbound vs outbound. `Source` is inbound (reflect external → verse). Acting on the
external system (the write-back: comment, merge, transition) is the outbound executor
path — a gated action/process calling the API, like any other effect. A connector
reflects; an action writes back. Don't conflate them.

Source state is dogfood. A poll cursor (updated-since, etag, page token) lives *as
an entity* (`source:cursor:github`) so the connector's own progress is observable in
the verse like everything else.

## Git: the exemplar (and the intended first Source)

A git repo fits the pattern more cleanly than the SaaS ones, needs no credentials or
network, and is SDLC-native — so it is the right first Source, and it is the one that is
built.

- The "webhook" is a git hook — `post-commit` / `reference-transaction` locally,
  `post-receive` on a bare repo — curling `/hooks/git`. No signing, no network. verse
  installs it the way `connect` installs its poll.
- Reflection is cursorless, because git is content-addressed: `git for-each-ref` →
  a `ref→sha` map, which converge diffs against the verse's `git:ref:*` entities. Comparing
  shas *is* the level-triggered diff — no since-token, no missed-event window in the poll.
- It subsumes `connect` — it reflects git-*aware* state (branch, HEAD, dirty,
  ahead/behind), which is what SDLC flows react to ("commit landed on main," "branch
  pushed," "tag created"), not "a byte changed."

Two truths, both reflected, index-not-warehouse:
- committed state — refs/commits/tags: immutable, content-addressed
- working tree — index + uncommitted: mutable (what `connect` reflects today)

Entities: `git:ref:main {sha,type}`, `git:head:<repo> {ref,sha,dirty,ahead,behind}`,
`git:commit:<sha> {msg,author,parents}` (bounded to the tip/recent range — a process
needing a diff follows the ref into the real repo via `git show`), and the working
tree via `connect`'s `local:dir-status-<scope>:*`. Single writer for `git:*`, so
idempotent and CAS-free.

Reflect, concretely — and it only reads; the diff-and-write half is converge's:
```
git for-each-ref …            → git:ref:<repo>/<short>, covered by {git, ref, {repo}}
git symbolic-ref HEAD; rev-parse HEAD; rev-list --count @{u}..  → git:head:<repo>
git log -n <depth>            → git:commit:<repo>/<sha>, covered by NOTHING (history)
```

A credential-free end-to-end test is straightforward: `git init` in a temp dir,
commit / branch / delete-branch, reconcile, assert the `git:ref:*` entities appear and
tombstone, and that a trigger fires on "commit on main."

## A Source is not a controller, and cannot be

An earlier version of this note ended with the opposite: that a verse `Source` *is* a
`libctl.Handler` run under `RunController`, mounted at its system's subtree, with the
reconcile freshening what `Match` returns. That was written when controller mounts still
rendered under the entity root, and it is now wrong. It is recorded rather than deleted
because the mistake is an easy one to make twice — the shape really does fit, right up
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
may be a mount; facts something reacts to must be ordinary writes.** So the reflected slice is
written to logd exactly as the sketch above has it — the Reflector converges by writing — and
`Source` stays a verse-level interface with nothing underneath it. docd is still in the path,
because verse's store points at it, but a Source has no more to do with that than any other
writer does.

## Next step

The git Source exists and reconciles; what it lacks is a way in. It needs a command that
registers a repo and runs it — `AddRepo` plus a `Reflector` under `verse up`, and a route the
installed hook can POST to — after which the end-to-end path (commit → hook → reconcile →
`git:ref:*` changes → a trigger fires) is exercisable rather than merely unit-tested.

Where that should land is [external sources](working/sources.md): a Source deployed and
installed the way a [runner](runners.md) is, reached over a url. The seam that makes it
possible is already cut — `Reconcile` is split into a `Reflect` the external program answers
and a converge that stays on verse's side — so what remains is the transport and the
registration.

Write-back stays deferred to the outbound executor path
([the write half of expose](roadmap/expose.md#the-write-half)); a Source only reflects.
