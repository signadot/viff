# Writing a docd controller for verse

A docd controller implements a subtree of the entity verse as a process: a
[`libctl.Handler`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/system/libctl)
mounted at a
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath), to which docd
routes every client operation (`Match`/`Patch`/`Watch`) for that subtree by longest-prefix
mount. To verse's store client, which points at docd's client address, a controller-served
subtree is indistinguishable *to a read*: `demo:*` and `task:*` resolve to logd,
`local:dir-content-*` resolves to a controller, with no store rewrite. To a *watch* it is not
indistinguishable at all, and that is the first thing to know.

`verse connect` mounts one over your working tree; see
[reference/connect.md](reference/connect.md). The content controller (`source/content.go`)
serves file bytes on demand under `local:dir-content-<scope>:*`.

This note is the how-to, and the invariants a controller must honor. Read them before you write
a `Watch`, starting with the one that decides whether to write a controller at all.

## Should this be a controller at all?

**A mounted slice fires no triggers.** Mounts live under `entity.MountRoot`, a top-level segment
disjoint from the entity root, because a mount is task-driven and so has no history, and a
history-bearing watch must either never span one or be resilient to mounts coming and going
(`entity/logdstore.go`). `refPath` renders a mount-owned slice under `MountRoot`;
`LogdStore.WatchSubtree` renders only under the entity root. So a controller-served subtree is
reachable by its ordinary ref and **invisible to every watch there is**: no change events, no
`Since` replay, and nothing a charter can react to.

The rule has no exceptions. **Bytes somebody asks for may be a mount; facts something reacts to
must be ordinary writes.** `verse connect` is the worked example on both sides of it, federating
one working tree as two slices: file bytes served on demand by the content controller, and
`dir-status-<scope>` metadata written log-backed precisely because it is what the per-file
validator trigger matches. `source.TestFederationKindsAreMountOwned` pins the split so neither
half drifts into the other.

The escape that suggests itself does not exist. Mounting under the entity root would put the
slice back where watches can see it, and re-create what `MountRoot` prevents: a composed root
watch that silently drops `FromCommit` and returns a successful-looking empty replay, plus a
mount that stalls ~5s on the overlapping watch and then force-ends it.

A [Source](sources.md) is not a controller, for the same reason. A Source exists so that triggers
fire on external change, so its reflected entities are writes.

## The Handler contract

```go
type Handler interface {
    Match(ctx context.Context, path string, pattern *ir.Node, opts MatchParams) (*ir.Node, error)
    Patch(ctx context.Context, path string, data *ir.Node, opts PatchParams) (*api.PatchResult, error)
    Watch(ctx context.Context, path string, opts WatchParams, emit func(*api.WatchEvent) error) error
}
```

- `Match` returns the data at `path`. `pattern` is the client's optional field selection or
  filter. `opts.Scope` is the copy-on-write scope the read belongs to: a scope-aware controller
  returns its scoped view, a scope-agnostic one may ignore it.
- `Patch` applies a write and reports where it landed. `PatchResult.Commit` is the commit it
  committed at, `Data` the resulting data, with any generated ids. A logd-backed controller
  returns its `LogdSession.PatchWith` result unchanged, so the client sees the same commit a
  direct logd write would give it. A *self-backed* controller has no logd commit to report and
  leaves `Commit` zero rather than invent one from its own timeline, which is also why it cannot
  serve `opts.Commit` on a read. `opts.TxID` means the client is coordinating a multi-mount
  transaction the controller joins by writing to logd with that tx id. `opts.Match` carries a CAS
  precondition. A read-only controller returns `libctl.ErrUnsupported`.
- `Watch` streams events until `ctx` is cancelled. The first event's `State` is the full state at
  `path`. Every subsequent event's `Patch` is root-rooted: the delta expressed from the document
  root, so a change to `a.b.x` is `{a:{b:{x:…}}}`, not relative to `path`. docd composes an
  ancestor watch by forwarding these absolute deltas unchanged, so you must emit root-rooted
  patches.

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
where the subtree lives: under `MountRoot`, not under the entity root. The entity namespace is
unchanged by that. The slice is the same two segments it always was — a mount owns a SLICE and
not an entity, so there is no id in it — and only the path those segments render to differs. Add
the `(system, kind-prefix)` pair to `entity.mountedKinds` so
`refPath` routes reads there; a mount the store does not know about is a mount every read misses.
Cancel `ctx` to unmount, and docd tombstones the mount cleanly.

Readiness. `RunController` establishes the mount synchronously but exposes no readiness signal,
and a mount is not instantaneous. Do not write metadata that fires consumers, such as a trigger's
validators, until the mount actually serves. Probe first, as `cmd/verse/connect.go`'s
`waitContentReady` does with a baseline read of a known key until it returns, or you race a
not-yet-serving mount and consumers see "not found".

## No root watches

A watch on the whole verse, or on any ancestor of where controllers mount, has to be resilient to
mount processes coming and going. Verse simply has no such watches, and that simplifies things.

## Invariants for the Handler itself

1. Implement `Watch` even when read-only, as a no-op, never `ErrUnsupported`. A declining `Watch`
   breaks any composed ancestor watch. A read-only controller's `Watch` blocks until `ctx` is
   cancelled and emits nothing:

   ```go
   func (c *Content) Watch(ctx context.Context, _ string, _ libctl.WatchParams,
       _ func(*logdapi.WatchEvent) error) error {
       <-ctx.Done() // "watched", contributes no events; do NOT return ErrUnsupported
       return ctx.Err()
   }
   ```
   `Patch` may still decline with `ErrUnsupported`. A Patch decline does not poison an ancestor's
   composition the way a Watch decline does.

2. Only valid UTF-8, bounded content on the wire. A node carrying non-UTF-8 bytes fails docd's
   tokenizer (`read error: bad utf8`), which resets the controller's session and tombstones the
   whole mount, after which every `read_file` in the session fails "not found". Never put raw
   bytes on the wire: guard with `utf8.Valid` and serve a marker instead, as `source/content.go`
   returns `{path, size, binary:"non-utf8"}`, and `{truncated:"size-cap"}` for an over-cap file.
   Bound size too; a subtree read should not stream an unbounded blob into one node.

3. Own the subtree, as its single writer. A controller owns `system:*`, or a `system.kind.*`
   slice, and is the only writer there, so reflected data needs no CAS. Mount per-system or
   per-`(system,kind)`.

## Consuming a controller's stream: apply, do not decode

A subtree watcher must apply each event and read entities from the reconstructed object, never
interpret the patch's structure. A scoped watch emits *computed* diffs whose first delta, from a
null baseline, is a `!replace` that a merge-walk cannot decode, and it silently drops that entity.
Maintain the subtree as a document, apply each event
([`tony.Patch`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony#Patch) for a Patch,
replace for a State), and diff the reconstructed entity set against the last snapshot for `Set`
and `Delete`. On a `session_mounted` or `session_unmounted` force-end, re-establish the watch, so
it re-composes against the current mount set, and let the fresh `State` resync close the gap. See
[change events](change-events.md) and `LogdStore.WatchSubtree`.

## Serving something that changes on its own

A controller backed by something that moves, a filesystem or an API, keeps `Match` current by
answering from the live thing rather than from a cache, and emits on `Watch` what it has learned
changed. The discipline that governs a low-latency change notification is the one verse runs
everywhere, `hint : level-read :: Observe : Since`. A notification is a hint that may be missed,
duplicated or reordered, so it never carries authoritative data. It prompts a read of the truth.
The content controller sidesteps all of it by holding nothing: every `Match` stats and reads the
file, so there is no staleness to reconcile.

If what you are serving is a system whose changes something must *react* to, stop. That is a
[Source](sources.md), not a controller, and the first section says why.

## Scope-awareness

`opts.Scope` on `Match`, `Patch` and `Watch` is the copy-on-write scope of the operation. A
scope-aware controller returns and writes the scoped view; a logd-backed one carries the scope on
its logd connection. A controller may also be scope-agnostic when isolation comes from elsewhere.
The content controller ignores `opts.Scope` because each `connect` session mounts a distinct
nonce'd kind (`dir-content-<scope>`), so the mount itself is per-session: the nonce, not a
copy-on-write scope, provides the isolation.

## Lifecycle

`RunController` blocks until `ctx` is cancelled or the connection ends. Run it under a context you
cancel on shutdown, and reap the goroutine, so the mount is tombstoned cleanly rather than
orphaned. `verse connect` runs the content controller for the session's lifetime and drops it, and
its scope, on exit.

## Checklist

- [ ] Nothing needs to trigger on this slice — a mount fires no events, so facts something reacts
      to are log-backed writes instead.
- [ ] Mount at `entity.MountKPath(system, kind)`, and add the slice to `entity.mountedKinds`; own
      it as sole writer.
- [ ] `Watch` implemented (no-op + block if read-only), emitting root-rooted patches.
- [ ] No non-UTF-8 or unbounded bytes on the wire — serve markers.
- [ ] No root or ancestor watch anywhere in the consuming path; watch the matched subtree.
- [ ] Consumers apply deltas (`tony.Patch`) rather than decoding them; resync on a
      `session_mounted` or `session_unmounted` force-end.
- [ ] Readiness probed before firing consumers.
