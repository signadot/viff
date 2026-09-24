# scopes: nothing records a scope, so none can be listed and none is reclaimed unless disconnected

From `docs/roadmap/scope-lifecycle.md`, removed with the roadmap; the note whole is
`git show fb75880d:docs/roadmap/scope-lifecycle.md`.

## What is built

`disconnect -scope <name>` drops an overlay: sources stop, the engine stops, the scope's live
tasks are cancelled, logd's `DeleteScope` runs (`docs/reference/connect.md`).

## The gap

Creation is universal and implicit — any write carrying `--scope` makes an overlay — and nothing
records that it did. Scopes cannot be listed, so nobody can ask "what scopes are there", and
nothing reclaims one nobody disconnects. (A name only READ under leaks a session and an
attribution too: `f80cejye`, which needs the same "does this scope exist" answer.)

## The direction

Record a scope on its first scoped WRITE, in baseline, at a place the system names for its own
scope records — who made it, when, ephemeral (leased, reapable) or named (explicit delete), a
last-write or heartbeat. Because it is data, reclaiming is reactive: a rule reaps lapsed
ephemeral scopes. A surface over it: list, get, delete (the union of what `disconnect` does and
dropping the record), and optionally create.

## Open

Record at the store seam (uniform, but a scoped store writing into baseline) or in versed's
write path (misses direct store clients). Once-and-cheap: write-if-absent plus a per-client
cache. Delete while a client is live: fence it, like task cancel. A registry rule must not wake
itself.