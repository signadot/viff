package runnerd

import (
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/task"
)

// A task's stream that drops, and then a subscribe the client reports as a failed frame, both
// while the task's process runs, are neither of them the task ending: the runner resumes from the
// last cursor, and remembers the end the task really reached.
func TestAStreamThatDropsIsResumed(t *testing.T) {
	defer func(s time.Duration) { settleTimeout = s }(settleTimeout)
	defer SetLingerAfterEnd(0)()
	settleTimeout = 50 * time.Millisecond

	running := task.Frame{Kind: task.EvStatus, Text: string(task.Running)}
	f := &fakeProcess{streams: [][]task.Frame{
		{withCursor(running, 3)}, // and the connection drops
		{{Kind: task.EvStatus, Text: string(task.Failed), Err: "subscribe t1: 502 Bad Gateway"}},
		{withCursor(running, 4), withCursor(task.Frame{Kind: task.EvStatus, Text: string(task.Done)}, 5)},
	}}
	p := &proc{id: "t1", rt: f, ready: make(chan struct{}), gone: make(chan struct{})}
	(&SubprocessHandler{SubprocessConfig: SubprocessConfig{Log: io.Discard}}).supervise(p)

	final, ok := p.snapshot()
	if !ok || final.Status != task.Done || final.Result != "the answer" {
		t.Fatalf("remembered %+v, want the task's own end", final)
	}
	if want := []int64{0, 3, 3}; !slices.Equal(f.since, want) {
		t.Fatalf("subscribed since %v, want %v", f.since, want)
	}
}

func withCursor(f task.Frame, c int64) task.Frame { f.Cursor = c; return f }

// fakeProcess is a task's process as the runner reaches it: each subscribe is handed the next of
// streams, and the snapshot says the task runs until the last one is handed out.
type fakeProcess struct {
	mu      sync.Mutex
	streams [][]task.Frame
	since   []int64
}

func (f *fakeProcess) Spawn(task.Launch) task.Handle { return nil }
func (f *fakeProcess) Attach(string) task.Handle     { return fakeHandle{f: f} }
func (f *fakeProcess) Ref() string                   { return "fake" }
func (f *fakeProcess) ListTasks() ([]task.Instance, error) {
	return nil, nil
}

func (f *fakeProcess) TaskSnapshot(id string) (task.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.streams) == 0 {
		return task.Instance{ID: id, Status: task.Done, Result: "the answer"}, nil
	}
	return task.Instance{ID: id, Status: task.Running}, nil
}

type fakeHandle struct {
	task.Handle
	f *fakeProcess
}

func (h fakeHandle) SubscribeSince(since int64) (<-chan task.Frame, func()) {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	h.f.since = append(h.f.since, since)
	out := make(chan task.Frame, 8)
	if len(h.f.streams) > 0 {
		for _, fr := range h.f.streams[0] {
			out <- fr
		}
		h.f.streams = h.f.streams[1:]
	}
	close(out)
	return out, func() {}
}
