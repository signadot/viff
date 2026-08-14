package task

import (
	"errors"
	"fmt"
	"testing"
)

// THE ENUMERATION. Every ordering of a bounded input set, driven through step, with the
// contract's predicates asserted at EVERY transition.
//
// This is what the machine exists for. The old loop's behaviour under several pending inputs
// was decided by the order of nine statements in one function, and the only way to test it was
// to drive a real task and hope the interleaving you cared about happened. Sampling a
// concurrent system for a rule you have not written down finds the orderings you thought of.
//
// WHAT IT ESTABLISHES: safety over the bounded set (the four safety properties in
// contract.go), and that the machine terminates on every path where the inputs it is waiting
// for arrive. THE SET, stated rather than left to be inferred from the code below, because the
// first version of this said "every ordering" over a set that quietly excluded most of the ways
// a run goes wrong:
//
//   - arrivals: two children reporting, an operator intervening, a policy change — and in the
//     second enumeration a cancel and a pause instead of the say.
//   - replies: a driver proposing two delegations, an action that fails and one that works, a
//     gate that refuses the second question it is asked, a contract that rejects one answer
//     before accepting it, and a finisher that proposes a closing action.
//   - faults: any ONE seam failing, at any point, on any path (see world.fault).
//
// WHAT IT DOES NOT: that an input ARRIVES. No enumeration of a bounded set can — liveness is a
// claim about the world, and it is argued in contract.go and pinned in bounds_test.go. Nor
// anything in the SHELL: goroutines, contexts, the pause chain, the read plane. Nor two
// simultaneous faults. Nor — and this is the one worth saying out loud — whether the contract
// in contract.go is the RIGHT contract. It checks the code against those five properties; a
// property nobody wrote is not a property this can miss. A staging drive found that a single
// `fail` token discards a run whose children have all reported, and no amount of interleaving
// would have surfaced it: the machine is correct on that path, and the question is whether the
// path should exist.
//
// WHAT IT DOES NOT: that an input ARRIVES. No enumeration of a bounded set can — liveness is a
// claim about the world, and it is argued in contract.go and pinned in bounds_test.go. Nor
// anything in the SHELL: goroutines, contexts, the pause chain, the read plane. A path that
// runs out of inputs here is not a hang; it is the end of what was enumerated, and the
// assertion made about it is the weaker one that the machine could still name what it was
// waiting for.

var (
	errBoom      = errors.New("the action failed")
	errDriver    = errors.New("the agent declared failure")
	errCancelled = errors.New("cancelled")
	errContract  = errors.New("the answer contract cannot be used")
)

// world answers the machine's blocking effects deterministically. It is the enumeration's
// stand-in for every seam at once — the driver, the resolver, the executor, the spawn factory,
// the contract, the finisher — and it is a VALUE, so a DFS can fork it.
type world struct {
	acts    int // how much of the script has been proposed
	gates   int // questions posed
	spawned int
	repairs int // contract rejections still to hand out
	execs   int
	faulted bool // a seam has already failed on this path (see fault)
}

// script is what the driver proposes, in order: two delegations, an action that fails, and one
// that works. Then it goes quiet forever, which is the only way a driver ends a turn.
var script = []ToolUse{
	{Name: "delegate-1", CallAgent: &CallAgentRequest{Agent: "a", Grant: Grant{Allow: []GateMatch{{}}}}},
	{Name: "delegate-2", CallAgent: &CallAgentRequest{Agent: "b", Grant: Grant{Allow: []GateMatch{{}}}}},
	{Name: "boom"},
	{Name: "ok"},
}

// reply is the deterministic answer to one blocking effect.
func (w world) reply(e effect) (input, world) {
	switch v := e.(type) {
	case effHold:
		return inHeld{tok: v.tok}, w
	case effAsk:
		if w.acts < len(script) {
			tu := script[w.acts]
			w.acts++
			tu.ID = fmt.Sprintf("a%d", w.acts)
			return inDriver{tu: tu, more: true, tok: v.tok}, w
		}
		return inDriver{more: false, answer: "the answer", tok: v.tok}, w
	case effGate:
		w.gates++
		// The second question is REFUSED, so the denial branch is on every path that reaches
		// it — a gate that always says yes tests the plumbing and not the decision.
		res := Resolution{
			Proceed: w.gates != 2,
			By:      "human:test",
			Question: GateQuestion{ID: fmt.Sprintf("q%d", w.gates), Mode: Ask,
				Gate: "g", Reason: "because"},
		}
		return inGate{res: res, tok: v.tok}, w
	case effExecute:
		w.execs++
		if v.tu.Name == "boom" {
			return inExecuted{out: StepResult{Use: v.tu}, err: errBoom, tok: v.tok}, w
		}
		return inExecuted{out: StepResult{Use: v.tu, Output: "ok"}, tok: v.tok}, w
	case effSpawn:
		w.spawned++
		return inSpawned{id: kidID(w.spawned), res: StepResult{Use: v.tu, Output: "started"}, tok: v.tok}, w
	case effCheck:
		if w.repairs > 0 {
			w.repairs--
			return inChecked{repair: "that is not a document; write one", tok: v.tok}, w
		}
		return inChecked{tok: v.tok}, w
	case effFinish:
		return inFinish{tu: ToolUse{Name: "deliver", ID: "deliver"}, ok: true, tok: v.tok}, w
	}
	return nil, w
}

func kidID(n int) string { return fmt.Sprintf("kid%d", n) }

// fault is the same effect answered by a seam that FAILED, and it is where this enumeration was
// thinner than it claimed.
//
// The first version modelled exactly one failure — an executor error — out of six seams that
// can produce one, and said "every ordering of a bounded input set" over it. The omission that
// mattered most was inDriver{err}: that is what the `fail` tool IS (harness/llm.go returns the
// model's reason as a driver error), so the single most likely way a real run ends badly was
// not in the set at all. A staging drive ended exactly that way.
//
// AT MOST ONE per path, which is the usual depth for fault injection and the right one here:
// what these properties can get wrong is a failure arriving while something else is pending —
// a driver error with two children outstanding, a contract that cannot be used while an
// operator is saying something — and that is a first-order interaction. A second simultaneous
// fault multiplies the state space to test a case the first already covers the shape of.
//
// It returns false where a seam has no failure worth modelling separately.
func (w world) fault(e effect) (input, world, bool) {
	if w.faulted {
		return nil, w, false
	}
	w.faulted = true
	switch v := e.(type) {
	case effHold:
		// The hold broke: a cancel arrived while this task was held.
		return inHeld{err: errCancelled, tok: v.tok}, w, true
	case effAsk:
		// The `fail` tool, or a model call that could not be made. Both reach the machine as a
		// driver error, which is why one case covers them.
		return inDriver{err: errDriver, tok: v.tok}, w, true
	case effGate:
		// The resolver's context ended under it — the question left the docket unanswered.
		return inGate{err: errCancelled, tok: v.tok}, w, true
	case effExecute:
		return inExecuted{out: StepResult{Use: v.tu}, err: errBoom, tok: v.tok}, w, true
	case effSpawn:
		// The factory refused: no spawn configured, or a child scope exceeding the parent's.
		// No child starts, so nothing is owed a completion.
		return inSpawned{res: StepResult{Use: v.tu, Blocked: true, Note: "scope exceeds parent"},
			tok: v.tok}, w, true
	case effCheck:
		// The contract itself is unusable, or the repair budget is spent. Either way the run
		// fails rather than asking again.
		return inChecked{err: errContract, tok: v.tok}, w, true
	case effFinish:
		// Not a failure so much as the other branch: nowhere to deliver, so nothing proposed.
		return inFinish{ok: false, tok: v.tok}, w, true
	}
	return nil, w, false
}

// tokOf is the reply token a blocking effect will be answered with — the identity that keeps a
// stale answer from being folded as this action's.
func tokOf(e effect) int {
	switch v := e.(type) {
	case effHold:
		return v.tok
	case effAsk:
		return v.tok
	case effGate:
		return v.tok
	case effExecute:
		return v.tok
	case effSpawn:
		return v.tok
	case effCheck:
		return v.tok
	case effFinish:
		return v.tok
	}
	return 0
}

// blocking finds the one effect that owes a reply. At most one per batch is an invariant of the
// machine, not a coincidence, and it is asserted rather than assumed: it is what keeps "which
// wait am I in" a single field.
func blocking(t *testing.T, effs []effect) effect {
	t.Helper()
	var found effect
	for _, e := range effs {
		switch e.(type) {
		case effHold, effAsk, effGate, effExecute, effSpawn, effCheck, effFinish:
			if found != nil {
				t.Fatalf("two blocking effects in one transition: %T and %T", found, e)
			}
			found = e
		}
	}
	return found
}

// arrival is an input from outside, with when it is POSSIBLE. A child cannot report before it
// has started, and enumerating orderings that could not happen would be enumerating a
// different machine.
type arrival struct {
	name  string
	in    input
	ready func(state) bool
}

func anytime(state) bool { return true }

func childReady(id string) func(state) bool {
	return func(s state) bool { return contains(s.children, id) }
}

// The main enumeration: two children, an operator's say, and a policy change, interleaved with
// every reply, over a script that delegates twice, is refused once, fails an action, and has
// both an answer contract and a closing act.
func TestEveryOrderingHoldsTheContract(t *testing.T) {
	arrivals := []arrival{
		{"say", inSay{msgs: []convMsg{{text: "actually, do it this way", by: "human:test"}}}, anytime},
		{"vector", inVector{}, anytime},
		{"kid1", inChild{id: kidID(1), reason: string(Done), summary: "child kid1 completed (done)"}, childReady(kidID(1))},
		{"kid2", inChild{id: kidID(2), reason: string(Failed), failed: true, summary: "child kid2 ended with error: boom"}, childReady(kidID(2))},
	}
	enumerate(t, arrivals, world{repairs: 1}, true)
}

// The same, with a CANCEL in place of the say: the winding-down paths, where a child that
// reports is joined rather than heard and nothing an operator says re-enters.
//
// Termination is not required of every path here, and that is the point rather than a
// weakening: a cancel with a child outstanding waits for that child to report, and whether it
// does is exactly the arrival this cannot establish. What IS asserted on those paths is that
// the machine still names what it is waiting for.
func TestEveryOrderingHoldsTheContractUnderCancellation(t *testing.T) {
	arrivals := []arrival{
		{"cancel", inCancel{cause: errors.New("cancelled: test")}, anytime},
		{"pause", inPause{}, anytime},
		{"kid1", inChild{id: kidID(1), reason: string(Cancelled), summary: "child kid1 completed (cancelled)"}, childReady(kidID(1))},
		{"kid2", inChild{id: kidID(2), reason: string(Cancelled), summary: "child kid2 completed (cancelled)"}, childReady(kidID(2))},
	}
	enumerate(t, arrivals, world{}, false)
}

// enumerate walks every interleaving depth-first. mustEnd requires each path that exhausts its
// inputs to have reached a terminal state.
func enumerate(t *testing.T, arrivals []arrival, w world, mustEnd bool) {
	t.Helper()
	start := state{
		frames:         []*frame{{label: "root"}},
		scope:          Grant{Allow: []GateMatch{{}}},
		hasFinisher:    true,
		hasAnswerCheck: true,
		failFast:       true,
	}
	s, effs := step(start, inStart{})

	e := &explorer{t: t, arrivals: arrivals, mustEnd: mustEnd, seen: map[string]bool{}}
	watch := newContractWatch()
	if bad := watch.observe(start, s, effs); len(bad) > 0 {
		t.Fatalf("the opening transition broke the contract: %v", bad)
	}
	e.walk(s, w, blocking(t, effs), nil, 0, watch)

	t.Logf("%d transitions over %d paths (%d distinct nodes)", e.steps, e.paths, len(e.seen))
	if e.paths == 0 {
		t.Fatal("nothing was enumerated")
	}
}

type explorer struct {
	t        *testing.T
	arrivals []arrival
	mustEnd  bool
	seen     map[string]bool
	steps    int
	paths    int
}

// walk explores every input that could come next. `used` is a bitmask of the arrivals already
// delivered on this path — an input arrives once. `pending` is the reply still owed, which
// SURVIVES an arrival: an operator saying something does not answer the executor. `stale` is
// the reply to a call the machine abandoned, which is still running and still going to answer.
func (e *explorer) walk(s state, w world, pending, stale effect, used int, watch *contractWatch) {
	if e.t.Failed() {
		return
	}
	if key := fingerprint(s, w, pending, stale, used, watch); e.seen[key] {
		return // this node was reached another way; its subtree is the same
	} else {
		e.seen[key] = true
	}

	type choice struct {
		name  string
		in    input
		w     world
		used  int
		reply bool // it answers what was owed, so nothing is owed after it
		stale bool
	}
	var choices []choice
	if pending != nil {
		in, w2 := w.reply(pending)
		choices = append(choices, choice{fmt.Sprintf("reply:%T", pending), in, w2, used, true, false})
		// …and the same effect answered by a seam that failed. At most one per path; see fault.
		if bad, w3, ok := w.fault(pending); ok {
			choices = append(choices, choice{fmt.Sprintf("fault:%T", pending), bad, w3, used, true, false})
		}
	}
	if stale != nil {
		// The abandoned call answers anyway. Folding it must change NOTHING — that is what the
		// reply token is for, and it is asserted here because getting it wrong turned an
		// operator's say into a cancelled task before this test existed.
		in, _ := w.reply(stale)
		choices = append(choices, choice{fmt.Sprintf("stale:%T", stale), in, w, used, false, true})
	}
	for i, a := range e.arrivals {
		if used&(1<<i) != 0 || !a.ready(s) {
			continue
		}
		choices = append(choices, choice{a.name, a.in, w, used | 1<<i, false, false})
	}

	if len(choices) == 0 {
		e.paths++
		e.leaf(s, watch)
		return
	}
	for _, c := range choices {
		s2, effs := step(s, c.in)
		e.steps++
		if c.stale {
			if len(effs) > 0 {
				e.t.Fatalf("a reply to an abandoned call produced %d effect(s) in phase %s: "+
					"it was folded as the answer to something else", len(effs), s.ph)
			}
			e.walk(s, c.w, pending, nil, c.used, watch)
			continue
		}
		w2 := watch.clone()
		if bad := w2.observe(s, s2, effs); len(bad) > 0 {
			e.t.Fatalf("after %s (phase %s → %s): %v", c.name, s.ph, s2.ph, bad)
		}
		if s2.ph == phEnd {
			e.paths++
			e.leaf(s2, w2)
			continue
		}
		owed := pending
		if c.reply {
			owed = nil // it has been answered, whether or not the machine wanted it
		}
		next, dropped := carry(owed, blocking(e.t, effs), s2)
		if dropped == nil {
			dropped = stale
		}
		e.walk(s2, c.w, next, dropped, c.used, w2)
	}
}

// carry decides what is still owed after a transition: the newly issued blocking effect if
// there is one, else the one that was already outstanding — unless the machine has moved past
// it, in which case it becomes a stale reply still in flight.
func carry(owed, issued effect, after state) (next, stale effect) {
	switch {
	case issued != nil && owed != nil && tokOf(owed) != after.tok:
		return issued, owed // abandoned mid-wait: its answer is still on its way
	case issued != nil:
		return issued, nil
	case owed != nil && tokOf(owed) == after.tok:
		return owed, nil // an arrival does not answer what a seam owes
	}
	return nil, owed
}

// leaf is what must hold where a path stops.
func (e *explorer) leaf(s state, watch *contractWatch) {
	if s.ph == phEnd {
		if bad := watch.done(); len(bad) > 0 {
			e.t.Fatalf("a terminated run left the contract broken: %v", bad)
		}
		return
	}
	// The path ran out of enumerated inputs. That is not a hang — it is the boundary of what
	// a bounded set can say — but property 5 still applies: the machine must be able to name
	// what it is waiting for.
	if s.Waiting() == "" {
		e.t.Fatalf("the machine stopped in phase %s with nothing to wait for: property 5", s.ph)
	}
	// A path that stops with nothing outstanding and no children is one the enumeration ran
	// dry on rather than one the machine could not finish; anything else here would be a
	// machine parked on a reply nobody owes.
	if e.mustEnd && (s.ph == phHinge || s.ph == phExit) && len(s.children) == 0 {
		e.t.Fatalf("phase %s with no children and nothing pending — a run that cannot end", s.ph)
	}
}

// fingerprint collapses nodes that are the same in every way the contract can see. Without it
// the interleavings multiply into the hundreds of millions and the test is one nobody runs;
// with it, distinct STATES are covered rather than distinct paths to them.
func fingerprint(s state, w world, pending, stale effect, used int, watch *contractWatch) string {
	return fmt.Sprintf("%v|%d|%d|%v%v%v%v%v%v%v|%d|%T|%T|%d|%+v|%v%d%v|%d",
		s.ph, len(s.children), len(s.conv), s.driverQuiet, s.answerOK, s.finished,
		s.cancelling, s.joining, s.closing, s.failing, s.tok,
		pending, stale, used, w,
		watch.terminated, watch.closings, watch.openInput, len(watch.started))
}

func (w *contractWatch) clone() *contractWatch {
	c := *w
	c.started = append([]string(nil), w.started...)
	c.ended = map[string]int{}
	for k, v := range w.ended {
		c.ended[k] = v
	}
	return &c
}
