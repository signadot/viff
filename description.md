# residual: a condition's read resolved per candidate, with $subject bound

Status: residual, narrowed 2026-09-22. This issue began as what the merge-gate drive left:
merging an agent's branch, what the charter could not say, and what a person was asked. That is
built, and the earlier text is kept in this issue's history (`git log` on its ref), which is what
the code comments citing `git-issue 8wt1ta8r` refer to. One design question remains.

## What closed the rest

- **A standing gate is withdrawn when its subject stops qualifying.** `untilMoot` in
  `trigger/engine.go` watches the subject while the question stands; when the rule would no longer
  select it, the question leaves the docket with the rule's own words as its reason, nothing is
  decided and no `onDenied` runs. A yes that lands after the subject left is re-checked and the
  firing withdraws rather than acts. Since `2ea01439` (2026-09-10), pinned by
  `trigger/gatemoot_test.go`; trigger-model.md, "A gate's answer applies to the world it was asked
  in".
- **A person sees what is being decided.** `verse docket` shows a subject's `says` ahead of its
  address (`06476112`), and the merge train carries the review's own `says`.
- **A condition can read another entity** (`trigger/condread.go`, `!let` with `!get-path`), which
  is how a proposal cut from a tip that main has passed is selected as stale.
- **The ledger marks a finding resolved only once main is right** (`proposedAt`).
- **A charter gate's deadline runs out through `onDenied`.** A vote with `deadline: {within: "72h", default: deny}`, `onDenied: {patch:
  {outcome: dropped}}` reached its deadline with every candidate unanswered, and every one reads
  `outcome: dropped`.
- **Motions and `reviewedSha` no longer exist**, and with them the `verse gate` / `verse vote`
  confusion and `onFailed`'s stale sha.

## What remains: a read resolved per candidate

A condition's read cannot name `$subject`. It is refused at install (`trigger/condread.go`;
trigger-model.md, "A condition may read too, and that one does track"), because the read is
answered once per firing and a condition is what chooses the subject: there is no one entity to be
about yet. That refusal is right for the read it describes.

The rule that wants more is a sweep that must not retire what is still in flight: *nothing standing
names me*. Retiring a stale proposal's branch while a question about that proposal stands, or while
the merge it approved is running, loses the work, and it did once on the first live drive (the
branch retired within the same second the merge task started). The question stands at
`verse.gate.question.*` with its `subject`, and a running task at `verse.task.instance.*` with its
flow's subject, so the entities to read exist. What does not is binding the read to the candidate:
the predicate evaluated against each entity the match selects, with `$subject` bound to it, at the
cost of one read per candidate rather than one per firing.

That changes what a condition costs, and the one-snapshot-per-firing argument (convergence.md, "One
snapshot, so two reads cannot disagree") has to be restated for it. A design question, not a
defect. It is the second time this limit has been what stood in the way.

**Until then, the containment is ordering: retire late.** A branch main has passed costs a ref and
nobody reads it; a merge that loses its branch loses the work. A sweep that skips proposals younger
than their gate's deadline collides with nothing and needs to see no docket. Expressible today; not
recorded as tried.
