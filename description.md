# versed: startup phases decide a read failed by the store's shared flag, not by their own read

The startup restores list their slice with `entity.Below` and then ask `entity.ReadFailed(store)` whether that read happened. `ReadFailed` is the store's LAST read error, shared by every reader on it (entity/doc.go says so above `ListErr`: "a neighbour's success hides this read's failure, and a neighbour's failure condemns a read that was fine"). At startup the firings of every restored rule read through the same store, so:

- a phase whose read succeeded reports "could not be read" because a firing's read just timed out, and the phase is asked again whole, with every phase after it waiting on it;
- a phase whose read failed is cleared by a firing's success and restores NOTHING, saying nothing: a package comes back with no rules.

Staging's startup at 08:07 UTC 2026-10-23 had "restored packages: asked again until it answers: the strata could not be read" at 08:11, in the middle of a storm of firings' reads timing out; from the log it cannot be told whether that read actually failed.

Sites:
- trigger: `LoadStrata`, `LoadCharter`;
- versed: `restoreSources` (io.go), `packagesSeenErr` (two listings, then one check: the second's success clears the first's failure), `reconcileTasks` (server.go), `readSuspensions` (suspend.go), the system-charter prune (systemprune.go, which also reads `ActsAs` between the listing and the check).

Fix: each reads with `BelowErr` / `ListErr` and reports its own failure, as `entity.Unread`, so the phase retry is unchanged.