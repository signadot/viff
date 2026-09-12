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

The eleven, and what each is a residual of:

| | of | what is in it |
|---|---|---|
| [firing-residual.md](firing-residual.md) | [trigger-model.md](../trigger-model.md), [convergence.md](../convergence.md) | time as a cause, the read per commit, what `verse entity rm` should mean |
| [attached-resolvers-residual.md](attached-resolvers-residual.md) | [attached-resolvers.md](../attached-resolvers.md) | the two unbuilt drivers, a `by` nobody verifies |
| [gate-unify-residual.md](gate-unify-residual.md) | [trigger-model.md §gates](../trigger-model.md#gates--whether-it-proceeds), [reference/tasks.md](../reference/tasks.md) | the runtime vector gate, where the record lives, two clocks on one record, what an exclusive binding does not refuse |
| [runners-residual.md](runners-residual.md) | [runners.md](../runners.md) | how control is applied is convention, and whether `local` should be nameable |
| [sources-residual.md](sources-residual.md) | [sources.md](../sources.md) | dial-in, and the levers a resync has not spent |
| [gh-source-residual.md](gh-source-residual.md) | [reference/sourced.md §github](../reference/sourced.md#github) | the kinds no repository can own, and what the budget measured |
| [goals-residual.md](goals-residual.md) | — (removed) | the definition no use case earned, and why goals were taken back out |
| [derive-residual.md](derive-residual.md) | [strata.md](../strata.md) | the fan-out landed; the sweep, the two neighbours, and what `!each` deliberately does not reach |
| [coding-runner-residual.md](coding-runner-residual.md) | [reference/examples/README.md](../reference/examples/README.md#running-the-coderunner) | what `principal: runner` actually contains and the refusal nothing makes, staged work never swept |
| [merge-gate-residual.md](merge-gate-residual.md) | [reference/examples/README.md §push](../reference/examples/README.md#a-push-is-a-capability-with-named-destinations) | the sweep's discovery: a condition that reads another entity's value, which `!let` is the shape for |
| [grants-residual.md](grants-residual.md) | [perms.md](../perms.md) | policy edits as gated changes, a claim as an owner record, what is not attributed yet, and where a question stands |

[provider-selection-improvements.md](provider-selection-improvements.md) is the same genre under
an older name: it reads against [provider selection](../provider-selection.md) and carries what
that landing left, which is why [contributing.md](contributing.md) cites it as one.

Nothing above owes coherence to the *others* — a residual is a relation between one note and one
landed thing, not a shared document.

## The file that used to be missing

`verse-dev-comment-drift.md` — the drive's numbered findings — lived only on `use/verse-dev`,
so four notes ([absence-plan](absence-plan.md), [derive-each-plan](derive-each-plan.md),
[derive-residual](derive-residual.md), [occurrence-plan](occurrence-plan.md)) cited "finding 10"
and "finding 19" and linked to nothing. It has been copied across, which is the move this section
used to say was right and belonged to a deliberate sync rather than to whoever next noticed a
broken link.

It is a COPY, and the branch keeps its own. That is the honest arrangement for a drive's
scratch: the drive goes on writing there, and what is here is what the notes citing it were
written against. When it drifts far enough to matter, copy it again — the alternative is a
pointer that resolves differently depending on which checkout a reader has.
