# Federating working-tree content

`verse connect .` [federates](glossary.md#federation) a working tree under a
[scope](glossary.md#scope) that belongs to one session. It writes the metadata of each file
to docd, and it serves the contents through a docd [controller](glossary.md#controller). So a
scoped validator reads the contents of a file *through the [store](glossary.md#store)*, and
not from the disk of the server.

This page is for somebody who changes verse itself.

## How `connect` federates

`connect` is a process of its own. It works in a scope `<scope>`, and the name of the scope
is a nonce. It divides each file into two halves, and it federates the two halves in
different ways.

| Half | What it is | How `connect` federates it |
|---|---|---|
| status | the index | scoped writes |
| content | the bytes | a controller |

The two ways differ because a docd mount is not specific to a scope. A mount is too far from
the copy-on-write mechanism to exist once for each scope.

**Status.** `connect` writes the rich metadata of each file: the full fstat, and a hash of
the content. It writes the metadata to docd as `local.dir-status-<scope>.<path>` entities, in
the copy-on-write scope `<scope>`. These entities live in logd, through docd. The scope
isolates the tree of one developer from baseline and from the other developers. These
entities are what [rules](glossary.md#rule) watch.

**Content.** `connect` runs a read-only content controller, mounted at
`local.dir-content-<scope>`. The controller serves the bytes of a file on demand. It reads
the bytes live from the tree, and it writes nothing to the store.

- The mount is in baseline. The nonce keeps it from colliding with the mount of another
  session.
- Large files and binary files are served, and not stored.
- The controller is the single owner of that content. So no second copy exists that can
  differ from the first.

The two names are symmetric: `dir-status-<scope>` and `dir-content-<scope>`. They stand at
*different* addresses, so the content mount never hides the reads of the metadata.

### The task side

The `read_file` of a scoped validator resolves through the store. It fetches
`local.dir-content-<scope>.<path>`, and docd routes the fetch to the content controller of
`connect`.

The principle "follow the reference into the real system" holds. The real system is
reachable through the store, wherever the task runs. A federated task never reads a file from
the disk of the server.

```
  federated (scoped writes + content controller):
    connect ──scoped metadata──▶ docd:  local.dir-status-<scope>.*   (index, in logd)
    connect = content controller ──mount──▶ docd: local.dir-content-<scope>.*
    validator.read_file ──through the verse──▶ content controller serves it
                                               (on demand, owner-served)   ← any machine
```

With `disconnect`, the owner leaves. Dropping the scope stops the scoped engine and removes
the overlay. The content mount ends when the `connect` process exits.

## What remains

- **Only `read_file` is federated.** `list_dir`, `write_file` and `run_bash` use the local
  filesystem. A validator that is fully remote and needs those tools needs them federated
  too.
- **Read fidelity.** The results of tools that the driver receives share the preview cap of
  the event stream, wherever the contents come from. So an agent can validate a large file
  from a truncated view. The preview is for observers, and the result is for the agent. The
  two must not share a cap. This seam is not the seam of federation, and it is worth
  correcting with it.
- The content controller also caps the size of one served file.
