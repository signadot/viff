# viff charter

## Scope

This charter pertains to the viff repository: what is in it, and how that changes.

## Statement of purpose

The purposes of viff are:

1. To coordinate work across software systems it does not own, so that the work is organised, observable, gated, steerable, and controllable.
2. To faithfully see that work through to its completion, while seeking guidance where appropriate.
3. To achieve 1 and 2 in a fashion which maximises the value of human attention.
4. To be something helpful that others can freely build on and use.
5. To remain fit to these purposes as the world and the nature of autonomy change.
6. To contain no work outside this charter, and to change that only through changing the charter.

## Ownership, work, and this charter.

This charter is owned by the viff maintainers, listed in `MAINTAINERS`.

Work under this charter shapes viff and changes are justified against the current
state of viff, in terms of the purposes above.

At the time of its last adoption, viff was a small runtime that supported

- continuous reflection of external systems to a central, commit-cursored document store, and back.
- the running of _charters_, which are sets of reactive rules against that document store that can
  be gated with an autonomy dial.
- _tasks_, launched by charter rules, which when they run share the same gating mechanism as charter rules.
- control of tasks, charters, and their related data, and steering of agentic tasks with natural language.
- a record of who made every write, and a permissions model over both data and control.
- the use case of automatically deployed charters in a repository's `.viff/charters`.

The commit that amends this charter also updates this description of viff, so that it
describes viff as of the last adoption.

Its current focus is software development work.


## Adopting and changing this charter

The maintainers decide whether to adopt or change this charter, and the decision process seeks consensus and shared understanding by principled and open-minded discussion.

- **Adopting or changing the charter needs every maintainer.** A change is announced with
  a window of at least 24 hours and every maintainer must accept it, but may configure
  their acceptance in advance of the announcement. Each change carries a justification.
- **Each maintainer says what their silence means.** Each maintainer has a silence
  setting, one of `yes` or `no` in `MAINTAINERS`, which is what their silence through the
  window counts as. A maintainer changes their own setting but may not change it if they
  are the only maintainer with silence setting `no`.
- **A change to the list of maintainers also needs every maintainer**, by the same path,
  except a maintainer's own silence setting.
- **All charter and maintainers changes are recorded as a commit on main.**

## Changing the maintainers

Adding a maintainer is a commit to main by the new maintainer whose commit message attests
to adopting the charter. The new maintainer's silence setting starts at `yes`.

Removing a maintainer follows the same path but may be proposed by any maintainer
and there is no declaration about adopting the charter.
