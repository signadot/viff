# Writing a docd controller for verse

A [controller](glossary.md#controller) is a process that serves one subtree of the
[store](glossary.md#store). This page says how to write one, and which invariants it must
keep. It is for somebody who changes verse itself.

A controller is a
[`libctl.Handler`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/system/libctl)
that is mounted at a
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath), which is a
[kinded path](glossary.md#kinded-path). docd routes every client operation for that subtree
(`Match`/`Patch`/`Watch`) to the controller. Where several mounts match, docd chooses the
mount with the longest prefix.

The store client of verse connects to the client address of docd. What that client sees of a
subtree that a controller serves depends on the operation.

| Operation | What the client sees |
|---|---|
| a read | No difference. `demo.*` and `verse.task.*` resolve to logd, and `local.dir-content-*` resolves to a controller. The store rewrites nothing. |
| a watch | Nothing. The subtree is invisible to a watch. This is the first thing to know. |

`verse connect` mounts a controller over your working tree; see
[reference/connect.md](reference/connect.md). The content controller (`io/content.go`)
serves the bytes of a file on demand, under `local.dir-content-<scope>.*`.

Read the invariants before you write a `Watch`. Start with the invariant that decides
whether to write a controller at all.

## Should this be a controller at all?

**A mounted [address](glossary.md#address) fires no [rules](glossary.md#rule).**

Mounts live under `entity.MountRoot`. `entity.MountRoot` is a top-level segment, and it is
disjoint from the *entity root*, which is the segment under which the log keeps the entities.

| Function | Where it renders a path |
|---|---|
| `refPath` | under `MountRoot`, for a path through a mounted address |
| `LogdStore.WatchSubtree` | under the entity root only |

So a client reaches a subtree that a controller serves by its ordinary address, and the
subtree is **invisible to every watch**. It gives no change events and no `Since` replay, and
a [charter](glossary.md#charter) can react to nothing in it.

Mounts are apart from the entity root because a mount has no history. A process serves a
mount, and the log does not. A watch that carries history must never span a mount, or it must
tolerate mounts that come and go (`entity/logdstore.go`).

This principle has no exceptions. **Bytes that somebody asks for may be a mount. Facts that
something reacts to must be ordinary writes.**

`verse connect` shows both sides of the principle. It
[federates](glossary.md#federation) one working tree as two subtrees.

| Subtree | What it holds | How it is served |
|---|---|---|
| the content | the bytes of each file | The content controller serves them on demand. |
| `dir-status-<scope>` | the metadata of each file | `connect` writes it to the log, because the validator rule for each file matches it. |

`io.TestFederationKindsAreMountOwned` pins the split, so neither subtree moves into the
other.

An [io](io.md) is not a controller, for the same reason. An io exists so that rules fire when
an external system changes. So the entities that an io reflects are writes.

[Mounts are under `MountRoot`](#mounts-are-under-mountroot) says why a mount cannot stand
under the entity root.

## The Handler contract

```go
type Handler interface {
    Match(ctx context.Context, path string, pattern *ir.Node, opts MatchParams) (*ir.Node, error)
    Patch(ctx context.Context, path string, data *ir.Node, opts PatchParams) (*api.PatchResult, error)
    Watch(ctx context.Context, path string, opts WatchParams, emit func(*api.WatchEvent) error) error
}
```

**`Match`** returns the data at `path`.

- `pattern` is optional. It is the selection of fields, or the filter, that the client gives.
- `opts.Scope` is the copy-on-write [scope](glossary.md#scope) that the read belongs to. A
  scope-aware controller returns its scoped view. A scope-agnostic controller may ignore
  `opts.Scope`.

**`Patch`** applies a write and reports where the write landed.

| Field | What it holds |
|---|---|
| `PatchResult.Commit` | the commit that the write committed at |
| `Data` | the resulting data, with any generated ids |
| `opts.TxID` | The client coordinates a transaction over several mounts. The controller joins the transaction by writing to logd with that tx id. |
| `opts.Match` | a CAS precondition |

| Controller | What `Patch` returns |
|---|---|
| *logd-backed* | the result of its `LogdSession.PatchWith`, unchanged. So the client sees the same commit that a direct write to logd gives. |
| *self-backed* | `Commit` is zero. The controller has no logd commit to report, and it does not invent a commit from its own timeline. For the same reason it cannot serve `opts.Commit` on a read. |
| read-only | `libctl.ErrUnsupported` |

**`Watch`** streams events until `ctx` is cancelled.

- The `State` of the first event is the full state at `path`.
- The `Patch` of every later event is root-rooted. A root-rooted patch expresses the delta
  from the root of the document. So a change to `a.b.x` is `{a:{b:{x:…}}}`, and it is not
  relative to `path`.

docd composes a watch on an ancestor by forwarding these absolute deltas unchanged. So you
must emit root-rooted patches.

Mount the controller with `RunController`. `RunController` blocks for as long as the
controller lives:

```go
libctl.RunController(ctx, &libctl.ControllerConfig{
    DocdAddr:   mountAddr,                 // docd's mount (controller-facing) face
    Controller: "content-" + scope,        // identifies this controller to docd
    Path:       entity.MountKPath("local", "dir-content-"+scope), // <MountRoot>.local.dir-content-<scope>
    Handler:    io.NewContent(root, mount),
})
```

`entity.MountKPath` renders the kpath, so that the store and the controller agree on where
the subtree lives: under `MountRoot`, and not under the entity root. The addresses do not
change. A path through the mount is the same path that it is anywhere else, and only its
rendering differs.

Add the address to the `mounts` of `entity`: its literal segments, then a prefix of the
mounted segment. With this, `refPath` routes reads to the mount. Every read misses a mount
that the store does not know about, and `MountKPath` panics on a path that reaches no mount.

Cancel `ctx` to unmount. docd then tombstones the mount cleanly.

**Readiness.** `RunController` establishes the mount synchronously, but it gives no signal of
readiness, and a mount takes time. Do not write metadata that fires consumers, such as the
validators of a rule, before the mount serves. Probe first. `waitContentReady` in
`cmd/verse/connect.go` does this: it reads a known key from baseline until the read returns.
Without a probe, the write races a mount that does not serve yet, and the consumers see "not
found".

## No root watches

A watch on the whole store, or on any ancestor of an address where controllers mount, must
tolerate controller processes that start and stop. Verse has no such watch, so nothing in
verse must tolerate them.

## Invariants for the Handler itself

1. Implement `Watch` even in a read-only controller, as a no-op. Never return
   `ErrUnsupported` from it. A `Watch` that declines breaks any composed watch on an
   ancestor. The `Watch` of a read-only controller blocks until `ctx` is cancelled, and
   emits nothing:

   ```go
   func (c *Content) Watch(ctx context.Context, _ string, _ libctl.WatchParams,
       _ func(*logdapi.WatchEvent) error) error {
       <-ctx.Done() // "watched", contributes no events; do NOT return ErrUnsupported
       return ctx.Err()
   }
   ```
   `Patch` may still decline with `ErrUnsupported`. A Patch that declines does not break the
   composed watch of an ancestor, as a Watch that declines does.

2. Put only valid UTF-8, of bounded size, on the wire. A node that carries non-UTF-8 bytes
   fails the tokenizer of docd (`read error: bad utf8`). The failure resets the session of
   the controller and tombstones the whole mount. After that, every `read_file` in the
   session fails with "not found".

   Never put raw bytes on the wire. Guard with `utf8.Valid` and serve a marker. `io/content.go`
   returns `{path, size, binary:"non-utf8"}` for such a file, and `{truncated:"size-cap"}`
   for a file over the cap. Bound the size too; a read of a subtree must not stream an
   unbounded blob into one node.

3. Own the subtree, as its single writer. A controller owns everything under the address
   that it mounts at, and it is the only writer there. So reflected data needs no CAS.

## Consuming a controller's stream: apply, do not decode

A watcher of a subtree must apply each event, and read the entities from the object that it
reconstructs. It must never interpret the structure of the patch.

A scoped watch emits *computed* diffs. The first delta, from a null baseline, is a
`!replace`. A merge-walk cannot decode a `!replace`, and it drops that entity with no error.

A watcher does four things:

1. It maintains the subtree as a document.
2. It applies each event: [`tony.Patch`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony#Patch)
   for a Patch, and a replace for a State.
3. It compares the reconstructed set of entities with the last snapshot, to find each `Set`
   and `Delete`.
4. On a `session_mounted` or `session_unmounted` force-end, it establishes the watch again.
   The watch then composes against the current set of mounts, and the fresh `State` closes
   the gap.

See [change events](change-events.md) and `LogdStore.WatchSubtree`.

## Serving something that changes on its own

Some controllers serve something that changes by itself, such as a filesystem or an API.
Such a controller keeps `Match` current: it answers from the live thing, and not from a
cache. On `Watch` it emits what it has learned of the changes.

A notification of change with low latency follows the discipline that verse follows
everywhere, `hint : level-read :: Observe : Since`. A notification is a hint. It may be
missed, duplicated or reordered, so it never carries authoritative data. It asks the reader
to read the truth.

The content controller avoids all of this, because it holds nothing. Every `Match` stats and
reads the file, so nothing can be stale.

If you serve a system whose changes something must *react* to, stop. That system needs an
[io](io.md), and not a controller. The first section says why.

## Scope-awareness

`opts.Scope` on `Match`, `Patch` and `Watch` is the copy-on-write scope of the operation.

| Controller | What it does with the scope |
|---|---|
| scope-aware | It returns and writes the scoped view. |
| logd-backed | It carries the scope on its connection to logd. |
| scope-agnostic | It ignores the scope. This is correct when the isolation comes from elsewhere. |

The content controller ignores `opts.Scope`. Each `connect` session mounts a distinct segment
that holds a nonce (`dir-content-<scope>`), so the mount itself belongs to one session. The
nonce gives the isolation, and a copy-on-write scope does not.

## Lifecycle

`RunController` blocks until `ctx` is cancelled or the connection ends. Run it under a
context that you cancel at shutdown, and wait for its goroutine to end. docd then tombstones
the mount cleanly, and the mount is not orphaned.

`verse connect` runs the content controller for as long as the session lives. At exit it
drops the controller and the scope of the session.

## Checklist

- [ ] No rule needs to fire on this address. A mount fires no events, so facts that something
      reacts to are writes to the log.
- [ ] Mount at `entity.MountKPath(<path>)`, and add the address to the `mounts` of `entity`.
      Own the subtree as its only writer.
- [ ] `Watch` is implemented, and it emits root-rooted patches. In a read-only controller it
      is a no-op that blocks.
- [ ] No non-UTF-8 bytes and no unbounded bytes are on the wire. Serve markers.
- [ ] No watch on the root or on an ancestor is anywhere in the consuming path. Watch the
      matched subtree.
- [ ] Consumers apply deltas (`tony.Patch`), and do not decode them. They resync on a
      `session_mounted` or `session_unmounted` force-end.
- [ ] Readiness is probed before anything fires consumers.

## Why it is this way

### Mounts are under `MountRoot`

A mount under the entity root looks like a way to make a mount visible to watches. It is not
one. It puts the mount where watches can see it, and it creates again the two faults that
`MountRoot` prevents:

- A watch on the root becomes a composed watch. A composed watch drops `FromCommit` with no
  error, and returns an empty replay that looks like a success.
- The mount stalls for about 5s on the watch that overlaps it, and then force-ends the
  watch.
