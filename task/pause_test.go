package task

import (
	"context"
	"testing"
	"time"
)

// The pause chain is a subtree operation: pausing a gate holds its descendants,
// not its ancestors.
func TestPauseGateChainIsSubtree(t *testing.T) {
	root := &pauseGate{}
	child := &pauseGate{parent: root}

	if child.heldOn() != nil {
		t.Fatal("child held before any pause")
	}
	root.pause()
	if child.heldOn() == nil {
		t.Fatal("pausing root must hold the child (subtree)")
	}
	root.unpause()
	if child.heldOn() != nil {
		t.Fatal("resuming root must release the child")
	}
	child.pause()
	if child.heldOn() == nil {
		t.Fatal("child pause must hold the child")
	}
	if root.heldOn() != nil {
		t.Fatal("child pause must NOT hold the parent (pause is downward only)")
	}
}

// A running loop paused at a boundary holds there (no further boundaries) until
// resumed, then continues.
func TestSubtreePauseHoldsAndResumes(t *testing.T) {
	reached := make(chan int, 16)
	d := &scriptDriver{steps: []ToolUse{tool("a"), tool("b"), tool("c")}}
	p := NewTask("t", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	paused := make(chan struct{})
	p.onEnterBoundary = func(seq int) {
		reached <- seq
		if seq == 2 {
			p.Pause()
			close(paused)
		}
	}

	done := make(chan struct{})
	go func() { p.Run(context.Background()); close(done) }()

	<-paused
	if got := <-reached; got != 1 {
		t.Fatalf("first boundary = %d, want 1", got)
	}
	if got := <-reached; got != 2 {
		t.Fatalf("second boundary = %d, want 2", got)
	}
	// held at boundary 2: no further boundary should arrive while paused
	select {
	case seq := <-reached:
		t.Fatalf("advanced to boundary %d while paused", seq)
	case <-time.After(100 * time.Millisecond):
	}

	p.Resume()
	if got := <-reached; got != 3 {
		t.Fatalf("after resume, next boundary = %d, want 3", got)
	}
	<-done
}

// Cancel outranks pause: a cancelled, paused loop still exits.
func TestCancelOutranksPause(t *testing.T) {
	d := &scriptDriver{steps: []ToolUse{tool("a"), tool("b"), tool("c")}}
	p := NewTask("t", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	paused := make(chan struct{})
	p.onEnterBoundary = func(seq int) {
		if seq == 2 {
			p.Pause()
			close(paused)
		}
	}

	done := make(chan struct{})
	go func() { p.Run(context.Background()); close(done) }()

	<-paused
	p.Cancel("test") // while held at boundary 2
	select {
	case <-done: // exited despite being paused
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not break the pause hold")
	}
}
