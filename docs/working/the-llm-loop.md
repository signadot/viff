# The llm loop

Status: design. Two halves — what runs today, verified against the code, and the shape it
should have. The plan that used to sit beside this is deleted: it proposed a `FINISHING` state,
and a state an external event can un-enter is a moment wearing a costume.

Written because the rules turned out to be implicit. The loop is described everywhere in terms
like "when there is nothing else to do", and nothing says what *else* there could be. The first
half is that list. The second is what to do about it.

---

# Part one: what runs today

## What it is

**One goroutine that turns a driver's proposals into effects, one at a time, pausing at a
reified boundary between them.**

A `Driver` says what to do next (an LLM, or a script); an `Executor` does it; between any two
actions there is a BOUNDARY, and everything that is not "doing the action" happens there —
gates, steering, children folding in, cancellation, pausing, unwinding. The boundary is the
loop's only hinge, and it is the reason the design is worth having: a control plane that acts at
a known point rather than racing an agent mid-thought.

What it is NOT: a scheduler (one task, one goroutine, no concurrency inside it), a workflow
engine (no persistence, no resume of its own — a restart loses it), or an agent framework (it
knows nothing about models; the LLM lives behind `Driver`).

## The seams

| seam | who supplies it | what it answers |
|---|---|---|
| `Driver.Next(prev) (ToolUse, more, err)` | harness (`llmDriver`), tests (`scriptDriver`) | what to do next, or that there is nothing |
| `Executor.Execute(tu) (StepResult, defers, err)` | `fsExec`, the coderunner's confined variant | the effect, plus resources to unwind later |
| `Supervisor` = `Vector` + `Registry` | the spawn (`vector:`) and versed | may this action proceed, and who is asked |
| `SpawnFunc` | harness | how to build a CHILD task for `call_agent` |
| `Finisher` | the runner (coderunner: `deliver`) | a closing action composed onto the end |
| `Resulter` (optional) | `llmDriver` | the task's final answer |
| `Steerable` (optional) | `llmDriver` | fold an operator's instruction into the conversation |
| `Grant` (authority) | the spawn | the hard envelope: what may be attempted at all |
| `ContentReader` (optional) | versed, for a scope | read files through the verse rather than the disk |
| the read plane (`emit`/`Subscribe`) | the loop itself | what observers see, cursored and replayable |

Two of these are not seams and pretend to be. The ANSWER CONTRACT lives inside `llmDriver`,
checked when the model stops talking. The FINISHER wraps the driver rather than being consulted
by the loop. Both decide when the task is over, from outside the loop's view — see part two.

## One boundary, in order

The de-facto specification, which exists only as the order of statements in `Run`. It has two
phases, and only the second is numbered in the code — which is itself part of the problem, since
the unnumbered half is where everything the loop OWNS happens:

**The hinge** (unnumbered in the source):

- `seq++`, emitted as `EvBoundary`. Observers count these.
- **cancellation** — `ctx.Err()`; a cancelled subtree exits here, before anything else.
- **pause** — `pauseGate.hold(ctx)`; cancel outranks a pause.
- **fold conversation** — `foldSteer()` then `foldCompletions()`: latched operator instructions
  and finished children, both delivered into the driver's conversation (see *what a steer is*).

**The action** (the source's own numbering):

1. **ask the driver** — `Next(prev)`: an action, or nothing.
2. **authority** — does the task's `Grant` admit it? A refusal is not a question.
3. **gate** — derive → evaluate the `Vector` → resolve (ask a resolver on ASK).
4. **execute** — the executor runs it; on success its `defers` register on the current frame.

And when 1 says *nothing*: children outstanding → `awaitChild` blocks, folds a completion, and
returns to the hinge; no children → `teardown(Done)`, unwinding frames bottom-up.

Note that a completion is absorbed in TWO places — the hinge's fold and `awaitChild` — and they
are the same function (`absorbCompletion`). Nothing else about the boundary is reachable from
the second one, which is why "it folds at the boundary" is true and misleading.

## Everything that could be "to do"

Any of these may be pending at a boundary, and several may be pending at once:

| pending | arrives from | consumed at | owned by |
|---|---|---|---|
| cancellation | `Cancel()`, parent's ctx | the hinge, first | ctx |
| pause | `Pause()` | the hinge | `pauseGate` |
| an operator `say` | `Handle.Steer` | the hinge's fold | `p.steer` under `p.mu` |
| a child completion | the child's goroutine | the hinge's fold, OR `awaitChild` | `p.completions` (buffered 64) |
| an outstanding child | `call_agent` earlier | the `!more` branch | `p.children` |
| the driver's next action | the model | action 1 | driver-internal |
| a gate awaiting an answer | action 3 | action 3, blocking | `Registry` |
| registered defers | a previous execute | teardown, bottom-up | `p.frames` |
| an answer that fails its contract | the driver, when the model stops | **inside** `Next` | `llmDriver` |
| the closing action | the finisher, when the driver stops | **inside** `Next` | `finishDriver` |
| a fail-fast sibling cancel | a child erroring | inside `absorbCompletion`, either caller | `p.failing` |

The last two are the ones that hurt: pending work of the loop that the loop cannot see, because
both are implemented by wrapping the driver. The loop asks "what next?" and gets back a verdict
on whether the task is over.

## What a steer is

The word is overloaded, and untangling it is prior to everything in part two.

**A steer is an input from the OPERATOR.** That is the whole definition, and it deliberately
spans two kinds of effect:

- **say** — becomes a message the driver reads at the next hinge (`Handle.Steer`, folded by
  `foldSteer`). The agent sees it.
- **control** — `Cancel`, `Pause`, `Resume`, `SetVector`. The agent is not told; it experiences
  them as the world behaving differently.

Both are steering because both are a person redirecting a run in flight. Splitting them by
mechanism — "conversation" versus "control" — describes the plumbing and hides the thing that
matters, which is that a person is talking.

They do NOT share one precedence, and the line that divides them is not the mechanism either
(see part two): `Cancel`, `Pause`, `Resume` and a `say` are interventions in THIS RUN, while
`SetVector` is a change of POLICY. The first four interrupt what the task is doing; the last
governs what it is asked next.

Two things that are NOT steers, and are confusable with them:

- **A child's result.** It travels on the same internal mechanism today —
  `absorbCompletion` calls `Steerable.Steer` with the child's summary — but nobody sent it. It
  is the task's own work returning. That shared mechanism is a naming defect in the code, not a
  shared meaning.
- **A gate resolution.** It answers something the loop already asked, arriving through the
  `Registry` rather than the `Handle`. An answer is not a redirection.

So `Steer` currently names two things one level apart: `Handle.Steer(instruction, by)` is an
operator saying something, and `Steerable.Steer([]string)` is the loop delivering *any* message
into the conversation. Part two keeps the second as a mechanism and stops calling it steering.

*Done: the second is now `task.Conversation.Fold(messages)` — a driver that HOLDS a conversation,
into which the loop folds a message. `Handle.Steer` is unchanged, because an operator really is
steering.*

## What a feeder can expect

- A spawn returns a handle immediately; the task runs concurrently.
- The read plane is cursored and replayable (`?since=`); a reader that resumes cannot tell.
- `Result()` is the driver's last message — what a parent folds in from a child, and what a
  charter derefs as `$result`/`$data`.
- A terminal status is one of `done | failed | cancelled | orphaned`, and the last frame is
  always the terminal.
- A gate stops the task at a boundary, not mid-action, and is answerable by id from outside.
- Cancel is a subtree operation: top-down signal, bottom-up unwind, "cancelled" at the next
  boundary.
- A child's result arrives as a tool result in the parent's conversation; a child inherits the
  parent's tools, frame and confinement, but not its answer contract.

## What is not specified

1. **What "finished" means.** `!more` is read as "the task is over" by four things: the contract
   check, the finisher, the executor that runs the closing action, and the loop's join. With
   children outstanding it means the opposite — *I am waiting* — and only the join knows. Hence:
   an agent that sleeps rather than ending its turn, a delivery that fires before the children
   fold, an answer validated while a child is still writing files, and a `posted` latch that
   then discards what the children said.
2. **Whether an answer is final.** Checked at the first `!more`; nothing rechecks if a child
   later changes the tree.
3. **What happens when several things are pending.** Steer during a gate; cancel during a fold;
   a child completing while the driver is mid-`Next`. Statement order decides; no rule says so.
4. **What a child that never reports means.** A child is an in-process goroutine the parent owns
   and its completion is sent unconditionally after `Run` returns — but there is no `recover()`
   in `task/`, so a panicking child sends nothing and the parent waits forever. Observed.
5. **Which waits are bounded.** `awaitChild` blocks on a channel with cancellation and no
   timeout: correct if completions are guaranteed, unbounded if they are not (see 4).
6. **What the finisher may assume.** It sees `!more` and nothing else.
7. **Whether a task may end with a gate unanswered**, and what the resolver is told.
8. **That it terminates.** Nothing states it, and nothing argues it.

## Where it can block today

The loop is one goroutine, so every hang is one of these, and the question for each is what
discharges it and who guarantees that arrives:

| blocks in | until | guaranteed by | if it never comes |
|---|---|---|---|
| `awaitChild` | a child completes, a steer arrives, ctx is done | the child's goroutine sends **unconditionally after `Run` returns** | **hangs.** No `recover()` in `task/`: a panicking child sends nothing. Observed as `~2` |
| `Registry.Resolve` | the question is answered, or ctx is done | a resolver, or a person reading the docket | **hangs**, unless the binding carries a deadline (`escalate`) or somebody cancels |
| `pauseGate.hold` | resume, or ctx is done | an operator | **hangs by request** — a pause is a hang somebody asked for |
| `Driver.Next` | the model answers | the HTTP client's own timeout | a driver with no timeout hangs the task, and the loop cannot see inside |
| `Executor.Execute` | the action returns | nothing — `run_bash` has no timeout | **hangs.** `sleep 120` is a task that cannot be steered for two minutes |
| a child's send on `p.completions` | the parent reads (buffer 64) | the parent's hinge | 65+ simultaneous completions with a parent not reading |

Two things are NOT on this list and are worth saying so: the read plane never blocks the loop —
`emit` drops for a slow subscriber and counts the gap (`s.dropped++`) — and `ctx.Done()` is
checked first at every hinge, so cancellation discharges everything above it except a wait
INSIDE a seam that ignores its context.

---

# Part two: the shape it should have

## Inputs in, results out

The framing already exists in this codebase, in the resolver design
([attached-resolvers.md](../attached-resolvers.md), "Two planes, one spine"): a resolver above a
task *"reads two streams with independent cursors and no common order"*, and the reference
driver is *"the one where verse runs nothing — it hands over two streams and applies what comes
back"*.

**The loop is that too: it consumes a stream of INPUTS and produces a stream of RESULTS.**

An INPUT is anything that can make the task do more: the initial prompt, conversation (an
operator's say, a child's result, a contract repair), control (cancel, pause, resume, vector),
a decision (a gate answer). A RESULT is anything it produces: narration, actions, answers, the
delivery. Everything in the "what could be to do" table is one of the two.

Called input rather than *ask* on purpose: `ask` is taken — it is a gate MODE (`mode: ask`) and
the verb the resolver documentation uses for posing a question, and a reader moving between the
two documents should not have to hold two meanings of it.

What follows from it, and each of these is a defect fixed by construction rather than by a rule:

- **Terminal is a consequence, not a decision.** The task ends when the input is closed and
  drained. Nothing declares it over, so there is nothing for four mechanisms to disagree about.
  `FINISHING` is not a state, because a `say` arriving would un-enter it — a state you can be
  removed from by an outside event is a moment wearing a costume.
- **An answer is a value on the output stream**, not a terminal event. So the contract is a
  PREDICATE on a value, asked when the stream would otherwise go quiet, and a rejection is just
  another ask arriving. It stops being a verdict about a moment.
- **Parent and child are the same thing.** A parent consumes a child's output stream as inputs.
  Today this is faked by folding `Result()` in as a tool result, which is why a child that
  produces nothing produces *nothing* rather than a closed stream — the hole `~2` fell through.
- **Delivery is per quiescence, not once ever.** A steer after a delivery reopens the input, so
  more results follow, so the finisher is consulted again — and delivers the new work. `posted`
  makes that impossible today, which is a bug rather than a policy.

## No wrappers

`finishDriver` exists only to inject an action without the loop's cooperation, and the goal it
was reaching for is right and survives: the closing act should be an ACTION, so the vector gates
it, the read plane narrates it, and it lands before the terminal — *"a hook is a fourth thing to
supervise, and an action is already supervised"*.

That goal does not require impersonating the driver. The loop can ask the finisher for a closing
action at the point where the stream would go quiet, then run it through the same authority →
gate → execute path. Same four properties, no latch: being asked once is a consequence of the
state rather than a flag.

The contract goes the same way. It lives in the driver because a rejection must re-enter the
conversation — but the loop already has that mechanism and uses it for humans (`Steerable`,
folded at the boundary). A repair is a steer the runtime writes.

The general rule worth stating, since it is what went wrong: **a wrapper inverts the direction
of knowledge.** The loop holds the whole pending list; a wrapper sees one boolean. Any decision
needing the list must be made by the loop, or the wrapper will keep private state to compensate
— and that private state is the bug.

## State as data

The transition should read as `state → (state, effects)`, with the state a small struct and the
effects the only way the world is touched. Two properties are bought by that and by nothing
else: every interleaving of a bounded event set can be ENUMERATED by a test rather than sampled
by a drive, and the invariants can be asserted over a value.

Continuation-passing is the tempting alternative, and it reads better in the small — what
happens next is written where the decision is. The catch is that plain CPS puts the state in the
closures, which is the same disease being removed from the wrappers: `posted` is a latch hidden
in a driver, and a captured variable is a latch hidden in a lambda. The version that keeps both
is CPS with DEFUNCTIONALIZED continuations — the "what next" carried as data on the state — and
that is the same object as the struct, arrived at from the other side.

## What the streams framing does not settle

It says what flows, not who goes first. A task's boundary IS a sequencer — one goroutine merging
its inputs — so the merge policy has to be stated, and it is a policy call rather than a
derivation:

1. **Precedence at the boundary.** Cancel over pause is settled. Steer versus a pending gate,
   a child completion versus a driver proposal, a contract repair versus a steer: unstated.
2. **Whether an input may arrive while an action is in flight**, or only at a boundary. Today:
   only at a boundary, and that is worth keeping deliberately rather than by accident.
3. **What closes the input.** The driver having nothing to say does not close it; a cancel does.
   Whether an unanswered gate holds it open is question 7 above, restated.
4. **Whether a child's stream closing is an input** (the parent gets "child ended") or merely the
   absence of one. It must be the former, or `~2` recurs by another name.

## Precedence, decided

The streams framing says what flows, not who goes first. A task's boundary IS a sequencer — one
goroutine merging its inputs — so the merge policy is a choice, and these are the choices:

**An intervention outranks everything; a policy change waits its turn.** The operator's inputs
split on that line, and only the first half preempts:

- **cancel, pause, resume and say interrupt a pending gate.** *Corrected in building it: RESUME
  does not, and the code is the corrected version. A question can only be pending if the task
  was not held, so a resume that meets one has no hold to lift and redirects nothing —
  withdrawing it would destroy a question for nothing, which an operator can do by clicking
  resume twice. The rule the code implements is this bullet's own justification rather than its
  list: an intervention withdraws a standing question when it changes what the task does next.
  See [loop-plan.md](loop-plan.md#what-building-it-found) and `machine.go`'s `inResume`.*
  The question is abandoned. It can
  therefore DISAPPEAR without being answered, and whoever was asked learns that only by watching
  it go — accepted deliberately for now, with gates to be extended later so the transition is
  smoother than a vanishing. The justification is that all four are about this run: a cancel
  ends it, a pause suspends it, a say may redirect it into work the question no longer bears on.
  Leaving somebody staring at a question about an action the task will now never take is worse
  than withdrawing it.

- **`SetVector` does NOT interrupt a pending gate.** It is prospective: sliding a running task
  to AUTO says *stop asking me about actions like this*, not *approve the one already in front
  of me*. Dissolving the pending question would turn a policy change into an approval of a
  specific action nobody looked at — the operator never saw what was being asked, and an
  envelope that widens is not consent to what was already refused entry. So the pending question
  stands and is answered on its own terms; the new vector governs the next evaluation.

  This is also today's behaviour, and the one place where the current loop is already right:
  the gate is posed and blocked on the registry, and a new vector applies only to actions not
  yet evaluated.
- **steer over a contract repair.** A run that has been told something new is not a run that
  should be arguing with its own answer. The repair is dropped; the driver will produce a new
  answer, and it is checked when the stream next goes quiet.

**A child's result is not an operator input and does not preempt.** It folds at the hinge, like
any other work the task produced for itself. A completion arriving while an action is already
proposed does not overtake it: the action runs, and the completion is there at the next hinge.
This is today's behaviour and it is kept on purpose — preemption is for people.

What that leaves as two sentences: **at a hinge, an operator's intervention first, then
everything the task produced for itself, then ask the driver.** A policy change is not an
intervention — it takes effect where policy is read, which is the next evaluation of the next
action.

The residue, worth writing down rather than discovering: abandoning a gate leaves a resolver
holding a question that will never be answered, and `mirror.DropQuestion` is the existing seam
for saying so. Whether the resolver is told "withdrawn" or merely watches the record vanish is
part of the gate extension, not of this loop.

*Settled while building it, because withdrawal stopped being hypothetical.* The record does not
vanish — `DropQuestion` settles it to `orphaned` with a timestamp, and that already worked. What
was missing is WHY: `orphaned` was documented as "the asker is gone", which is now false in the
common case, where the asker is alive and an operator redirected it. The waiter's context CAUSE
carries the reason from the loop through `Registry.OnDrop` to the record's `why`, so a docket
reader can tell "the task died" from "you were overruled". A distinct `withdrawn` STATUS is the
alternative and is deliberately not taken: a status value is something a charter can match on,
so it is a wire change, and nothing yet needs to filter on the distinction — only to read it.

## It must not hang, and why it cannot

The properties, stated as requirements rather than hopes. The first four are safety (nothing bad
happens), the last is liveness (something good keeps happening), and they are checked
differently: safety by enumerating interleavings of the pure transition, liveness by an argument
about who discharges each wait, because no enumeration of a bounded event set can show that an
event ARRIVES.

**Safety**

1. A task reaches a terminal state at most once, and nothing is emitted after it.
2. A task does not reach a terminal state while a child is outstanding.
3. Every child that starts ends exactly once, with an outcome.
4. The closing action is proposed at most once per quiescence, and never while the input is
   open.

**Liveness — the requirement**

5. A task always makes progress or terminates. Equivalently: **every wait names its discharge,
   and every discharge is guaranteed by something that cannot itself wait forever.**

That is the rule the current loop cannot satisfy, and the table above says exactly why: three of
its six waits are discharged by "something that should arrive", with no answer for what happens
when it does not. The design must close each one, and the closures are different in kind:

- **A child's completion becomes structural.** A child is an in-process goroutine the parent
  owns, so its completion is not a hope — it is a `defer` away. `recover()` turns a panic into
  an outcome, and then wait 5 is discharged by the Go runtime rather than by the child behaving.
  This is the one that makes "cannot hang" a statement about the code rather than about luck.
- **A gate's wait is discharged by policy, not by patience.** The precedence rule already gives
  one exit — a steer preempts it — and a deadline gives another. What must not remain is a wait
  whose only discharge is a person choosing to look.
- **An executor's action is the seam's problem, and the loop must say so.** `run_bash` with no
  timeout is a hang the loop cannot see or interrupt. The loop's contract with an `Executor`
  should state that `Execute` must honour its context, and the loop should treat one that does
  not as a defect of that executor rather than as its own fate.
- **A pause is a hang by request** and stays one. It is the exception that proves the rule: the
  discharge is an operator, and the operator asked for it.

**What the argument is worth.** It establishes that no wait is unbounded BY DESIGN. It does not
establish that the process makes progress — a driver that proposes an action per boundary
forever is a live loop doing nothing useful, and that is a cost question (tokens, wall clock)
rather than a correctness one. Nor does it cover a seam that lies about honouring its context.
Those are the two things to keep saying out loud, because an argument that claims more than it
shows is how the last one got confirmed and was wrong.
