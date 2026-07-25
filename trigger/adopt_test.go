package trigger

import (
	"sync"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/process"
)

// a ProcHandle that records control calls; its read plane stays open (never terminal).
type recHandle struct {
	mu        sync.Mutex
	cancelled bool
	ch        chan process.Frame
}

func (h *recHandle) Subscribe() (<-chan process.Frame, func()) { return h.ch, func() {} }
func (h *recHandle) Pause()                                    {}
func (h *recHandle) Resume()                                   {}
func (h *recHandle) Cancel(string)                             { h.mu.Lock(); h.cancelled = true; h.mu.Unlock() }
func (h *recHandle) Steer(string, string)                      {}
func (h *recHandle) SetVector(*process.Vector, string)         {}
func (h *recHandle) Result() string                            { return "" }

// A resumed process (Adopt) must be steerable, not merely observed: it is re-tracked
// under its flow, so flow control (here CancelFlow) reaches it.
func TestAdoptMakesResumedProcessSteerable(t *testing.T) {
	eng := New(entity.NewMemStore(), process.AutoResolver{})
	h := &recHandle{ch: make(chan process.Frame)}
	eng.Adopt("demo:pr:1", "review-demo:pr:1", h)

	if n := eng.CancelFlow(mustExpr(t, "demo:pr:1")); n != 1 {
		t.Fatalf("CancelFlow reached %d processes, want 1 (adopted process not tracked)", n)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.cancelled {
		t.Fatal("adopted process was not cancelled via CancelFlow — re-tracking missing")
	}
}
