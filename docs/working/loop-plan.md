# The loop as one machine: implementation plan

Status: plan, nothing built. The design is [the-llm-loop.md](the-llm-loop.md) — what runs today,
the contract, the precedence policy, and the argument that it must not hang. This file is the
order of work.

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

One comment-drift review on staging with a real fan-out, start to finish: boundary count against
the twenty-five that were mostly sleeping, delivery firing once after the children folded, and
the answer on the record being the one written last.

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
