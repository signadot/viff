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

// A task read that gave no snapshot is the server's words, to a command that goes on to act on
// what the snapshot says.
func TestATaskReadThatGaveNoSnapshotIsTheServersWords(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		is     func(error) bool
	}{
		{http.StatusNotFound, "no task \"t1\"", client.NotInView},
		{http.StatusForbidden, "refused: bob holds nothing at verse.task.instance.t1", client.Refused},
		{http.StatusServiceUnavailable, "the store could not be read: timeout", client.Unavailable},
	} {
		taskServer(t, always(c.status, c.body))
		var snap struct {
			Status string `tony:"field=status"`
		}
		err := taskSnapshot("t1", &snap)
		if err == nil || !strings.Contains(err.Error(), c.body) || !c.is(err) {
			t.Errorf("%d: err = %v, want the server's words and its status", c.status, err)
		}
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

// `task run` names its contract file `--answer-contract`. `--answer` is the gate's, an option
// id, and on `task run` it is no flag at all.
func TestTaskRunNamesItsContractFileAnswerContract(t *testing.T) {
	serve(t)
	_, err := runCLI(t, "task", "run", "--driver", "scripted", "--answer", "shape.tony", "true")
	if err == nil || !strings.Contains(err.Error(), "answer") {
		t.Fatalf("task run --answer: err = %v, want the flag refused", err)
	}
	_, err = runCLI(t, "task", "run", "--driver", "scripted", "--answer-contract", "no-such-file.tony", "true")
	if err == nil || !strings.Contains(err.Error(), "--answer-contract") || !strings.Contains(err.Error(), "no-such-file.tony") {
		t.Fatalf("task run --answer-contract <missing file>: err = %v, want the file named", err)
	}
}
