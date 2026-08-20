package trigger

import (
	"sync"
	"testing"

	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// a Handle that records control calls; its read plane stays open (never terminal).
type recHandle struct {
	mu        sync.Mutex
	cancelled bool
	ch        chan task.Frame
}

func (h *recHandle) Subscribe() (<-chan task.Frame, func()) { return h.ch, func() {} }
func (h *recHandle) SubscribeSince(int64) (<-chan task.Frame, func()) {
	return h.ch, func() {}
}
func (h *recHandle) Pause() error  { return nil }
func (h *recHandle) Resume() error { return nil }
func (h *recHandle) Cancel(string) error {
	h.mu.Lock()
	h.cancelled = true
	h.mu.Unlock()
	return nil
}
func (h *recHandle) Steer(string, string) error           { return nil }
func (h *recHandle) SetVector(*task.Vector, string) error { return nil }
func (h *recHandle) Result() string                       { return "" }
func (h *recHandle) Err() string                          { return "" }

// A resumed task (Adopt) must be steerable, not merely observed: it is re-tracked
// under its flow, so flow control (here CancelFlow) reaches it.
func TestAdoptMakesResumedTaskSteerable(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	h := &recHandle{ch: make(chan task.Frame)}
	eng.Adopt(`demo.pr."1"`, "review-demo.pr.1", h)

	if n := eng.CancelFlow(mustExpr(t, `demo.pr."1"`)); n != 1 {
		t.Fatalf("CancelFlow reached %d tasks, want 1 (adopted task not tracked)", n)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.cancelled {
		t.Fatal("adopted task was not cancelled via CancelFlow — re-tracking missing")
	}
}
