# askttl: a settled ask's expiry is written once

TestASettledAskGoesAfterItsKeep fails about one run in eleven on main (7/80 on 20e82070): the ask still stands after its keep.

Cause: expireLater (versed/askttl.go) writes expires = now+keep on every watch event that shows the ask settled without an expiry. After an answer the ask takes more than one write, so two such events can be delivered before the first expiry write is visible, and each computes its own instant. When the clock moves between them the second write moves the expiry later; the timer armed on the first fires, re-reads, and re-arms for the later instant. Under the test's fake clock that instant never comes. On a real clock the expiry drifts from when the ask settled by the watch's latency.

Fix: the expiry write also requires that no expiry stands yet (entity.Created()), so only the first pass commits. Verified 80/80 against 7/80 before.