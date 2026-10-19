package runnerd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// A REPLACED POLICY IS CHECKED BY THE RUNNER'S OWN PROCESS, before the task's process hears of
// it, and once the task has taken it, it is the one the runner decides the task's acts by. The
// task's process here is a stub that takes anything, so what refuses the slide past the ceiling
// is this side, and what the channel holds afterwards is the replacement.
func TestAReplacedPolicyIsCheckedByTheRunnerAndReachesItsChannel(t *testing.T) {
	var reached atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	allow, deny, ask := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenDeny}, task.Then{Mode: task.ThenAsk}
	h := NewSubprocessHandler(SubprocessConfig{Log: io.Discard})
	h.Describe(runner.Description{Offers: []runner.Offer{{Agent: "verse", With: builds(),
		Systems: map[string]runner.System{
			"git": {Ceiling: task.Policy{
				{Name: "no-release", Match: &task.Match{System: "git", Verb: "push", Params: map[string]string{"ref": "refs/heads/release/**"}}, Then: deny},
				{Then: allow},
			}},
		}}}})
	l := &runner.Launch{ID: "x", Doer: runner.Doer{Agent: "verse", With: runner.With{Git: []runner.Repo{{Repo: "acme/app"}}}},
		Policy: task.Policy{{Match: &task.Match{System: "git"}, Then: ask}, {Then: ask}}}
	ch, err := delivery.Open(l.ID, &workspace.Workspace{}, nil, func(workspace.Repo) (string, error) { return "", nil }, delivery.Services{}, nil, l, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	close(ready)
	p := &proc{id: l.ID, l: l, ready: ready, gone: make(chan struct{}), done: make(chan struct{}),
		prox: httputil.NewSingleHostReverseProxy(u), co: &checkout{l: l, ch: ch}}
	h.mu.Lock()
	h.tasks[l.ID] = p
	h.mu.Unlock()
	srv := httptest.NewServer(h.HTTPHandler())
	t.Cleanup(srv.Close)

	put := func(body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/tasks/x/policy", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/tony")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		msg, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(msg)
	}

	// Past the ceiling: refused here, and the task's process never asked.
	if code, msg := put(`{policy: [{name: r, match: {system: git, verb: push, ref: refs/heads/release/1.0}, then: allow}, {then: ask}]}`); code != http.StatusBadRequest || !strings.Contains(msg, "no-release") {
		t.Errorf("a slide past the ceiling = %d %q, want 400 naming the ceiling's row", code, msg)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the task's process was asked %d time(s) about a policy the runner refuses", n)
	}
	if got := ch.Policy(); !reflect.DeepEqual(got, l.Policy) {
		t.Errorf("the channel's policy moved on a refused replacement: %v", got)
	}

	// Within it: the task's process takes it, and the channel holds it from then on.
	want := task.Policy{{Name: "auto", Match: &task.Match{System: "git", Verb: "push", Params: map[string]string{"ref": "refs/heads/verse/auto/*"}}, Then: allow}, {Then: ask}}
	if code, msg := put(`{policy: [{name: auto, match: {system: git, verb: push, ref: "refs/heads/verse/auto/*"}, then: allow}, {then: ask}]}`); code != http.StatusNoContent {
		t.Fatalf("a replacement within the ceiling = %d %q, want 204", code, msg)
	}
	if n := reached.Load(); n != 1 {
		t.Errorf("the task's process was asked %d time(s), want once", n)
	}
	if got := ch.Policy(); !reflect.DeepEqual(got, want) {
		t.Errorf("the channel's policy after the replacement:\n got %+v\nwant %+v", got, want)
	}
}
