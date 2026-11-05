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
	parent := NewTask("p", toolSup(allowAll(), AutoResolver{}), d, &recExec{})
	parent.SetLog(io.Discard)
	parent.SetDelegate(func(pt *Task, req *CallAgentRequest) (*Task, error) {
		return pt.NewChild("kid", req, panicDriver{}, &recExec{}), nil
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

// boomCheck panics where the loop calls it INLINE, on the Run goroutine itself — not in a
// seam, which has its own recover.
type boomCheck struct{}

func (boomCheck) Check(string) (string, error) { panic("the contract check fell over") }

// The second layer, which the first test does not reach: a panic on the child's OWN goroutine,
// off the seam path. The answer contract, the delegate factory, emit and the
// executor calls that unwind frames at teardown all run there.
//
// It matters because the two recovers cover different things and only one of them is reached
// by a panicking driver. A child that fell over inside its own shell must still report an
// outcome, or its parent waits on a subtree that has stopped existing — the original bug by
// another route.
func TestAPanicOnTheChildsOwnGoroutineStillCompletesItsParent(t *testing.T) {
	d := &callThenDone{}
	parent := NewTask("p", toolSup(allowAll(), AutoResolver{}), d, &recExec{})
	parent.SetLog(io.Discard)
	parent.SetDelegate(func(pt *Task, req *CallAgentRequest) (*Task, error) {
		c, err := pt.NewChild("kid", req, &scriptDriver{}, &recExec{}), error(nil)
		if err == nil {
			c.SetLog(io.Discard)
			c.SetAnswerCheck(boomCheck{})
		}
		return c, err
	})

	done := make(chan Outcome, 1)
	go func() { oc, _ := parent.Run(context.Background()); done <- oc }()
	select {
	case oc := <-done:
		if oc.Reason != string(Done) {
			t.Fatalf("parent ended %s, want done", oc.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the parent is waiting for a child that panicked off the seam path")
	}
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

// And the root, which has no parent to be guarded by: a panic on its inline path ends the TASK
// rather than the process, and — the part that is easy to miss — it still says so on the read
// plane.
//
// A loop that dies without a terminal frame is worse than a crash and quieter: every subscriber
// stays open on a task that has stopped speaking, and the durable entity says `running`
// forever. That is the leak the terminal-frame handling exists to prevent, reachable by a
// second route.
func TestAPanickingRootFailsTheTaskAndStillSaysSo(t *testing.T) {
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), &scriptDriver{}, &recExec{})
	p.SetLog(io.Discard)
	p.SetAnswerCheck(boomCheck{})
	ch, unsub := p.Subscribe()
	defer unsub()

	out, err := p.Run(context.Background())
	if err == nil {
		t.Fatal("a panicking loop reported success")
	}
	if out.Reason != string(Failed) {
		t.Errorf("reason %q, want failed", out.Reason)
	}
	if !strings.Contains(p.Err(), "panicked") {
		t.Errorf("the entity would carry no account of why: %q", p.Err())
	}
	terminalSaysWhy(t, ch, "panicked")
}

// terminalSaysWhy drains a task's stream and checks it ended with a terminal frame carrying the
// reason. The mirror projects a task's err off that frame; a frame without it left a failed
// task's entity with its result and no err, and a charter's onFailed reading err wrote nothing
// (zm5vx3z2h12ks5x8vhn0).
func terminalSaysWhy(t *testing.T, ch <-chan Event, why string) {
	t.Helper()
	var terminal *Event
	for f := range ch { // the stream ENDS after the terminal, so this drains and stops
		if f.Kind == EventStatus && Status(f.Text).Terminal() {
			terminal = &f
		}
	}
	if terminal == nil {
		t.Fatal("the loop died without a terminal frame: every subscriber is left listening to " +
			"a task that has stopped speaking")
	}
	if !strings.Contains(terminal.Err, why) {
		t.Errorf("the terminal frame's err is %q, want it to say %q", terminal.Err, why)
	}
}

// The same guarantee one level down: a seam that panics fails ITS task rather than the
// process, because the machine is waiting on a reply only that call can produce. A panic
// there is not just a crash — it is a wait that can never be discharged, at the one place the
// loop cannot see into.
func TestAPanickingDriverFailsItsOwnTask(t *testing.T) {
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), panicDriver{}, &recExec{})
	p.SetLog(io.Discard)
	ch, unsub := p.Subscribe()
	defer unsub()
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
	terminalSaysWhy(t, ch, "the child fell over")
}
