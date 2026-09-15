# verse: instructions for every agent working in this repository

## Addresses are paths. This is not negotiable.

verse uses paths, not a fixed structure near the root. Deal with it and do not deviate from it.

- Everything is referenced by a path — a kinded path (go-tony `ir/kpath` is the grammar; the
  command line's steps are only a spelling of one, and the empty path is the root).
- There is no `system:kind:id`, no `{system, kind, id}` shorthand, no `$subject.system/.kind/.id`,
  no `Ref.System/Kind/ID`, no entity depth, no "three segments", no half with its own depth.
  None of these may be added back — not as a helper, not as a compatibility shim, not as sugar
  compiled to a path, not in a test fixture, not in a doc.
- A fixed structure is acceptable only where the SYSTEM decides the structure: where permissions
  are seeded, where the perms mirror is placed, where verse keeps its own task records and
  registries. That structure is named by the code that owns it, for that place only; it is never
  a default anything else inherits.
- A short path names the node at it. A glob is written `!glob`. There is no backward
  compatibility to preserve for the old grammar: nobody depends on it, so a spelling that only
  existed for it is deleted, not kept.
- Sources and anything else that reflects or covers a slice of the tree address it by path too.

This has been decided in at least five separate sessions and drifted back each time. If a design
seems to need a fixed position or depth, stop and ask the owner; do not add one. The model is
`docs/working/paths-and-nodes.md`; `scripts/check-paths.sh` in `make check` is a ratchet on the
old vocabulary — make it pass by removing uses, never by raising its baseline.
