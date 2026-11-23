package runnerd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/client"
	"github.com/signadot/verse/pkg/task"
)

// A TASK HAS ONE END, on its stream and its snapshot alike, when its own end and its lease's
// arrive together. Whichever the runner takes first stands: a lease that runs out after the
// task's terminal frame was followed ends nothing, and a terminal frame followed after the
// lease ended the task is not kept.
func TestATaskWhoseLeaseRunsOutAsItEndsHasOneEnd(t *testing.T) {
	done := task.Event{Kind: task.EventStatus, Text: string(task.Done), Cursor: 2}
	working := task.Event{Kind: task.EventSay, Text: "working", Cursor: 1}

	t.Run("the end followed first", func(t *testing.T) {
		p := &proc{id: "t", known: task.Snapshot{ID: "t"}}
		p.record(working)
		p.record(done)
		if end, decided := p.endOrphaned("no reader for 4m"); decided || end.Status != task.Done {
			t.Fatalf("the lease ended a task whose end was already followed (%s, decided %v)", end.Status, decided)
		}
		if _, ended := p.snapshot(); ended {
			t.Fatal("a last word was set by a lease that ended nothing")
		}
	})
	t.Run("the lease first", func(t *testing.T) {
		p := &proc{id: "t", known: task.Snapshot{ID: "t"}}
		p.record(working)
		if _, decided := p.endOrphaned("no reader for 4m"); !decided {
			t.Fatal("the lease did not end a task with no end")
		}
		p.record(done)
		final, _ := p.snapshot()
		events := p.eventsSince(0)
		last := events[len(events)-1]
		if last.Kind == task.EventStatus && task.Status(last.Text).Terminal() && last.Text != string(final.Status) {
			t.Fatalf("the stream ends %s and the snapshot %s", last.Text, final.Status)
		}
		if final.Status != task.Failed || !final.Orphaned {
			t.Fatalf("snapshot = %s orphaned=%v, want failed and orphaned", final.Status, final.Orphaned)
		}
	})
}

// A SUBSCRIBE ANSWERED AFTER THE LEASE RAN OUT IS ANSWERED WITH THE RUNNER'S END. The process
// may say done on the stream as the lease ends the task, and the reader is told what the
// snapshot says: failed and orphaned, the end the runner decided.
func TestASubscribeAnsweredAfterTheLeaseRanOutIsTheRunnersEnd(t *testing.T) {
	h := &SubprocessHandler{tasks: map[string]*proc{}, Log: io.Discard}
	l := &runner.Launch{}
	l.ID = "t"
	p := &proc{id: "t", known: task.Snapshot{ID: "t"}, ready: make(chan struct{}), gone: make(chan struct{}),
		done: make(chan struct{}), lease: newLease(l, time.Hour), abort: func(error) {}}
	close(p.ready)
	h.tasks["t"] = p

	// The task's process answers the subscribe as the lease runs out, and says done.
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.lease.mu.Lock()
		p.lease.over = true
		p.lease.mu.Unlock()
		sse, _ := codec.NewSSE(w, r)
		_ = sse.Send(task.Event{Kind: task.EventStatus, Task: "t", Text: string(task.Done), Cursor: 2})
		sse.Flush()
	}))
	defer child.Close()
	target, _ := url.Parse(child.URL)
	p.prox = httputil.NewSingleHostReverseProxy(target)
	p.prox.FlushInterval = -1
	front := httptest.NewServer(h.HTTPHandler())
	defer front.Close()

	ch, stop := client.HTTP(front.URL).Attach("t").(task.Accepting).SubscribeAccepted(1)
	defer stop()
	var end task.Event
	for ev := range ch {
		if ev.Kind == task.EventStatus && task.Status(ev.Text).Terminal() {
			end = ev
			break
		}
	}
	snap, _ := p.snapshot()
	if snap.Status != task.Failed || !snap.Orphaned {
		t.Fatalf("snapshot = %s orphaned=%v, want the lease's end", snap.Status, snap.Orphaned)
	}
	if end.Text != string(snap.Status) || end.Orphaned != snap.Orphaned {
		t.Fatalf("the stream ends %s orphaned=%v and the snapshot %s orphaned=%v", end.Text, end.Orphaned, snap.Status, snap.Orphaned)
	}
}
