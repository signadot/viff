# Federating working-tree content (why `connect`, not `--root`)

Status: built. `verse connect .` federates a tree under a per-session scope —
writing metadata to docd and serving content through a docd controller — and a
scoped validator reads file contents *through the verse*, not off the server's disk.
This note records why `--root` cannot federate a tree, and how `connect` does.

## Why `--root` cannot federate

`connect` projects your working tree into the verse as entities carrying
`path`/`size`/`mtime`/hash — an index, not the contents (the entity-verse
doctrine: "an index, not a warehouse"). A process that must *act* on a file follows
the reference into the real system — it reads the file.

A read straight through the process executor's `read_file` is confined to a
server-side filesystem root (`fsExec.root`, set by `verse up --root .`), so
the two halves of a file come from two different places:

- metadata — from the *client's* tree, via `connect`;
- contents — from wherever the *server* happens to be rooted.

They agree only when server and tree are the same filesystem — a coincidence,
not a design. The moment the runtime isn't on your laptop, `--root` can't see your
files, and the loop that otherwise just works (`connect .` → an agent validates a file)
silently stops. `--root` is wrong on two counts for this job: wrong side (the *client* knows
which tree is federated, and this configures the *server*) and wrong reach (it assumes
the runtime shares a disk with the tree, defeating remote federation).

## How `connect` federates instead

`connect` is its own process, operating in a nonce scope `<scope>`. It splits the
file into its two halves along the index/warehouse line, and — because docd mounts
are not scope-specific (a mount is too far from the copy-on-write mechanism to make
per-scope) — each half is federated differently:

- Status (the index) → scoped writes. `connect` writes each file's rich metadata
  (full fstat + a content hash) to docd as `local:dir-status-<scope>:<path>` entities
  in copy-on-write scope `<scope>`. These live in logd (via docd); the scope isolates
  one dev's tree from baseline and from other devs. This is what triggers watch.
- Content (the bytes) → a controller. `connect` runs a read-only content
  controller mounted at `local:dir-content-<scope>` that serves a file's bytes on
  demand — read live from the tree, nothing written to the store. The nonce keeps
  this baseline mount collision-free across sessions. Large/binary files are served,
  not warehoused; the controller is the single owner of that content (no second copy
  to drift).

The two kinds are deliberately symmetric — `dir-status-<scope>` and
`dir-content-<scope>` — and sit at *different* paths so the content mount never
shadows the metadata reads.

### The process side

A scoped validator's `read_file` resolves through the verse: it fetches
`local:dir-content-<scope>:<path>`, which docd routes to the connect content
controller, rather than `fsExec` hitting a path under `--root`. "Follow the reference
into the real system" stays the rule; the real system is reachable via the verse,
wherever the process runs. `--root` serves only the non-federated,
single-machine case (an agent reading its own working directory); a federated
process never touches the server's disk for a file.

```
  --root (single machine):
    connect (client) ──put metadata──▶ verse (logd)
    fsExec.read_file ──local FS──▶ server:--root/<path>            ← must be same machine

  federated (scoped writes + content controller):
    connect ──scoped metadata──▶ docd:  local:dir-status-<scope>:*   (index, in logd)
    connect = content controller ──mount──▶ docd: local:dir-content-<scope>:*
    validator.read_file ──through the verse──▶ content controller serves it
                                               (on demand, owner-served)   ← any machine
```

`disconnect` is the owner leaving: dropping the scope stops the scoped engine and
removes the overlay; the content mount goes away when the `connect` process exits.

## What remains

- Only `read_file` is federated. `list_dir`/`write_file`/`run_bash` still hit the
  local filesystem, so a fully-remote validator that needs those wants them federated
  too (write-back and a listing path over the content controller).
- Read fidelity. Independent of *where* contents come from, tool results fed to
  the driver share the read-plane's preview cap, so an agent can validate a large file
  from a truncated view. The preview is for observers; the result is for the agent —
  they should not share a cap. A different seam from federation, worth fixing
  alongside. (The content controller also caps a single served file; see
  `maxContentBytes`.)
