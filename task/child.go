package task

import (
	"fmt"
)

// ChildMode is how a CallAgent gate's ASK is answered — the autonomy dial at the
// delegation edge. It configures the CHILD's supervision (resolver + vector)
// only; it never affects scope (authority), which is always bounded by
// parent ∩ Grant regardless of mode. Autonomous drops supervision recursively,
// never authority.
type ChildMode uint8

const (
	// ChildInheritGates (the default, zero value): the child runs the parent's gate
	// vector and escalates its ASK gates to the parent's resolver.
	ChildInheritGates ChildMode = iota
	// ChildAutonomous: the child — and its whole subtree, recursively —
	// auto-resolves its gates ("stop asking me, within the granted scope"). No
	// bubbling.
	ChildAutonomous
	// ChildCareful: like InheritGates but every child action is re-asked
	// (non-sticky escalation). Tightening/non-stickiness is a later refinement;
	// today it shares the vector and resolver.
	ChildCareful
)

func (m ChildMode) String() string {
	switch m {
	case ChildAutonomous:
		return "autonomous"
	case ChildInheritGates:
		return "inherit-gates"
	case ChildCareful:
		return "careful"
	default:
		return "unknown"
	}
}

// ChildModeNamed reads a child mode off an option id, and reports whether the id names one.
// `proceed` is the default mode — the yes every gate has always taken — so a delegation
// answered yes-or-no is supervised the way it was before the modes were choosable.
func ChildModeNamed(id string) (ChildMode, bool) {
	switch id {
	case Proceed, ChildInheritGates.String():
		return ChildInheritGates, true
	case ChildAutonomous.String():
		return ChildAutonomous, true
	case ChildCareful.String():
		return ChildCareful, true
	}
	return ChildInheritGates, false
}

// DelegateOptions is the question a delegation poses: each child mode is an outcome, with
// `proceed` standing for the default one, and `deny` refuses. `need` is the threshold every
// mode takes — a delegation that wants two voices wants them whichever mode they choose —
// and a refusal takes one, as it does on every gate.
//
// The modes are options rather than a field beside the answer because a field beside the
// answer is one a docket cannot show and a resolver cannot choose: Resolution.ChildMode was
// exactly that, settable from one HTTP body and spelled nowhere on the question.
func DelegateOptions(need int) []Option {
	if need < 1 {
		need = 1
	}
	return []Option{
		{ID: Proceed, Need: need},
		{ID: ChildCareful.String(), Need: need},
		{ID: ChildAutonomous.String(), Need: need},
		{ID: Deny, Need: 1},
	}
}

// supervisorFor builds a child Supervisor for a mode — the supervision axis only
// (resolver + vector). Scope is handled separately and is not this function's
// concern.
func supervisorFor(mode ChildMode, parentVec *Vector, parentRes Resolver) *Supervisor {
	switch mode {
	case ChildAutonomous:
		return NewSupervisor(parentVec, AutoResolver{Who: "auto:delegated"})
	case ChildCareful:
		return NewSupervisor(tighten(parentVec), parentRes)
	default: // ChildInheritGates
		return NewSupervisor(parentVec, parentRes)
	}
}

// tighten returns a copy of v where every action is at least ASK — the "careful"
// posture: the child re-asks on everything, not just the parent's gated actions.
func tighten(v *Vector) *Vector {
	t := &Vector{Default: Ask}
	for _, g := range v.Gates {
		if g.Mode < Ask {
			g.Mode = Ask
		}
		t.Gates = append(t.Gates, g)
	}
	return t
}

// NewChild constructs (does not run) a child Task for a CallAgent along two
// orthogonal axes:
//
//   - supervision (soft) — resolver + vector, from mode (supervisorFor).
//   - authority (hard)   — scope = parent ∩ Grant, ALWAYS, independent of mode.
//
// It requires req.Grant ⊆ the parent's scope and errors otherwise: authority is
// never delegated upward, in ANY mode (including ChildAutonomous). Because the
// Grant is verified ⊆ parent, the child's scope is exactly req.Grant (= parent ∩
// Grant). The caller supplies the child's Driver and Executor.
func (p *Task) NewChild(id string, mode ChildMode, req *CallAgentRequest, d Driver, e Executor) (*Task, error) {
	if !p.scope.Subset(req.Grant) {
		return nil, fmt.Errorf("child %q scope exceeds parent", req.Agent)
	}
	child := NewTask(id, supervisorFor(mode, p.sup.Vector(), p.sup.Resolver()), d, e)
	child.SetScope(req.Grant)                         // == parent ∩ Grant, since Grant ⊆ parent
	child.pauseGate = &pauseGate{parent: p.pauseGate} // link into the parent's pause chain
	return child, nil
}
