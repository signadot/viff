# A driver that proposes a list of calls: implementation plan

Status: plan, nothing built. The design is the section *"The mechanical tasks want a script, and
a script cannot push"* in [coding-runner-residual.md](coding-runner-residual.md) — why a shell is
the wrong shape, why a call list needs no new machinery, and why `scripted` is its degenerate
case. This file is the order of work.

The change in one line: **work with one right answer stops being an LLM, without stopping being
supervised.**

## Why this lands in steps

Every step below leaves a working tree and a runnable verse, and the first three are worth having
even if the fourth is never built: they take the LLM out of the merge task, which is four git
commands driven by a model today. The fourth needs something that does not exist yet and says so.

**And step 2 clobbers.** `scripted`'s behaviour changes for every charter that uses it, with no
opt-in and no "absent means the old thing" fallback. That is deliberate: the only users of this
repo are the people writing it, a compatibility path is a second thing to maintain bought for
nobody, and the charters that break are in the tree and get fixed in the same commit. The rule
holds until there is a consumer outside the team, which is the same trigger
[contributing.md](contributing.md) already names for versioning.

## Order of work

### 1. The driver, in `task/`, with a fixed list

A driver whose `Next` walks a list of `{tool, input, fatal}` entries and returns each as a
ToolUse. The list arrives as a tony document — in the prompt, so no charter grammar changes yet.

It lives in `task/` rather than `versed/` because both verse and the coding runner need it, and
the whole point is the runner's mechanical tasks. `scriptDriver` is in `versed/server.go` today
and could not be reused by a second program without moving.

One rule it does not get to choose: **an answer contract fails the spawn.** There is no model to
ask again, and a dropped contract is worse than none.

**What `fatal:` is for.** The rule `scripted` inherits — an `Err` ends the run, a `Blocked` does
not — is right for two different reasons that do not always agree, so the list states it per call
rather than the driver deciding for everyone. A refusal is a decision about ONE call: the sweep
deleting ten refs where the remote declines one should delete the other nine. A merge is the
opposite: if `git merge --ff-only` did not merge, the push that follows it must not happen. Both
are call lists; only the author knows which.

So each entry names the outcomes that end the list, out of three:

- `err` — the step failed. `StepResult.Err`.
- `blocked` — it was refused, by a gate or by the executor itself. `StepResult.Blocked`, built
  for push.go and this is its second caller.
- `nonzero` — a command ran and exited non-zero.

Absent means all three: a list is ordered because its calls depend on each other, so stopping is
the safe read. The sweep is the one known caller that wants otherwise and writes `fatal: [err]`.

`nonzero` is a member because **the two executors already disagree about it**, and this driver is
the first thing that would notice. `versed`'s `shellExec` folds a non-zero exit into `Err`
deliberately (*"a failure nothing reports is a failure that reads as success"*); the harness's
`fsExec` reports the exit code as data, equally deliberately, because the agent reads it and
decides. Neither is wrong for its own driver, and a call list must not have to know which one it
is running on — so it names the outcome it cares about and both executors keep their reporting.

**Proves:** an action list is supervised per action, and each call says what ends it. Tests: two
calls with a vector that ASKs on the second — the first executes, exactly one question is posed,
and denying it leaves the first done and the third unrun. Then two calls again with `fatal:`
varied — a first call that is blocked stops the second under the default and does not under
`fatal: [err]`. Plus the answer-contract refusal on its own.

### 2. `scripted` becomes the degenerate case, and its exemption goes

`NewScriptedTask` builds a one-element list rather than its own driver, and the hardcoded auto
vector goes away with it. A spawn's `vector:` governs, exactly as it does for `llm` — including
the case where a spawn names none.

**This breaks charters, which is the point.** `NewScriptedTask` writes `&task.Vector{Default:
Auto}` unconditionally today: it does not merely default to auto, it *ignores* `l.Vector`, so a
charter that already says `vector: ask` on a scripted spawn is being silently disobeyed. Fixing
that means every scripted spawn gets the vector it actually asked for, and the ones that asked
for nothing get the default rather than a blanket exemption — so charters that ran ungated start
posing questions. The repair in each is one line, `vector: auto`, which `NamedVector` has
understood since it was written; the charters needing it are in `docs/tutorial.md`,
`docs/trigger-model.md` and the runner examples, and they move in this commit.

The exemption survives in exactly one place: `verse task run --driver scripted`, where "you
authorized it by typing it" is true. That is not a compatibility shim — it is an argument about
who addressed the request, and it was always the only ground the exemption had. It stops
travelling to charters that never typed anything.

Tier: `contract`, with `Breaks:` naming scripted spawns that carry no vector, and a `Notice:`
saying what to add.

**Proves:** the equivalence, and that the exemption stopped travelling further than its argument.
Test: `verse task run --driver scripted "echo hi"` runs one ungated `run_bash` as it always did;
the same command as a charter spawn with no vector poses a question, and with `vector: auto` does
not.

### 3. The coderunner offers its extras, so a call list can push

`buildCommandTask` gets the finisher and not the runtime's extra tools, so `push` is offered to
the `llm` driver alone. Give a call-list task the same extras, and the merge task stops being an
LLM: `[{run_bash: git merge --ff-only …}, {push: {destination: main}}]` — the first entry taking
the default `fatal`, which is what makes the second one safe.

**Proves:** the thing this is all for. Test: a call list naming `push` resolves the destination,
derives the same effect the llm driver's tool does, and is gated the same way — the runner cannot
tell which driver proposed it, which is the property that makes this cheap.

Stop here if the rest is deferred. The merge is the expensive task and the one with a
consequence; the sweep is housekeeping.

### 4. The sweep, which needs something that does not exist

The sweep's problem is DISCOVERY, not deleting. Its prompt tells a task to `git fetch origin
--prune` and enumerate branches; on its first real run the task had neither the objects nor a
route, and spent eighteen boundaries hunting the filesystem for a credential that is not there.

The charter already knows which branches are stale — `merge:proposal` carries `base`, and main
moving is itself the staleness test — so the calls should come from the charter and the task
should do no git at all. That needs a fan-out this grammar may not have: *when
`git:head:verse` moves, act on every open proposal*. A rule's action patches its own subject, and
those proposals are not the subject of the head-moved edge. See
[merge-gate-residual.md](merge-gate-residual.md), where that ask is stated on its own terms.

**Proves:** nothing until the fan-out exists, which is why it is last and why steps 1–3 do not
wait for it.

## What this plan deliberately does not do

- **A `spawn.actions:` field.** The prompt already carries arbitrary text and a document parses
  out of it. A field reads better and is a contract change to `launch.Spec`; it is worth it only
  once something writes call lists routinely, which is exactly what steps 1–3 would tell us.
- **Give a scripted task the credential directly.** The residual argues it would be safe — the
  invariant is about the MODEL, and there is none — but it is a second way a secret reaches a
  task, and the argument rests on a property nothing enforces yet. Steps 1–3 need none of it: a
  call list asks over the channel the llm driver already uses.
- **Reconcile the two executors' reading of a non-zero exit.** Each is right for its own driver,
  and `fatal: [nonzero]` spans both. Making them agree would change what a model sees in a
  `run_bash` result, which is a much larger change than this one.
- **Anything about retries.** A fatal call ends the list. Whether the list should be resumable is
  a question about the loop, not about this driver.

## Risks worth watching

- **A call list is a plan made before the work starts.** The llm driver decides what to do next
  after seeing each result; this cannot. That is the right trade for four git commands and the
  wrong one for anything that branches — and the failure will look like a list whose later calls
  were pointless, not like an error.
- **The break in step 2 lands when the binary does**, not when the charters are edited. The
  charters needing `vector: auto` live in this repo and on `use/verse-dev`, and the second ships
  on its own clock. Sequence it: charters first, binary after.
- **`fatal:` is per call, so a list can now be wrong in a way `scripted` could not be.** A list
  whose every entry says `fatal: [err]` because that is what the sweep did will push after a
  merge that failed. The default is the protection, and it is only protection if nothing copies
  a sweep list as a template.
