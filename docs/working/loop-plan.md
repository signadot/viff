# The loop as one machine: implementation plan

Status: **steps 1–8 built and landed; step 9 (the staging drive) is not done.** The design is
[the-llm-loop.md](the-llm-loop.md) — what runs today, the contract, the precedence policy, and
the argument that it must not hang. This file is the order of work, and below it is what the
work found.

Part one of the design — "what runs today" — describes the loop as it was BEFORE this landed
and is now a record of the thing that was replaced, not of the code. It is left standing
because the second half argues against the first, and deleting the first would leave an
argument without its subject.

The change in one line: **the loop becomes an explicit machine over a stream of inputs, and the
two things that decide when a task is over stop being driver wrappers.**

## Why this lands as one change

Following signadot#7301's argument, which applies here unchanged: the components are interlocked
and the intermediate states are ones where the invariants do not hold.

- Moving the **contract check** without moving the **finisher** leaves a window where the answer
  is judged early and delivered late — today's bug with one leg shortened.
- Moving the **finisher** without the **child accounting** leaves a drain with no exit: the loop
  now knows it should not close while children are outstanding, and still has waits that never
  discharge.
- Adding **`recover()`** without the machine gives a panicking child an outcome that arrives at a
  loop which still cannot tell "waiting" from "finished", so it is folded into a driver that has
  already latched.
- Encoding **precedence** without the input stream means writing the policy against three
  arrival mechanisms (a mutex-guarded slice, a buffered channel, a registry callback) that
  cannot be ordered against each other.

So the steps below are the order of THINKING and of building in a branch. The diff lands once,
with the old paths deleted rather than left beside the new ones.

## Order of work

### 1. The contract, in code

`task/contract.go`: the five properties from the design as prose, each with the answer to "what
may a reader conclude, and what may they never" — and each as a predicate the tests can call.

1. A task reaches a terminal state at most once; nothing is emitted after it.
2. A task does not terminate while a child is outstanding.
3. Every child that starts ends exactly once, with an outcome.
4. The closing action is proposed at most once per quiescence, never while input is open.
5. Every wait names its discharge, and no discharge depends on a person choosing to look.

**Proves:** nothing mechanical. It is what every later step cites, and what a reviewer disagrees
with before any code is worth reading.

### 2. A child's completion becomes structural

`defer`/`recover` in the goroutine that runs a child, converting a panic into an outcome. One
test: a child whose driver panics still completes its parent, and the parent reports it as a
failed child rather than waiting.

**Proves:** `awaitChild`'s discharge is the Go runtime rather than the child behaving. This is
the `~2` fix, it is the smallest step here, and it is the one that makes property 5 true of the
wait that hangs today.

### 3. The machine, as data

`step(state, input) -> (state, []effect)`: no I/O, no goroutines, no clock.

- **state**: the input queue, children, frames, the answer on offer, the pending gate, and the
  "what next" as data rather than as a closure — a defunctionalized continuation, which is what
  makes step 6 possible at all.
- **inputs**: an operator INTERVENTION (cancel, pause, resume, say), a POLICY change
  (set-vector), work the task produced for itself (a child's result, an executor's result), a
  DECISION (a gate answer), and the driver's proposal.
- **effects**: ask the driver, execute an action, pose a gate, consult the finisher, check the
  answer, emit a frame, tear down.

The loop becomes a shell: read an input, call `step`, perform the effects.

**Proves:** the four readers of `!more` become one. "Is this task over" has a single answer,
computed where the whole pending list is visible.

### 4. The wrappers deleted

`WithFinisher` goes; the finisher is consulted by the machine when the input would otherwise go
quiet, and its closing action runs through the same authority → gate → execute path — keeping
every property the wrapper existed to get ("the vector gates it because it is an action") and
losing the latch, because being asked once is a consequence of the state rather than a flag.

The answer contract moves out of `llmDriver` and becomes a predicate the machine evaluates at
the same moment; a rejection re-enters as conversation through the mechanism steering already
uses.

**Proves:** delivery is per quiescence rather than once ever (a steer after a delivery gets a
second delivery of the new work); the contract judges the answer that stands rather than a draft
written while a child was still editing the tree.

### 5. Precedence, encoded

The decided policy, as arms of the switch rather than as statement order:

- an **intervention** (cancel, pause, resume, say) preempts, including a pending gate — which is
  abandoned, with `mirror.DropQuestion` telling the docket rather than leaving a question nobody
  will answer;
- a **policy change** (set-vector) does not preempt: it governs the next evaluation, so a
  pending question stands and is answered on its own terms;
- **work the task produced for itself** folds at the hinge and never overtakes an action already
  proposed.

**Proves:** the policy is readable in one place, and a reviewer can disagree with it by reading
one function instead of inferring it from the order of nine statements.

### 6. The enumeration

A test that generates every ordering of a bounded input multiset — two children, an
intervention, a policy change, a gate answer, an executor error, a rejected answer — drives
`step` through all of them, and asserts the contract's predicates at **every** transition, plus
that every run reaches a terminal state.

**Proves:** safety over the bounded set, and termination of the machine. Its comment must say
what it does NOT establish: that an input arrives (liveness lives in step 7), and anything that
lives in the shell rather than the function. Expect it to produce a counterexample against
something this plan currently asserts without evidence — that is the point of writing it rather
than reasoning.

### 7. The bounds the machine assumes

The shell must make the machine's assumptions true, and the design's table says which are open:

- **`Executor.Execute` must honour its context.** Stated in the interface's own doc as an
  obligation, with a test that a context-ignoring executor is a defect of that executor. The
  loop cannot interrupt a `run_bash` that ignores cancellation, and must say so rather than
  appear to.
- **A gate's wait needs a discharge that is not a person choosing to look** — a deadline, or the
  intervention rule. A binding with neither is a wait that fails property 5, and the install is
  where that should be refused or warned.
- **A pause remains a hang by request.** The exception, stated as one.

**Proves:** that "cannot hang" is a property of the code rather than of luck.

### 8. Cut over

One commit: the machine, the shell, the deletions. `Tier: change` unless the enumeration turns
up a behaviour a charter could depend on, in which case `contract` and a notice.

### 9. Drive it

**Not done.** One comment-drift review on staging with a real fan-out, start to finish: boundary
count against the twenty-five that were mostly sleeping, delivery firing once after the children
folded, and the answer on the record being the one written last.

What stands in for it so far is the LOCAL end-to-end smoke (`./smoke.sh`, 44 checks), which
exercises the surfaces this change rewrote — `task run`, `task say`, `task vector`, `task
cancel`, gate ask/answer, and the connect/validate flow that spawns tasks per file — against a
real server with a real store. That establishes the stack agrees with itself; it does not
establish anything about a model in the loop, a real fan-out, or the boundary count, which is
the whole point of the drive and is still owed.

## What building it found

Four things the plan did not know, recorded here rather than only in the code, because three of
them are corrections to what this file asserted.

**A reply needs an identity, not just a phase.** The machine waits on one blocking effect at a
time, so "which wait am I in" looked like a single field. It is not: a WITHDRAWN gate's call is
still running and still going to answer, and by the time it does the machine can be at a gate
again about a different action. Matching the reply on phase alone folded that stale answer as
this action's — an operator's say turned into a cancelled task, reproducibly. Every blocking
effect now carries a token and only its own reply counts (`state.tok`). The enumeration models
the abandoned call answering anyway and asserts it changes nothing, which is what makes this a
property rather than a fix.

**Resume does not withdraw a standing question, and the design says it should.** Step 5's
policy lists all four interventions as preempting a gate. Three of them change what the task
does next; a resume, in the only situation where it can meet a standing question, does not — a
question can only be pending if the task was NOT held, so there is no hold to lift. Withdrawing
one would destroy a question for nothing, which an operator can do by double-clicking resume.
The implemented rule is the design's own justification rather than its enumeration: *an
intervention withdraws a standing question when it changes what the task does next.*
[the-llm-loop.md](the-llm-loop.md#precedence-decided) is wrong on that line and should be
edited to match, or this should be changed back.

**A gate's `Steer` must not reach the agent when the action is the CLOSING one.** A resolver
refusing a delivery with a note ("not this branch") had that note folded into the agent's
conversation — about an action the agent was never told about. It reopened a run that had
nothing left to do, and for the coderunner's command driver, which cannot resume, it FAILED the
task. Caught by that runner's existing test. Such a note now goes on the read plane, attributed,
where the person who wrote it is talking to the people watching.

**A panicking seam is a wait that can never be discharged, and it took three recovers rather
than one.** Step 2 named the child. Running the seams off the Run goroutine — which is what lets
an intervention preempt a gate — made the same hole at every other seam, since the machine is
parked on a reply only that call can produce. And both of those leave the INLINE path: the answer
contract, the finisher, the spawn factory, `emit`, and the executor calls that unwind frames at
teardown all run on the Run goroutine itself. For a child, its parent catches that. For a ROOT
there was nobody, and the failure mode was worse than a crash and quieter — no terminal frame at
all, so every subscriber stays open on a task that has stopped speaking and the durable entity
says `running` forever. So: `seam` guards the seams, `runChild` guards a child, and `Run` guards
itself, ending as `failed` with a terminal on the plane. fail.go already made this argument for a
spawn that cannot be honoured ("rather than a nil that vanishes or a panic that stops the loop");
this is the same argument reaching the case it named.

**A boundary costs about eight times what it did, and that is left alone.** ~6.4µs against
~0.8µs (`BenchmarkBoundary`), and a CPU profile says it is goroutine scheduling rather than
`step`: the shell hands blocking seams off to goroutines, which is what lets an intervention
preempt a standing question. Getting it back means running the driver and executor inline again,
which reintroduces the ordering this change exists to fix — an operator's say arriving while an
action runs must be folded BEFORE the reply that ends the turn is acted on, or a run that has
just been given new work delivers without it. Against a boundary whose cheapest realistic
content is a millisecond of executor, microseconds of handoff are not worth that. The benchmark
is there to be watched, not beaten.

**The enumeration was thinner than the words around it.** It modelled ONE failure — an executor
error — out of six seams that can produce one, while step 6 and the commit that landed it both
said "every ordering of a bounded input set". The omission that mattered was `inDriver{err}`:
that is what the `fail` tool IS, so the single most likely way a real run ends badly was not in
the set. It now injects a fault at every seam, at most one per path, which is the depth that
catches a failure arriving while something else is pending. Mutating the driver-error branch to
tear down without joining its children is caught at exactly that point and was unreachable
before.

Worth separating from the finding that prompted it. A staging drive ended when the model called
`fail` with "placeholder - not actually failing, retracting", discarding a review whose children
had all reported clean. Wider enumeration would NOT have caught that: the machine is correct on
that path — it joined all thirteen children, tore down, and recorded the reason. The question
`fail` raises is whether a single token should be able to end a finished run, which is a claim
about what the contract SHOULD say, and enumeration only checks code against a contract already
written. What found it was the drive, which is the argument for making drives cheap.

Two things the plan predicted and got right: the enumeration is cheap enough to run every time
(~3,700 transitions over ~700 paths, milliseconds), and it does catch what it claims — removing
the token guard, the child-outstanding check, or the quiescence reset each fails it.

## The drive, and what it cost to learn anything

Step 9 ran on staging and produced two findings, neither of them a loop defect. It also produced
the more useful result, which is about the process rather than the code.

**Finding 1: the "end your turn" advice did not take, and the instrument changed.** A review
narrated "Still waiting for server_go_reviewer and test_go_reviewer" at every boundary — exactly
what the advice asks for — and attached a tool call anyway, spawning `noop`, `noop2` … `noop10`
and a dozen `run_bash echo done` across twenty-nine boundaries. A turn carries content AND tool
calls, so "say it and stop" asks for a restraint a model has no way to commit to. Waiting was
the one thing it wanted with no verb for it, so it kept inventing one — first `sleep 120`, then
a sub-agent called `noop`. Fixed by giving it the verb: `wait` ends the turn, costs one model
call, and the loop then blocks in Go until a child reports.

**Finding 2: `fail` was used as a scratchpad.** The run ended on
`err: "placeholder - not actually failing, retracting"`, discarding a review whose children had
both reported clean. Same shape as Finding 1, and worth stating as the rule it implies: **a
failure result should not be the nearest thing to hand when a model wants to jot, retract or
experiment.** No fix here yet, deliberately — see below.

**And the process finding, which is the one that matters.** Fifteen minutes per iteration with a
shared outage (`replicas=1, strategy=Recreate`) is a feedback loop that selects what gets
validated: `229b9ccd`'s advice was written, shipped and never once watched, because watching it
cost fifteen minutes. So the drive moved local — a real model, a real fan-out, three files in a
temp directory, no repository, runner or cluster (`task/harness/drive_test.go`, ~20s a run).

That immediately paid for itself by falsifying the fix's own evidence. The first local drive ran
only the FIXED code, passed, and proved nothing: run against the previous code it passed
identically. Six shapes were then tried to make the polling reproduce — fast children, slow
children, a parent told to survey first so it arrives at the wait several tool calls deep, an
answer contract so "end your turn" collides with "your final message IS the result", children
forced to take forty seconds — and in every one the model fanned out, ended its turn and waited
correctly.

So the honest state of Finding 1's fix: the mechanism is proven, the model uses the verb when it
has one, and giving it a verb removes a class that advice asking for restraint cannot. What is
NOT established is that the class is common — one drive saw it, and six local attempts could not
make it happen again. That is a much weaker claim than "fixed", and it is the claim.

It is also why Finding 2 has no fix in this pass. The lesson of six failed reproductions is that
hypotheses about model behaviour are cheap and usually wrong, and the last two attempts at this
exact problem were both shipped on reasoning alone. A `note` or `scratch` verb is the obvious
move by analogy with `wait`, and analogy is what has been failing. It wants a reproducer first.

## Verification, and what each part cannot show

- **The enumeration** — safety and termination of the machine over a bounded input set. Not
  liveness, not the shell.
- **Fault injection**, because a generator does not know how things break: panic a child; cancel
  during a fold; steer while a gate is pending; refuse a delivery; return an executor error
  after the model has stopped.
- **The staging drive** — that the whole stack agrees, once, on a real repository.
- **None of it** shows the contract in step 1 is the right one. Only use does, which is why it is
  written where an operator can disagree with it.

## Not in this plan

- **What a charter sees.** Statuses, `task:instance`, the completion idiom
  (`when: {system: task, kind: instance, value: {status: done}}`) are untouched.
- **Rewriting the drivers.** `llmDriver` loses the contract check and keeps everything else.
- **A `run_bash` timeout.** The loop states the obligation; whether the coderunner's executor
  enforces one is that runner's call, and a separate change.
- **Gate withdrawal semantics.** Telling a resolver its question was abandoned rather than
  letting it watch the record vanish belongs to the gate extension, and this plan only has to
  not make it harder.
