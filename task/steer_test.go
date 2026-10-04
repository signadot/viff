package task

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// steerDriver records the interleaving of Fold and Next calls so a test can
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

// Fold records WHAT was said, not how it was framed. `fold` puts the speaker and their standing
// ahead of the words and the words last (see loop.go), so the last line is what somebody
// actually said — which is what the ordering tests below are about, and lets them keep asserting
// the payload rather than being rewritten every time the framing is.
func (d *steerDriver) Fold(msgs []string) {
	for _, m := range msgs {
		said := m
		if i := strings.LastIndex(m, "\n\n"); i >= 0 {
			said = m[i+2:]
		}
		d.events = append(d.events, "steer:"+said)
	}
}

// A steer injected at a boundary is delivered to the driver at that boundary,
// before the boundary's Next — cooperative, latched at the hinge.
func TestSteerCooperative(t *testing.T) {
	d := &steerDriver{steps: []ToolUse{tool("A"), tool("B")}}
	p := NewTask("t", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	p.onEnterBoundary = func(seq int) {
		if seq == 1 {
			p.Steer("s1", "human:test", "")
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

// A say to a driver that holds no conversation is ignored, not fatal, and leaves no entry in
// the journal — there is nothing to record, because nothing was delivered.
func TestASayIsIgnoredByADriverThatHoldsNoConversation(t *testing.T) {
	d := &scriptDriver{steps: []ToolUse{tool("A")}}
	p := NewTask("t", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	p.onEnterBoundary = func(seq int) {
		if seq == 1 {
			p.Steer("ignored", "human:test", "")
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

// An operator's words reach the driver with WHO said them and what standing they have.
//
// They used to arrive as bare text. A model has no way to tell a person's decision from a tool
// result or its own earlier reasoning, so a steer is one more thing to weigh — and a run on
// staging, told "stop verifying and produce your answer", weighed it and ran the same command
// again. The loop knew who was speaking the whole time and dropped it on the floor.
func TestAnOperatorsSteerArrivesWithItsStanding(t *testing.T) {
	said := "hurry — produce your answer now"
	got := fold(convMsg{text: said, by: "human:scott", kind: convSay})

	if !strings.Contains(got, said) {
		t.Fatalf("the operator's own words are gone: %q", got)
	}
	if !strings.Contains(got, "human:scott") {
		t.Errorf("the speaker is not named, so a person and a service read alike: %q", got)
	}
	// The load-bearing claim: they can see what the model cannot. That is what makes it a
	// decision rather than a suggestion, and it is the only thing here the model could not
	// have worked out for itself.
	for _, want := range []string{"see everything you", "decision"} {
		if !strings.Contains(got, want) {
			t.Errorf("the framing does not say %q, so it reads as one more message: %q", want, got)
		}
	}
	// And it must not promise authority the loop will then refuse. An operator outranks the
	// plan; they do not widen the grant or answer a gate.
	for _, must := range []string{"grant", "approve", "authoris", "authoriz"} {
		if strings.Contains(strings.ToLower(got), must) {
			t.Errorf("the framing implies %q, which the loop does not honour: %q", must, got)
		}
	}
}

// The runtime's own repair speaks for itself, and a child's result is the task's own work
// returning — neither is somebody talking to it.
func TestOnlyAPersonsWordsAreFramed(t *testing.T) {
	for name, m := range map[string]convMsg{
		"an answer-contract repair": {text: "your answer did not match", kind: convRepair},
		"a child returning":         {text: "kid completed", kind: convChild},
	} {
		if got := fold(m); got != m.text {
			t.Errorf("%s was framed as somebody speaking: %q", name, got)
		}
	}
}

// A say that is an ask carries it onto the say frame the task emits when it takes the words
// (9ce3bva0, stage 6), which is how the ask is known to be heard.
func TestASayEchoesTheAskItIs(t *testing.T) {
	d := &steerDriver{steps: []ToolUse{tool("A"), tool("B")}}
	p := NewTask("t", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	p.onEnterBoundary = func(seq int) {
		if seq == 1 {
			p.Steer("s1", "user:bob", "say-1")
		}
	}
	frames, stop := p.SubscribeSince(0)
	defer stop()
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	for f := range frames {
		if f.Kind == EvSay && f.Text == "s1" {
			if f.ID != "say-1" || f.By != "user:bob" {
				t.Fatalf("the say frame is %+v, want it to carry ask say-1, by bob", f)
			}
			return
		}
		if f.Kind == EvStatus && Status(f.Text).Terminal() {
			break
		}
	}
	t.Fatal("no say frame for s1")
}
