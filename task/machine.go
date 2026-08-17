package task

import (
	"fmt"
	"slices"
	"strings"
)

// THE LOOP AS ONE MACHINE.
//
// `step(state, input) -> (state, []effect)`: no I/O, no goroutines, no clock. The loop in
// loop.go is a SHELL around it — read an input, call step, perform the effects — and every
// decision about what the task does next is made here, over a value, where the whole pending
// list is visible.
//
// That is the point, and it is what the wrappers this replaced could not do. `finishDriver`
// and the driver-internal answer check both decided when a task was over from a position that
// could see one boolean: `!more`. With children outstanding that boolean means the OPPOSITE —
// I am waiting — and only the loop knew. A wrapper needing the whole list keeps private state
// to compensate, and that private state is the bug (`posted`, which made a second delivery
// impossible, and a contract checked against a draft written while a child was still editing
// the tree).
//
// Terminal is therefore a CONSEQUENCE here rather than a decision: the task ends when the
// input has closed and drained. Nothing declares it over, so there is nothing for four
// mechanisms to disagree about.

// phase is the machine's continuation, defunctionalized — "what happens next" carried as data
// on the state rather than in a closure. Continuation-passing reads better in the small, and
// it puts the state in the closures, which is the same disease being removed from the
// wrappers: a captured variable is a latch hidden in a lambda. This is the version that keeps
// the readability and lets step 6's enumeration exist at all, since a value can be enumerated
// over and a closure cannot.
type phase uint8

const (
	// phHinge: at rest at a boundary-to-be. Nothing is outstanding; an arrival advances it.
	// Reached when the driver has gone quiet and a child is still running — the one wait
	// that is not a reply to something the machine asked for.
	phHinge  phase = iota
	phHeld         // the pause hold is outstanding
	phAsk          // the driver's proposal is outstanding
	phGate         // a gate question is outstanding (the one preemptible wait)
	phExec         // an action is outstanding
	phSpawn        // a child's start is outstanding
	phCheck        // the answer contract's verdict is outstanding
	phFinish       // the finisher's proposal is outstanding
	phExit         // winding down: children are cancelled and being joined
	phEnd          // terminal; nothing follows
)

func (p phase) String() string {
	switch p {
	case phHinge:
		return "hinge"
	case phHeld:
		return "held"
	case phAsk:
		return "ask"
	case phGate:
		return "gate"
	case phExec:
		return "exec"
	case phSpawn:
		return "spawn"
	case phCheck:
		return "check"
	case phFinish:
		return "finish"
	case phExit:
		return "exit"
	case phEnd:
		return "end"
	}
	return "?"
}

// convKind distinguishes who is speaking into the driver's conversation. The MECHANISM is one
// — a message folded in at the boundary — and that is deliberate: the loop had two (a
// mutex-guarded slice for operators, a channel for children) and no way to order them against
// each other. What must stay distinguishable is the SPEAKER, because a person saying something
// and the runtime handing back a diagnosis are different events on the read plane.
type convKind uint8

const (
	convSay    convKind = iota // an operator said something (Handle.Steer)
	convChild                  // a child of this task reported its result
	convRepair                 // the runtime is asking for the answer to be repaired
	convGate                   // a resolver attached context to its answer
)

// convMsg is one message on its way into the conversation, with its speaker.
type convMsg struct {
	text string
	by   string
	kind convKind
}

// state is the whole of what the loop knows. Every field is data — no closures, no channels,
// no handles — which is what lets the enumeration assert the contract over it.
type state struct {
	ph  phase
	seq int // boundary cursor; only ever advances
	// tok identifies the reply the machine is waiting for. Phase alone is not enough: a
	// withdrawn gate's goroutine is still running and still going to answer, and by the time
	// it does the machine can be at a gate again — about a DIFFERENT action. Matching on
	// phase folded that stale answer as this action's, which turned an operator's say into a
	// cancelled task. So every blocking effect carries a token and only its own reply counts.
	tok int

	// The input side.
	conv        []convMsg // conversation waiting to be folded in at the next boundary
	children    []string  // outstanding children, by id
	driverQuiet bool      // the driver has said it has nothing more to propose

	// The output side.
	prev   StepResult // what the last action produced, fed to the driver
	answer string     // the answer on offer — the driver's last message, as of its last reply

	// What is in flight.
	action  ToolUse // the action a pending gate is about, or the one executing
	closing bool    // that action is the finisher's, not the driver's
	// regate: a PAUSE withdrew the question about `action`, and the same one is posed again
	// when the hold lifts. A pause is not an answer — see inPause — so the alternative was
	// telling the driver its action had been refused, which ends any driver that cannot
	// re-plan.
	regate bool

	// What this quiescence has already done. Both reset when the input reopens, which is what
	// makes delivery per-quiescence rather than once-ever.
	answerOK bool
	finished bool

	// Winding down.
	cancelling bool   // an intervention or a failure is ending this run
	exitStatus Status // what it will end as
	cause      error  // why (first cause wins — a later unwind must not overwrite the reason)
	joining    bool   // the children have been told to stop

	// Frames and the id counter, which the shell reads back out at teardown.
	frames  []*frame
	failing bool // fail-fast has already fired

	// Configuration the machine consults. Copied in at the start; a vector is NOT here,
	// because a policy change takes effect where policy is read (the supervisor) rather than
	// where the machine records it.
	scope          Grant
	failFast       bool
	hasFinisher    bool
	hasAnswerCheck bool
}

func (s state) clone() state {
	s.conv = slices.Clone(s.conv)
	s.children = slices.Clone(s.children)
	s.frames = slices.Clone(s.frames)
	return s
}

// ── inputs ──────────────────────────────────────────────────────────────────────
//
// An INPUT is anything that can make the task do more. They split three ways, and the split
// is the precedence policy rather than a taxonomy:
//
//   - an INTERVENTION (cancel, pause, resume, say) is an operator redirecting THIS RUN. It
//     preempts, including a pending gate.
//   - a POLICY change (set-vector) is prospective. It does NOT preempt: sliding a running task
//     to AUTO says "stop asking me about actions like this", not "approve the one already in
//     front of me", and dissolving the pending question would turn a policy change into
//     approval of a specific action nobody looked at.
//   - WORK THE TASK PRODUCED FOR ITSELF (a child's result) folds at the hinge and never
//     overtakes an action already proposed. Preemption is for people.
//
// Everything else is a REPLY to an effect the machine asked for, and is discarded if the
// machine has moved past the phase that asked.

type input interface{ input() }

type (
	inStart  struct{}                 // begin
	inCancel struct{ cause error }    // intervention: end this run
	inPause  struct{}                 // intervention: hold at the next boundary
	inResume struct{}                 // intervention: lift the hold
	inSay    struct{ msgs []convMsg } // intervention: an operator said something
	inVector struct{}                 // policy: the vector was slid
	inChild  struct {
		id, reason, summary string
		failed              bool
	} // the task's own work, returning

	inHeld struct { // reply: the hold is over
		err error
		tok int
	}
	inDriver struct { // reply: what the driver proposes
		tu     ToolUse
		more   bool
		err    error
		answer string
		tok    int
	}
	inGate struct { // reply: how the gate resolved
		res Resolution
		err error
		tok int
	}
	inExecuted struct { // reply: what the action produced
		out    StepResult
		defers []ToolUse
		err    error
		tok    int
	}
	inSpawned struct { // reply: a child was started (or was not)
		id  string
		res StepResult
		tok int
	}
	inChecked struct { // reply: the contract's verdict
		repair string
		err    error
		tok    int
	}
	inFinish struct { // reply: the finisher's closing action
		tu  ToolUse
		ok  bool
		tok int
	}
)

func (inStart) input()    {}
func (inCancel) input()   {}
func (inPause) input()    {}
func (inResume) input()   {}
func (inSay) input()      {}
func (inVector) input()   {}
func (inChild) input()    {}
func (inHeld) input()     {}
func (inDriver) input()   {}
func (inGate) input()     {}
func (inExecuted) input() {}
func (inSpawned) input()  {}
func (inChecked) input()  {}
func (inFinish) input()   {}

// ── effects ─────────────────────────────────────────────────────────────────────
//
// The only way the world is touched. Each blocking one has exactly one reply, which is what
// keeps "which wait am I in" a single field rather than a set of flags.

type effect interface{ effect() }

type (
	effEmit    struct{ f Frame }
	effJournal struct{ line string }
	effLog     struct{ line string }

	effHold struct{ tok int } // → inHeld
	effAsk  struct {
		prev StepResult
		tok  int
	} // → inDriver
	effGate struct {
		tu  ToolUse
		tok int
	} // → inGate
	effDropGate struct{ why string } // withdraw the standing question; no reply
	effExecute  struct {
		tu      ToolUse
		closing bool
		tok     int
	} // → inExecuted
	effSpawn struct {
		tu   ToolUse
		mode ChildMode
		tok  int
	} // → inSpawned
	effConverse struct{ msgs []convMsg } // deliver into the driver's conversation; no reply
	effCheck    struct {
		answer string
		tok    int
	} // → inChecked
	effFinish struct {
		answer string
		tok    int
	} // → inFinish
	effCancelChildren struct{ ids []string }

	// effTeardown is the last effect of a run: unwind the frames bottom-up and emit the
	// terminal. It carries the frames rather than reading them off the state so that the
	// state it leaves behind has none — a machine that has torn down cannot tear down again.
	effTeardown struct {
		status Status
		frames []*frame
		cause  error
		seq    int
	}
)

func (effEmit) effect()           {}
func (effJournal) effect()        {}
func (effLog) effect()            {}
func (effHold) effect()           {}
func (effAsk) effect()            {}
func (effGate) effect()           {}
func (effDropGate) effect()       {}
func (effExecute) effect()        {}
func (effSpawn) effect()          {}
func (effConverse) effect()       {}
func (effCheck) effect()          {}
func (effFinish) effect()         {}
func (effCancelChildren) effect() {}
func (effTeardown) effect()       {}

// ── the transition ──────────────────────────────────────────────────────────────

// step folds one input into the state and returns what the shell must do about it. It is pure:
// same state, same input, same effects, every time.
func step(s state, in input) (state, []effect) {
	s = s.clone()
	return s, s.fold(in)
}

func (s *state) fold(in input) []effect {
	if s.ph == phEnd {
		return nil // nothing is emitted after the terminal (property 1)
	}
	switch v := in.(type) {
	case inStart:
		return s.hinge()

	// ── interventions: an operator redirecting this run ─────────────────────
	case inCancel:
		// Cancel outranks everything — a pause, a pending gate, another intervention — and a
		// cancel in progress is not interruptible: once winding down, nothing re-enters.
		//
		// It is LATCHED and honored at the next boundary, which is the property the whole
		// design is for: a control plane that acts at a known point rather than racing an
		// agent mid-thought. An action already proposed in this boundary still runs. Every
		// outstanding wait is discharged by the same context that carried the cancel — the
		// hold breaks, the registry returns, the executor is asked to stop — so each phase's
		// reply arrives and folds into the exit rather than being waited out. The pending
		// question, if any, leaves the docket by that same route (Registry.OnDrop →
		// mirror.DropQuestion), rather than leaving somebody staring at a question about an
		// action this task will now never take.
		if s.cancelling {
			return nil
		}
		s.cancelling = true
		s.exitStatus = Cancelled
		s.setCause(v.cause)
		if s.ph == phHinge || s.ph == phExit {
			return s.exit(Cancelled)
		}
		return nil

	case inSay:
		if s.winding() {
			return nil
		}
		s.conv = append(s.conv, v.msgs...)
		s.reopen()
		return s.interrupt("an operator said something while this was being asked about")

	case inPause:
		// A pause is about to suspend this run, so a question about an action it will not
		// reach for is withdrawn rather than left standing across the hold.
		if s.winding() {
			return nil
		}
		// A PAUSE IS NOT AN ANSWER, and it used to read as one. The other interventions
		// REDIRECT — a say gives the driver something new, a cancel ends the run — so telling
		// it the action was refused is right for them. A pause says "stop here and carry on
		// later", and the action it was asking about is the same action afterwards: nobody
		// declined it, nobody withdrew it, the task was simply held.
		//
		// Read as a refusal it was fatal for any driver that cannot re-plan: a scripted or
		// calls task died on `flow pause` + `flow resume`, with its charter's onFailed firing,
		// for a control the tutorial describes as "freeze its tasks at their next boundary".
		//
		// So the question is withdrawn (a held task must not leave one in front of somebody)
		// and asked AGAIN when the hold lifts. `s.action` is untouched, `s.prev` is not made
		// into a refusal, and `s.closing` stands — a closing act that was being asked about is
		// still the closing act.
		if s.ph == phGate {
			s.regate = true
			return append([]effect{effDropGate{
				why: "the task was paused; the same question is asked again when it resumes"}},
				s.hinge()...)
		}
		return s.interrupt("the task was paused while this was being asked about")

	case inResume:
		// Deliberately nothing, and deliberately NOT symmetric with the other three.
		//
		// The design lists resume among the interventions that abandon a pending gate. A
		// question can only be pending if the task was not held, so a resume that meets one
		// has no hold to lift and redirects nothing — it would destroy a question for
		// nothing, which an operator can do by clicking resume twice. The other three each
		// change what the task does next; this one, here, does not. The hold itself is
		// discharged by the shell's pause chain, which is where a subtree pause lives.
		return nil

	// ── policy: prospective, and it does not preempt ────────────────────────
	case inVector:
		// An arm that does nothing, on purpose. The new vector is already live on the
		// supervisor and governs the NEXT evaluation; a question already posed stands and is
		// answered on its own terms. Writing it as a case rather than as an absence is what
		// makes the policy readable in one place instead of inferred from what is missing.
		return nil

	// ── work the task produced for itself ───────────────────────────────────
	case inChild:
		return s.childEnded(v)

	// ── replies ─────────────────────────────────────────────────────────────
	case inHeld:
		if !s.expecting(phHeld, v.tok) {
			return nil
		}
		if v.err != nil { // cancel broke the hold
			s.setCause(v.err)
			s.cancelling = true
			return s.exit(Cancelled)
		}
		return s.atBoundary()

	case inDriver:
		if !s.expecting(phAsk, v.tok) {
			return nil
		}
		s.answer = v.answer
		if v.err != nil {
			s.setCause(v.err)
			return s.exit(Failed)
		}
		if !v.more {
			s.driverQuiet = true
			return s.quiesce()
		}
		return s.propose(v.tu)

	case inGate:
		if !s.expecting(phGate, v.tok) {
			return nil // the question was withdrawn; its answer is about an action not taken
		}
		return s.resolved(v)

	case inExecuted:
		if !s.expecting(phExec, v.tok) {
			return nil
		}
		return s.executed(v)

	case inSpawned:
		if !s.expecting(phSpawn, v.tok) {
			return nil
		}
		if v.id != "" {
			s.children = append(s.children, v.id)
		}
		s.prev = v.res
		return s.hinge()

	case inChecked:
		if !s.expecting(phCheck, v.tok) {
			return nil
		}
		return s.checked(v)

	case inFinish:
		if !s.expecting(phFinish, v.tok) {
			return nil
		}
		return s.finish(v)
	}
	return nil
}

// winding reports whether this run is on its way out, in which case an operator's input has
// nothing left to redirect. "A cancel in progress is not interruptible."
func (s *state) winding() bool { return s.cancelling || s.ph == phExit || s.ph == phEnd }

// interrupt is what an intervention does to whatever is outstanding. Only a pending GATE is
// preempted: an action already in flight is not, because an input may arrive at a boundary and
// not mid-action, and that is worth keeping deliberately rather than by accident.
func (s *state) interrupt(why string) []effect {
	switch s.ph {
	case phGate:
		// The action is not taken. The driver is told so as a refusal it can re-plan around,
		// which is the same shape a denial already has — unless the action was the CLOSING one,
		// which the agent was never told about and must not now be told was refused. The
		// finisher is simply consulted again at the next quiescence, against whatever the
		// intervention leads to.
		if !s.closing {
			s.prev = StepResult{Use: s.action, Blocked: true,
				Note: "the question about this action was withdrawn: " + why}
		}
		s.closing = false
		effs := []effect{effDropGate{why: why}}
		return append(effs, s.hinge()...)
	case phHinge:
		return s.hinge()
	}
	return nil // it queues; the hinge will find it
}

// reopen is what an arriving input does to a quiescence: the task has something new to answer,
// so the answer it was about to stand on and the delivery it was about to make are both stale.
//
// This is the whole of "delivery is per quiescence, not once ever" — no latch, because being
// asked once is a consequence of the state rather than a flag.
func (s *state) reopen() {
	if s.driverQuiet {
		// The driver already consumed the last action's result and said it was finished;
		// what it is being called about now is the new input, not that result.
		s.prev = StepResult{}
	}
	s.driverQuiet = false
	s.answerOK = false
	s.finished = false
}

// hinge is the boundary: the one hinge of the loop, and the only place anything that is not
// "doing the action" happens.
func (s *state) hinge() []effect {
	s.seq++
	if s.cancelling { // a cancelled subtree exits here, before anything else
		return s.exit(s.exitStatus)
	}
	return []effect{
		effLog{fmt.Sprintf("· boundary %d", s.seq)},
		effEmit{Frame{Kind: EvBoundary, Seq: s.seq}},
		// cancel outranks it; the shell's pause chain covers the subtree
		effHold{tok: s.await(phHeld)},
	}
}

// atBoundary is the hinge past the hold: fold everything that arrived, then decide.
//
// The order IS the precedence policy: an operator's intervention first, then everything the
// task produced for itself, then ask the driver.
func (s *state) atBoundary() []effect {
	if s.cancelling {
		return s.exit(s.exitStatus)
	}
	var effs []effect
	msgs := s.drainConversation()
	if len(msgs) > 0 {
		s.reopen()
		effs = append(effs, effConverse{msgs: msgs})
	}
	// A QUESTION WITHDRAWN BY A PAUSE IS ASKED AGAIN HERE, which is what puts the task back
	// where it was rather than telling its driver an action it proposed was refused.
	//
	// Unless something arrived while it was held: a say is an intervention that REDIRECTS, and
	// re-posing the old question would ask about an action the operator may have just talked
	// the task out of. Then the driver is asked, with the new context, and proposes afresh.
	if s.regate {
		s.regate = false
		if len(msgs) == 0 {
			return append(effs, effGate{tu: s.action, tok: s.await(phGate)})
		}
	}
	if s.driverQuiet {
		return append(effs, s.quiesce()...)
	}
	return append(effs, effAsk{prev: s.prev, tok: s.await(phAsk)})
}

// drainConversation takes everything waiting, an operator's words first.
func (s *state) drainConversation() []convMsg {
	if len(s.conv) == 0 {
		return nil
	}
	out := make([]convMsg, 0, len(s.conv))
	for _, m := range s.conv {
		if m.kind == convSay {
			out = append(out, m)
		}
	}
	for _, m := range s.conv {
		if m.kind != convSay {
			// Steer over a contract repair: a run that has been told something new is not a
			// run that should be arguing with its own answer. The repair is dropped; the
			// driver will produce a new answer, checked when the stream next goes quiet.
			if m.kind == convRepair && len(out) > 0 {
				continue
			}
			out = append(out, m)
		}
	}
	s.conv = nil
	return out
}

// quiesce is the ladder the machine climbs when the input would otherwise go quiet. It is the
// single answer to "is this task over", computed where the whole pending list is visible —
// which is what the four readers of `!more` could not do.
func (s *state) quiesce() []effect {
	if s.cancelling {
		return s.exit(s.exitStatus)
	}
	if len(s.conv) > 0 {
		return s.hinge() // not quiet after all: fold at a boundary, like anything else
	}
	if len(s.children) > 0 {
		// Property 2: never terminal while a child is outstanding. This is a wait, and its
		// discharge is named — a child's completion, which is structural (see the shell's
		// recover) rather than a hope.
		s.ph = phHinge
		return nil
	}
	if s.hasAnswerCheck && !s.answerOK {
		return []effect{effCheck{answer: s.answer, tok: s.await(phCheck)}}
	}
	if s.hasFinisher && !s.finished {
		return []effect{effFinish{answer: s.answer, tok: s.await(phFinish)}}
	}
	s.ph = phEnd
	return []effect{effTeardown{status: Done, frames: s.takeFrames(), seq: s.seq}}
}

// propose runs the driver's action through the hard envelope. Authority comes BEFORE the
// vector and it REFUSES rather than asking: the grant says what the caller authorized and the
// vector says which of that must be confirmed, so an action outside the envelope is not a
// decision anyone present may make. Asking would offer the resolver an authority the caller
// never delegated to them.
func (s *state) propose(tu ToolUse) []effect {
	if !s.scope.Permits(tu) {
		effs := []effect{
			effLog{"⛔ " + tu.Name + " refused: outside the task's grant"},
			effEmit{Frame{Kind: EvExec, Tool: tu.Name, Err: "outside grant"}},
		}
		effs = append(effs, record(tu, Resolution{Question: GateQuestion{
			Gate: "grant", Reason: "outside the task's grant"}})...)
		s.prev = StepResult{Use: tu, Blocked: true,
			Note: "outside the grant this task was launched with: not permitted, and not something a gate can approve"}
		return append(effs, s.hinge()...)
	}
	s.action = tu
	s.closing = false
	return []effect{effGate{tu: tu, tok: s.await(phGate)}}
}

// deniedNote says the action was DENIED, and by whom — not why it was ASKED.
//
// The note is what ends up in the run's `err` (calls.describe folds it as "refused: <note>"),
// and it carried the QUESTION's reason, which is a different sentence entirely. A person denying
// a `run_bash` with `--say "not now"` produced:
//
//	err: "call 1 of 1 (run_bash): refused: no gate matched"
//
// — "no gate matched" being why the vector asked in the first place. It reads as a configuration
// problem, sends whoever finds it looking for a missing gate, and loses both the fact that a
// person decided and the words they decided with. All of it was already on the answer.
func deniedNote(res Resolution) string {
	who := res.By
	if who == "" {
		who = "the gate"
	}
	if said := strings.TrimSpace(res.Note); said != "" {
		return fmt.Sprintf("denied by %s: %s", who, said)
	}
	if say := strings.TrimSpace(strings.Join(res.Steer, "; ")); say != "" {
		return fmt.Sprintf("denied by %s: %s", who, say)
	}
	if why := strings.TrimSpace(res.Question.Reason); why != "" {
		// No words to carry, so the question's own reason is the most useful thing left —
		// said AS the reason it was asked, which is what it is.
		return fmt.Sprintf("denied by %s (it was asked because: %s)", who, why)
	}
	return "denied by " + who
}

// resolved folds a gate's answer.
func (s *state) resolved(v inGate) []effect {
	effs := record(s.action, v.res)
	if v.err != nil { // e.g. the context ended while blocked at the gate
		s.setCause(v.err)
		s.cancelling = true
		return append(effs, s.exit(Cancelled)...)
	}
	for _, note := range v.res.Steer {
		// A gate that informs: context folds in at the next turn — unless the action is the
		// CLOSING one, which the agent was never told about. Answering a question about an
		// action it did not propose, in its own conversation, is a non-sequitur to the agent
		// and was observed reopening a run that had nothing left to do. It goes on the read
		// plane instead, where the person who wrote it is talking to the people watching.
		if s.closing {
			effs = append(effs, effEmit{Frame{Kind: EvSay, By: v.res.By, Text: note}})
			continue
		}
		s.conv = append(s.conv, convMsg{text: note, by: v.res.By, kind: convGate})
	}
	if !v.res.Proceed {
		if s.closing {
			// A refusal is not a failure. Being told not to deliver is a decision the task
			// complied with; the read plane already carries the denial and the workspace is
			// what preserves the work. The agent is never told, because the closing act was
			// never the agent's.
			s.closing = false
			return append(effs, s.quiesce()...)
		}
		effs = append(effs, effLog{fmt.Sprintf("⛔ %s held by gate %q (%s)",
			s.action.Name, v.res.Question.Gate, v.res.Question.Reason)})
		s.prev = StepResult{Use: s.action, Blocked: true, Note: deniedNote(v.res)}
		return append(effs, s.hinge()...)
	}
	if !s.closing && s.action.CallAgent != nil { // uniformly async: spawn detached, join later
		return append(effs, effSpawn{tu: s.action, mode: v.res.ChildMode, tok: s.await(phSpawn)})
	}
	return append(effs, effExecute{tu: s.action, closing: s.closing, tok: s.await(phExec)})
}

// executed folds an action's result.
func (s *state) executed(v inExecuted) []effect {
	if s.closing {
		s.closing = false
		if v.err != nil {
			if s.cancelling {
				return append(s.unknown(), s.exit(s.exitStatus)...)
			}
			// The closing action was the last thing this task does, and HOW it ends is the
			// difference between a lost push and a reported one. A run whose work did not
			// leave is not a run that succeeded.
			s.setCause(v.err)
			return s.exit(Failed)
		}
		return append([]effect{effEmit{Frame{Kind: EvExec, Tool: v.out.Use.Name,
			Text: evTrunc(v.out.Output)}}}, s.quiesce()...)
	}
	if v.err != nil {
		if s.cancelling {
			return append(s.unknown(), s.exit(s.exitStatus)...)
		}
		s.prev = StepResult{Use: s.action, Err: v.err} // no defers registered on failure
		return append([]effect{
			effLog{fmt.Sprintf("✗ %s failed: %v", s.action.Name, v.err)},
			effEmit{Frame{Kind: EvExec, Tool: s.action.Name, Err: v.err.Error()}},
		}, s.hinge()...)
	}
	s.registerDefers(v.defers) // only on success — defer iff setup succeeded
	s.prev = v.out
	return append([]effect{effEmit{Frame{Kind: EvExec, Tool: s.action.Name,
		Text: evTrunc(v.out.Output)}}}, s.hinge()...)
}

// unknown is the record of an action that was in flight when the run was torn down.
//
// A cancel racing an action cannot be resolved and the record should stop implying it was. The
// case that makes this concrete is a push: the loop tears down while the push may already have
// landed, and no ordering tells the person who cancelled which won — at-most-once is not
// something this side can offer across a boundary it does not control. What it CAN do is stop
// pretending, and until now it did not even do that: this path exited without emitting anything,
// so the attempt left no trace at all and the run read as one where nothing was tried.
//
// `unknown` rather than `failed`, because the error here is usually the teardown arriving rather
// than the action being refused, and reporting it as a failure asserts something nobody checked.
// Whoever cares can find out from the world: a ref is reflected level-triggered, so whether the
// branch actually moved arrives on its own without anybody asking.
func (s *state) unknown() []effect {
	return []effect{
		effLog{fmt.Sprintf("? %s: unknown — cancelled while it was in flight", s.action.Name)},
		effEmit{Frame{Kind: EvExec, Tool: s.action.Name,
			Err: "unknown: the run was cancelled while this action was in flight, so whether it " +
				"took effect is not something this loop can say"}},
	}
}

// checked folds the answer contract's verdict. The contract is a PREDICATE on the value the
// task is about to return, asked at the moment the stream would go quiet — not a verdict about
// a moment, which is what it was inside the driver, where it judged a draft written while a
// child was still editing the tree.
func (s *state) checked(v inChecked) []effect {
	if v.err != nil {
		s.setCause(v.err)
		return s.exit(Failed)
	}
	if v.repair != "" {
		s.conv = append(s.conv, convMsg{text: v.repair, kind: convRepair})
		s.reopen()
		return s.hinge()
	}
	s.answerOK = true
	return s.quiesce()
}

// finish folds the finisher's proposal. It runs through the same authority → gate → execute
// path as anything else, which is the whole argument for this seam over an OnComplete hook: a
// hook is a fourth thing to supervise, and an action is already supervised.
func (s *state) finish(v inFinish) []effect {
	s.finished = true
	if !v.ok {
		// An environment with nowhere to deliver has nothing to propose, and proposing a
		// no-op action would put a gate in front of nothing.
		return s.quiesce()
	}
	if !s.scope.Permits(v.tu) {
		// Refused by the task's own envelope, which no gate can approve and no rephrasing
		// makes permitted. It fails rather than ending quietly, because the work did not
		// leave and nobody can be asked to let it.
		s.setCause(fmt.Errorf("the closing action %q is outside the grant this task was "+
			"launched with, so its work cannot leave and no gate can approve it", v.tu.Name))
		return s.exit(Failed)
	}
	s.action = v.tu
	s.closing = true
	return []effect{effGate{tu: v.tu, tok: s.await(phGate)}}
}

// childEnded folds a child's completion. It is not an operator input and does not preempt: it
// folds at the hinge, like any other work the task produced for itself.
func (s *state) childEnded(v inChild) []effect {
	s.children = slices.DeleteFunc(s.children, func(id string) bool { return id == v.id })
	if s.joining || s.cancelling {
		// A cancel or a failure is in progress. The child is joined, not folded — the
		// conversation it would have gone into is over. `cancelling` and not just `joining`,
		// because a cancel is latched: the parent can learn it is winding down a boundary
		// before it acts on it, and a child that reports in that window has still been joined
		// rather than heard.
		effs := []effect{
			effJournal{"[child] " + v.id + " joined on exit"},
			effLog{"⑃ " + v.id + " joined on exit"},
		}
		return append(effs, s.exit(s.exitStatus)...)
	}
	effs := []effect{
		effJournal{"[child] " + v.summary},
		effLog{"⑃ " + v.summary},
		effEmit{Frame{Kind: EvChild, Tool: v.id, Text: v.summary}},
	}
	s.conv = append(s.conv, convMsg{text: v.summary, kind: convChild})
	s.reopen()
	if v.failed && s.failFast && !s.failing && len(s.children) > 0 {
		s.failing = true
		effs = append(effs, effLog{"↯ fail-fast: cancelling siblings"},
			effCancelChildren{ids: slices.Clone(s.children)})
	}
	if s.ph == phHinge { // the machine was at rest waiting for exactly this
		effs = append(effs, s.hinge()...)
	}
	return effs
}

// exit winds the run down: signal top-down, join bottom-up — children's frames close before
// the parent's — and only then tear down.
func (s *state) exit(status Status) []effect {
	if status == "" {
		status = Cancelled
	}
	s.exitStatus = status
	if status != Done {
		s.cancelling = true
	}
	var effs []effect
	if len(s.children) > 0 {
		if !s.joining {
			s.joining = true
			effs = append(effs, effCancelChildren{ids: slices.Clone(s.children)})
		}
		s.ph = phExit // wait: every child that started must end (property 3)
		return effs
	}
	s.ph = phEnd
	return append(effs, effTeardown{status: status, frames: s.takeFrames(), cause: s.cause, seq: s.seq})
}

// await parks the machine on one reply and stamps the token that reply must carry.
//
// One blocking effect at a time, one reply each: that is what keeps "which wait am I in" a
// single field, and the token is what keeps it honest when a wait is ABANDONED. The abandoned
// call is still running and still going to answer; without the token its answer is folded as
// the answer to whatever the machine asked next.
func (s *state) await(ph phase) int {
	s.ph = ph
	s.tok++
	return s.tok
}

// expecting reports whether a reply is the one the machine is waiting for.
func (s *state) expecting(ph phase, tok int) bool { return s.ph == ph && s.tok == tok }

func (s *state) setCause(c error) {
	if s.cause == nil { // first cause wins — a later unwind must not overwrite the reason
		s.cause = c
	}
}

func (s *state) registerDefers(ds []ToolUse) {
	if len(ds) == 0 {
		return
	}
	f := s.frames[len(s.frames)-1]
	f.defers = append(f.defers, ds...)
}

func (s *state) takeFrames() []*frame {
	fs := s.frames
	s.frames = nil
	return fs
}

// record is the journal line and the read-plane frame for one gate evaluation.
func record(tu ToolUse, res Resolution) []effect {
	return []effect{
		effJournal{fmt.Sprintf("[%s] %s → %s by %s (%s)",
			tu.ID, tu.Name, res.Question.Mode, res.By, res.Question.Reason)},
		effEmit{Frame{
			Kind: EvDecision, ID: res.Question.ID, Tool: tu.Name, Gate: res.Question.Gate,
			Mode: res.Question.Mode.String(), Proceed: res.Proceed, By: res.By,
			Text: res.Question.Reason,
		}},
	}
}
