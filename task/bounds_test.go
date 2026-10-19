package task

import (
	"context"
	"io"
	"testing"
	"time"
)

// THE BOUNDS THE MACHINE ASSUMES.
//
// Property 4 — every wait names its discharge, and no discharge depends on a person choosing
// to look — is a claim about the SHELL and the seams, not about step. The enumeration cannot
// touch it: no enumeration of a bounded input set can show that an event arrives. So each wait
// is pinned here, by the thing that actually discharges it.
//
// The one that is NOT closed is stated rather than hidden, which is the whole point of writing
// them down: an argument that claims more than it shows is how the last one got confirmed and
// was wrong.

// Every phase the machine can be blocked in names what would release it. A phase that names
// nothing is a hang the loop cannot account for, and the shell fails the task rather than
// waiting on it — a bug that is found instead of one that is waited out.
func TestEveryWaitNamesItsDischarge(t *testing.T) {
	for _, ph := range []phase{phHeld, phAsk, phGate, phExec, phDelegate, phCheck} {
		if got := (state{ph: ph}).Waiting(); got == "" {
			t.Errorf("phase %s is a wait with no named discharge", ph)
		}
	}
	// The two resting phases are waits only while a child is outstanding. With none, they are
	// not patience — they are a machine with nothing to do and no way to be told, which is a
	// defect, and Waiting says so by naming nothing.
	for _, ph := range []phase{phHinge, phExit} {
		if got := (state{ph: ph}).Waiting(); got != "" {
			t.Errorf("phase %s with no children named a discharge (%q); it has none", ph, got)
		}
		if got := (state{ph: ph, children: []string{"kid"}}).Waiting(); got == "" {
			t.Errorf("phase %s with a child outstanding named no discharge", ph)
		}
	}
	if got := (state{ph: phEnd}).Waiting(); got != "" {
		t.Errorf("a terminated machine is waiting for %q", got)
	}
}

// A GATE'S WAIT is discharged by policy, not by patience. Nobody answers this question — the
// resolver is a registry with no client — and the task still ends, because a cancel breaks it.
// The other exit is an operator's say (TestASayWithdrawsAStandingQuestion).
//
// What must NOT remain is a wait whose only discharge is a person choosing to look. A resolver
// that carries neither a deadline nor an answerer is one this loop can now end, and the install
// is where such a resolver should be refused or warned about — that part is not here, and saying
// so is the point.
func TestAStandingQuestionIsDischargedByACancel(t *testing.T) {
	d := &scriptDriver{steps: []ToolUse{tool("A")}}
	reg := NewRegistry()
	p := NewTask("t", toolSup(askOn("A"), reg), d, &recExec{})
	p.SetLog(io.Discard)
	reg.OnPost(func(Pending) { p.Cancel("nobody is going to answer that", "") })

	done := make(chan Outcome, 1)
	go func() { oc, _ := p.Run(context.Background()); done <- oc }()
	select {
	case oc := <-done:
		if oc.Reason != string(Cancelled) {
			t.Errorf("reason %q, want cancelled", oc.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a question nobody will answer held the task past a cancel")
	}
}

// AN EXECUTOR THAT HONOURS ITS CONTEXT is released by a cancel, at the seam, without the loop
// having to reach into it.
func TestACancelReachesAnExecutorThatHonoursItsContext(t *testing.T) {
	entered := make(chan struct{})
	ex := &blockingExec{on: "slow", entered: entered, release: make(chan struct{})} // never released
	d := &scriptDriver{steps: []ToolUse{tool("slow")}}
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), d, ex)
	p.SetLog(io.Discard)

	done := make(chan Outcome, 1)
	go func() { oc, _ := p.Run(context.Background()); done <- oc }()
	<-entered
	p.Cancel("stop", "")
	select {
	case oc := <-done:
		if oc.Reason != string(Cancelled) {
			t.Errorf("reason %q, want cancelled", oc.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancel never reached an executor that selects on ctx.Done")
	}
}

// AN EXECUTOR THAT IGNORES ITS CONTEXT is a defect of THAT EXECUTOR, and this pins the
// consequence rather than pretending otherwise.
//
// `Execute must honour ctx` is stated as an obligation on the interface (see Executor). The
// loop is one goroutine that pauses between actions, so an action that ignores cancellation is
// a wait the loop can neither see into nor interrupt — `run_bash {command: "sleep 120"}` is a
// task nobody can steer for two minutes. There is no fix available here: the loop cannot kill
// somebody else's function call. What it can do is not claim to.
//
// So this asserts the honest thing: the task is NOT released while such an executor runs, and
// IS released the moment it returns. A future change that made the loop abandon an in-flight
// action would break this test, and it should — abandoning it would leave the action running
// with nobody accounting for it, which is worse than waiting for it.
func TestAContextIgnoringExecutorHoldsTheTaskAndThatIsItsDefect(t *testing.T) {
	ex := &deafExec{release: make(chan struct{}), entered: make(chan struct{})}
	d := &scriptDriver{steps: []ToolUse{tool("deaf")}}
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), d, ex)
	p.SetLog(io.Discard)

	done := make(chan Outcome, 1)
	go func() { oc, _ := p.Run(context.Background()); done <- oc }()
	<-ex.entered
	p.Cancel("stop", "")

	select {
	case oc := <-done:
		t.Fatalf("the loop returned %q while an action was still running: it abandoned an "+
			"in-flight action rather than waiting for it", oc.Reason)
	case <-time.After(100 * time.Millisecond):
		// Correct: the loop is waiting, and Executor's doc says whose fault that is.
	}
	close(ex.release)
	select {
	case oc := <-done:
		if oc.Reason != string(Cancelled) {
			t.Errorf("reason %q, want cancelled once the executor returned", oc.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the task never ended even after its executor returned")
	}
}

// deafExec runs to completion whatever its context says — the defect Executor names.
type deafExec struct {
	entered chan struct{}
	release chan struct{}
	once    bool
}

func (e *deafExec) Execute(_ context.Context, tu ToolUse) (StepResult, []ToolUse, error) {
	if !e.once {
		e.once = true
		close(e.entered)
	}
	<-e.release // deliberately not selecting on ctx.Done
	return StepResult{Use: tu, Output: "eventually"}, nil, nil
}

// A PAUSE IS A HANG BY REQUEST, and stays one. It is the exception that proves the rule: the
// discharge is an operator, and the operator asked for it. A cancel still outranks it, which is
// what keeps "by request" from meaning "forever".
func TestAPauseIsAHangByRequestAndACancelOutranksIt(t *testing.T) {
	d := &scriptDriver{steps: []ToolUse{tool("A"), tool("B")}}
	ex := &recExec{}
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), d, ex)
	p.SetLog(io.Discard)
	p.Pause("")

	done := make(chan Outcome, 1)
	go func() { oc, _ := p.Run(context.Background()); done <- oc }()
	select {
	case oc := <-done:
		t.Fatalf("a paused task ran to %q", oc.Reason)
	case <-time.After(100 * time.Millisecond):
	}
	if len(ex.order) != 0 {
		t.Fatalf("a paused task took actions: %v", ex.order)
	}
	p.Cancel("enough", "")
	select {
	case oc := <-done:
		if oc.Reason != string(Cancelled) {
			t.Errorf("reason %q, want cancelled — cancel outranks pause", oc.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancel did not break a pause")
	}
}
