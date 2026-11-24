package client_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/client"
	"github.com/signadot/verse/pkg/task"
)

// A task that failed at launch has no stream, and what a reader needs then is not the news
// that there is no stream.
//
// Attaching to one answered `subscribe <id>: 404 page not found` — honest about the question
// it was asked, and silent about the run. Meanwhile the runner's own record said: "launch
// refused by http://verse-runner:9802: the task process exited before it served anything (exit
// status 1) — its output is on this runner's log, tagged <id>". The first is a fact about a
// route; the second ends the investigation. Getting from one to the other cost three trips
// to `kubectl logs` (git-issue ykpt9g3eh12kss9wfhn0).
func TestAttachToAFailedTaskReportsWhyItFailed(t *testing.T) {
	const reason = "launch refused: the task process exited before it served anything (exit status 1)"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(runner.AnswerHeader, "1")
		// The events route has nothing to forward — the truthful 404 that used to be the
		// whole report.
		if strings.HasSuffix(r.URL.Path, "/events") {
			http.NotFound(w, r)
			return
		}
		// And the record, which knows.
		w.Header().Set(codec.HeaderFormat, codec.Tony.String())
		b, _ := codec.Marshal(task.Snapshot{ID: "run-1", Status: task.Failed, Err: reason}, codec.Tony)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	h := client.HTTP(srv.URL).Attach("run-1")
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

// The other side of it: when the record has no end to report, only the runner's own word fails
// the task. A runner that still holds the task live has refused a stream and nothing more, so the
// stream closes with no frame and the reader asks again, as it does where the runner could not
// be asked about the task at all. A runner that knows nothing of the id has no such task, and the
// task ends failed and orphaned.
func TestARefusedStreamFailsOnlyATaskTheRunnerDoesNotHave(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snap     func(w http.ResponseWriter, r *http.Request)
		orphaned bool
	}{
		{"the remote thinks it is still running", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(codec.HeaderFormat, codec.Tony.String())
			b, _ := codec.Marshal(task.Snapshot{ID: "run-1", Status: task.Running}, codec.Tony)
			_, _ = w.Write(b)
		}, false},
		{"the remote cannot be asked", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "down", http.StatusBadGateway)
		}, false},
		{"the remote does not know the id", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(runner.AnswerHeader, "1")
				if strings.HasSuffix(r.URL.Path, "/events") {
					http.Error(w, "no stream here", http.StatusNotFound)
					return
				}
				tc.snap(w, r)
			}))
			defer srv.Close()

			h := client.HTTP(srv.URL).Attach("run-1")
			ch, _ := h.Subscribe()
			ev, open := <-ch
			if !tc.orphaned {
				if open {
					t.Fatalf("a frame %+v for a task its runner still holds, want the stream closed with none", ev)
				}
				return
			}
			if !open {
				t.Fatal("no frame at all")
			}
			if ev.Text != string(task.Failed) || !ev.Orphaned || ev.Err == "" {
				t.Fatalf("frame = %+v, want failed and orphaned, with a reason", ev)
			}
		})
	}
}

// A stream that is refused any other way has said nothing about the task, whatever the runner
// answers for the task itself short of an end: it closes with no frame.
func TestAnOutageOnTheStreamFailsNothing(t *testing.T) {
	for name, task := range map[string]int{"the task cannot be asked either": http.StatusBadGateway, "the task is not found": http.StatusNotFound} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(runner.AnswerHeader, "1")
				if strings.HasSuffix(r.URL.Path, "/events") {
					http.Error(w, "down", http.StatusBadGateway)
					return
				}
				http.Error(w, "no", task)
			}))
			defer srv.Close()
			ch, _ := client.HTTP(srv.URL).Attach("run-1").Subscribe()
			if ev, open := <-ch; open {
				t.Fatalf("a frame %+v from an outage, want the stream closed with none", ev)
			}
		})
	}
}

// A refused stream for a task the runner says has ended is that end, as the runner has it,
// with or without a reason.
func TestARefusedStreamReportsTheEndTheRunnerHolds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(runner.AnswerHeader, "1")
		if strings.HasSuffix(r.URL.Path, "/events") {
			http.Error(w, "no stream here", http.StatusNotFound)
			return
		}
		w.Header().Set(codec.HeaderFormat, codec.Tony.String())
		b, _ := codec.Marshal(task.Snapshot{ID: "run-1", Status: task.Failed, Orphaned: true}, codec.Tony)
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	ch, _ := client.HTTP(srv.URL).Attach("run-1").Subscribe()
	ev, open := <-ch
	if !open || ev.Text != string(task.Failed) || !ev.Orphaned {
		t.Fatalf("frame = %+v (open %v), want the runner's failed, orphaned end", ev, open)
	}
}

// A 404 FROM IN FRONT OF THE RUNNER IS NOT THE RUNNER'S. An ingress's default backend, a route
// not yet set up, a pod that is not the runner: each answers 404 to the stream and to the task,
// and neither is the runner saying it has no such task. The stream closes with no frame, and the
// task is asked about again.
func TestA404FromInFrontOfTheRunnerEndsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer srv.Close()
	ch, _ := client.HTTP(srv.URL).Attach("run-1").Subscribe()
	if ev, open := <-ch; open {
		t.Fatalf("a frame %+v from a server that is not the runner, want the stream closed with none", ev)
	}
}
