package runner_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// A task that failed at spawn has no stream, and what a reader needs then is not the news
// that there is no stream.
//
// Attaching to one answered `subscribe <id>: 404 page not found` — honest about the question
// it was asked, and silent about the run. Meanwhile the runner's own record said: "spawn
// refused by http://coderunner:9802: the task process exited before it served anything (exit
// status 1) — its output is on this runner's log, tagged <id>". The first is a fact about a
// route; the second ends the investigation. Getting from one to the other cost three trips
// to `kubectl logs` (git-issue ykpt9g3eh12kss9wfhn0).
func TestAttachToAFailedTaskReportsWhyItFailed(t *testing.T) {
	const reason = "spawn refused: the task process exited before it served anything (exit status 1)"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The events route has nothing to forward — the truthful 404 that used to be the
		// whole report.
		if strings.HasSuffix(r.URL.Path, "/events") {
			http.NotFound(w, r)
			return
		}
		// And the record, which knows.
		w.Header().Set(codec.HeaderFormat, codec.Tony.String())
		b, _ := codec.Marshal(task.Instance{ID: "run-1", Status: task.Failed, Err: reason}, codec.Tony)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	h := runner.HTTP(srv.URL).Attach("run-1")
	if h == nil {
		t.Fatal("no handle for a task the runner knows about")
	}
	ch, _ := h.Subscribe()
	ev, open := <-ch
	if !open {
		t.Fatal("a failed attach produced no frame; a reader waits forever")
	}
	if ev.Err != reason {
		t.Fatalf("reason = %q, want the record's own account (%q)", ev.Err, reason)
	}
	if ev.Text != string(task.Failed) {
		t.Fatalf("status = %q, want %q", ev.Text, task.Failed)
	}
}

// The other side of it: when the record has nothing better to say, the transport error is
// still the best available account and must not be swallowed.
//
// Two cases, and they fail differently for the same reason — a live task and an unknown one
// both leave the reader with only "the stream refused me", which is exactly what to report.
func TestAttachKeepsTheTransportErrorWhenTheRecordHasNoBetterAccount(t *testing.T) {
	for _, tc := range []struct {
		name string
		snap func(w http.ResponseWriter, r *http.Request)
	}{
		{"the remote thinks it is still running", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(codec.HeaderFormat, codec.Tony.String())
			b, _ := codec.Marshal(task.Instance{ID: "run-1", Status: task.Running}, codec.Tony)
			_, _ = w.Write(b)
		}},
		{"the remote does not know the id", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/events") {
					http.Error(w, "no stream here", http.StatusNotFound)
					return
				}
				tc.snap(w, r)
			}))
			defer srv.Close()

			h := runner.HTTP(srv.URL).Attach("run-1")
			ch, _ := h.Subscribe()
			ev, open := <-ch
			if !open {
				t.Fatal("no frame at all")
			}
			if !strings.Contains(ev.Err, "no stream here") {
				t.Fatalf("reason = %q, want the transport error kept", ev.Err)
			}
		})
	}
}
