package task

import (
	"context"
	"sync"
)

// pauseGate is one node in a per-task pause chain that mirrors the task
// tree (a child's gate links to its parent's, set in NewChild). Pausing a gate
// holds that task AND its whole subtree: descendants observe an ancestor's
// paused state by walking the chain at their own boundary. This makes pause a
// genuine subtree operation without traversing the Run-goroutine-owned children
// map from an external caller — each gate carries its own mutex, so Pause/Resume
// are concurrency-safe. Cancel still outranks pause (ctx.Done breaks the hold).
type pauseGate struct {
	mu     sync.Mutex
	paused bool
	resume chan struct{}
	parent *pauseGate
}

func (g *pauseGate) pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused {
		g.paused = true
		g.resume = make(chan struct{})
	}
}

func (g *pauseGate) unpause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		g.paused = false
		close(g.resume)
		g.resume = nil
	}
}

// heldOn returns the resume channel of the nearest paused gate in the chain (self
// first), or nil if nothing in the chain is paused.
func (g *pauseGate) heldOn() chan struct{} {
	for n := g; n != nil; n = n.parent {
		n.mu.Lock()
		paused, ch := n.paused, n.resume
		n.mu.Unlock()
		if paused {
			return ch
		}
	}
	return nil
}

// hold blocks while any gate in the chain is paused, re-checking after each
// resume. Returns ctx.Err() if cancelled while held — cancel outranks pause.
func (g *pauseGate) hold(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ch := g.heldOn()
		if ch == nil {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Pause holds this task and its whole subtree at their next boundaries — a
// subtree operation. Applied effects stay applied; the cursor just stops between
// actions. Cancel outranks pause; a say buffers until resume. Idempotent.
//
// The gate is the SUBTREE mechanism and the input is the INTERVENTION: the chain is what lets
// a descendant observe an ancestor's pause at its own boundary, and the input is what lets the
// machine treat this as an operator act — which it is, and which is why it withdraws a
// question standing at a gate. The task is about to be suspended, so a question about an
// action it will not reach is better withdrawn than left standing across the hold.
func (p *Task) Pause() error {
	p.pauseGate.pause()
	p.logf("⏸ pause (subtree)")
	p.emit(Frame{Kind: EvStatus, Text: "paused"})
	p.post(inPause{})
	return nil
}

// Resume lifts this task's pause; descendants held only by this gate proceed.
//
// It is posted as an input for symmetry and for the read plane, and the machine deliberately
// does nothing with it — see machine.go's inResume, which is the one place this design's
// precedence rule is not followed and says why.
func (p *Task) Resume() error {
	p.pauseGate.unpause()
	p.logf("▶ resume (subtree)")
	p.emit(Frame{Kind: EvStatus, Text: "resumed"})
	p.post(inResume{})
	return nil
}
