# entity: the store opens watches a few at a time, so a session drop re-establishes them in turn rather than all at once

Stage A of wv2pp5j0h12ks1ncwnn0.

## Problem

versed holds about 390 subtree watches on one logd session (staging, 2026-10-31). When the session drops, every watch's pump re-establishes itself in its own backoff loop (entity/logdstore.go, the loop after `pump:`), and nothing bounds how many do so at once. Each re-establish is one initial-state read on the server (the comment at the `RE-WATCH AND RECONCILE` block: one State carrying net state). logd serves at most 8 reads at a time per session (`maxConcurrentReads`, go-tony system/logd/server/session.go:266) and blocks the session's request loop when they are taken, and docd answers a ping only from that loop. So the ping waits behind the reopens, the 5s heartbeat times out, and the drop repeats. On staging that turned one timeout into 15–25 minutes of churn three times in a day (2026-10-30 20:07–20:31 and 00:19–00:35, 2026-10-31 08:08–08:13), with up to 4,607 abandoned watches per teardown.

The same reads land on the session when the restore arms every rule at start, and when a charter installs.

## Change

- `LogdStore` gets a gate of N slots. `establish` takes one; the pump releases it when the watch's first State is seeded, or the establish fails. Every watch the store opens passes through it: a rule arming (`WatchPattern`), a re-establish after a drop or a force-end, a source's slice.
- N starts at 4, under logd's 8, so pings, writes and ordinary reads keep flowing. A var, so a test can set it.
- FIFO. No priority between a re-establish and a new watch.
- A counter beside `subtreeReopens` for watches waiting at the gate, read by status (wv2pp5j0 decides what status says).

Nothing above the store changes. The restore and installs block on the gate as they arm, which is the batching wv2pp5j0 asks for, with no code of its own.

## Measure

Before and after, on a local verse with ~400 rules watching (a charter generated for it), a forced session drop: `subtreeReopens` over time, heartbeat timeouts, and how long until the last watch is seeded. Then staging's next versed restart, with the heartbeat watcher capturing goroutine dumps of versed and docd at the first timeout, if one comes.

## Done when

- A forced drop on the local verse re-establishes every watch with no second heartbeat timeout.
- A test shows at most N establishes in flight across a drop, and every watch seeded after it.
- The read-slot chain is confirmed or refuted by the dumps, and the issue says which.