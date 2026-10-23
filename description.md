# io: a reflector pass whose read of the stored slice fails rewrites the whole slice

`Reflector.current` (io/reflector.go) reads what the store holds for a reflection's covers with `rf.store.ListAt`, which drops the read's error (`LogdStore.ListAt` is `ListAtErr` with the error discarded). A listing that fails therefore reads as an empty slice: every entity in the reflection looks absent, and `converge` writes every one of them.

Seen on staging 2026-10-23: from 07:46 to 07:53 UTC the git-issue iod's scheduled sweep wrote each of tony-format's issues once (248 slow commits in docd's log, each 0.3-1.3s in apply, mid-compaction), while every issue ref on tony-format's remote matched the last sweep's. versed logged `context deadline exceeded` on every read 07:46:15-07:46:20. At ~1s a write that is ~7 minutes of serial writes into a store already slow, and every trigger over the slice sees each issue as changed.

Tombstones are not at risk (the empty map covers nothing), only the rewrite.

Fix: read with `ListAtErr`, and a failed read ends the pass with its error before any write. The next pass reads again; no retry inside the pass.