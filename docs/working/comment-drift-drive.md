# verse comment-drift, and the merge gate: what the drive built and found

Status: it runs on staging. Two charters, one loop, first end-to-end merge on 2026-08-14.

## What it does

When main moves, a review reads the commit's Go files and asks one question per declaration:
**does this comment disagree with the code beside it?** Staleness is a disagreement, not a gap —
terse and missing are fine. It fixes what it finds in place, and the runner delivers the result
as a branch under `verse/auto/<sha>/<task>`. Findings are facts in a ledger, so the next review
reads its own memory rather than re-auditing whole files.

[merge-gate.tony](merge-gate.tony) picks it up from there: a delivered branch becomes a
**proposal**, the proposal carries a gate with a 24-hour deadline that defaults to deny, and a
yes merges `--ff-only` to main. Staleness needs no comparison operator — a branch fast-forwards
exactly while main is still where it was cut from.

Proven by seeding one wrong comment and letting it run: found, fixed, branched, proposed, voted,
merged. `git log` on main carries the result.

## What it took

The runner could not push at all. A push became [a capability with named
destinations](../reference/examples/README.md#a-push-is-a-capability-with-named-destinations):
the environment declares where work may go, a task names one and never a ref, the vector gates
the resolved ref, and **approval is what releases the credential**. Merging lives in its own
environment, so a charter not pointed at it cannot reach main however its vector is set.

## What it found

A write carrying a match operator made the store unreadable — refused at the store now
(`entity/raw.go`). Nothing bounds a run ([task-budget.md](task-budget.md)); an
operator's `say` landed and was ignored; a docket asked `whether open` and nothing more.

Left: the sweep needs a delete no destination expresses, the deadline has never run out.
