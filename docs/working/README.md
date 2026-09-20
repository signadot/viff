# Working notes

Design thinking that is worth keeping in the repo but has not been reconciled with anything
else: ideas, half-designs, an argument still finding its shape. These are durable so they
survive being picked up again, not because they are settled.

The notes are in [`work/`](work/). Beside it stand the files that say how the work is governed
and where it stands, which is why they are not in it:

| | |
|---|---|
| [charter.md](charter.md) | what verse is for, who owns it, and how work under it is owned |
| [contributing.md](contributing.md) | how the repository carries that out |
| [owners.tony](owners.tony) | who holds which work, and until when |
| [done.md](done.md) | what landed, and where it is described now |
| [residuals.md](residuals.md) | what a landing did not build, and where each gap is tracked |

Nothing here owes coherence — not to another working note, and not to the
[roadmap](../roadmap/README.md). Two notes may reach opposite conclusions and both stay,
which is the point: the disagreement is the state of the thinking, and flattening it early
loses the reason one side was preferred.

A note leaves in one of two directions. If it gets built, it becomes a doc in
[`docs/`](../README.md) describing what runs and naming any corner that does not. If its
argument settles while still undone, it is synthesized into the roadmap — which usually
means editing an existing roadmap note rather than moving this file, since the roadmap is
read as one whole.

Two things follow, both worth stating because the temptation runs the other way:

- **Say what state it is in.** A note that opens with a status line ("Status: proposal,
  nothing here runs") costs one sentence and saves a reader from mistaking a sketch for a
  plan. Whether the reasoning argues from code that exists is a separate matter and no
  reason to file it elsewhere — most good design notes do.
  The same line says whether the note is **in work**, by naming the claim in
  [owners.tony](owners.tony) that carries it (see [Unclaimed](#unclaimed) below).
- **Do not tidy by deleting.** A note that was wrong is more useful than no note, provided
  it says so. Mark it, keep the reasoning, and link what replaced it.

## In work, free, done

Three states, and one place says each. A note is **in work** when a live claim in
[owners.tony](owners.tony) carries it — a named owner, a pending item, and a deadline that has
not passed. When the work lands the note leaves, its headline and commits going to
[done.md](done.md). Everything else is **free**.

A claim over a note names the note: `work: task-budget` claims `work/task-budget.md`. Work that
is not a note names itself the same way, so one file holds both.

```sh
make claims          # who holds what, what has lapsed, and what is free
```

**Lapsed is its own list.** The [charter](charter.md) says an expired claim is void, so lapsed
work and work nobody ever claimed are equally free — but they do not tell you the same thing,
and the difference is worth knowing before you pick one up: somebody meant to, and life moved.

**A note may say who holds it, and then it is checked.** One line under the title, and
`make check` fails when it disagrees with `owners.tony`:

```
Claimed: trent@example.com until 2026-11-01
```

A note that says nothing is free to be free. Saying something is the only way to be wrong, which
is why that is the part a check looks at — the claims themselves live in one file, so they cannot
drift from themselves.

Deriving all of this rather than keeping it by hand is the `tracking-unclaimed` work in
[owners.tony](owners.tony), and `scripts/claims.sh` is its bootstrap and its acceptance test: a
charter that reads the same three places should answer what the script answers.

## Residuals

A residual is what a landing named and did not build. It is an issue now rather than a file here,
and [residuals.md](residuals.md) is the index: what each gap is missing from, and where it is
tracked. The rule that produces one is unchanged — a landing deletes the note and the plan it came
from and rewrites `docs/` to say what is true
([contributing.md](contributing.md#from-a-note-to-a-plan-to-a-doc)) — and what it skipped goes on
that list rather than becoming an unknown gap.

Two things hold wherever the gap is written down:

- **It names what it is missing from**, because everything it says is "what is missing from
  *that*".
- **A looseness inside something that runs is recorded where it runs.** A residual carries what
  was *not built*; a corner of what was built belongs in the doc that describes it, and is a
  residual only when the fix is a design rather than a patch.
