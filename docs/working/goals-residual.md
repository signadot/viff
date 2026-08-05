# Goals: what is left

Status: the abstraction has not settled. [Goals](../reference/goals.md) run — a target, a
deadline fail-latch, a live `waitingOn`, and owned triggers reaped on a terminal outcome — and
nothing here is a plan to change what runs. What is here is the finding that every use case
tried against the *current definition* has fallen short, and the shapes a better definition
might take. It will take some iterations to arrive at a good answer.

From a reply to [Ani's question](questions.md) about whether flows advance toward goals by
executing tasks.

## The finding

Every (toy) use case for the current definition has fallen quite short. That is the whole of
it, and it is not a defect in what runs: the latch, the deadline and the reaping all do what
[the reference](../reference/goals.md) says they do. It is the definition failing to earn its
place on any concrete thing tried against it.

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
