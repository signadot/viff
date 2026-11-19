package client_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/runner/client"
	"github.com/signadot/verse/pkg/task"
)

// A subscribe the runner accepted says so, ahead of the runner's own frames, and only to a
// reader that asked: one the runner refused closes with no frame, and a plain subscribe never
// carries the client's frame.
func TestAnAcceptedSubscribeSaysSo(t *testing.T) {
	var refuse atomic.Bool
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	h := client.HTTP(srv.URL).Attach("p")
	ch, stop := h.(task.Accepting).SubscribeAccepted(0)
	select {
	case ev, ok := <-ch:
		if !ok || ev.Kind != task.EventAccepted {
			t.Fatalf("first frame = %+v (open %v), want the client's accepted frame", ev, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame from a subscribe the runner accepted")
	}
	stop()

	plain, stop := h.SubscribeSince(0)
	select {
	case ev, ok := <-plain:
		if ok {
			t.Fatalf("a plain subscribe carried %+v, want only the runner's frames", ev)
		}
	case <-time.After(100 * time.Millisecond):
	}
	stop()

	refuse.Store(true)
	ch, stop = h.(task.Accepting).SubscribeAccepted(0)
	defer stop()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("a refused subscribe carried %+v, want it closed with no frame", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a refused subscribe stayed open")
	}
}
