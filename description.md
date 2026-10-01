# github out: a merge the io made, whose status write was lost, is recorded as one made around verse

**Precondition:** `mergePull` succeeds, and the status write that follows it is lost (verse unavailable, the io restarted, the write refused).

**What happens:** the next pass reads the pull request as merged and records `{merged: true, mergedAt, head}` with no `commit`. The docs say that shape means the merge was made around verse. So a reader or a rule checking whether the io merged at the decided sha is told it did not.

The same lost write is how finding 2 of the review issue (self-adoption on `open`) arises. That one is fixed by checking the author. A merge has no author check that settles it: the merge commit's committer is GitHub's for a squash or a merge commit made through the API.

Documented in docs/reference/iod.md (the out half of github), and not fixed. Possible fixes:
- read the merge's actor from the pull request's timeline events;
- or write an intent to the status before merging, so a lost write afterwards is known.