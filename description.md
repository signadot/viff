# runner: a mirror's fetch deletes every ref, and leaves packed-refs real git refuses

A push into a mirror fails with `fatal: unexpected line in ./packed-refs: ^<sha>`, three times so far on staging.

## Cause

Two defects in go-git v5.19.2, met by the way the runner fetches a mirror.

1. `RefSpec.Reverse()` carries the `+` of a forced refspec onto the destination. The mirror's refspecs are `+refs/heads/*:refs/heads/*` and `+refs/tags/*:refs/tags/*`, fetched with `Prune: true`, so prune looks up `+refs/heads/main` at the remote, finds nothing, and removes every branch and tag. The fetch then writes them all back loose. `FetchIssues` does the same to `refs/git-issues/*`.
2. Each removal rewrites `packed-refs` without the ref's line and keeps the `^` peel line that followed it.

The runner's push into a mirror runs real git's `receive-pack`, whose automatic gc packs the refs, peel lines included, and detaches. So: fetch, push (gc packs), fetch. What is left is peel lines with no ref above them, which go-git ignores and real git refuses.

The gc also writes `packed-refs` outside the mirror's lock and under git's own `packed-refs.lock`, which go-git does not take: a go-git rewrite made from the file as it was can replace the one gc just wrote, and the refs gc packed and removed loose are then gone.

## Fix

- The fetches name their refspecs without `+` and force the update, so prune removes only what the remote no longer has.
- A mirror never runs gc on its own (`receive.autogc false`, `gc.auto 0`); the runner runs it, in the foreground, holding the mirror's lock, after a fetch.
- After that gc, and still under the lock, `packed-refs` is rewritten with no peel lines and no traits, as a checkout's is (`packTags`). A mirror already left with orphaned peel lines is repaired by its next launch.