package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/task"
)

// A cancel reaches run_bash even when the command left something running.
//
// task.Executor states that Execute must honour its context, and this executor did not — which
// made verse's own executor the counterexample to its own interface. exec.CommandContext looks
// like it honours it and does not: stdout is a buffer, so os/exec copies through a pipe and Wait
// waits for the COPY rather than for the process. Whatever the command backgrounded inherits
// that pipe and holds it open forever.
//
// `sleep 300 &` is the whole reproduction, and it is not a corner case — a coding agent starting
// a dev server writes exactly this. Before WaitDelay the task could not be cancelled at all: not
// by an operator, not by a parent's subtree cancel, not by the runner shutting down.
func TestACancelReachesRunBashPastABackgroundedChild(t *testing.T) {
	e := &fsExec{root: t.TempDir(), log: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan task.StepResult, 1)
	go func() {
		out, _, _ := e.Execute(ctx, task.ToolUse{Name: "run_bash",
			Input: map[string]any{"command": "sleep 300 & echo started; sleep 300"}})
		done <- out
	}()

	time.Sleep(200 * time.Millisecond) // let it get going
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the cancel never reached run_bash: the task cannot be stopped by anybody")
	}
}

// A command that exits cleanly but detaches something is NOT reported as a failure. It
// succeeded; what it left behind is holding the pipe, and telling the agent its command broke
// would send it to debug a command that worked.
func TestABackgroundedChildDoesNotLookLikeAFailedCommand(t *testing.T) {
	e := &fsExec{root: t.TempDir(), log: io.Discard}
	start := time.Now()
	out, _, err := e.Execute(context.Background(), task.ToolUse{Name: "run_bash",
		Input: map[string]any{"command": "sleep 300 & echo started"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if took := time.Since(start); took > orphanWaitDelay+5*time.Second {
		t.Fatalf("waited %s on a command that had already exited", took.Round(time.Second))
	}
	if strings.HasPrefix(out.Output, "ERROR:") {
		t.Fatalf("a command that succeeded was reported as an executor failure:\n%s", out.Output)
	}
	for _, want := range []string{"exit: 0", "started", "still holds its output"} {
		if !strings.Contains(out.Output, want) {
			t.Errorf("the result does not carry %q:\n%s", want, out.Output)
		}
	}
}

// And an ordinary command pays none of this: nothing holds the pipe, so nothing waits.
func TestAnOrdinaryCommandDoesNotWait(t *testing.T) {
	e := &fsExec{root: t.TempDir(), log: io.Discard}
	start := time.Now()
	out, _, err := e.Execute(context.Background(), task.ToolUse{Name: "run_bash",
		Input: map[string]any{"command": "echo hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > orphanWaitDelay {
		t.Fatalf("an ordinary command waited %s — the delay applies where it should not", took)
	}
	if !strings.Contains(out.Output, "hi") {
		t.Errorf("output = %q", out.Output)
	}
}
