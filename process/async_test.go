package process

import (
	"context"
	"strings"
	"testing"
)

// callThenDone emits one CallAgent, then reports done; it records folded messages
// (steers and child completions) so a test can assert the join.
type callThenDone struct {
	called bool
	folded []string
}

func (d *callThenDone) Next(_ context.Context, _ StepResult) (ToolUse, bool, error) {
	if !d.called {
		d.called = true
		return ToolUse{Name: "delegate", CallAgent: &CallAgentRequest{Agent: "child", Grant: Grant{Allow: []GateMatch{{}}}}}, true, nil
	}
	return ToolUse{}, false, nil
}

func (d *callThenDone) Steer(msgs []string) { d.folded = append(d.folded, msgs...) }

// A CallAgent spawns a detached child; the parent structurally joins it and folds
// its completion back into the conversation before finishing.
func TestCallAgentAsyncSpawnAndJoin(t *testing.T) {
	d := &callThenDone{}
	p := NewProcess("p", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	p.SetSpawn(func(parent *Process, mode ChildMode, req *CallAgentRequest) (*Process, error) {
		return parent.NewChild("child-1", mode, req, &scriptDriver{}, &recExec{})
	})

	out, err := p.Run(context.Background())
	if err != nil || out.Reason != "completed" {
		t.Fatalf("run: reason=%q err=%v", out.Reason, err)
	}
	joined := false
	for _, m := range d.folded {
		if strings.Contains(m, "child-1 completed") {
			joined = true
		}
	}
	if !joined {
		t.Fatalf("child completion was not folded into the parent: %v", d.folded)
	}
	found := false
	for _, l := range p.Journal() {
		if strings.Contains(l, "[child] child child-1 completed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no child-completion journal entry: %v", p.Journal())
	}
}

// Authority is enforced at the call edge before spawning: a child Grant that
// exceeds the parent's scope is denied and the factory never runs.
func TestCallAgentAsyncScopeDenied(t *testing.T) {
	d := &callThenDone{}
	p := NewProcess("p", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	p.SetScope(Grant{Allow: []GateMatch{{Verbs: []Verb{Read}}}}) // bounded; child requests wildcard
	spawned := false
	p.SetSpawn(func(parent *Process, mode ChildMode, req *CallAgentRequest) (*Process, error) {
		spawned = true
		return parent.NewChild("c", mode, req, &scriptDriver{}, &recExec{})
	})

	out, err := p.Run(context.Background())
	if err != nil || out.Reason != "completed" {
		t.Fatalf("run: reason=%q err=%v", out.Reason, err)
	}
	if spawned {
		t.Fatalf("child exceeding parent scope should have been denied before spawn")
	}
}
