# Merging an agent's branch: what the charter cannot say yet

Status: residual. The charter is [merge-gate.tony](merge-gate.tony) — which belongs on
`use/verse-dev` beside comment-drift.tony and is here only because that is where it was written —
and it
is four rules that work with today's grammar. This is the part that does not, written down
because a gap named in a file is a thing somebody can decide about and a gap left in a head is
not.

## The blocker: a task cannot reach a remote, and the fix is not in this charter

Found by checking before installing rather than after. Rules 2 and 4 both spawn a task that runs
`git push`, and the coderunner says twice and by design that this cannot work:

> The checkout is the parent's work — it holds the mirrors and the credential. This side opens
> what is already there, which is also why its Trees carry no auth: **nothing on this side of the
> boundary can reach a remote.**

The first fix I reached for was to add `merge` and `retire` ops beside `deliver`. That is wrong,
and wrong in the same way the charter's `--ff-only`-in-a-prompt is wrong: it puts a policy in a
mechanism and leaves the next flow needing a third op. The delivery channel's own comment already
says what it should be — *"a capability the parent holds and the task may ask it to exercise"* —
and what is implemented is one op with no target, which is a button.

So the change belongs to the pushing, not to this charter:
[push-as-a-capability.md](push-as-a-capability.md). The environment declares destinations, the
task names one and never a ref, the vector gates the resolved ref, and approval is what MINTS a
scoped credential — so the gate stays load-bearing without the parent having to understand every
operation anyone will ever want.

`--ff-only` does not move with it, and that is decided rather than left open: it stays an
instruction in the prompt and becomes a rule at the receiving end
(`receive.denyNonFastForwards`), because `where` scopes onto a credential and `how` does not
scope at all. An invariant enforced by the receiver beats one enforced by the caller; the cost is
that it lives outside verse and the docket cannot show it.

Until that lands, rules 1 and 2 up to the gate are the drivable part — the derive, the stratum,
the `!get-path`, the gate formula, and whether anybody answers a docket question. Those are also
the parts nothing has verified yet.

## The one that was asked for, and is not expressible

**A branch is deleted; the question standing on it should disappear.**

Two halves are already there. The delete is ordinary — the sweep pushes `--delete` and the git
source stops reflecting the ref on its next pass, level-triggered, no event to miss. And the
withdrawal *mechanism* exists at the loop: `Registry.OnDrop` → `mirror.DropQuestion` settles the
question and now records WHY, so a docket reader can tell "the asker is gone" from "an operator
withdrew it".

What is missing is the thing between them: **nothing can cause a standing gate to be withdrawn
when its subject stops qualifying.** A trigger's gate is posed by a firing, and the firing waits
on the registry. It is discharged by an answer, by its formula's deadline, or by the firing being
cancelled — and nothing cancels a firing because the world moved.

So today the sequence is: the sweep deletes a stale branch, `git:ref:*` vanishes, the
`merge:proposal` entity stays `open`, and the question stays on the docket, about a branch that
no longer exists, until its 24-hour deadline denies it. That is not broken, it is merely slow and
misleading — somebody reads a docket entry asking them to merge something that is gone.

### Two ways to close it, and they are not the same size

**Deletion as a cause.** Make an entity disappearing a rising edge a rule can fire on, so the
charter says "when the ref goes, mark the proposal withdrawn". This is the general fix and it is
the bigger one — it is [absence-plan.md](absence-plan.md)'s territory, and that plan opens by
saying the absence latch the trigger model *already teaches*
(`value: !not {reviewed: null}`) is silently broken across a restart, because the store spells
absence by writing a bare null and hides every bare null it reads. So absence is not a small
addition here; it is a repair with a plan already written, and this is one more caller for it.

**Level-triggered gates.** Much smaller, and it is the same rule verse already applies one layer
down. `task/machine.go` withdraws a standing question when an operator intervenes, on the
argument that *"leaving somebody staring at a question about an action the task will now never
take is worse than withdrawing it"* — and a gate about a branch that has been deleted is exactly
that question. The ask is: a firing's gate is re-evaluated when its `subject` stops matching, and
withdrawn through the path that already exists, with `mirror.DropQuestion`'s `why` saying the
subject went.

That would also fix a case nobody has hit yet but will: a proposal that is merged BY HAND while
its question is standing.

I would take the second. It is a rule verse already believes at one layer, it reuses the
withdrawal path built this week, and it does not wait on a store repair.

## Smaller things the charter works around

**The sweep cannot see the docket.** It must not delete a branch whose question is still standing
— today it would, and the operator answers into a void. It is *nearly* expressible: questions are
entities (`gate:question:*` with `status: pending`), so `!list-paths {system: gate, kind:
question, value: {status: pending}}` would hand the sweep task the standing questions to avoid.
Left out of the first version because it makes the prompt do bookkeeping the charter should do,
and because with level-triggered gates the ordering stops mattering — delete the branch, the
question goes.

**A merge task is an LLM doing four git commands.** `driver: llm` for something mechanical is
both unreliable and expensive, and a `commandDriver` running a script is the right shape — an
entry under the runner's `agents:` config, which is a runner change rather than a charter one.
It matters less than it looks now that `--ff-only` is enforced by the receiving end: a model
that ignores the instruction gets its push refused rather than merging some other way.

**`onFailed` conflates two runs.** Not this charter's, but found while reading the other one:
comment-drift's `onFailed` clears `result` and leaves `reviewedSha`, so after a failed review the
entity reads as "reviewed at `<older sha>`, failed" — a sha this run never touched. It wants
`reviewedSha: !delete null` alongside, the way `result` already has.

## What it cannot be tested against yet

There are **zero** `verse/auto/*` branches on origin. Every delivery so far has ended "nothing to
deliver: no repository with a push target was changed", because every review has found the
comments accurate — and the one branch on origin, `agent/comment-drift_…`, predates the rename
to `verse/auto/`.

So installing this today gives an empty selection and teaches nothing. The first real subject is
a review that actually changes something, which means either a commit with genuine comment drift
in it or a drive pointed at a subject chosen to have some. That is the prerequisite, and it is
worth saying plainly rather than discovering after the install.

The thing it WOULD test, once it has a subject, is the open question `verse-dev.md` has been
carrying since it was written: whether `verse docket` is somewhere a dogfooder actually looks. A
merge gate is a good instrument for that, because unlike a notice it has a consequence — and if
nobody answers it, the deadline denies, which is a finding rather than a hang.
