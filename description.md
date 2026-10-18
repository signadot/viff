# trigger: a commit delivered late on a second watch is dropped as a replay, and a condition read's change never wakes its rule

A rule whose condition reads another subtree can miss the change it is waiting for, and nothing wakes it again.

## How it happens (scoped engine)

`Engine.firstSight` (trigger/edge.go) keeps one watermark per rule: the highest revision seen. A delivery at or below the watermark is dropped as "a replay whose verdict has already been reached". The comment assumes all deliveries of one commit arrive together. A scoped engine opens one watch per subtree a rule reads, and those watches do not order their deliveries against each other.

The sequence that loses the wake:

1. A firing F of rule R about subject S is in flight.
2. Commit 120 changes something R's condition READS, not S.
3. Commit 121 is F's own write to S.
4. 121 arrives first, on S's watch. It is a change to the subject during F's flight, so `claimOrDefer` drops it, as designed (8z4azhtj). It still raises the watermark to 121.
5. 120 arrives on the read's watch. `firstSight(R, 120)` is false, so it is dropped as a replay. `claimOrDefer`'s deferral for non-subject changes is never reached.

Nothing else commits, so R never sees 120.

## Evidence

verse/next 34f9wqb3, `versed/mergegate_test.go` (TestTheMergeGateTakesOnlyWhatIsDeliverable). That is merge.tony's `train-load`, whose `told`/`offered` read the proposals while the train is its subject.

- Without the change below, the train stayed loaded with `offered: []` after the proposal's `says` had landed, in about 1 of 12 runs (5 failures in 60).
- With `firstSight` changed to never drop: 0 failures in 60.

The comment on train-load about drift's report ("the train sat loaded with nothing and nobody was asked") may be the same defect.

## Why not just drop the watermark

A late delivery is evaluated at its OWN snapshot (`AsOf(c.Rev)`). Evaluated after a newer commit, it can fire past a latch written since, which would mean a second patch or a second question. Candidates:

- A late delivery wakes the rule at the watermark's revision: the newest state, which includes the late commit. Track whether that revision was actually evaluated or was dropped in flight, so a verdict is not reached twice.
- Have the subject-change drop in `claimOrDefer` lower the watermark back, so the dropped revision is not counted as seen.

## Workaround on verse/next

merge.tony's train takes an offer only once its words have landed. A ready branch then wakes the train once instead of twice, which narrows the window but does not close it.