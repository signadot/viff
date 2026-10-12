# entity: the package doc still describes an entity as a <system, kind, id> triple

The package comment at the top of `entity/entity.go` says an entity is named by a `<system, kind, id>` triple and that the store is "an index, not a warehouse" holding small metadata payloads.

The rest of the package no longer says that: a Ref is an address at any depth (`RefAt`, `ParseAddr`), the store is one tony document read at a kinded path (`doc.go`), and a payload is any tony value. A newcomer reading `go doc entity` meets the old model first.

Rewrite the package doc to the current model: the verse as one document, addresses as kinded paths at any depth, the one write and the cursor-based change stream, and the attributed view. Comment-only change.