# Writing a docd controller for verse

A controller is how verse gains a subtree of the entity-verse that isn't backed by logd
storage: a [`libctl.Handler`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/system/libctl)
mounted at a [kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath), to
which docd routes every client operation (`Match`/`Patch`/`Watch`) for that subtree by
longest-prefix mount. To verse's store client — which points at docd's client address — a
controller-served subtree is indistinguishable from logd: `demo:*`/`process:*` resolve to
logd, `local:dir-content-*` (and, ahead, `git:*`, `k8s:*`) resolve to a controller, with no
store rewrite.

You can see one running in [tutorial §8](tutorial.md#8-federate-your-local-working-tree),
where `verse connect` mounts the content controller over your working tree.

Two things are built as controllers today: the content controller (`source/content.go`
— serves file bytes on demand under `local:dir-content-<scope>:*`) and, ahead, every
Source ([sources](sources.md) — reflect an external system into the slice it owns). This
note is the how-to and, more importantly, the invariants a controller must honor —
several of them learned the hard way, so read them before you write a `Watch`.

## The Handler contract

```go
type Handler interface {
    Match(ctx, path string, pattern *ir.Node, opts MatchParams) (*ir.Node, error)
    Patch(ctx, path string, data *ir.Node, opts PatchParams) (*ir.Node, error)
    Watch(ctx, path string, opts WatchParams, emit func(*api.WatchEvent) error) error
}
```

- `Match` returns the data at `path`. `pattern` is the client's optional field
  selection/filter; `opts.Scope` is the copy-on-write scope the read belongs to (a
  scope-aware controller returns its scoped view; a scope-agnostic one may ignore it).
- `Patch` applies a write and returns the result. `opts.TxID` means the client is
  coordinating a multi-mount transaction the controller joins by writing to logd with
  that tx id; `opts.Match` carries a CAS precondition. A read-only controller returns
  `libctl.ErrUnsupported`.
- `Watch` streams events until `ctx` is cancelled. The first event's `State` is the
  full state at `path`; every subsequent event's `Patch` is root-rooted — the
  delta expressed from the document root (a change to `a.b.x` is `{a:{b:{x:…}}}`), not
  relative to `path`. docd composes an ancestor watch by forwarding these absolute
  deltas unchanged, so you must emit root-rooted patches.

Mount it with `RunController`, which blocks for the controller's lifetime:

```go
libctl.RunController(ctx, &libctl.ControllerConfig{
    DocdAddr:   mountAddr,                 // docd's mount (controller-facing) face
    Controller: "content-" + scope,        // identifies this controller to docd
    Path:       entity.MountKPath(system, kind), // <root>.<system>.<kind> — store & controller agree
    Handler:    source.NewContent(root, mount),
})
```

`entity.MountKPath(system, kind)` renders the kpath so the store (which maps a ref
`system:kind:id` to `<root>.<system>.<kind>.<id>`) and the controller agree on where the
subtree lives. Cancel `ctx` to unmount (docd tombstones the mount cleanly).

Readiness. `RunController` establishes the mount synchronously but exposes no
readiness signal, and a mount is not instantaneous. Do not write metadata that fires
consumers (a trigger's validators) until the mount actually serves — probe first, as
`cmd/verse/connect.go`'s `waitContentReady` does (a baseline read of a known key until it
returns), or you race a not-yet-serving mount and consumers see "not found".

## The load-bearing invariant: no root watches

A watch on the whole verse (or any ancestor of where controllers mount) is untenable,
for two independent reasons:

1. A mount force-ends overlapping watches. Mount/unmount coordinates with the watch
   readers overlapping the mounted path: docd waits `force_after` (default 5s) for them
   to drain, then force-ends them (`WatchEndedError` reason `membership_changed`). A
   *standing* root watch overlaps every mount under the root, so every `connect`/every
   Source mount stalls ~5s and then kills the reader. (This is why `verse connect` once
   took ~5s and silently broke the engine; see `verse-root-watches-eliminated`.)
2. A root watch depends on every controller under it. docd composes an ancestor
   watch by folding in each overlapping mount's `Watch` stream. If any one controller
   declines `Watch`, the composed ancestor watch has no stream for that subtree and
   cannot establish. A root watch is thus hostage to every controller in the tree.

Hence: no root watches. An engine (or any consumer) watches exactly the
`(system[,kind])` subtree it matches — `LogdStore.WatchSubtree` / `Engine.ObserveScoped`
— never the root. A subtree watch collides with a mount only if the mount is *in that
subtree*, which is rare and, when it happens, is survived by re-establishing the watch
and resyncing (below), not by dying. An unfiltered `verse watch` (no `--system`/`--kind`)
may take a transient root watch while a human is actively watching, and it retries on a
force-end — but nothing holds one at rest.

## Invariants for the Handler itself

1. Implement `Watch` even when read-only — as a no-op, never `ErrUnsupported`. Per
   the point above, a declining `Watch` breaks any composed ancestor watch. A read-only
   controller's `Watch` blocks until `ctx` is cancelled and emits nothing:

   ```go
   func (c *Content) Watch(ctx context.Context, _ string, _ libctl.WatchParams,
       _ func(*logdapi.WatchEvent) error) error {
       <-ctx.Done() // "watched", contributes no events; do NOT return ErrUnsupported
       return ctx.Err()
   }
   ```
   `Patch` may still decline (`ErrUnsupported`) — a Patch decline doesn't poison an
   ancestor's composition the way a Watch decline does.

2. Only valid UTF-8, bounded content on the wire. A node carrying non-UTF-8 bytes
   fails docd's tokenizer (`read error: bad utf8`), which resets the controller's
   session and tombstones the whole mount — after which every `read_file` in the
   session fails "not found". Never put raw bytes on the wire: guard with `utf8.Valid`
   and serve a marker instead (as `source/content.go` returns `{path, size,
   binary:"non-utf8"}`, and `{truncated:"size-cap"}` for an over-cap file). Bound size
   too; a subtree read shouldn't stream an unbounded blob into one node.

3. Own the subtree — be its single writer. A controller owns `system:*` (or a
   `system:kind:*` slice) and is the only writer there, so reflected data needs no CAS
   (the parked lost-update concern doesn't apply to a single-writer slice). Mount
   per-system or per-`(system,kind)`.

## Consuming a controller's stream: apply, don't decode

A subtree watcher must apply each event and read entities from the reconstructed
object — never interpret the patch's structure. A scoped (copy-on-write) watch emits
*computed* diffs whose first delta, from a null baseline, is a `!replace` a merge-walk
can't decode (it silently drops that entity — the N→N−1-validators bug). Maintain the
subtree as a document, apply each event
([`tony.Patch`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony#Patch) for a
Patch, replace for a State), and diff the reconstructed entity set against the last snapshot
for `Set`/`Delete`. On a
`membership_changed` force-end, re-establish the watch (it re-composes against the current
mount set) and let the fresh `State` resync close the gap. See [change events](change-events.md)
and `LogdStore.WatchSubtree`.

## Reflecting an external system: webhook is a hint, reconcile is truth

For a Source (reflect git/GitHub/k8s/… into its owned slice), the internal mechanism that
keeps `Match`/`Watch` current follows `webhook : poll-reconcile :: Observe : Since`:

> A webhook never writes entities. It triggers a *reconcile* of the affected ref(s); the
> reconcile reads the external source of truth and writes to converge.

That buys idempotency, self-healing, and order/duplicate independence. A periodic full
reconcile is the safety net for missed hooks. The controller's own progress (a poll
cursor: `updated-since`, etag, page token) lives as an entity (`source:cursor:<system>`)
so it's observable like everything else. Reflection is inbound; acting *on* the external
system (comment, merge, deploy) is the outbound executor path — a gated effect, not the
Source. See [sources](sources.md).

## Scope-awareness

`opts.Scope` on `Match`/`Patch`/`Watch` is the COW scope of the operation. A scope-aware
controller returns/writes the scoped view (a logd-backed one carries the scope on its
logd connection). A controller may also be scope-agnostic when isolation comes from
elsewhere: the content controller ignores `opts.Scope` because each `connect` session
mounts a distinct nonce'd kind (`dir-content-<scope>`), so the mount itself is per-session
— the nonce, not a COW scope, provides isolation.

## Lifecycle

`RunController` blocks until `ctx` is cancelled or the connection ends. Run it under a
context you cancel on shutdown (and reap the goroutine), so the mount is tombstoned
cleanly rather than orphaned. `verse connect` runs the content controller for the
session's lifetime and drops it (and its scope) on exit.

## Checklist

- [ ] Mount at `entity.MountKPath(system, kind)`; own that slice as sole writer.
- [ ] `Watch` implemented (no-op + block if read-only), emitting root-rooted patches.
- [ ] No non-UTF-8 / unbounded bytes on the wire — serve markers.
- [ ] No root/ancestor watch anywhere in the consuming path; watch the matched subtree.
- [ ] Consumers apply deltas (`tony.Patch`), don't decode them; resync on
      `membership_changed`.
- [ ] Readiness probed before firing consumers.
- [ ] (Source) webhook triggers reconcile; reconcile is the single writer; cursor is an
      entity.
