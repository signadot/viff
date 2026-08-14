# Working notes

Design thinking that is worth keeping in the repo but has not been reconciled with anything
else: ideas, half-designs, an argument still finding its shape. These are durable so they
survive being picked up again, not because they are settled.

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
- **Do not tidy by deleting.** A note that was wrong is more useful than no note, provided
  it says so. Mark it, keep the reasoning, and link what replaced it.

## Residuals

`$X-residual.md` is the one genre here that *does* owe something. A landing deletes the note and
the plan it came from and rewrites `docs/` to say what is true
([contributing.md](contributing.md#from-a-note-to-a-plan-to-a-doc)); the residual is what those two named and the
landing did not build, kept so a known gap does not become an unknown one. So a residual is read
against something that runs, and three rules follow from that:

- **It names where the built thing is documented**, in its first line, because everything it says
  is "what is missing from *that*".
- **It is edited when the tree moves, not appended to.** A correction folded into the argument is
  worth more than a strike-through under it — the exception being a wrong turn whose reasoning
  still teaches, which is kept as prose rather than as a scar.
- **A looseness inside something that runs is recorded where it runs.** A residual carries what
  was *not built*; a corner of what was built belongs in the doc that describes it, and appears
  here only when the fix is a design rather than a patch.

The nine, and what each is a residual of:

| | of | what is in it |
|---|---|---|
| [firing-residual.md](firing-residual.md) | [trigger-model.md](../trigger-model.md), [convergence.md](../convergence.md) | time as a cause, the read per commit, what `verse entity rm` should mean |
| [attached-resolvers-residual.md](attached-resolvers-residual.md) | [attached-resolvers.md](../attached-resolvers.md) | the gate fork, the two unbuilt drivers, a `by` nobody verifies |
| [runners-residual.md](runners-residual.md) | [runners.md](../runners.md) | how control is applied is convention, and whether `local` should be nameable |
| [sources-residual.md](sources-residual.md) | [sources.md](../sources.md) | dial-in, and the levers a resync has not spent |
| [gh-source-residual.md](gh-source-residual.md) | [reference/sourced.md §github](../reference/sourced.md#github) | the kinds no repository can own, and what the budget measured |
| [goals-residual.md](goals-residual.md) | [reference/goals.md](../reference/goals.md) | the definition that no use case has earned |
| [derive-residual.md](derive-residual.md) | [strata.md](../strata.md) | the fan-out landed; the sweep, the two neighbours, and what `!each` deliberately does not reach |
| [coding-runner-residual.md](coding-runner-residual.md) | [reference/examples/README.md](../reference/examples/README.md#running-the-coderunner) | what `principal: runner` actually contains and the refusal nothing makes, staged work never swept |
| [merge-gate-residual.md](merge-gate-residual.md) | [reference/examples/README.md §push](../reference/examples/README.md#a-push-is-a-capability-with-named-destinations) | the sweep's discovery: a condition that reads another entity's value, which `!let` is the shape for |

[provider-selection-improvements.md](provider-selection-improvements.md) is the same genre under
an older name: it reads against [provider selection](../provider-selection.md) and carries what
that landing left, which is why [contributing.md](contributing.md) cites it as one.

Nothing above owes coherence to the *others* — a residual is a relation between one note and one
landed thing, not a shared document.

## One file this directory links to and does not have

`verse-dev-comment-drift.md` — the drive's numbered findings — is on the `use/verse-dev` branch
and not here, so four notes ([absence-plan](absence-plan.md),
[derive-each-plan](derive-each-plan.md), [derive-residual](derive-residual.md),
[occurrence-plan](occurrence-plan.md)) cite "finding 10", "finding 19" and link to nothing.

Recorded rather than repaired, because both repairs are wrong on their own. Repointing them at
[comment-drift-drive.md](comment-drift-drive.md) would be a lie — that is a summary and has no
numbered findings. Copying the file over is the right move and belongs to a deliberate sync of
that branch, not to whoever next notices a broken link. It is also the case
[contributing.md](contributing.md) has in mind when it says the working note is the deliverable:
a drive's findings that only exist on the drive's own branch are findings nobody else has.
