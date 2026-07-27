# Sources: reflecting external systems into the verse

Status: built as a library, not yet reachable from the CLI. The `Source` interface and its
`Reflector` are in `source/reflector.go`, and `source/git.go` is a multi-repo git Source —
`Reconcile`, `Webhook`, and `InstallHook`/`RemoveHook`, with tests. What is missing is the
wiring: no command registers a repo or starts a reflector, so `NewGit` has no caller outside
its test. Reading this as a design sketch is the one mistake to avoid; the design landed.

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
truth — and is the same "controller owns a subtree and answers for it" shape as the
content controller in [content federation](content-federation.md).

## The core law: a webhook is a hint, reconcile is the truth

External systems offer two integration modes, and they are the same split verse
already runs internally:

> webhook : poll-reconcile :: Observe : Since

A webhook is a low-latency hint ("PR 42 changed") that may be missed, duplicated,
or arrive out of order. A poll-reconcile is the authoritative, level-triggered
pass that reads external truth and converges the owned slice. So the load-bearing
rule:

> A webhook never writes entities. It triggers a reconcile of the affected ref(s);
> the reconcile reads the external source of truth and Put/Deletes to converge.

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
// A Source reflects an external system into the entity-verse slice it owns.
type Source interface {
    System() string // the owned slice: <System>:*

    // Reconcile converges the owned slice with external truth. ref == zero → a full
    // resync; a concrete ref → the targeted reconcile a webhook asked for. It reads
    // the external system and Put/Deletes entities; it is the authority.
    Reconcile(ctx context.Context, store entity.Store, ref entity.Ref) error

    // Webhook maps a raw delivery to the ref(s) to reconcile — it does NOT write
    // entities. Nil/zero requests a full resync.
    Webhook(r *http.Request) ([]entity.Ref, error)
}
```

Plus a `Reflector` (the informer shape): serve `/hooks/<system>` → `Webhook` → enqueue
a targeted `Reconcile`; and run a periodic full `Reconcile` as the resync net.
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
network, and is SDLC-native — so it is the right first Source once the controller
primitive is ready.

- The "webhook" is a git hook — `post-commit` / `reference-transaction` locally,
  `post-receive` on a bare repo — curling `/hooks/git`. No signing, no network. verse
  installs it the way `connect` installs its poll.
- Reconcile is cursorless, because git is content-addressed: `git for-each-ref` →
  a `ref→sha` map, diffed against the verse's `git:ref:*` entities. Comparing shas
  *is* the level-triggered diff — no since-token, no missed-event window in the poll.
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

Reconcile, concretely:
```
git for-each-ref …            → desired ref set; diff vs List(git:ref:*); Put/Delete
git symbolic-ref HEAD; rev-parse HEAD; rev-list --count @{u}..  → git:head:<repo>
git status --porcelain        → working-tree entities
```

A credential-free end-to-end test is straightforward: `git init` in a temp dir,
commit / branch / delete-branch, reconcile, assert the `git:ref:*` entities appear and
tombstone, and that a trigger fires on "commit on main."

## Built on docd's controller primitive

The docd and controller work has landed in
[go-tony's `libctl`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/system/libctl),
and a Source maps onto it directly. The `Source` sketch above is the *verse-facing* shape;
the *substrate* is:

- `libctl.Handler` — `Match(path, pattern)`, `Patch(path, data, opts)`,
  `Watch(path, opts, emit)`. A controller mounts a subtree via `RunController` and
  docd forwards client operations for that subtree to these methods. A read-only
  reflector declines `Patch` (`ErrUnsupported`) but must still implement `Watch` —
  a Source's `Watch` emits its reflected changes, and even a purely on-demand controller
  must serve `Watch` as a no-op rather than decline it, because docd composes an ancestor
  watch by folding in each mount's `Watch` and a declining mount breaks it (hence: no
  root watches; see [writing a controller](controllers.md) for the full contract and invariants). "The
  controller owns the subtree and answers for it; content need not live in logd" — so a
  git Source answers `Match`/`Watch` straight from the repo.
- docd speaks the logd session protocol to clients, routing each op by
  longest-prefix mount to the owning controller (and everything else to logd). So
  verse's store client points at docd's client address unchanged and gets
  federation for free: `demo:*`/`process:*` resolve to logd, `git:*` to the git
  controller. No store rewrite.
- The `Reconcile`/`Webhook` split above is the Source's *internal* mechanism for
  staying current — the poll and the git hook drive what `Watch` emits and freshen
  what `Match` returns. The webhook-is-a-hint law lives inside the Handler.

So the mapping is: a verse `Source` = a `libctl.Handler` (+ its internal
poll/webhook reconcile) run under `RunController`, mounted at its system's subtree;
verse's store points at docd; triggers and the mirror are unchanged.

## Next step

The git Source exists and reconciles; what it lacks is a way in. It needs a command that
registers a repo and runs it — `AddRepo` plus a `Reflector` under `verse up`, and a route the
installed hook can POST to — after which the end-to-end path (commit → hook → reconcile →
`git:ref:*` changes → a trigger fires) is exercisable rather than merely unit-tested.

Write-back stays deferred to the outbound executor path
([the write half of expose](roadmap/expose.md#the-write-half)); a Source only reflects.
