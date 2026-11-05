package task

import (
	"context"
	"testing"
	"time"
)

// ENDING A TURN is how a parent waits for a child, and it costs nothing.
//
// The loop joins children only where a turn ends: `!more` with children outstanding blocks on
// awaitChild, folds the completion, and calls the driver again. So a parent that has nothing
// left to do should stop — it blocks in Go, spends no tokens, and is re-invoked the moment a
// child reports.
//
// Nothing told the model that. call_agent said only that a result "comes back when it
// finishes", so a model that wanted to wait reached for the wait it could see —
// `run_bash {command: "sleep 120; echo done"}` — and paid a model round trip per poll to
// learn nothing. A review on staging spent eighteen minutes and twenty-five boundaries mostly
// asleep that way (git-issue 91r674qzh12ks35vfnn0).
//
// This pins the mechanism the advice now describes: end the turn, get called again with the
// result. If this ever stops being true, the tool description below becomes a lie and the
// sleeping comes back.
func TestEndingATurnIsHowAParentWaits(t *testing.T) {
	// Turn 1 delegates. Turn 2 (and every turn after) ends the turn — which is the whole
	// point: the driver never asks to sleep, and never needs to.
	d := &scriptDriver{steps: []ToolUse{{
		Name:      "call_agent",
		CallAgent: &CallAgentRequest{Agent: "kid", Prompt: "do a thing"},
	}}}
	parent := NewTask("p", toolSup(allowAll(), AutoResolver{}), d, &recExec{})

	childRan := make(chan struct{})
	parent.SetDelegate(func(p *Task, req *CallAgentRequest) (*Task, error) {
		// A child that takes a moment, so the parent genuinely has to wait rather than
		// finding the result already there.
		kid := &slowDriver{after: 60 * time.Millisecond, done: childRan}
		return p.NewChild("c", req, kid, &recExec{}), nil
	})

	oc, err := parent.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-childRan:
	default:
		t.Fatal("the parent finished without the child having run")
	}
	if oc.Reason != string(Done) {
		t.Fatalf("outcome %q, want done", oc.Reason)
	}
	// Called again AFTER the child was folded in: turn 1 delegated, turn 2 ended the turn
	// while the child was still running, and turn 3 is the one the join produced.
	if d.i < 1 {
		t.Fatalf("the driver was asked %d times; a parent that waits is re-invoked", d.i)
	}
}

// slowDriver finishes after a delay, so the parent has to wait for it.
type slowDriver struct {
	after time.Duration
	done  chan struct{}
	fired bool
}

func (d *slowDriver) Next(context.Context, StepResult) (ToolUse, bool, error) {
	if !d.fired {
		d.fired = true
		time.Sleep(d.after)
		close(d.done)
	}
	return ToolUse{}, false, nil
}
