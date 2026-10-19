package task

import "fmt"

// contractWatch accumulates what a run has done, so the properties in contract.go can be asserted at
// every transition rather than at the end. It is what the enumeration drives; a violation is
// returned as prose naming the property, because a numbered failure tells a reader which line
// of the contract to argue with.
type contractWatch struct {
	terminated   bool     // property 1: a terminal was reached
	effectsAfter int      // property 1: effects emitted after it
	started      []string // property 3: children that started
	ended        map[string]int
}

func newContractWatch() *contractWatch {
	return &contractWatch{ended: map[string]int{}}
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
