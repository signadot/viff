# viff constitution

## Scope

This constitution pertains to the viff repository: what is in it, and how that changes.

## Statement of purpose

The purposes of viff are:

1. To coordinate work across software systems it does not own, so that the work is organised, observable, gated, steerable, and controllable.
2. To faithfully see that work through to its completion, while seeking guidance where appropriate.
3. To achieve 1 and 2 in a fashion which maximises the value of human attention.
4. To be something helpful that others can freely build on and use.
5. To remain fit to these purposes as the world and the nature of autonomy change.
6. To contain no work outside this constitution, and to change that only through changing the constitution.

## Ownership, work, and this constitution

This constitution is owned by the viff maintainers, listed in `MAINTAINERS`.

Work under this constitution shapes viff and changes are justified against the current
state of viff, in terms of the purposes above.

At the time of its last adoption, viff was a small runtime that supported

- continuous reflection of external systems to a central, commit-cursored document store, and back.
- the running of _charters_, which are sets of reactive rules against that document store that can
  be gated with an autonomy dial.
- _tasks_, launched by charter rules, which when they run share the same gating mechanism as charter rules.
- control of tasks, charters, and their related data, and steering of agentic tasks with natural language.
- a record of who made every write, and a permissions model over both data and control.
- the use case of automatically deployed charters in a repository's `.viff/charters`.

The commit that amends this constitution also updates this description of viff, so that it
describes viff as of the last adoption.

Its current focus is software development work.


## Adopting and changing this constitution

The maintainers decide whether to adopt or change this constitution and its maintainers, and the decision process seeks consensus and shared understanding by principled and open-minded discussion.

The decision for proposed changes is taken 7 days after the time the request is
made by a maintainer.  It is adopted if and only if at that time

1. At least 2 maintainers approve.
2. The number of denials is strictly less than the number of approvals.
