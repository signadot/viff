# `verse connect` and docd

How `connect` federates your working tree into the verse, and how it leans on docd's two
faces to do it. This is the mechanics-and-docd view; for the general controller model see
[writing a controller](controllers.md), and to run it first see
[tutorial §8](tutorial.md#8-federate-your-local-working-tree).

## What connect is

`verse connect [path]` projects your local working tree into the entity store under a
private, per-session scope, and installs a trigger so the engine spawns a validator per
file — the local-dev loop. It is its own long-running process (a second terminal), not a
subcommand of `up`: it is a *client* of the verse and of docd, and it stays up to keep
serving your files. On exit it drops what it created.

The key move: a file is split along the *index / warehouse* line, and the two halves are
federated through different docd faces.

## docd in one paragraph

docd is the store daemon behind a durable verse (over logd). It exposes two network faces:

- a client face — where store clients read and write entities, including scoped
  (copy-on-write) writes; this is what `entity.OpenLogdStore`/`OpenScopedStore` connect to.
- a mount (controller) face — where a controller mounts a subtree of the document
  tree to *own* it, serving reads/writes for that subtree on demand (docd composes the
  mounted controller's response into the read). See [writing a controller](controllers.md).

`connect` uses both: the client face for metadata, the mount face for content.

## The two halves

```
                       ┌──────────────────────── docd ────────────────────────┐
   verse connect  ──1─▶│ client face:  local:dir-status-<scope>:<path>  (logd) │  the INDEX
   (client, scope S)   │               copy-on-write scope S                    │
                  ──2─▶│ mount  face:  local:dir-content-<scope>  ◀─ controller │  the CONTENT
                       └───────────────────────────────────────────┬───────────┘
                                                                    │ (served on demand,
   validator.read_file(local:dir-content-<scope>:<path>) ──read──▶  │  read live from the
                                                                    ▼  tree — never stored)
                                                            connect's content controller
```

1. Status (the index) → scoped writes on the client face. For each file `connect` writes
its rich metadata (full fstat + a content hash, *not the bytes*) to docd as
`local:dir-status-<scope>:<path>` entities, in copy-on-write scope `<scope>`. These land
in logd via docd's client face; the scope isolates one dev's tree from baseline and from
other devs. This is what triggers watch — a change to a `dir-status` entity is the event
that fires a validator.

2. Content (the bytes) → a controller on the mount face. `connect` runs a read-only
content controller and mounts it at `local:dir-content-<scope>` on docd's mount face. A
read of `local:dir-content-<scope>:<path>` is routed by docd to that controller, which reads
the file live from the tree and returns it — nothing is written to the store. So a
validator's `read_file` resolves *through the verse* (fetch the `dir-content` entity → docd →
the controller), and therefore works wherever the process runs, not only on the machine
holding the tree.

The two kinds are deliberately symmetric (`dir-status-<scope>` / `dir-content-<scope>`) and
sit at different paths, so the content mount never shadows the metadata reads.

## Why metadata is scoped but content is mounted

A docd mount is not scope-specific — a mount sits too far from the copy-on-write
mechanism to be made per-scope. So the index (which *must* be isolated per dev, and which
triggers react to) goes through scoped writes, while the content controller is a baseline
mount whose per-session isolation comes from the nonce in `<scope>` rather than from COW.
Two concurrent `connect`s therefore write to two isolated scopes but mount at two distinct,
collision-free `dir-content-<nonce>` paths. (This is the crux [content federation](content-federation.md)
explains in full.)

## Finding docd's faces

`connect` is a separate process, so it can't assume the ephemeral ports `up` chose. A
docd-backed verse publishes both docd addresses on `GET /status` (`DocdClient`,
`DocdMount`); `connect` reads them (`docdAddrs()`) — the client address for the scoped
metadata store, the mount address for the content controller. This is why the two faces are
surfaced on the operator API at all: so a controller process can find docd's mount face
without guessing.

`connect` also waits for the content controller to be mounted and serving before
installing the trigger (`waitContentReady`), so a validator can't fire and race an unmounted
controller into a spurious "file not found."

## Lifecycle: connect owns the scope

`connect` is the owner of its federation. A generated (nonce) scope is ephemeral to the
session: on ctrl-c `connect` drops it (`dropScope` → docd `DeleteScope`), which stops the
scoped engine and removes the whole overlay in one shot; the content mount goes away when
the `connect` process exits. A user-named scope (`--scope me`) is left in place for
reuse; `verse disconnect --scope me` tears it down explicitly.

The scoped engine observes `dir-status-<scope>` through per-subtree watches (each trigger
watches exactly the `system.kind` it matches — there is no root watch), so a dev's reactive
loop runs isolated in its own scope, off the shared baseline.

## What is (and isn't) federated

Today only `read_file` resolves through the content controller. `list_dir` /
`write_file` / `run_bash` still hit the local filesystem, so a fully-remote validator that
needs those wants them federated too — the write path is the subject of [write-back](write-back.md).
`--root` remains only as the non-federated, single-machine default (an agent reading its own
working directory); a federated process never touches the server's disk for a file.

## See also

- [content federation](content-federation.md) — why `connect` replaced `--root` (the wrong side, the wrong reach).
- [writing a controller](controllers.md) — how to write a docd controller (the general form of the content controller).
- [write-back](write-back.md) — federating the *write* path (`write_file`) back through the verse.
- [the overview](overview.md) — where connect sits in the whole system.
