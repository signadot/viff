# TestSeqStoppedRunReleasesItsClaim reads the claim once more after it saw it free

After the stopped run's off-ramp has landed, the test polls until the run's claim reads free, then reads it once more and fails if it is held. Every later firing on the subject (the `ended` publish and the onFailed patch both change it) takes the claim for the moment it decides under it, as startSeq is designed to, and gives it back; the test's extra read can land inside that moment. Seen as "verse.task.claim."review@demo.pr.1" is still held after the run stopped" on a test that took 0.40s, i.e. the poll had already seen the claim free.

What a leak looks like is a claim that never reads free. Assert that: fail only when the poll never saw it free.