package task

import (
	"context"
	"reflect"
	"testing"
)

// recResolver records how many times it was asked (used to check whether a child
// escalated to the parent's resolver).
type recResolver struct{ calls int }

func (r *recResolver) Resolve(_ context.Context, d GateQuestion, _ ToolUse) (Resolution, error) {
	r.calls++
	return Resolution{Proceed: true, By: "rec", Question: d}, nil
}

func askVec() *Vector {
	return &Vector{Default: Auto, Gates: []Gate{
		{Name: "shell", Mode: Ask, Match: GateMatch{Verbs: []Verb{Exec}}},
	}}
}

// Authority axis: the child's scope is exactly the (verified-⊆) Grant.
func TestNewChildScopeIsGrant(t *testing.T) {
	parent := NewTask("p", NewSupervisor(askVec(), &recResolver{}), &scriptDriver{}, &recExec{})
	grant := Grant{Allow: []GateMatch{{Verbs: []Verb{Read}}}}
	child, err := parent.NewChild("c", ChildInheritGates, &CallAgentRequest{Agent: "x", Grant: grant}, &scriptDriver{}, &recExec{})
	if err != nil {
		t.Fatalf("NewChild: %v", err)
	}
	if !reflect.DeepEqual(child.scope, grant) {
		t.Fatalf("child scope = %+v, want %+v", child.scope, grant)
	}
}

// Authority is never delegated upward — in ANY mode — even autonomous.
func TestNewChildAuthorityNeverExceedsParent(t *testing.T) {
	parent := NewTask("p", NewSupervisor(askVec(), &recResolver{}), &scriptDriver{}, &recExec{})
	parent.SetScope(Grant{Allow: []GateMatch{{Verbs: []Verb{Read}}}}) // bounded
	wide := Grant{Allow: []GateMatch{{}}}                             // wildcard child
	for _, mode := range []ChildMode{ChildAutonomous, ChildInheritGates, ChildCareful} {
		if _, err := parent.NewChild("c", mode, &CallAgentRequest{Agent: "x", Grant: wide}, &scriptDriver{}, &recExec{}); err == nil {
			t.Fatalf("mode %s: expected scope-exceeds-parent error, got nil", mode)
		}
	}
}

// Supervision axis: mode selects who resolves the child's gates.
func TestChildSupervisionByMode(t *testing.T) {
	tu := derive(ToolUse{Name: "bash", Input: map[string]any{"command": "ls"}}) // Exec → ASK "shell"
	wildGrant := &CallAgentRequest{Agent: "x", Grant: Grant{Allow: []GateMatch{{}}}}

	// autonomous: child auto-resolves; the parent's resolver is NOT consulted.
	rr := &recResolver{}
	p1 := NewTask("p", NewSupervisor(askVec(), rr), &scriptDriver{}, &recExec{})
	c1, _ := p1.NewChild("c", ChildAutonomous, wildGrant, &scriptDriver{}, &recExec{})
	if res, _ := c1.sup.Check(context.Background(), tu); !res.Proceed || rr.calls != 0 {
		t.Fatalf("autonomous: proceed=%v parentCalls=%d (want proceed, 0)", res.Proceed, rr.calls)
	}

	// inherit-gates: child escalates to the parent's resolver.
	rr2 := &recResolver{}
	p2 := NewTask("p", NewSupervisor(askVec(), rr2), &scriptDriver{}, &recExec{})
	c2, _ := p2.NewChild("c", ChildInheritGates, wildGrant, &scriptDriver{}, &recExec{})
	res, _ := c2.sup.Check(context.Background(), tu)
	if !res.Proceed || res.By != "rec" || rr2.calls != 1 {
		t.Fatalf("inherit: by=%q parentCalls=%d (want rec, 1)", res.By, rr2.calls)
	}
}

// Careful tightens: an action that would be AUTO under the parent's vector is
// re-asked under a careful child; the same action does not ask under inherit.
func TestChildCarefulAsksEverything(t *testing.T) {
	tu := derive(ToolUse{Name: "read", Input: map[string]any{"path": "x"}}) // AUTO under autoVec
	wild := &CallAgentRequest{Agent: "x", Grant: Grant{Allow: []GateMatch{{}}}}

	rr := &recResolver{}
	p := NewTask("p", NewSupervisor(autoVec(), rr), &scriptDriver{}, &recExec{})
	careful, _ := p.NewChild("c", ChildCareful, wild, &scriptDriver{}, &recExec{})
	if res, _ := careful.sup.Check(context.Background(), tu); !res.Proceed || rr.calls != 1 {
		t.Fatalf("careful should ask on an otherwise-AUTO action: proceed=%v calls=%d", res.Proceed, rr.calls)
	}

	rr2 := &recResolver{}
	p2 := NewTask("p", NewSupervisor(autoVec(), rr2), &scriptDriver{}, &recExec{})
	inherit, _ := p2.NewChild("c", ChildInheritGates, wild, &scriptDriver{}, &recExec{})
	if res, _ := inherit.sup.Check(context.Background(), tu); !res.Proceed || rr2.calls != 0 {
		t.Fatalf("inherit should NOT ask on an AUTO action: calls=%d", rr2.calls)
	}
}
