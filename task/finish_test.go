package task

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A scripted driver that yields a fixed list of actions and then stops, with the optional
// capabilities switchable so the wrapper can be checked against each shape.
type scripted struct {
	acts   []string
	i      int
	result string
	steers []string
}

func (d *scripted) Next(_ context.Context, _ StepResult) (ToolUse, bool, error) {
	if d.i >= len(d.acts) {
		return ToolUse{}, false, nil
	}
	d.i++
	return ToolUse{Name: d.acts[d.i-1]}, true, nil
}
func (d *scripted) Result() string      { return d.result }
func (d *scripted) Fold(instr []string) { d.steers = append(d.steers, instr...) }

type recorder struct{ ran []string }

func (r *recorder) Execute(_ context.Context, tu ToolUse) (StepResult, []ToolUse, error) {
	r.ran = append(r.ran, tu.Name)
	return StepResult{Use: tu, Output: "ok"}, nil, nil
}

type finisher struct {
	action  string
	gotFrom string
	ran     int
	err     error
	none    bool
	onDo    func() // fires while the closing action is executing
}

func (f *finisher) Finish(result string) (ToolUse, bool) {
	if f.none {
		return ToolUse{}, false
	}
	f.gotFrom = result
	return ToolUse{Name: f.action, Effects: []Effect{{Verb: Push, Resource: Resource{Kind: KindGitRef, Ref: "refs/heads/x"}}}}, true
}

func (f *finisher) Do(context.Context, ToolUse) (string, error) {
	f.ran++
	if f.onDo != nil {
		f.onDo()
	}
	return "delivered", f.err
}

// The closing action runs LAST, exactly once, and sees the driver's result.
//
// Ordering is the whole point: it is the runtime's act on finished work, so a version that
// proposed it early would gate a push of a half-written tree.
func TestFinisherRunsAfterTheDriverAndOnlyOnce(t *testing.T) {
	d := &scripted{acts: []string{"a", "b"}, result: "the answer"}
	f := &finisher{action: "deliver"}
	rec := &recorder{}
	p := NewTask("t", NewSupervisor(&Vector{Default: Auto}, nil), d, rec)
	p.SetFinisher(f)
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The driver's own actions went to the driver's executor; the closing one did NOT — it is
	// the runtime's act and the executor that knows how to perform it is the finisher.
	if strings.Join(rec.ran, ",") != "a,b" {
		t.Errorf("actions reaching the driver's executor = %v, want [a b]", rec.ran)
	}
	if f.ran != 1 {
		t.Errorf("the closing action ran %d time(s), want exactly one", f.ran)
	}
	if f.gotFrom != "the answer" {
		t.Errorf("the finisher saw result %q, not the driver's", f.gotFrom)
	}
	// And the task's result is still the DRIVER's, not the finisher's output — a charter's
	// $result derefs the work, not the delivery note.
	if p.Result() != "the answer" {
		t.Errorf("Result() = %q, want the driver's", p.Result())
	}
}

// A finisher with nothing to do proposes nothing, so a task with nowhere to deliver does not
// meet a gate in front of a no-op.
func TestFinisherMayDeclineToAct(t *testing.T) {
	d := &scripted{acts: []string{"a"}}
	rec := &recorder{}
	p := NewTask("t", NewSupervisor(&Vector{Default: Auto}, nil), d, rec)
	p.SetFinisher(&finisher{none: true})
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.ran) != 1 {
		t.Errorf("actions = %v, want just the driver's", rec.ran)
	}
}

// A closing act that fails FAILS the task. A run whose work did not leave did not succeed, and
// the reason this seam exists is that the failure used to be a line in a runner's log.
func TestAFailedClosingActFailsTheTask(t *testing.T) {
	d := &scripted{acts: []string{"a"}}
	f := &finisher{action: "deliver", err: errors.New("remote hung up")}
	p := NewTask("t", NewSupervisor(&Vector{Default: Auto}, nil), d, &recorder{})
	p.SetFinisher(f)
	out, err := p.Run(context.Background())
	if err == nil {
		t.Fatal("a failed delivery completed the task")
	}
	if !strings.Contains(err.Error(), "remote hung up") {
		t.Errorf("failure does not carry the reason: %v", err)
	}
	if out.Reason != string(Failed) {
		t.Errorf("outcome = %s, want failed", out.Reason)
	}
}

// The vector gates it like any other action, which is the entire reason it is an action.
func TestTheClosingActIsGated(t *testing.T) {
	d := &scripted{acts: []string{"a"}}
	f := &finisher{action: "deliver"}
	// Deny anything carrying a push effect.
	vec := &Vector{Default: Auto, Gates: []Gate{
		{Name: "delivery", Mode: Ask, Match: GateMatch{Verbs: []Verb{Push}}},
	}}
	reg := NewRegistry()
	asked := make(chan Pending, 4)
	reg.OnPost(func(pd Pending) { asked <- pd })
	p := NewTask("t", NewSupervisor(vec, reg), d, &recorder{})
	p.SetFinisher(f)
	go func() {
		pd := <-asked
		reg.Answer(pd.Question.ID, Resolution{Proceed: false, By: "human:test"})
	}()
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("a denied closing act failed the task rather than ending it: %v", err)
	}
	// Asserted rather than inferred: an earlier version of this test passed because the closing
	// act was never proposed at all, which is indistinguishable from "the gate held it" if all
	// you check is that it did not run.
	if len(asked) != 0 {
		t.Fatal("more questions than the one closing act")
	}
	if f.ran != 0 {
		t.Error("the closing act ran despite being denied — the gate is decoration")
	}
	// And the same task with the gate answering yes DOES deliver, so the test above is about the
	// answer and not about the plumbing.
	d2 := &scripted{acts: []string{"a"}}
	f2 := &finisher{action: "deliver"}
	reg2 := NewRegistry()
	reg2.OnPost(func(pd Pending) { reg2.Answer(pd.Question.ID, Resolution{Proceed: true, By: "human:test"}) })
	p2 := NewTask("t2", NewSupervisor(vec, reg2), d2, &recorder{})
	p2.SetFinisher(f2)
	if _, err := p2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f2.ran != 1 {
		t.Error("an approved closing act did not run")
	}
}

// Delivery is per QUIESCENCE, not once ever — which is what the wrapper this replaced could
// not do, and the reason it could not is the whole argument for moving the decision into the
// loop.
//
// `finishDriver` needed a `posted` latch, because it saw one boolean (`!more`) and had to
// remember whether it had already acted on it. A latch is a fact about the past, and the
// question is about the present: an operator who says something after a delivery has REOPENED
// the input, so there is new work, so it must be delivered too. The wrapper made that
// impossible; here it is a consequence of the state rather than a flag, so it needs no code of
// its own to be true.
func TestDeliveryIsPerQuiescence(t *testing.T) {
	d := &reopened{}
	f := &finisher{action: "deliver"}
	rec := &recorder{}
	p := NewTask("t", NewSupervisor(&Vector{Default: Auto}, nil), d, rec)
	p.SetFinisher(f)
	// Said while the FIRST delivery is in flight, so it lands at the boundary after it — the
	// exact shape the latch made unreachable.
	f.onDo = func() {
		if f.ran == 1 {
			p.Steer("one more thing", "human:test")
		}
	}
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Join(rec.ran, ",") != "a,b" {
		t.Errorf("the driver's actions = %v, want [a b] — the run did not reopen", rec.ran)
	}
	if f.ran != 2 {
		t.Errorf("delivered %d time(s), want 2: the work that followed the say was never delivered", f.ran)
	}
}

// reopened does one action and stops; told something, it does one more and stops again.
type reopened struct {
	said bool
	acts int
}

func (d *reopened) Next(context.Context, StepResult) (ToolUse, bool, error) {
	switch {
	case d.acts == 0:
		d.acts++
		return ToolUse{Name: "a"}, true, nil
	case d.said:
		d.said = false
		d.acts++
		return ToolUse{Name: "b"}, true, nil
	}
	return ToolUse{}, false, nil
}

func (d *reopened) Fold([]string)  { d.said = true }
func (d *reopened) Result() string { return "the answer" }

// `no-delegate` means what it says: the whole envelope minus delegation.
//
// This exists because adding the `push` verb broke it silently. The name is documented in
// docs/trigger-model.md and spelled in charters; its allow-list was six literals; a seventh verb
// therefore narrowed a published promise without touching it, and the symptom would have been a
// coding task whose delivery was refused by its own scope — which Grant refuses rather than asks,
// so no gate and no operator could have let it through.
//
// Pinned against AllVerbs rather than against a list, so the NEXT verb fails here instead.
func TestNoDelegateAdmitsEveryVerbButDelegation(t *testing.T) {
	g, ok, err := NamedGrant("no-delegate")
	if err != nil || !ok {
		t.Fatalf("NamedGrant: %v %v", ok, err)
	}
	// Excluded on purpose: delegation, because a sub-agent nobody can approve is a park; and
	// answering, because a task that may resolve gate questions approves itself, which is the
	// last thing an envelope for UNATTENDED work should hand out. Both are pinned so a widening
	// has to be deliberate.
	excluded := map[Verb]bool{CallAgent: true, Resolve: true}
	for _, v := range AllVerbs {
		tu := ToolUse{Name: "probe", Effects: []Effect{{Verb: v, Resource: Resource{Ref: "x"}}}}
		permitted := g.Permits(tu)
		if excluded[v] {
			if permitted {
				t.Errorf("no-delegate permits %q, which it excludes on purpose", v)
			}
			continue
		}
		if !permitted {
			t.Errorf("no-delegate refuses %q — a new verb narrows this published name unless it "+
				"is added to AllVerbs, and the narrowing is a refusal no gate can approve", v)
		}
	}
	// read-only stays read-only: adding verbs must not widen the narrow name either.
	ro, _, _ := NamedGrant("read-only")
	if ro.Permits(ToolUse{Effects: []Effect{{Verb: Push, Resource: Resource{Ref: "x"}}}}) {
		t.Error("read-only permits a push")
	}
}
