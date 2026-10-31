# charters: a reconciler keeps the engine's armed rules in step with the installed records, in batches, from start and at every install and teardown

## Problem

The rule records in the store say what should be running. What the engine has armed drifts from them, and today each kind of drift is handled once, at one moment, if at all:

- **A restore that coincides with a slow store** (2026-10-31 08:30, staging). `restored packages in 22.861s`. Re-applying the system charter for `repo-charters cli` then failed ("reading the declaration at repo-charters.cli … context deadline exceeded"), and `repo-charters.cli` has 0 rules over its stored records. The same step failed for `repo-charters verse` on 2026-10-30.
- **A rule refused at load for a reason that later goes away** (2026-10-30 17:22, the staging.27 ship). The restore ran before the runners were re-registered with their subjects. `drift.review-range`, `merge.proposal-outrun` and `merge.train-run` were refused ("no runner offers scripts … unreadable"), and nothing tried them again once the runners were readable.
- **A failed read taken for an empty one** (wb2pp5j0h12kssy9whn0). Every confined package's rules were refused as reading nothing.
- **A logd session teardown.** Every watch (about 390 on staging) is abandoned and reopened at once. Each reopening reads what stands under its path (`WatchPattern(..., noInit=false)`), and the rule evaluates its condition over it, all through logd's 8 read slots per session (`maxConcurrentReads`, go-tony `system/logd/server/session.go:266`). The ping waits behind them, the next heartbeat times out, and one timeout turns into 15–25 minutes of churn. On staging that was 20:07–20:31 and 00:19–00:35 on 2026-10-30/31, and 08:08–08:13 on 2026-10-31, with up to 4,607 abandoned watches per teardown. The read-slot chain is from reading the code; the dumps that would confirm it are still to be captured.

The only retry today is per rule, when a watch fails to open (`armTriggerSubtree`'s backoff, trigger/engine.go:419). A rule refused at load is never tried again, and nothing paces the arming.

## Design

A reconciler in versed that keeps comparing **what is wanted**, the charter records (package declarations, strata, rules), with **what is armed**, the rules the engine has loaded with their watches open. It works through the difference in batches, for as long as versed runs.

1. **Two kinds of refusal.**
   - *Can succeed later*: a store read failed, a runner isn't readable yet, a watch didn't open. The rule stays pending and is tried again.
   - *Won't succeed*: the rule is invalid as written. It is reported once and not retried until its record changes.
   Which refusals fall in which kind is part of the work.
2. **Batches paced by the store.** A batch is done when its watches have delivered their initial state. The next starts then, so the pace is how fast docd answers, not a fixed rate. The order is the same as today: declarations, then strata, then rules.
3. **Every path feeds the same queue**: the restore at start, a new install, a package's declaration changing, and a session teardown reopening watches. For a teardown, the store layer reopens watches today. Moving that onto the batched queue, or batching it in the same way, is where the store and charter layers meet, and the part to look at hardest.
4. **Nothing is missed while pending.** A watch opens with what already stands, so a pending rule sees the current state once it's armed, as a restore gives today. Changes made while it was pending arrive as that initial state, not as separate edges.
5. **The system charters and documents install after the queue first drains** at start, so their reads don't queue behind the restore.
6. **Status shows it.** For each package: rules armed, and rules pending with the reason.

Open: what an install call answers (cli, mcp, the verse system's `charter-install`): on entering the queue, with a way to wait until armed, or as today.

## Measure first

Capture docd/logd and versed goroutine dumps during one versed restart, and at the next heartbeat timeout. Count goroutines in logd's read handlers and waiting on `readSlots`, and which paths the 8 reads are for. This shows how much of the stall is arming as opposed to whole-subtree reads like `taskCount`'s (wq2pp5j0h12ks355wnn0).

## Subsumes

wb2pp5j0h12kssy9whn0 becomes a refusal that can succeed later. Restoring in chunks becomes item 2.