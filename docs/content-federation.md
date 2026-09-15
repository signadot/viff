# Federating working-tree content

`verse connect .` federates a working tree under a per-session scope. It writes metadata to
docd and serves content through a docd controller, so a scoped validator reads file contents
*through the verse* rather than off the server's disk. [expose](https://github.com/signadot/verse/blob/main/docs/roadmap/expose.md) generalizes
this to local filesystem trees, and possibly to processes.

## How `connect` federates

`connect` is its own process, operating in a nonce scope `<scope>`. It splits each file along
the index/warehouse line and federates the two halves differently, because docd mounts are not
scope-specific: a mount is too far from the copy-on-write mechanism to make per-scope.

- Status (the index) → scoped writes. `connect` writes each file's rich metadata (full fstat
  and a content hash) to docd as `local.dir-status-<scope>.<path>` entities in copy-on-write
  scope `<scope>`. These live in logd, via docd, and the scope isolates one dev's tree from
  baseline and from other devs. This is what triggers watch.
- Content (the bytes) → a controller. `connect` runs a read-only content controller mounted at
  `local.dir-content-<scope>` that serves a file's bytes on demand, read live from the tree,
  with nothing written to the store. The nonce keeps this baseline mount collision-free across
  sessions. Large and binary files are served rather than warehoused, and the controller is the
  single owner of that content, so there is no second copy to drift.

The two kinds are symmetric, `dir-status-<scope>` and `dir-content-<scope>`, and sit at
*different* paths so the content mount never shadows the metadata reads.

### The task side

A scoped validator's `read_file` resolves through the verse. It fetches
`local.dir-content-<scope>.<path>`, which docd routes to the connect content controller.
"Follow the reference into the real system" stays the rule. The real system is reachable via
the verse, wherever the task runs, and a federated task never touches the server's disk for a
file.

```
  federated (scoped writes + content controller):
    connect ──scoped metadata──▶ docd:  local.dir-status-<scope>.*   (index, in logd)
    connect = content controller ──mount──▶ docd: local.dir-content-<scope>.*
    validator.read_file ──through the verse──▶ content controller serves it
                                               (on demand, owner-served)   ← any machine
```

`disconnect` is the owner leaving. Dropping the scope stops the scoped engine and removes the
overlay, and the content mount goes away when the `connect` process exits.

## What remains

- Only `read_file` is federated. `list_dir`, `write_file` and `run_bash` still hit the local
  filesystem, so a fully-remote validator that needs those wants them federated too.
  [expose](https://github.com/signadot/verse/blob/main/docs/roadmap/expose.md) carries both halves: the write half, and the mapping of an agent's
  file tools onto a surface.
- Read fidelity. Tool results fed to the driver share the read plane's preview cap, wherever the
  contents come from, so an agent can validate a large file from a truncated view. The preview is
  for observers and the result is for the agent; they should not share a cap. This is a different
  seam from federation, worth fixing alongside. The content controller also caps a single served
  file.
