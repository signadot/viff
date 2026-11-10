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
		return ToolUse{Name: "call-" + a, CallAgent: &CallAgentRequest{Agent: a}}, true, nil
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

	parent := NewTask("p", toolSup(allowAll(), AutoResolver{}), &callThenDone{}, &recExec{})
	log := &syncLog{}
	parent.SetLog(log)
	parent.SetDelegate(func(pTask *Task, req *CallAgentRequest) (*Task, error) {
		return pTask.NewChild("kid", req, childDrv, childExec), nil
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
	parent.Cancel("test", "")
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
	if !strings.Contains(log.String(), "kid joined on exit") {
		t.Fatalf("parent did not join the child bottom-up:\n%s", log.String())
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

	parent := NewTask("p", toolSup(allowAll(), AutoResolver{}), &callN{agents: []string{"A", "B"}}, &recExec{})
	parent.SetFailFast(true)
	parent.SetDelegate(func(pTask *Task, req *CallAgentRequest) (*Task, error) {
		if req.Agent == "A" {
			return pTask.NewChild("childA", req, &blockThenErr{release: releaseA}, aExec), nil
		}
		return pTask.NewChild("childB", req, bDrv, bExec), nil
	})

	done := make(chan struct{})
	go func() { parent.Run(context.Background()); close(done) }()

	<-bBlocked      // both children started; B blocked; A blocked on releaseA
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

// inFlightExec blocks in the middle of an action, then errors — the shape of a push that a
// teardown arrives on top of.
type inFlightExec struct {
	started chan struct{}
	release chan struct{}
}

func (e *inFlightExec) Execute(_ context.Context, tu ToolUse) (StepResult, []ToolUse, error) {
	close(e.started)
	<-e.release
	return StepResult{Use: tu}, nil, fmt.Errorf("torn down mid-action")
}

// An action that was IN FLIGHT when the run was cancelled leaves a record saying so.
//
// The case is a push and the honest answer is `unknown`: the loop tears down while the push may
// already have landed, and no ordering tells the person who cancelled which won — at-most-once is
// not on offer across a boundary this side does not control. What was wrong was not the
// uncertainty, it was the silence. This path exited without emitting anything, so the attempt
// left no trace at all and the run read as one where nothing had been tried.
func TestAnActionCancelledInFlightIsRecordedAsUnknown(t *testing.T) {
	e := &inFlightExec{started: make(chan struct{}), release: make(chan struct{})}
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), &scriptDriver{steps: []ToolUse{tool("push")}}, e)

	frames, cancelSub := p.Subscribe()
	defer cancelSub()
	done := make(chan struct{})
	go func() { _, _ = p.Run(context.Background()); close(done) }()

	<-e.started
	p.Cancel("operator changed their mind", "")
	close(e.release)
	<-done

	var said string
	for f := range frames {
		if f.Kind == EventExec && f.Tool == "push" {
			said = f.Err
		}
	}
	if said == "" {
		t.Fatal("an action cancelled in flight left no trace, so the record implies nothing was tried")
	}
	if !strings.Contains(said, "unknown") {
		t.Errorf("the record says %q, which does not say the outcome is unknown", said)
	}
}

// errOnCancel blocks in Next until the context ends, then returns the context's error, as a
// driver waiting on a model does.
type errOnCancel struct{ blocked chan struct{} }

func (d *errOnCancel) Next(ctx context.Context, _ StepResult) (ToolUse, bool, error) {
	close(d.blocked)
	<-ctx.Done()
	return ToolUse{}, false, context.Cause(ctx)
}

// A cancel that reaches a task while its driver is thinking ends the run cancelled. The
// driver's turn fails because the cancel ended it, and that failure is not the task's.
func TestACancelWhileTheDriverThinksEndsTheRunCancelled(t *testing.T) {
	d := &errOnCancel{blocked: make(chan struct{})}
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), d, &recExec{})

	type ran struct {
		out Outcome
		err error
	}
	done := make(chan ran, 1)
	go func() {
		out, err := p.Run(context.Background())
		done <- ran{out, err}
	}()

	<-d.blocked
	p.Cancel("operator changed their mind", "")
	r := <-done

	if r.out.Reason != "cancelled" {
		t.Fatalf("reason = %q (err %v), want cancelled", r.out.Reason, r.err)
	}
}
