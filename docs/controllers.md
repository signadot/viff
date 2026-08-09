# Writing a docd controller for verse

A controller is how verse gains a subtree of the entity-verse that isn't backed by logd
storage: a [`libctl.Handler`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/system/libctl)
mounted at a [kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath), to
which docd routes every client operation (`Match`/`Patch`/`Watch`) for that subtree by
longest-prefix mount. To verse's store client — which points at docd's client address — a
controller-served subtree is indistinguishable *to a read*: `demo:*`/`task:*` resolve to
logd, `local:dir-content-*` resolves to a controller, with no store rewrite. To a *watch* it is
not indistinguishable at all, which is the first thing to know and is below.

You can see one running in [reference/connect.md](reference/connect.md),
where `verse connect` mounts the content controller over your working tree.

One thing is built as a controller today: the content controller (`source/content.go` — serves
file bytes on demand under `local:dir-content-<scope>:*`). This note is the how-to and, more
importantly, the invariants a controller must honor — several of them learned the hard way, so
read them before you write a `Watch`, starting with the one that decides whether to write a
controller at all.

## The first question: should this be a controller at all?

**A mounted slice fires no triggers.** Mounts live under `entity.MountRoot`, a top-level segment
disjoint from the entity root, because a mount is task-driven and so has no history, and a
history-bearing watch must never span one (`entity/logdstore.go`, and the "no root watches"
invariant below is the other half of the same story). `refPath` renders a mount-owned slice
under `MountRoot`; `LogdStore.WatchSubtree` renders only under the entity root. So a
controller-served subtree is reachable by its ordinary ref and **invisible to every watch there
is** — no change events, no `Since` replay, and therefore nothing a charter can react to.

That gives a rule with no exceptions: **bytes somebody asks for may be a mount; facts something
reacts to must be ordinary writes.** `verse connect` is the worked example on both sides of it,
federating one working tree as two slices — file bytes served on demand by the content
controller, and `dir-status-<scope>` metadata written log-backed precisely because it is what
the per-file validator trigger matches. `source.TestFederationKindsAreMountOwned` pins the split
so neither half drifts into the other.

The escape that suggests itself does not exist. Mounting under the entity root would put the
slice back where watches can see it, and re-create exactly what `MountRoot` was introduced to
fix: a composed root watch that silently drops `FromCommit` and returns a successful-looking
empty replay, plus a mount that stalls ~5s on the overlapping watch and then force-ends it.

This is why a [Source](sources.md) is *not* a controller, though this note and that one both
once said it would be: a Source exists so that triggers fire on external change, so its
reflected entities are writes. See [sources](sources.md).

## The Handler contract

```go
type Handler interface {
    Match(ctx context.Context, path string, pattern *ir.Node, opts MatchParams) (*ir.Node, error)
    Patch(ctx context.Context, path string, data *ir.Node, opts PatchParams) (*api.PatchResult, error)
    Watch(ctx context.Context, path string, opts WatchParams, emit func(*api.WatchEvent) error) error
}
```

- `Match` returns the data at `path`. `pattern` is the client's optional field
  selection/filter; `opts.Scope` is the copy-on-write scope the read belongs to (a
  scope-aware controller returns its scoped view; a scope-agnostic one may ignore it).
- `Patch` applies a write and reports where it landed: `PatchResult.Commit` is the commit
  it committed at, `Data` the resulting data (with any generated ids). A logd-backed
  controller returns its `LogdSession.PatchWith` result unchanged, so the client sees the
  same commit a direct logd write would give it. A *self-backed* controller has no logd
  commit to report and leaves `Commit` zero rather than invent one from its own timeline —
  the same reason it cannot serve `opts.Commit` on a read. `opts.TxID` means the client is
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
    Path:       entity.MountKPath(system, kind), // <MountRoot>.<system>.<kind> — store & controller agree
    Handler:    source.NewContent(root, mount),
})
```

`entity.MountKPath(system, kind)` renders the kpath so the store and the controller agree on
where the subtree lives — under `MountRoot`, not under the entity root, per the rule above. The
entity namespace is unchanged by that: the slice is still addressed as the ordinary ref
`system:kind:id`, and only the path it renders to differs. Add the `(system, kind-prefix)` pair
to `entity.mountedKinds` so `refPath` routes reads there; a mount the store does not know about
is a mount every read misses. Cancel `ctx` to unmount (docd tombstones the mount cleanly).

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
   to drain, then force-ends them (`WatchEndedError` whose reason says which way the mount
   set moved: `session_mounted` or `session_unmounted`). A *standing* root watch overlaps
   every mount under the root, so every `connect` mount stalls ~5s and then kills the
   reader. (This is why `verse connect` once took ~5s and
   silently broke the engine; see `verse-root-watches-eliminated`.)
2. A root watch depends on every controller under it. docd composes an ancestor
   watch by folding in each overlapping mount's `Watch` stream. If any one controller
   declines `Watch`, the composed ancestor watch has no stream for that subtree and
   cannot establish. A root watch is thus hostage to every controller in the tree.

Hence: no root watches. An engine (or any consumer) watches exactly the
`(system[,kind])` subtree it matches — `LogdStore.WatchSubtree` / `Engine.ObserveScoped`
— never the root. Two things then keep a watch clear of a mount, and the belt matters as
much as the braces: mounts render under `MountRoot`, disjoint from the entity root, so no
watch verse takes *can* contain one; and a watch that somehow overlapped anyway survives by
re-establishing and resyncing (below) rather than by dying. An unfiltered `verse watch` (no
`--system`/`--kind`) may take a transient root watch while a human is actively watching,
and it retries on a force-end — but nothing holds one at rest.

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
`session_mounted`/`session_unmounted` force-end, re-establish the watch (it re-composes against the current
mount set) and let the fresh `State` resync close the gap. See [change events](change-events.md)
and `LogdStore.WatchSubtree`.

## Serving something that changes on its own

A controller backed by something that moves — a filesystem, an API — keeps `Match` current by
answering from the live thing rather than from a cache, and emits on `Watch` what it has learned
changed. The discipline that governs a low-latency change notification is the one verse runs
everywhere, `hint : level-read :: Observe : Since`: a notification is a hint that may be missed,
duplicated or reordered, so it never carries authoritative data — it prompts a read of the
truth. The content controller sidesteps all of it by holding nothing: every `Match` stats and
reads the file, so there is no staleness to reconcile.

If what you are serving is a system whose changes something must *react* to, stop — that is a
[Source](sources.md), it is not a controller, and the first section says why.

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

- [ ] Nothing needs to trigger on this slice — a mount fires no events, so facts something
      reacts to are log-backed writes instead.
- [ ] Mount at `entity.MountKPath(system, kind)`, and add the slice to `entity.mountedKinds`;
      own it as sole writer.
- [ ] `Watch` implemented (no-op + block if read-only), emitting root-rooted patches.
- [ ] No non-UTF-8 / unbounded bytes on the wire — serve markers.
- [ ] No root/ancestor watch anywhere in the consuming path; watch the matched subtree.
- [ ] Consumers apply deltas (`tony.Patch`), don't decode them; resync on a
      `session_mounted`/`session_unmounted` force-end.
- [ ] Readiness probed before firing consumers.
