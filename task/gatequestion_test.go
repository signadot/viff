package task

import (
	"context"
	"strings"
	"testing"
)

// A gate's question rides with its mode. The deciding gate says how many ballots settle it,
// what it settles on, and how long it stands; a question that lost those between the vector
// and the resolver would be posed as yes-or-no however the vector was written.
func TestTheDecidingGateCarriesItsQuestion(t *testing.T) {
	clock := &Deadline{Within: "4h", Default: Deny}
	v := &Vector{Default: Auto, Gates: []Gate{
		{Name: "shell", Mode: Ask, Match: GateMatch{Tools: []string{"run_bash"}}, Need: 2, Deadline: clock},
		{Name: "route", Mode: Ask, Match: GateMatch{Tools: []string{"deploy"}},
			Options: []Option{{ID: "canary", Need: 1}, {ID: "full", Need: 2}}},
		{Name: "plain", Mode: Ask, Match: GateMatch{Tools: []string{"write_file"}}},
	}}

	q := v.Evaluate(ToolUse{Name: "run_bash"})
	if q.Gate != "shell" || q.Deadline != clock {
		t.Fatalf("shell: %+v", q)
	}
	// `need` is the short spelling of the default two with proceed at the threshold.
	if len(q.Options) != 2 || q.Options[0].ID != Proceed || q.Options[0].Need != 2 || q.Options[1].ID != Deny || q.Options[1].Need != 1 {
		t.Fatalf("need: 2 posed %+v", q.Options)
	}
	if q := v.Evaluate(ToolUse{Name: "deploy"}); len(q.Options) != 2 || q.Options[1].ID != "full" || q.Options[1].Need != 2 {
		t.Fatalf("options of the gate's own: %+v", q.Options)
	}
	// A gate that says nothing poses nothing, which is the two defaults at one each.
	if q := v.Evaluate(ToolUse{Name: "write_file"}); q.Options != nil || q.Deadline != nil {
		t.Fatalf("a plain gate carries %+v / %+v", q.Options, q.Deadline)
	}
}

// A vector whose gate cannot be posed as written is refused, naming the gate and the
// spelling that would work — the same refusals a charter gate meets at install.
func TestAVectorGateIsValidatedAsACharterGateIs(t *testing.T) {
	bad := []struct {
		gate Gate
		want string
	}{
		{Gate{Name: "g", Mode: Ask, Need: -1}, "at least one"},
		{Gate{Name: "g", Mode: Ask, Need: 2, Options: []Option{{ID: "x"}}}, "written together"},
		{Gate{Name: "g", Mode: Ask, Options: []Option{{ID: "x"}, {ID: "x"}}}, "written twice"},
		{Gate{Name: "g", Mode: Ask, Options: []Option{{ID: ""}}}, "no id"},
		{Gate{Name: "g", Mode: Ask, Deadline: &Deadline{Within: "soon"}}, "quote the duration"},
		{Gate{Name: "g", Mode: Ask, Deadline: &Deadline{Within: "-1h"}}, "in the future"},
		{Gate{Name: "g", Mode: Ask, Deadline: &Deadline{Within: "1h", Default: "abort"}}, "not one of this gate's options (proceed, deny)"},
		{Gate{Name: "g", Mode: Auto, Need: 2}, "asks nobody"},
	}
	for _, c := range bad {
		err := (&Vector{Gates: []Gate{c.gate}}).Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "gate g") {
			t.Errorf("%+v: err = %v, want one naming gate g and saying %q", c.gate, err, c.want)
		}
	}
	good := &Vector{Gates: []Gate{
		{Mode: Ask, Need: 2, Deadline: &Deadline{Within: "1h", Default: Deny}},
		{Mode: Park, Options: []Option{{ID: "ship"}, {ID: "hold", Need: 2}},
			Deadline: &Deadline{Within: "30m", Default: "hold"}},
		{Mode: Auto},
	}}
	if err := good.Validate(); err != nil {
		t.Fatalf("a well-formed vector was refused: %v", err)
	}
	// Named by position when the gate has no name, so the refusal still points somewhere.
	err := (&Vector{Gates: []Gate{{Mode: Ask}, {Mode: Auto, Need: 2}}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "gate #2") {
		t.Fatalf("an unnamed gate's refusal = %v, want it named by position", err)
	}
}

// A spawn's vector is its name and its gates: gates over a named default, gates alone over
// auto, and a name alone is the bare default it always was.
func TestSpawnVectorIsNameAndGates(t *testing.T) {
	gates := []Gate{{Name: "shell", Mode: Ask, Match: GateMatch{Tools: []string{"run_bash"}}, Need: 2}}
	v, err := SpawnVector("ask", gates)
	if err != nil || v.Default != Ask || len(v.Gates) != 1 || v.Gates[0].Need != 2 {
		t.Fatalf("named with gates: %+v, %v", v, err)
	}
	if v, err := SpawnVector("", gates); err != nil || v.Default != Auto || len(v.Gates) != 1 {
		t.Fatalf("gates alone: %+v, %v", v, err)
	}
	if v, err := SpawnVector("", nil); err != nil || v != nil {
		t.Fatalf("nothing said: %+v, %v — want nil, the runtime's default", v, err)
	}
	if v, err := SpawnVector("park", nil); err != nil || v.Default != Park || len(v.Gates) != 0 {
		t.Fatalf("a name alone: %+v, %v", v, err)
	}
	if _, err := SpawnVector("", []Gate{{Mode: Auto, Need: 2}}); err == nil {
		t.Fatal("a gate that cannot be posed was accepted at spawn")
	}
	// The spawn's own slice, not the caller's.
	v, _ = SpawnVector("", gates)
	v.Gates[0].Need = 9
	if gates[0].Need != 2 {
		t.Fatal("SpawnVector aliased the caller's gates")
	}
}

// A delegation's question offers the child modes, and choosing one from the docket — by
// option id, as any question with options is answered — spawns the child under it. The mode
// used to be a field beside the answer that no docket showed and no resolver could choose.
func TestADelegationIsAnsweredByChildMode(t *testing.T) {
	for _, c := range []struct {
		choice string
		want   ChildMode
	}{{"careful", ChildCareful}, {"autonomous", ChildAutonomous}, {Proceed, ChildInheritGates}} {
		reg := NewRegistry()
		vec := &Vector{Default: Auto, Gates: []Gate{
			{Name: "delegate", Mode: Ask, Match: GateMatch{Verbs: []Verb{CallAgent}}},
		}}
		parent := NewTask("p", NewSupervisor(vec, reg), &callThenDone{}, &recExec{})
		got := ChildMode(99)
		spawned := make(chan struct{}, 1)
		parent.SetSpawn(func(pTask *Task, mode ChildMode, req *CallAgentRequest) (*Task, error) {
			got = mode
			spawned <- struct{}{}
			return pTask.NewChild("kid", mode, req, &scriptDriver{}, &recExec{})
		})
		done := make(chan struct{})
		go func() { parent.Run(context.Background()); close(done) }()

		waitFor(t, func() bool { return len(reg.Pending()) == 1 })
		p := reg.Pending()[0]
		// The question SHOWS the modes: a person at the docket can see what a yes means.
		want := DelegateOptions(1)
		if len(p.Question.Options) != len(want) {
			t.Fatalf("a delegation posed %+v, want the child modes %+v", p.Question.Options, want)
		}
		for i := range want {
			if got := p.Question.Options[i]; got.ID != want[i].ID || got.Need != want[i].Need {
				t.Fatalf("option %d = %+v, want %+v", i, got, want[i])
			}
		}
		// Answered the way the answer path answers: by option id, with Proceed saying only
		// whether the option was named proceed.
		reg.Answer(p.Question.ID, Choose(c.choice, "human"))
		<-spawned
		<-done
		if got != c.want {
			t.Errorf("choosing %q spawned the child %s, want %s", c.choice, got, c.want)
		}
	}
}

// And `deny` on a delegation is still a no: the child is never spawned.
func TestADelegationDeniedSpawnsNothing(t *testing.T) {
	reg := NewRegistry()
	vec := &Vector{Default: Auto, Gates: []Gate{
		{Name: "delegate", Mode: Ask, Match: GateMatch{Verbs: []Verb{CallAgent}}},
	}}
	parent := NewTask("p", NewSupervisor(vec, reg), &callThenDone{}, &recExec{})
	parent.SetSpawn(func(pTask *Task, mode ChildMode, req *CallAgentRequest) (*Task, error) {
		t.Error("a denied delegation spawned a child")
		return pTask.NewChild("kid", mode, req, &scriptDriver{}, &recExec{})
	})
	done := make(chan struct{})
	go func() { parent.Run(context.Background()); close(done) }()
	waitFor(t, func() bool { return len(reg.Pending()) == 1 })
	reg.Answer(reg.Pending()[0].Question.ID, Choose(Deny, "human"))
	<-done
}

// A gate that wrote outcomes of its own over a delegation keeps them: the modes are what a
// delegation offers when nobody said otherwise, not an override of what somebody did say. A
// threshold on the gate is the threshold every mode takes.
func TestADelegationKeepsAGatesOwnOptions(t *testing.T) {
	own := []Option{{ID: "ship", Need: 1}, {ID: "hold", Need: 2}}
	sup := NewSupervisor(&Vector{Default: Auto, Gates: []Gate{
		{Name: "delegate", Mode: Ask, Match: GateMatch{Verbs: []Verb{CallAgent}}, Options: own},
	}}, questionOnly{})
	res, _ := sup.Check(context.Background(), ToolUse{Name: "delegate", CallAgent: &CallAgentRequest{Agent: "x"}})
	if len(res.Question.Options) != 2 || res.Question.Options[0].ID != "ship" {
		t.Fatalf("the gate's own options were replaced: %+v", res.Question.Options)
	}
	sup = NewSupervisor(&Vector{Default: Auto, Gates: []Gate{
		{Name: "delegate", Mode: Ask, Match: GateMatch{Verbs: []Verb{CallAgent}}, Need: 2},
	}}, questionOnly{})
	res, _ = sup.Check(context.Background(), ToolUse{Name: "delegate", CallAgent: &CallAgentRequest{Agent: "x"}})
	got := res.Question.Options
	if len(got) != 4 || got[1].ID != "careful" || got[1].Need != 2 || got[3].ID != Deny || got[3].Need != 1 {
		t.Fatalf("need: 2 on a delegation posed %+v, want %+v", got, DelegateOptions(2))
	}
}

// questionOnly hands the question back unanswered, for a test about what was posed.
type questionOnly struct{}

func (questionOnly) Resolve(_ context.Context, q GateQuestion, _ ToolUse) (Resolution, error) {
	return Resolution{Question: q}, nil
}
