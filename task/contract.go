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
//     terminal means the task is finished — see property 5's exceptions.
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
//     A reader may conclude: a spawned child produces exactly one completion, even if it
//     panics — the outcome is then a failure, not an absence. A reader may NEVER conclude
//     that the outcome is the child's own account of itself; a panicking child never got to
//     write one.
//
//  4. The closing action is proposed at most once per quiescence, and never while the input
//     is open.
//
//     A reader may conclude: the delivery ran against the tree as it stood when the task had
//     nothing left to do. A reader may NEVER conclude that it ran exactly once over the whole
//     run — an operator who says something after a delivery reopens the input, and the work
//     that follows is delivered too. That is the point of "per quiescence": a latch would
//     make the second delivery impossible, which is a bug rather than a policy.
//
//  5. Every wait names its discharge, and no discharge depends on a person choosing to look.
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
// It is property 5 made mechanical. The shell calls it before blocking, and a blocked machine
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
	case phSpawn:
		return "a child to be started"
	case phCheck:
		return "the answer contract's verdict"
	case phFinish:
		return "the finisher's closing action"
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

// contractWatch accumulates what a run has done, so the properties above can be asserted at
// every transition rather than at the end. It is what the enumeration drives; a violation is
// returned as prose naming the property, because a numbered failure tells a reader which line
// of the contract to argue with.
type contractWatch struct {
	terminated   bool     // property 1: a terminal was reached
	effectsAfter int      // property 1: effects emitted after it
	started      []string // property 3: children that were spawned
	ended        map[string]int
	closings     int  // property 4: closing actions proposed in the current quiescence
	quiescence   int  // bumped whenever the input reopens
	openInput    bool // property 4: the input is open (the driver has more to say)
}

func newContractWatch() *contractWatch {
	return &contractWatch{ended: map[string]int{}, openInput: true}
}

// observe folds one transition in and returns every property it broke.
func (w *contractWatch) observe(before, after state, effs []effect) []string {
	var bad []string

	// 3. every child that starts ends exactly once. Accounted FIRST, because property 2 is
	// stated over the children still running and a step that ends the last child and
	// terminates does both at once — reading the children off the state before the step would
	// report that legitimate transition as a violation.
	for _, id := range after.children {
		if !contains(w.started, id) {
			w.started = append(w.started, id)
		}
	}
	for _, id := range before.children {
		if contains(after.children, id) {
			continue
		}
		w.ended[id]++
		if w.ended[id] > 1 {
			bad = append(bad, "property 3: child "+id+" ended more than once")
		}
	}

	// 1. terminal at most once, nothing after it.
	if w.terminated && len(effs) > 0 {
		w.effectsAfter += len(effs)
		bad = append(bad, fmt.Sprintf("property 1: %d effect(s) emitted after the terminal", len(effs)))
	}
	for _, e := range effs {
		td, ok := e.(effTeardown)
		if !ok {
			continue
		}
		if w.terminated {
			bad = append(bad, "property 1: a second terminal state was reached")
		}
		w.terminated = true

		// 2. no terminal while a child is outstanding — where outstanding means started and
		// not yet ended, which is the thing a reader actually cares about.
		if n := w.outstanding(); n > 0 {
			bad = append(bad, fmt.Sprintf("property 2: terminated as %s with %d child(ren) outstanding",
				td.status, n))
		}
	}

	// 4. the closing action is proposed at most once per quiescence, never while input is open.
	if !before.driverQuiet && after.driverQuiet {
		w.openInput = false
	}
	if before.driverQuiet && !after.driverQuiet { // the input reopened
		w.openInput = true
		w.quiescence++
		w.closings = 0
	}
	for _, e := range effs {
		if _, ok := e.(effFinish); !ok {
			continue
		}
		if w.openInput {
			bad = append(bad, "property 4: the finisher was consulted while the input was open")
		}
		w.closings++
		if w.closings > 1 {
			bad = append(bad, "property 4: the closing action was proposed twice in one quiescence")
		}
	}
	return bad
}

// outstanding counts the children that have started and not yet ended.
func (w *contractWatch) outstanding() int {
	var n int
	for _, id := range w.started {
		if w.ended[id] == 0 {
			n++
		}
	}
	return n
}

// done reports what is still owed at the end of a run: property 3 requires every child that
// started to have ended, which is only checkable once there is no more run to observe.
func (w *contractWatch) done() []string {
	var bad []string
	for _, id := range w.started {
		if w.ended[id] == 0 {
			bad = append(bad, "property 3: child "+id+" started and never ended")
		}
	}
	if !w.terminated {
		bad = append(bad, "property 1: the run stopped without reaching a terminal state")
	}
	return bad
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
