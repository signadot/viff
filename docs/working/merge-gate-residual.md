# Merging an agent's branch: what the charter cannot say yet

Status: residual. The charter is [merge-gate.tony](merge-gate.tony) — which belongs on
`use/verse-dev` beside comment-drift.tony and is here only because that is where it was written —
and it
is four rules that work with today's grammar. This is the part that does not, written down
because a gap named in a file is a thing somebody can decide about and a gap left in a head is
not.

## The blocker, and the wrong turn worth keeping

**The merge runs; the sweep does not.** Both rules used to spawn a task that ran `git push`, and a
task on the coding runner could reach no remote at all. That is fixed and the fix was not in this
charter: a push is now
[a capability with named destinations](../reference/examples/README.md#a-push-is-a-capability-with-named-destinations)
— the environment declares where work may go, the task names one of those and never a ref, the
vector gates the ref it resolves to, and approval is what releases the credential. So rule 2
merges `--ff-only` locally and asks for the destination `main`, in the environment `verse-merge`
that exists to declare it.

Rule 4 still cannot run, and now for a sharper reason: it DELETES a branch, and a destination
resolves to a ref that a push writes. There is no spelling for a delete, so the sweep waits on a
destination that permits one — a runner change, recorded in
[coding-runner-residual.md](coding-runner-residual.md).

**The wrong turn is worth keeping because it is the same mistake one layer out.** The first fix
reached for was to add `merge` and `retire` ops beside `deliver`. That puts a policy in a
mechanism and leaves the next flow needing a third op — each one a new parser, a new authorization
decision, a new thing to audit — and it is exactly what the charter's `--ff-only`-in-a-prompt
would be if verse tried to enforce it. What the channel should hand over is a credential, at which
point it stops needing to know what git operation is being done.

`--ff-only` did not move with the rest, and that was decided rather than left open: it stays an
instruction in the prompt and a rule at the receiving end (`receive.denyNonFastForwards`, set on
this repository's `main`), because `where` scopes onto a credential and `how` does not scope at
all. An invariant enforced by the receiver beats one enforced by the caller; the cost is that it
lives outside verse and the docket cannot show it.

### What poking at it by hand verified, and what it did not

Installed into a local verse with comment-drift's `reflected` stratum beside it, and the two
subjects a git source would have reflected written **by hand** — `git:head:verse` and one
`git:ref:…` under `verse/auto/*`. No source, no runner, no delivered branch; everything else real.

**All four rules fire, and the chain closes.** Writing the ref fires rule 1, which derives

```tony
ref: merge:proposal:proposal-appeared@git:ref:verse_verse_auto_poke2+1f61cc97
payload:
  branch: verse/auto/poke2            # $subject.value.name
  base:   7281b716…                   # !get-path {system: git, kind: head, id: verse, path: sha}
  state:  open
  verse: {meta: {derivedFrom: {rule: proposal-appeared, subject: git:ref:…}}}
```

— which is the piece this charter calls its whole design, and it works: `!get-path` read main's
tip at the moment of the firing, the derive climbed from `reflected` (0) to `merge` (10), and the
address is (rule, subject) with the subject flattened and disambiguated. Rule 2 then selects the
derived proposal and poses its gate. Writing `git:head:verse` fires rule 3 the same way, and rule
4 spawns **ungated**, which is the call the charter argues for and which reads correctly when you
watch it: no question, straight to the work.

`verse vote <motion> approve` closes a motion `outcome=proceed tally=1` and the firing proceeds.
Both spawns are then refused for want of a runner, and `onFailed` does exactly what it says —
`state: failed` / `swept: failed`, with a refusal legible enough to act on: *no runner
"runners:pool:code" (none are installed) — a spawn naming a runner that is not installed is
refused rather than run somewhere else*.

**Then with a runner: the whole thing.** A scratch coderunner over a throwaway origin, a
`verse-merge` environment declaring `main`, and `pool:code` installed. A vote approved the
proposal, the task fast-forwarded the branch, called `push {destination: main}`, the runner staged
the work and released the credential, and **main moved**. Charter to remote, one path.

### `!when` was the wrong guess, and this is what it cost

This note's first question, answered by installing it: **there is no `!when` operator.** A patch
DEREFS and does not BRANCH, so what the store ended up holding was

```tony
state: !when
- true                 # $data.merged resolved — the deref inside it ran
- merged
- stale
```

— a tagged list, as data. The proposal sat in a state no rule selects and no reader can act on,
while `mergedSha` and `reason` beside it were correct. That is the worst shape a wrong spelling
can have: it parses, it installs, it round-trips through `verse trigger show`, the firing
succeeds, and the only thing wrong is the value in the store.

The fix is what this note already said and the charter did not do: record the answer as the FACT
it is (`merged: $data.merged`, `state: decided`) and branch in **two rules**, each selecting the
answer it is about. Neither re-fires, because writing the state takes the subject out of its own
selection. Driven both ways on a live verse: `decided + merged: true` → `merged`, `decided +
merged: false` → `stale`.

**Still unverified: the deadline.** A motion left standing should run its
`!until [!quorum 1, !deadline "24h"]` out and write `state: declined` through `onDenied`. It is
the cheapest test in this file and costs a day of waiting, which the verse it was standing on did
not have.

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

## The unhappy path is the ordinary path

Recorded here because it was got wrong first and the mistake is easy to repeat: main moves while
a question stands, so **"could not fast-forward" is the common outcome of this gate**, not its
error branch. The charter routed it through `onFailed`, which turns the feature working as
intended into an alarm on the record and loses the difference between "main moved" and "the
credential expired".

It is a semantic outcome, not an infrastructure failure — the task tried, correctly, and the
answer is no. So the task completes and says so, and the charter branches on the answer. The
runner draws that line for itself now: a remote's refusal comes back as the loop's `Blocked` and a
bad credential as an error, which is
[who enforces what](../reference/examples/README.md#who-enforces-what) landing in the one place it
has to. The mixed outcome nobody had settled — one repository pushed, the next refused — was
dissolved rather than managed: one push action is one repository, and an environment that could
make that ambiguous is refused when the config is read.

Still open in the charter itself: the `!when` spelling on `onDone` is unverified against the
grammar, and two rules conditioned on the answer would say the same thing with no new operator.

## Smaller things the charter works around

**The sweep cannot see the docket.** It must not delete a branch whose question is still standing
— today it would, and the operator answers into a void. It is *nearly* expressible, and closer
than this note first said: a gate carrying a formula becomes a **motion**, and a motion is an
entity. Checked by installing the charter and poking a proposal at it by hand:

```tony
ref: motion:channel:f846a045
payload:
  axis: whether          # status: open | closed
  gate: merge-to-main    # trigger: merge-proposal
  subject: merge:proposal:poke1
  deadline_at: "2026-08-15T16:02:26+02:00"
```

So the spelling is `!list-paths {system: motion, kind: channel, value: {status: open}, path:
subject}` — NOT `gate:question:*` with `status: pending`, which is what this note guessed and is
wrong in all three of system, kind and field. And it is better than guessed: the motion carries
its `subject`, so what the sweep needs to avoid is a field away rather than something it has to
join for.

Left out of the first version because it makes the prompt do bookkeeping the charter should do,
and because with level-triggered gates the ordering stops mattering — delete the branch, the
question goes.

**`verse gate approve <id>` does not answer one.** It 404s with *no such question*: `verse gate`
is the task-gate view, and a motion is answered by `verse vote <motion> approve --as <who>`. The
docket lists it either way, so the two are easy to confuse — and
[reference/tasks.md](../reference/tasks.md) describing `verse gate` as "engine + task gates,
unified" is at best true only of a gate with no formula.

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
