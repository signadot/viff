# Driving the drive locally

Status: **a harness, not a design.** It runs the comment-drift state machine on a local verse in
about fifteen seconds so the rules can be iterated on; the charter it installs is
[drift-local.tony](drift-local.tony), which is the real
[comment-drift.tony](../comment-drift.tony)'s first half with the review brokered.

```sh
./docs/working/verse-dev/drive-drift.sh          # every scenario
./docs/working/verse-dev/drive-drift.sh --keep   # …and leave the verse up to poke at
```

## Why

The flow's open problems are about ORDER, and order is what a ten-minute cycle cannot iterate
on. Against staging one turn is: push a commit, wait for the git source's poll, wait for an llm
review over a real checkout, wait for the coderunner to stage a branch. Getting a rule wrong
costs the same again. The two defects below were each found by accident during one such turn,
and neither could have been *studied* there.

Here the two slow parts are replaced and nothing else is:

- **The head is a write.** `verse data git:head:verse --put '{repo: verse, sha: …}'` is what a
  git source's reflection amounts to, so a "commit" costs a millisecond and a "push of two" is
  two writes with no sleep between them.
- **The review is a brokered task the script answers.** `verse task do complete <id> '<answer>'`
  supplies the findings, so the answer is a fixture and — the part that matters — the TIMING is
  the test's. A review lands exactly when a scenario says it does, which is the only way to hold
  a race still.

Everything else is the real thing: the same three rules, the same strata, the same hooks, the
same derive into the same ledger. A fix proved here transfers, and when the two charters diverge
it is drift-local.tony that is wrong.

## What it holds still

Both are printed as `~ KNOWN DEFECT` rather than asserted, so the script exits 0 while they
stand: it is a harness for fixing them, and a red harness tells you nothing new each run.

**A review that lands after the head moved writes over the newer state.** `head-moved` re-derives
`reviewed: pending` for the new commit, `gather` advances it to `gathered`, and then the older
review's `onDone` patches `reviewed: done, reviewedSha: <its own sha>` over the top. Nothing
fires again — `gather` wants `pending`, the review wants `gathered` — so the newer commit is
never reviewed and the flow is parked until the head moves again. Observed in staging first, with
`sha: 7004e3ff` and `reviewedSha: c4ac8fd1`; the recovery there was to write `reviewed: pending`
by hand.

**A push of two commits reviews one of them.** The head is a single slot and the review is a
single task id — `(rule, subject)`, and the subject is one entity — so the second write to
`git:head:verse` overwrites the first, and whichever commit loses the race is never a subject at
all. A finding fixed in the commit that loses stays open forever. Which one loses varies run to
run, which is itself the point.

Both have the same shape: the head fact is keyed by the SUBJECT rather than by the commit. A
head record per sha would give each commit its own state, its own task id and its own hook, and
neither race would have anywhere to land. That is a change to the real charter, and this is the
place to try it in seconds rather than in an afternoon.
