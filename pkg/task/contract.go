package task

import "fmt"

// THE LOOP'S CONTRACT: what a reader of a running task may conclude, and what they may never.
//
// Five properties. The first four are SAFETY — nothing bad happens — and are checked by
// enumerating interleavings of the pure transition (machine.go's step) and asserting them at
// every transition; see enumerate_test.go. The fifth is LIVENESS — something good keeps
// happening — and no enumeration of a bounded input set can establish it, because liveness is
// a claim that an event ARRIVES. It is checked by an argument about who discharges each wait,
// pinned by the tests in bounds_test.go and by Waiting below.
//
// They are written here, in one file, because they are what every part of the loop cites and
// what a reviewer should be able to disagree with before reading any code. A property nobody
// disputes and nobody can find is not a contract; it is a comment.
//
//  1. A task reaches a terminal state at most once, and nothing is emitted after it.
//
//     A reader may conclude: the terminal frame is final. Whatever it says is what happened,
//     and no later frame revises it. A reader may NEVER conclude that silence before the
//     terminal means the task is finished — see property 4's exceptions.
//
//  2. A task does not terminate while a child is outstanding.
//
//     A reader may conclude: when a parent reports done, every child it started has ended,
//     and the parent saw each one's outcome. A reader may NEVER conclude that the parent's
//     answer INCORPORATES what a child said — the parent is told, and what it does with that
//     is the agent's business.
//
//  3. Every child that starts ends exactly once, with an outcome.
//
//     A reader may conclude: a child that starts produces exactly one completion, even if it
//     panics — the outcome is then a failure, not an absence. A reader may NEVER conclude
//     that the outcome is the child's own account of itself; a panicking child never got to
//     write one.
//
//  4. Every wait names its discharge, and no discharge depends on a person choosing to look.
//
//     A reader may conclude: a task that is not making progress is waiting for something
//     nameable, and Waiting says what. A reader may NEVER conclude that the task will
//     therefore finish: a driver that proposes an action per boundary forever satisfies every
//     property here and never ends, and a seam that ignores its context (see Executor) can
//     hang inside a step this loop cannot see into.
//
// The exception, stated as one: a PAUSE is a hang by request. Its discharge is an operator,
// and the operator asked for it.

// Waiting names what the machine is blocked on, or "" when it has work to do or has ended.
//
// It is property 4 made mechanical. The shell calls it before blocking, and a blocked machine
// with nothing to name is a defect rather than patience — so the shell fails loudly instead of
// hanging, which is the difference between a bug that is found and one that is waited out.
func (s state) Waiting() string {
	if s.ph == phEnd {
		return ""
	}
	switch s.ph {
	case phHeld:
		return "an operator to resume this task (a pause is a hang by request)"
	case phAsk:
		return "the driver to propose an action (bounded by the driver's own timeout)"
	case phGate:
		return "a gate to be answered, an operator to intervene, or a cancel"
	case phExec:
		return "an action to finish (bounded only if the executor honours its context)"
	case phDelegate:
		return "a child to be started"
	case phCheck:
		return "the answer contract's verdict"
	case phTurn:
		return "the person to take their turn or end the conversation (a question on the docket, withdrawn by a say or a cancel)"
	case phExit:
		if len(s.children) > 0 {
			return fmt.Sprintf("%d cancelled child(ren) to report", len(s.children))
		}
		return ""
	case phHinge:
		if len(s.children) > 0 {
			return fmt.Sprintf("%d outstanding child(ren) to report", len(s.children))
		}
		return "" // nothing outstanding and nothing to do: not a wait, a defect
	}
	return ""
}
