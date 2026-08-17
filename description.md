# merge train: the charter cannot say which cars it left behind, or why

Follow-on to a separate (since-closed) issue, which fixes the batch by rebasing each car onto main inside the
task. That remedy is attempt-and-abort: the task discovers disjointness by trying, and the
CHARTER learns nothing. A car left behind leaves no trace -- it goes stale on the next tip like
every other proposal, and the docket cannot say whether it was skipped because it conflicted or
merely because main moved.

That is a step away from the property this half of the charter is built on. `proposal-outrun`
retired the LLM sweep precisely because the charter already knew what it needed: a proposal
records `base`, main moving IS the staleness test, and there is no model call, no fetch and
nothing asked. Discovering conflicts in a shell loop puts one of those questions back inside a
task.

## The other shape

A finding already carries `symbol`, so the file it touches is derivable. If a proposal recorded
the files it touches, then:

- `train-load` could assemble a train from cars known not to overlap, without asking git;
- a car left out would be left out for a stated reason, on the entity, before the task runs;
- the docket could show the batch AND what did not make it, which is what a person approving a
  batch rather than a change needs to see.

## What it costs

A new field on the proposal, written by whatever derives it from the review's answer, and a
predicate over two sets in `train-load` -- set intersection is not something a match says today,
so this probably wants a new operator or a `!let` shape that can express disjointness.

Disjoint-files is sufficient for a CONFLICT-FREE rebase in general; it is sufficient for a SAFE
one here because the changes are comment edits with no semantic interaction. A charter doing this
for ordinary code changes would be claiming more than the file sets support, and the field should
be named so that stays obvious.

## Why not now

Attempt-and-abort subsumes this one's safety -- a rebase that would conflict is aborted either
way -- so what this buys is legibility, not correctness. It is worth doing when somebody is
reading a docket and cannot tell why their branch did not land.