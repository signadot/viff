package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/client"
)

// taskServer answers GET /tasks/<id> with what next says, one call at a time, and points the
// CLI at itself.
func taskServer(t *testing.T, next func(n int) (int, string)) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, body := next(int(calls.Add(1)))
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	rootCmd() // the config layers, as a command would have loaded them
	at(t, strings.TrimPrefix(ts.URL, "http://"))
	return &calls
}

func always(status int, body string) func(int) (int, string) {
	return func(int) (int, string) { return status, body }
}

// A task read that gave no snapshot is the server's words, and never a statement about the
// task: a worker refused its task is not told the task is not brokered.
func TestATaskReadThatGaveNoSnapshotIsTheServersWords(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		is     func(error) bool
	}{
		{http.StatusNotFound, "in your view, no task \"t1\" stands", client.NotInView},
		{http.StatusForbidden, "refused: runner:w holds nothing at verse.task.instance.t1", client.Refused},
		{http.StatusServiceUnavailable, "the store could not be read: timeout", client.Unavailable},
	} {
		taskServer(t, always(c.status, c.body))
		_, err := brokerOf("t1")
		if err == nil || !strings.Contains(err.Error(), c.body) {
			t.Errorf("%d: err = %v, want the server's words", c.status, err)
		}
		if err != nil && strings.Contains(err.Error(), "not a brokered task") {
			t.Errorf("%d: err = %v, which describes a task that was not read", c.status, err)
		}
		if !c.is(err) {
			t.Errorf("%d: the status did not travel: %v", c.status, err)
		}
	}
	// A task that WAS read, and is not brokered, is said to be that.
	taskServer(t, always(http.StatusOK, `{id: t1, runner: local}`))
	if _, err := brokerOf("t1"); err == nil || !strings.Contains(err.Error(), "not a brokered task") {
		t.Errorf("a local task: err = %v", err)
	}
	taskServer(t, always(http.StatusOK, `{id: t1, runner: broker, dialURL: "http://127.0.0.1:1/b/"}`))
	if base, err := brokerOf("t1"); err != nil || base != "http://127.0.0.1:1/b" {
		t.Errorf("a brokered task: (%q, %v)", base, err)
	}
}

func waitContext(t *testing.T, ctx context.Context) (*cli.Context, *bytes.Buffer) {
	var out bytes.Buffer
	return &cli.Context{Out: nopCloser{&out}, Err: nopCloser{&bytes.Buffer{}},
		In: io.NopCloser(strings.NewReader("")), Go: ctx}, &out
}

// --wait outlasts a store that does not answer for a while: a read that did not happen is
// asked again, and the result is printed when the task ends.
func TestWaitAsksAgainWhenAReadDidNotHappen(t *testing.T) {
	calls := taskServer(t, func(n int) (int, string) {
		switch {
		case n <= 2:
			return http.StatusOK, `{status: running}`
		case n <= 4:
			return http.StatusServiceUnavailable, "the store could not be read: timeout"
		}
		return http.StatusOK, `{status: completed, result: "done it"}`
	})
	cc, out := waitContext(t, t.Context())
	if err := waitForResult(cc, "t1"); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if calls.Load() != 5 || !strings.Contains(out.String(), "t1 completed") || !strings.Contains(out.String(), "done it") {
		t.Fatalf("after %d reads the wait printed %q", calls.Load(), out.String())
	}
}

// --wait ends at once on a task that is not in the asker's view: asking again changes nothing.
func TestWaitEndsOnATaskThatIsNotInView(t *testing.T) {
	calls := taskServer(t, always(http.StatusNotFound, `in your view, no task "t1" stands`))
	cc, _ := waitContext(t, t.Context())
	err := waitForResult(cc, "t1")
	if !client.NotInView(err) || !strings.Contains(err.Error(), "in your view") {
		t.Fatalf("wait: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("it asked %d times", calls.Load())
	}
}

// --wait gives up at no time of its own: a task still running is waited on until the command
// is interrupted, and the interruption says what the wait last knew.
func TestWaitWaitsUntilInterrupted(t *testing.T) {
	calls := taskServer(t, always(http.StatusOK, `{status: running}`))
	ctx, cancel := context.WithCancel(t.Context())
	cc, _ := waitContext(t, ctx)
	done := make(chan error, 1)
	go func() { done <- waitForResult(cc, "t1") }()
	select {
	case err := <-done:
		t.Fatalf("the wait ended by itself after %d reads: %v", calls.Load(), err)
	case <-time.After(20 * waitPoll):
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "still running") {
			t.Fatalf("interrupted: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end when interrupted")
	}

	taskServer(t, always(http.StatusServiceUnavailable, "the store could not be read: timeout"))
	ctx, cancel = context.WithTimeout(t.Context(), 5*waitPoll)
	defer cancel()
	cc, _ = waitContext(t, ctx)
	if err := waitForResult(cc, "t1"); !client.Unavailable(err) || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("interrupted while the store did not answer: %v", err)
	}
}
