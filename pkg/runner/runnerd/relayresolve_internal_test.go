package runnerd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// AN ANSWER THE RUNNER RELAYS IS KEPT WITH THE CALL IT IS ABOUT. The task's process here is a
// stub that takes any resolve; what the runner knows of the gate is the pending event it
// followed, and the answer lands on the task's channel for the act to take. A gate about no
// call leaves nothing; a resolve the process refused leaves nothing.
func TestARelayedAnswerIsKeptWithTheCallItIsAbout(t *testing.T) {
	var code atomic.Int32
	code.Store(http.StatusNoContent)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(code.Load()))
	}))
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	h := NewSubprocessHandler(SubprocessConfig{Log: io.Discard})
	l := &runner.Launch{ID: "x", Prompt: "p", Agent: &runner.Agent{Name: "verse"}, Environment: gitEnv("acme/app"),
		Autonomy: task.Autonomy{{Match: &task.Match{System: "git"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAsk}}}}
	ch, err := delivery.Open(l.ID, &workspace.Workspace{}, nil, func(workspace.Repo) (string, error) { return "", nil }, delivery.Services{}, nil, l, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	close(ready)
	p := &proc{id: l.ID, l: l, ready: ready, gone: make(chan struct{}), done: make(chan struct{}),
		prox: httputil.NewSingleHostReverseProxy(u), co: &checkout{l: l, ch: ch}}
	push := map[string]string{"repo": "github.com/acme/app", "ref": "refs/heads/verse/auto/x", "delete": "false", "force": "false", "exists": "false", "own": "false"}
	p.record(task.Event{Kind: task.EventPending, Task: "x", Cursor: 1, ID: "x~d1", Step: task.StepGitPush, Fields: systems.FieldsDoc(task.StepGitPush, push)})
	p.record(task.Event{Kind: task.EventPending, Task: "x", Cursor: 2, ID: "x~d2"}) // a bare ask, about no call
	h.mu.Lock()
	h.tasks[l.ID] = p
	h.mu.Unlock()
	srv := httptest.NewServer(h.HTTPHandler())
	t.Cleanup(srv.Close)

	resolve := func(body string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/tasks/x/resolve", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/tony")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	// Every answer the process took is kept, by the decision's id: the one about a call this
	// side read, and the one about none (an act names its decision, so the id is what matches).
	if c := resolve(`{id: x~d2, choice: proceed, by: "user:ana"}`); c != http.StatusNoContent || ch.Answers() != 1 {
		t.Fatalf("a bare ask answered = %d, %d answer(s) kept; want 204 and one", c, ch.Answers())
	}
	code.Store(http.StatusGone)
	if c := resolve(`{id: x~d1, choice: proceed, by: "user:ana"}`); c != http.StatusGone || ch.Answers() != 1 {
		t.Fatalf("an answer the process refused = %d, %d kept; want 410 and still one", c, ch.Answers())
	}
	code.Store(http.StatusNoContent)
	if c := resolve(`{id: x~d1, choice: proceed, by: "user:ana"}`); c != http.StatusNoContent || ch.Answers() != 2 {
		t.Fatalf("an answer about a push = %d, %d kept; want 204 and two", c, ch.Answers())
	}
}
