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

## Order of work

### 1. The driver, in `task/`, with a fixed list

A driver whose `Next` walks a list of `{tool, input}` entries and returns each as a ToolUse. The
list arrives as a tony document — in the prompt, so no charter grammar changes yet.

It lives in `task/` rather than `versed/` because both verse and the coding runner need it, and
the whole point is the runner's mechanical tasks. `scriptDriver` is in `versed/server.go` today
and could not be reused by a second program without moving.

Two rules it does not get to choose, both inherited from `scripted`'s reasoning:

- **an answer contract fails the spawn** — there is no model to ask again, and a dropped contract
  is worse than none;
- **an `Err` ends the run, a `Blocked` does not.** Nobody is re-planning, so infrastructure
  failure stops the list; a refusal is a decision about ONE call — the remote declining one ref —
  and the rest still stand. This distinction is already built (push.go, StepResult.Blocked) and
  this is its second caller.

**Proves:** an action list is supervised per action. Test: two calls with a vector that ASKs on
the second — the first executes, exactly one question is posed, and denying it leaves the first
done and the third unrun. Plus the two rules above, each as its own test.

### 2. `scripted` becomes the degenerate case, additively

`NewScriptedTask` builds a one-element list rather than its own driver. The auto vector moves OUT
of the driver and onto the CLI path that justifies it (`verse task run --driver scripted`), where
"you authorized it by typing it" is true.

**Additively is the whole of the risk.** A charter with `driver: scripted` and no vector is
ungated today; it must stay ungated, or every such charter starts posing questions nobody
expects. So a call list with no vector runs auto, exactly as now — what changes is that a spawn
CAN say otherwise, which it could not before.

**Proves:** the equivalence, and that the exemption stopped travelling further than its argument.
Test: `scripted "echo hi"` runs one gated-by-nothing `run_bash` as it always did; the same spawn
carrying `vector: {gates: [{verbs: [exec], mode: ask}]}` now poses a question.

### 3. The coderunner offers its extras, so a call list can push

`buildCommandTask` gets the finisher and not the runtime's extra tools, so `push` is offered to
the `llm` driver alone. Give a call-list task the same extras, and the merge task stops being an
LLM: `[{run_bash: git merge --ff-only …}, {push: {destination: main}}]`.

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
- **Anything about retries.** A call that fails ends the list. Whether the list should be
  resumable is a question about the loop, not about this driver.

## Risks worth watching

- **Step 2 is where a charter's behaviour could move**, and the plan turns on it not doing so. If
  "no vector means auto" is got wrong in either direction, either every scripted charter starts
  asking questions or the vector on a call list silently means nothing.
- **A call list is a plan made before the work starts.** The llm driver decides what to do next
  after seeing each result; this cannot. That is the right trade for four git commands and the
  wrong one for anything that branches — and the failure will look like a list whose later calls
  were pointless, not like an error.
