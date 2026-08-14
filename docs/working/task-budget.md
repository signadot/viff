# Nothing bounds a run, and an operator's word is advice

Status: a finding with a proposed shape, nothing built. Found driving
[verse-dev](verse-dev.md) on staging: a review that had reached its conclusion kept working
for twenty minutes, was told to stop by an operator, and did not.

## What happened

A `comment-drift` review, spawned by a trigger against the tip of main, on the llm driver with
`vector: auto`. In twenty minutes it reached **43 boundaries** and:

- **delegated nothing.** Its prompt says *"For EACH of those files, call_agent once — one agent
  per file"*. Zero `call_agent` invocations. It read every file itself.
- **wrote a Go test file** with `write_file`, to check behaviour nobody asked it about. The
  working tree is the deliverable — the runner commits what is there — so that test was one
  approved push away from landing in a branch as though a reviewer had wanted it.
- **re-read.** The same `grep` six times, several other commands twice, against files whose
  contents had not changed.
- **concluded, and carried on.** *"No stale references remain. All comments accurately describe
  current code."* — and then twenty more boundaries of checking that.
- **was told to stop, and did not.** `verse flow say <subject> "…produce your answer now, and
  yield"` was accepted (`affected: 1`), crossed the hop, and folded into the driver's
  conversation — `↩ said-to: 1 instruction(s)` in the log, confirmed on both ends. The next
  thing it did was run the same grep again.

It ended because a human cancelled it.

## The two things this says

**The prompt half is fixable and has been fixed**, in `comment-drift.tony` on `use/verse-dev`:
delegate rather than read, write no tests, read each thing once, answer when the verdicts are
in. Each clause names something that run actually did. Worth stating what was NOT written, since
it was the first instinct: *"you have one minute, and anything you cannot finish is fine to
report as no findings"*. Both halves are wrong. A model cannot measure a minute — asking it to
is asking for a verb it does not have, which is the same shape as the `wait` story it was
reviewing when it hung ([llm.go](../../task/harness/llm.go): *"say it and stop asks for a thing
the model has no way to commit to"*). And "report no findings" would write a **false fact** into
a ledger whose premise is that findings are facts the next run reads back — an unexamined symbol
recorded as clean is one that never gets examined.

**The other half is not fixable by prompting, and that is the finding.** Every lever this run
had was advice:

| lever | what it is |
|---|---|
| the prompt | advice, standing. Ignored in three separate clauses. |
| an operator's `say` | advice, arriving mid-run. Landed and ignored — though it arrived as bare text, which has since changed: see below. |
| the vector | real, but it gates ACTIONS — and every action here was permitted. `auto` means proceed, and nothing about `run_bash` on the tenth identical grep is different from the first. |
| cancel | real, and it is the only one. It throws the work away. |

So a task that is neither failing nor blocked, and simply will not stop, has exactly one honest
end and it is destructive. **There is no turn cap, no model-call cap and no deadline anywhere in
the loop** — searched for; the only budget in `task/` is the answer contract's repair count.

That is the gap. This codebase's own discipline says a floor the subject may decline is not a
floor: a confiner that cannot unshare refuses rather than running the task with a proxy it is
free to ignore, and `run_bash`'s own doc says a command nobody cancels *"runs as long as it
likes, which is a policy question for whoever installs the executor"*. The loop has the same
question and has not been asked it.

## The shape it probably wants

**Exhaustion should force QUIESCENCE, not failure.** The machine already has a ladder for "the
input would go quiet" — children joined, answer checked, finisher consulted, terminal — and a
budget is just another way of arriving at it. That gets a bounded run a real answer: whatever
the agent has, put through the contract, delivered if it holds. Failing instead throws away work
that was probably fine, which is the cancel we already have.

It must not break the loop's property 2 — never terminal while a child is outstanding — so the
budget stops the loop asking for NEW work rather than ending it where it stands.

**Counted in boundaries**, probably. A boundary is the loop's own clock, it is what
`drive_test.go` already counts (*"%d boundaries for three files: polling by another name"*), and
it is the unit the driver cannot lie about. Wall time is what an operator cares about and is the
wrong thing to enforce, since one slow model call is not the failure being bounded. Model calls
are what it costs, and are a good second candidate.

**Set where?** A spawn field is the obvious home — the charter knows what a review is worth, and
a per-environment default in the runner would let a deployment cap what it pays for regardless.
Both, probably, with the spawn narrowing.

**And it should say so on the way out.** A run that ended on its budget is a different fact from
one that finished, and the answer should carry that — otherwise a bounded review is
indistinguishable from a thorough one, which is the same lie "report no findings" would have
told.

## What is not settled

- Whether the budget should also be *visible to the agent* ("you have N boundaries left"), which
  makes it advice again — but advice with a number attached, which is a different thing from a
  clock it cannot read. It would have to be honest: telling it a budget exists and then not
  enforcing one is worse than either alone.
- Whether a child's boundaries count against its parent's budget. They cost the same money and
  the parent chose to spawn them, which argues yes; a fan-out of ten agents then costs its
  parent ten times, which argues the cap belongs per-task with a separate one on the tree.
- Whether ignoring a `say` deserves its own answer. An operator's word landing and doing nothing
  is the part of this that reads worst, and a budget does not address it: it bounds the run, it
  does not make the run listen.

  **One thing was plainly wrong and is fixed.** The loop knew who was speaking — a convMsg
  carries `by` and a kind — and handed the driver bare text, so a person's decision arrived in
  the conversation looking like a tool result or the model's own earlier reasoning. It now
  arrives named, and saying the one thing the model could not work out for itself: that the
  speaker can see the run from outside, so this is a decision about what to do next rather than
  input to weigh. Deliberately silent about authority — an operator outranks the plan, and does
  not widen the grant or answer a gate.

  Whether that is enough is unknown, and it is a claim about a model rather than about the loop.
  If a framed steer is still ignored, the answer is not more words: it is that the only lever
  which does not depend on the model's cooperation is the one that stops asking it for another
  turn.
