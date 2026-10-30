# staging: docd has no headroom for a versed restart

## What happened (2026-10-30)

At about 16:26:50Z, a spot interruption restarted versed, github-iod, slack-iod and linear-iod. docd (on the on-demand stateful node) was OOMKilled at 16:27:25 under versed's restore. With the store down, the restore refused the confined packages' rules (wb2pp5j0h12kssy9whn0).

docd on staging (deploy/profiles/staging.tony) runs with:
- `docdMemoryRequest: 3Gi`, `docdMemoryLimit: 5Gi`, `docdGoMemLimit: 3500MiB`, GOMAXPROCS 2;
- compaction cutoff 1d with a 1h base interval;
- snapshot policy maxCommits 1000, maxBytes 4MB;
- logA 588M and logB 567M on disk.

The profile's comment sizes the limit from a steady 1.8Gi and a 2.7Gi high-water mark over a day. That measurement doesn't include a versed restart arriving at the wrong moment.

## Measured after the restart (pprof on :9223 via port-forward)

- In-use heap about 264MB. The cgroup has about 700MB anon and 2.48GB file (page cache).
- Allocation is the load, not residency: 223GB in 15 minutes, about 250MB/s.
  - folded reads 29%
  - compaction 19%
  - watch matching 14%
  - reads 11%
- Steady reads about 67MB/min. versed's start burst alone is about 0.5GB of reads.
- Path snapshots: 346 `no.incomplete-read` against 10 made. Reads mostly replay from the log rather than starting from a snapshot.
- Before the OOM, docd was already struggling:
  - compactions took 7–8 minutes (10:35, 11:36, 14:06), against about 35s since the restart;
  - session resets began at 16:09, before the spot event.

So the burst came on top of a docd that was already behind. The limit only decided who died.

## What to change

Headroom first, in the staging profile:
- raise `docdMemoryLimit` and `docdGoMemLimit` together, keeping GOMEMLIMIT at two thirds to three quarters of the limit (build.tony's rule);
- raise the CPU, so compaction and GC aren't fighting reads on 2 cores.

Re-read VmRSS/VmHWM from `/proc/1/status` and size from a controlled versed restart, not the day's high-water mark. Check the stateful node's allocatable before moving the request.

Related, for their own issues if they hold up:
- versed's restore reads in one burst. Serializing it would cap docd's peak.
- The `no.incomplete-read` count points at snapshots rarely being usable. That's tony-format's side (new tony-format issue).
- Compaction running at the same time as a restart burst. Scheduling or yielding compaction under read load.
- Task records growing without pruning, which every folded read pays for.

Also note `spotVersed: true` in the same profile: a spot interruption restarting versed is expected here, so the store has to take that burst.