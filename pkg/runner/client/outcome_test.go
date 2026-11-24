package client_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/runner/client"
	"go.viff.dev/viff/pkg/task"
)

type resolver interface {
	Resolve(decision, choice, say, by string) error
}

// A control call against a remote that is GONE says so, and says which kind of gone.
//
// It used to say nothing at all: post() discarded the error and every signature returned
// void, so a cancel that never left the building read exactly like a cancel that landed. An
// operator was told the task was stopping and it was not.
func TestControlAgainstADeadRemoteIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	h := client.HTTP(url).Attach("p")
	for _, c := range []struct {
		what string
		err  error
	}{
		{"cancel", h.Cancel("stop", "")},
		{"pause", h.Pause("")},
		{"resume", h.Resume("")},
		{"steer", h.Steer("do it differently", "human:walter", "")},
		{"resolve", h.(resolver).Resolve("d1", "proceed", "", "human:walter")},
	} {
		if !errors.Is(c.err, task.ErrUnreachable) {
			t.Errorf("%s against a dead remote = %v, want ErrUnreachable", c.what, c.err)
		}
	}
}

// A remote that is UP and refusing is a different fact, and must never be reported as
// unreachable — nothing about a refusal is worth retrying, and everything about a timeout is.
func TestControlAgainstARefusingRemoteIsNotUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the runtime is out of capacity", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	h := client.HTTP(srv.URL).Attach("p")
	err := h.Cancel("stop", "")
	if err == nil {
		t.Fatal("a refused cancel reported success")
	}
	if errors.Is(err, task.ErrUnreachable) {
		t.Fatalf("a refusal was reported as unreachable: %v", err)
	}
	if got := err.Error(); got == "" || !contains(got, "out of capacity") {
		t.Errorf("the refusal lost the remote's reason: %q", got)
	}
}

// The two refusals a resolve can meet are told apart, because they mean opposite things:
// one is "look for your typo", the other is "somebody was here first".
func TestARemoteResolveDistinguishesItsRefusals(t *testing.T) {
	for _, c := range []struct {
		status int
		want   error
	}{
		{http.StatusNotFound, task.ErrNoSuchDecision},
		{http.StatusConflict, task.ErrAlreadyAnswered},
		{http.StatusNotImplemented, task.ErrUnsupported},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", c.status)
		}))
		err := client.HTTP(srv.URL).Attach("p").(resolver).Resolve("d1", "proceed", "", "human:walter")
		if !errors.Is(err, c.want) {
			t.Errorf("a %d answer = %v, want %v", c.status, err, c.want)
		}
		srv.Close()
	}
}

// A remote that accepts the connection and never answers is bounded. Without a deadline the
// caller waits on that forever, holding whatever goroutine issued the call — which is how a
// launch, a cancel and an answer each hang.
//
// The bound is the runner's OWN client. http.DefaultClient is shared task-wide, so a
// timeout set on it would silently bound every other caller in the binary.
func TestControlAgainstAHangingRemoteIsBounded(t *testing.T) {
	// NOT RUN BY DEFAULT — set GOTEST_LONG=1. Its runtime IS what it demonstrates: the
	// deadline expiring is the observation, and a shorter one would be a different test.
	if os.Getenv("GOTEST_LONG") == "" {
		t.Skip("long: set GOTEST_LONG=1 to run (it waits out client.ControlTimeout)")
	}
	block := make(chan struct{})
	defer close(block)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	if http.DefaultClient.Timeout != 0 {
		t.Fatalf("http.DefaultClient.Timeout = %v; the runner must not bound the shared client",
			http.DefaultClient.Timeout)
	}

	done := make(chan error, 1)
	go func() { done <- client.HTTP(srv.URL).Attach("p").Cancel("stop", "") }()
	select {
	case err := <-done:
		if !errors.Is(err, task.ErrUnreachable) {
			t.Fatalf("a timed-out cancel = %v, want ErrUnreachable", err)
		}
	case <-time.After(client.ControlTimeout + 15*time.Second):
		t.Fatal("a control call against a hanging remote never returned")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
