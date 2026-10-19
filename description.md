# trigger: TestSeqStoppedRunReleasesItsClaim flakes under load

`trigger/seq_test.go:332` fails with `verse.task.claim."review@demo.pr.1" is still held after the run stopped` about 2–3% of the time on a loaded machine. It came up in the full-suite gate on the rebased verse/cut (26dd7554).

The engine fix for the dropped late commit (38f9, f681f53e) did not cause it. With 4–6 `yes` processes running and `-test.count=200`:

- 614b76e6 (before the fix): 4 failures, and 6 in a second run, all at seq_test.go:332
- 26dd7554 (with the fix): 5 failures

Run alone, without the extra load, it passes 30 of 30.

Suspected cause (not verified): `startSeq` takes the claim first and only then checks `seqConcluded`, so any firing that comes after the stop holds the claim for a moment before it releases it. The test polls until it sees the claim free, breaks, and then reads the claim a second time. A firing that lands between the two reads makes it look held. A 1s poll may also be too short under load. The next step is to find out which of the two it is before changing either.