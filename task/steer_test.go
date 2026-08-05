package task

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// steerDriver records the interleaving of Steer and Next calls so a test can
// assert cooperative delivery order.
type steerDriver struct {
	steps  []ToolUse
	i      int
	events []string
}

func (d *steerDriver) Next(_ context.Context, _ StepResult) (ToolUse, bool, error) {
	if d.i >= len(d.steps) {
		return ToolUse{}, false, nil
	}
	tu := d.steps[d.i]
	d.i++
	d.events = append(d.events, "next:"+tu.Name)
	return tu, true, nil
}

func (d *steerDriver) Steer(msgs []string) {
	for _, m := range msgs {
		d.events = append(d.events, "steer:"+m)
	}
}

// A steer injected at a boundary is delivered to the driver at that boundary,
// before the boundary's Next — cooperative, latched at the hinge.
func TestSteerCooperative(t *testing.T) {
	d := &steerDriver{steps: []ToolUse{tool("A"), tool("B")}}
	p := NewTask("t", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	p.onEnterBoundary = func(seq int) {
		if seq == 1 {
			p.Steer("s1", "human:test")
		}
	}
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{"steer:s1", "next:A", "next:B"}
	if !reflect.DeepEqual(d.events, want) {
		t.Fatalf("events = %v, want %v", d.events, want)
	}
}

// injectResolver approves an ASK gate and folds context into the task's next
// turn (a gate that informs) — e.g. an SCM meta-task adding org conventions.
type injectResolver struct{ note string }

func (r injectResolver) Resolve(_ context.Context, d GateQuestion, _ ToolUse) (Resolution, error) {
	return Resolution{Proceed: true, By: "scm", Question: d, Steer: []string{r.note}}, nil
}

// A gate resolution carrying Steer folds that context into the driver at the next
// boundary, before the next Next — the "gate that informs" (SCM injection) path.
func TestGateInjectsContext(t *testing.T) {
	d := &steerDriver{steps: []ToolUse{tool("A"), tool("B")}}
	vec := &Vector{Default: Auto, Gates: []Gate{
		{Name: "g", Mode: Ask, Match: GateMatch{Tools: []string{"A"}}}, // ASK on A → resolver consulted
	}}
	p := NewTask("t", NewSupervisor(vec, injectResolver{note: "ctx: heads up"}), d, &recExec{})
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	// A resolves at boundary 1 (inject latched); the context folds in at boundary 2,
	// before the driver decides B.
	want := []string{"next:A", "steer:ctx: heads up", "next:B"}
	if !reflect.DeepEqual(d.events, want) {
		t.Fatalf("events = %v, want %v", d.events, want)
	}
}

// A steer against a non-Steerable driver is ignored, not fatal, and leaves no
// steer entry in the journal.
func TestSteerIgnoredWhenNotSteerable(t *testing.T) {
	d := &scriptDriver{steps: []ToolUse{tool("A")}}
	p := NewTask("t", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	p.onEnterBoundary = func(seq int) {
		if seq == 1 {
			p.Steer("ignored", "human:test")
		}
	}
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, l := range p.Journal() {
		if strings.Contains(l, "[steer]") {
			t.Fatalf("steer should have been ignored, journal=%v", p.Journal())
		}
	}
}
