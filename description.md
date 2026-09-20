# runners: does Handle carry too much, or does a wrapper hold the retry and the reconnection?

Handle is nine methods now — Subscribe, SubscribeSince, Pause, Resume, Cancel, Steer, SetVector, Result, Err — and it is the seam a third party implements. The alternative considered when the seams were repaired: a wrapper that adapts a minimal Handle and holds the retry, the deadline and the reconnection, since that logic is identical for every runner.

Open question from docs/working/work/seam-repair.md. Not a defect; a shape to decide before there is a second third-party runner.