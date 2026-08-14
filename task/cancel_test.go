package task

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// callN emits one CallAgent per named agent, then reports done.
type callN struct {
	agents []string
	i      int
}

func (d *callN) Next(_ context.Context, _ StepResult) (ToolUse, bool, error) {
	if d.i < len(d.agents) {
		a := d.agents[d.i]
		d.i++
		return ToolUse{Name: "call-" + a, CallAgent: &CallAgentRequest{Agent: a, Grant: Grant{Allow: []GateMatch{{}}}}}, true, nil
	}
	return ToolUse{}, false, nil
}

func (d *callN) Fold(_ []string) {}

// blockThenErr blocks until released, then errors.
type blockThenErr struct{ release chan struct{} }

func (d *blockThenErr) Next(_ context.Context, _ StepResult) (ToolUse, bool, error) {
	<-d.release
	return ToolUse{}, false, fmt.Errorf("boom")
}

// blockAfter runs its steps, then blocks until the context is cancelled.
type blockAfter struct {
	steps   []ToolUse
	i       int
	blocked chan struct{}
}

func (d *blockAfter) Next(ctx context.Context, _ StepResult) (ToolUse, bool, error) {
	if d.i < len(d.steps) {
		tu := d.steps[d.i]
		d.i++
		return tu, true, nil
	}
	if d.blocked != nil {
		close(d.blocked)
		d.blocked = nil
	}
	<-ctx.Done()
	return ToolUse{}, false, nil
}

// Cancelling a parent with a running child joins it bottom-up: the child unwinds
// (its defer runs) and is joined before the parent returns "cancelled".
func TestSubtreeCancelJoinsBottomUp(t *testing.T) {
	childExec := &recExec{defers: map[string][]ToolUse{"csetup": {tool("child-teardown")}}}
	blocked := make(chan struct{})
	childDrv := &blockAfter{steps: []ToolUse{tool("csetup")}, blocked: blocked}

	parent := NewTask("p", NewSupervisor(autoVec(), AutoResolver{}), &callThenDone{}, &recExec{})
	parent.SetSpawn(func(pTask *Task, mode ChildMode, req *CallAgentRequest) (*Task, error) {
		return pTask.NewChild("kid", mode, req, childDrv, childExec)
	})

	type result struct {
		oc  Outcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		oc, err := parent.Run(context.Background())
		done <- result{oc, err}
	}()

	<-blocked // child is running and blocked; the parent is awaiting it
	parent.Cancel("test")
	r := <-done

	if r.oc.Reason != "cancelled" {
		t.Fatalf("reason = %q, want cancelled", r.oc.Reason)
	}
	// the child unwound (its defer ran) — bottom-up teardown
	ran := false
	for _, s := range childExec.order {
		if s == "child-teardown" {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("child did not unwind on cancel: %v", childExec.order)
	}
	// and the parent joined it before returning (no orphan)
	joined := false
	for _, l := range parent.Journal() {
		if strings.Contains(l, "kid joined on exit") {
			joined = true
		}
	}
	if !joined {
		t.Fatalf("parent did not join the child bottom-up: %v", parent.Journal())
	}
}

// With fail-fast, a child's error cancels its still-running sibling (the sibling
// unwinds), while the parent completes normally.
func TestFailFastCancelsSiblings(t *testing.T) {
	releaseA := make(chan struct{})
	aExec := &recExec{}
	bExec := &recExec{defers: map[string][]ToolUse{"bsetup": {tool("b-teardown")}}}
	bBlocked := make(chan struct{})
	bDrv := &blockAfter{steps: []ToolUse{tool("bsetup")}, blocked: bBlocked}

	parent := NewTask("p", NewSupervisor(autoVec(), AutoResolver{}), &callN{agents: []string{"A", "B"}}, &recExec{})
	parent.SetFailFast(true)
	parent.SetSpawn(func(pTask *Task, mode ChildMode, req *CallAgentRequest) (*Task, error) {
		if req.Agent == "A" {
			return pTask.NewChild("childA", mode, req, &blockThenErr{release: releaseA}, aExec)
		}
		return pTask.NewChild("childB", mode, req, bDrv, bExec)
	})

	done := make(chan struct{})
	go func() { parent.Run(context.Background()); close(done) }()

	<-bBlocked      // both children spawned; B blocked; A blocked on releaseA
	close(releaseA) // A errors → fail-fast cancels B
	<-done

	ran := false
	for _, s := range bExec.order {
		if s == "b-teardown" {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("fail-fast did not cancel sibling B (its teardown never ran): %v", bExec.order)
	}
}
