package trigger

import (
	"sync"
	"testing"

	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// a Handle that records control calls; its read plane stays open (never terminal).
type recHandle struct {
	mu     sync.Mutex
	paused bool
	ch     chan task.Frame
}

func (h *recHandle) Subscribe() (<-chan task.Frame, func()) { return h.ch, func() {} }
func (h *recHandle) SubscribeSince(int64) (<-chan task.Frame, func()) {
	return h.ch, func() {}
}
func (h *recHandle) Pause() error {
	h.mu.Lock()
	h.paused = true
	h.mu.Unlock()
	return nil
}
func (h *recHandle) Resume() error                        { return nil }
func (h *recHandle) Cancel(string) error                  { return nil }
func (h *recHandle) Steer(string, string) error           { return nil }
func (h *recHandle) SetVector(*task.Vector, string) error { return nil }
func (h *recHandle) Result() string                       { return "" }
func (h *recHandle) Err() string                          { return "" }

// A resumed task (Adopt) must be steerable, not merely observed: it is re-tracked
// under its flow, so a selection of its subject reaches it and flow control holds it.
func TestAdoptMakesResumedTaskSteerable(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	h := &recHandle{ch: make(chan task.Frame)}
	eng.Adopt(`demo.pr."1"`, "review-demo.pr.1", h)

	if n := len(eng.SelectFlow(mustSelect(t, "*", `demo.pr."1"`))); n != 1 {
		t.Fatalf("the selection reached %d tasks, want 1 (adopted task not tracked)", n)
	}
	eng.HoldTask("review-demo.pr.1", true)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.paused {
		t.Fatal("adopted task was not held through its handle — re-tracking missing")
	}
}
