# Federated `write_file`: writing through the verse (write-back)

Status: design note. `read_file` is federated — an agent follows a `local:file` reference
*through the verse* (the connect content controller serves bytes on demand), so a
validator needn't share a disk with the tree. The write tools (`write_file`, and later
`run_bash`) still hit the server's local filesystem. This note is about closing that
gap for **`write_file`**, and why it is not simply "read, but backwards."

## Read and write are not symmetric

`read_file` federates cleanly because a read is a **pure follow-the-reference**:
idempotent, side-effect-free, and honest with *index-not-warehouse* — the content is
served on demand from the one system that owns it (the dev's tree), never stored in the
verse. Nothing has to change anywhere.

A write **mutates an external system**. There is no "serve on demand" for a mutation —
something must actually apply it to the dev's tree. So the read model doesn't extend by
symmetry; a write is an **outbound effect**, the same category as "comment on a PR",
"push a tag", "transition an issue" — the outbound-executor path `sources.md` reserves.

## Constraints that shape the design

- **Read-your-writes.** After `write_file(p, c)`, a subsequent `read_file(p)` (which
  goes through the content controller) must return `c`. Whatever we do has to be visible
  to the very next read.
- **Content is already per-scope — via the ref, not COW.** A docd *mount* isn't
  copy-on-write-aware (one mount serves every COW scope alike), but content federation
  still makes each scope's content distinct by putting the scope (a nonce) *in the ref*:
  `local:dir-content-<scope>:*` is a **distinct mount per connect session**, each served
  by that dev's own controller over that dev's tree. So per-scope content isolation
  exists — it comes from the nonce-mount, not the COW `--scope` overlay (which carries
  the *metadata*). A write to `local:dir-content-<scope>:<path>` therefore lands,
  unambiguously, in that one scope's tree. That's an enabler, not a wall.
- **Index, not warehouse.** We don't want file *contents* living in logd. The dev's
  tree stays the single source of truth for bytes. (This, not scoping, is the real
  limit on any "staging overlay" — see option B.)
- **Conflict with the human.** The dev may be editing the same tree in their editor.
  An agent write and a human edit can race.
- **A write is a gated, attributable effect.** The harness already gates `write_file`
  (ASK by default) and derives it as a `Write` effect on a `File` resource. A federated
  write is an effect on *the dev's* tree — gating/attestation matters more, not less
  (ties into the permissions/attestation theme in `external-processes.md`).

## Options

**A. Write-through to the owning tree (via a read-write content controller).**
The agent's `write_file` is routed as a write to `local:dir-content-<scope>:<path>`;
docd delivers it to the **connect** content controller for that scope (which owns the
slice and already reads the tree); the controller **applies it to the dev's working
tree** (path-confined under the connect root). The poll re-federates the `dir-status`
metadata shortly after; the *content* is served live from the tree, so read-your-writes
works immediately (the controller re-reads the file), even before the metadata catches
up. Because the scope is in the ref, the write is already targeted at exactly one dev's
tree — no ambiguity about *which* tree.
- *Wins:* read-your-writes for free; index-not-warehouse holds (bytes never enter the
  verse); per-scope by construction; the agent literally edits that scope's working
  tree, which is the point of local-dev federation (a pair-programmer).
- *Loses:* mutates a live tree — races with a human editing the same scope's tree
  (conflict policy below).

**B. A staging overlay that leaves the dev's tree untouched.** Two very different
things get called this:
- *Staging as a separate tree* — the agent writes into a **different scope's**
  controller (a scratch worktree/branch `connect`ed as its own scope). This needs **no
  new machinery**: content is already per-scope by ref (above), so "somewhere else"
  is just another nonce-mount. It's option A pointed at a staging scope.
- *Staging as a transparent overlay on the same tree* — pending writes are invisible to
  the dev's real files but merged into reads. *That* is the dead end: it means
  warehousing the pending bytes (logd or a side store) and overlaying them in the
  controller's reads — warehousing content, against the doctrine. Avoid.

**C. Writes as proposed changes (diff entities).** A `write_file` records a *diff* as an
entity in the scope (`local:change-<scope>:<path>` or similar); the dev reviews and
applies it to their tree.
- *Wins:* review-friendly and SDLC-native (a change is a first-class thing); stores a
  diff, not content, so index-not-warehouse holds; no live mutation.
- *Loses:* breaks read-your-writes unless the content controller overlays pending diffs
  on reads — which is option B's blocked overlay again. So the agent can't iterate on
  its own edits without extra plumbing. Good as a *review surface*, weak as the agent's
  working substrate.

## Recommendation

**Start with A: a read-write content controller, write-through, gated.** Concretely:

1. The content controller (`source/content.go`) grows a **`Patch`** path for its slice:
   given `local:dir-content-<scope>:<path>` + new bytes, it `confine`s the path under
   the connect root (as reads already do) and writes the file. It stays read-only for
   everything else. This is "a Source can also be an *outbound* executor for the slice
   it owns."
2. The harness's `write_file`, when federated (the process has a content endpoint),
   routes the write **through the verse** to that slice instead of `os.WriteFile` on the
   server root — mirroring how `read_file` already routes reads. Same `ContentReader`
   seam, extended to a `ContentWriter` (or one `Content` interface with read+write).
3. The write stays a **gated effect** (ASK in the demo vector); for a remote/human
   worker it becomes an **attested** effect, recorded on the process journal — no new
   policy model, just the existing gate + the attestation seam.
4. **Isolation is first-class, not new machinery:** because content is per-scope by
   ref, "don't touch my live tree" = point the agent's writes at a *different* scope's
   controller — `connect` a scratch worktree/branch as its own scope and target that.
   Same write-through path, a different nonce-mount tree. (This is option B's
   separate-tree form; no copy-on-write-content overlay needed.)

Layer **C on top later** as the *review* surface: emit the write as a diff entity too
(for `gate`/PR-style approval) while A provides the agent's read-your-writes working
substrate. Don't build the *transparent-overlay* form of staging — it needs warehoused
content, which the design has deliberately closed off.

## Open questions

- **Conflict policy** when the human and the agent both write: last-write-wins + the
  dev's editor shows it? gate every write? a lock/lease on a path? Probably: gated by
  default (the human approves), last-write-wins when auto.
- **Atomicity / partial writes** across the wire (write temp + rename in the controller).
- **`list_dir`** is the trivial sibling: a read the content controller can already
  serve (a `Match` of the mount subtree is the listing) — worth doing alongside, it has
  none of write's hazards.
- **`run_bash`** is the hard one (remote execution where the files live) — out of scope
  here; it belongs with the remote-process work in `external-processes.md`.
