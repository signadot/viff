package task

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// panicDriver falls over on its first turn, the way real code does: not by returning an error.
type panicDriver struct{}

func (panicDriver) Next(context.Context, StepResult) (ToolUse, bool, error) {
	panic("the child fell over")
}

// A child whose driver panics still COMPLETES its parent, and the parent reports it as a
// failed child rather than waiting.
//
// This is the wait that hung on staging. A child is an in-process goroutine the parent owns
// and its completion used to be sent unconditionally after Run returned — which is not a
// guarantee, it is a hope, and a panic is exactly the case where the hope fails. There was no
// recover() anywhere in this package, so a panicking child sent nothing, its parent blocked on
// a channel with no timeout, and the subtree that was supposed to report had already stopped
// existing.
//
// Now the completion is structural: a defer converts a panic into an OUTCOME, so the parent's
// wait is discharged by the Go runtime rather than by the child behaving. That is what makes
// "this cannot hang" a statement about the code instead of about luck.
func TestAPanickingChildStillCompletesItsParent(t *testing.T) {
	d := &callThenDone{}
	parent := NewTask("p", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	parent.SetLog(io.Discard)
	parent.SetSpawn(func(pt *Task, mode ChildMode, req *CallAgentRequest) (*Task, error) {
		return pt.NewChild("kid", mode, req, panicDriver{}, &recExec{})
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

	select {
	case r := <-done:
		if r.err != nil || r.oc.Reason != string(Done) {
			t.Fatalf("parent ended %s (%v), want done — the panic is the CHILD's failure", r.oc.Reason, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the parent is still waiting for a child that panicked: the wait has no discharge")
	}

	// And the parent was TOLD, in the conversation, rather than merely released. An outcome
	// that never reaches the agent is the same as no outcome for everything downstream.
	var told bool
	for _, m := range d.folded {
		if strings.Contains(m, "kid") && strings.Contains(m, "panicked") {
			told = true
		}
	}
	if !told {
		t.Fatalf("the parent was released but never told what happened: %v", d.folded)
	}
}

// The same guarantee one level down: a seam that panics fails ITS task rather than the
// process, because the machine is waiting on a reply only that call can produce. A panic
// there is not just a crash — it is a wait that can never be discharged, at the one place the
// loop cannot see into.
func TestAPanickingDriverFailsItsOwnTask(t *testing.T) {
	p := NewTask("t", NewSupervisor(autoVec(), AutoResolver{}), panicDriver{}, &recExec{})
	p.SetLog(io.Discard)
	out, err := p.Run(context.Background())
	if err == nil {
		t.Fatal("a driver that panicked completed its task")
	}
	if out.Reason != string(Failed) {
		t.Errorf("reason = %q, want failed", out.Reason)
	}
	if !strings.Contains(err.Error(), "the child fell over") {
		t.Errorf("the failure does not carry what happened: %v", err)
	}
}
