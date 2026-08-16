# Goals: what is left

Status: REMOVED, and this is the record of why. Goals ran — a target, a deadline fail-latch, a
live `waitingOn`, and owned triggers reaped on a terminal outcome — and nothing was wrong with
what ran. What was wrong is below: every use case tried against the *current definition* fell
short of it, so the feature was a top-level charter concept nobody installed, in a model whose
whole claim is that it is small. It was removed rather than carried; the finding and the shapes
a better definition might take are what this page keeps. It will take some iterations to arrive
at a good answer, and where it is headed is
[dynamic-charter.md](../roadmap/dynamic-charter.md).

The code is one `git show` away — the removal commit's parent has `trigger/goal.go`,
`trigger/monitor.go` and the routes — and one thing there is worth reading before rebuilding
any of it: `Monitor`, the reactive re-evaluate-on-change wrapper a `doneWhen` waited on. The
temporal OPERATORS it composed (`until`/`eventually`/`always`/`sequence`) were never evaluated
by anything — `Formula.Eval` handles the atom and returns false for the rest — so a better
design starts from what should evaluate them, not from restoring the old file.

From a reply to [Ani's question](questions.md) about whether flows advance toward goals by
executing tasks.

## The finding

Every (toy) use case for the current definition has fallen quite short. That is the whole of
it, and it was not a defect in what ran: the latch, the deadline and the reaping all did what
the reference said they did. It is the definition failing to earn its place on any concrete
thing tried against it.

## Three shapes, none of them settled

- **A predicate on flows.** A goal is a predicate on flows, which one asks an agent to try to
  realize with a given charter by poking at the flow. The agent is what advances; the goal
  only says what would count as having arrived.
- **Ani's formulation.** Flows are instances of an SDLC-related procedure created from
  charters, they have goals, and they advance toward those goals by executing tasks — the
  statement in [questions.md](questions.md), still unanswered.
- **Standing goals, higher up.** Something like *"there is continuously evidence of flow
  release delivering a secure envelope of public-facing services"* — not a target a flow
  arrives at and latches, but a property meant to hold continuously, of which evidence is the
  observable.

Nothing has settled or clicked yet. The three are not variants of one idea, which is part of
why: the first is per-flow and agent-driven, the second is per-flow and charter-driven, and
the third is not per-flow at all.
