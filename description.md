# a run's record cannot say what it was held to: no answer contract on the entity, and the ad-hoc path records no envelope at all

Finding 12 of the coderunner drive.

## The finding, as the drive recorded it

A contract run completed `done` with an answer that **does not satisfy its contract** — checked
against the real result: *"file: absent: expected !irtype string"*. So either the contract never
reached the run, or it was not enforced. The plumbing is wired end to end (CLI → `api.Spawn` →
`versed` → the spawn wire → `taskd` → `harness.Build`), and a model told about a contract would
almost certainly have emitted the field it names, so the likely answer is that the flag was not
passed.

**The point is that this cannot be settled from the record.** `task:instance` carries prompt,
status, result, runner and resolver; `mirror.Meta` can carry `grant` and `tools`, and the charter
path fills them. It has no field for the answer contract at all, and the ad-hoc path records none
of the envelope — not vector, not grant, not tools. So "was this run governed, and by what?" is
unanswerable an hour later, which is exactly the question an operator asks after a run does
something surprising.

## The obligation already exists

[docs/roadmap/authority.md](docs/roadmap/authority.md) names it, about a derived envelope rather
than a declared one, and the argument does not care which:

> The derived envelope must be recorded on the task entity so it is inspectable (`task get`),
> reviewable after the fact, and **diffable against what the task actually did**.

That last clause is the one the run failed. The task did something — produced an answer of one
shape — and the record cannot say what was required of it, so no diff is possible.

## Why it matters beyond the drive

It is the same complaint as findings 6 and 7 — verse says *that* work happened and little about
it — moved from the read plane to the entity, and it is worse here because the envelope is
precisely the part a person needs after the fact rather than during.

It is also not a foreign-agent problem. The gap is in the record, so it applies to the
in-process loop too: `docs/task-control.md` now states what each of the three enforcement modes
guarantees, and nothing on the entity lets a reader check afterwards which one a given run got.