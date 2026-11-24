package runnerd

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/systems/systemstest"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/task"
)

// workEnv is an environment giving the shell and file writes, the steps these tests' drivers
// take: a step outside its environment is refused before any row is read.
var workEnv = systems.Environment{Systems: systems.Systems{
	"commands": {{Verbs: map[string][]systems.Shape{"run": {{}}}}},
	"files":    {{Verbs: map[string][]systems.Shape{"write": {{}}}}},
}}

// seqDriver emits one action per step, then reports done — no model.
type seqDriver struct {
	steps []task.ToolUse
	i     int
}

func (d *seqDriver) Next(_ context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	if d.i < len(d.steps) {
		tu := d.steps[d.i]
		d.i++
		return tu, true, nil
	}
	return task.ToolUse{}, false, nil
}

type okExec struct{}

func (okExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu, Output: "ok"}, nil, nil
}

// fakeBuild makes a hosted task whose single command a gate names, so the resolve/read-plane
// paths are exercised without a live model.
func fakeBuild(l runner.Launch, reg *task.Registry) *task.Task {
	g := task.Autonomy{{Name: "shell", Match: &task.Match{System: "commands", Verb: "run"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}}
	tu := task.ToolUse{Name: "run_bash", Input: map[string]any{"command": "x"}}
	return task.NewTask(l.ID, systemstest.Opened(task.NewSupervisor(workEnv, g, reg), workEnv), &seqDriver{steps: []task.ToolUse{tu}}, okExec{})
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(NewInProcessHandler(fakeBuild).HTTPHandler())
}

// Launch a task, watch its pending gate over SSE, resolve it by id, and see it
// complete — the full observe→resolve loop over HTTP.
func TestDaemonLaunchObserveResolve(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// launch
	resp, err := http.Post(srv.URL+"/tasks", "application/json", strings.NewReader(`{"id":"p1","agent":{"name":"test"},"prompt":"t","environment":{"systems":{"network":{"mode":"open"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()
	if sp.ID == "" {
		t.Fatal("no id returned")
	}

	// open the SSE stream and look for the pending decision
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/tasks/"+sp.ID+"/events", nil)
	es, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Body.Close()
	frameFmt := streamFmt(es)

	decisionID := ""
	sc := bufio.NewScanner(es.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev task.Event
		if codec.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev, frameFmt) != nil {
			continue
		}
		if ev.Kind == task.EventPending {
			decisionID = ev.ID
			break
		}
	}
	if decisionID == "" {
		t.Fatal("never saw a pending decision on the stream")
	}

	// resolve it by id
	rr, err := http.Post(srv.URL+"/tasks/"+sp.ID+"/resolve", "application/json",
		strings.NewReader(`{"id":"`+decisionID+`","choice":"proceed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if rr.StatusCode != http.StatusNoContent {
		t.Fatalf("resolve status = %d", rr.StatusCode)
	}
	rr.Body.Close()

	// the task should reach completion
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g, _ := http.Get(srv.URL + "/tasks/" + sp.ID)
		var snap struct {
			Status string `tony:"field=status"`
		}
		decodeResp(t, g, &snap)
		g.Body.Close()
		if snap.Status == "done" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task did not complete after resolve")
}

// Resolving an unknown decision id is a 404, not a silent success.
func TestDaemonResolveUnknown(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/tasks", "application/json", strings.NewReader(`{"id":"p1","agent":{"name":"test"},"prompt":"t","environment":{"systems":{"network":{"mode":"open"}}}}`))
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()

	rr, err := http.Post(srv.URL+"/tasks/"+sp.ID+"/resolve", "application/json",
		strings.NewReader(`{"id":"nope","choice":"proceed"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Body.Close()
	if rr.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-decision resolve status = %d, want 404", rr.StatusCode)
	}
}

// A resolve without a choice is refused, and the gate stays open. The body's decoder ignores
// keys it does not know, so a client still speaking an older shape ({decisionId, proceed} or
// {id, approve}) arrives with no choice at all; read as a deny, its approval would close the gate
// as a refusal nobody meant.
func TestDaemonResolveWithoutChoice(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/tasks", "application/json", strings.NewReader(`{"id":"p1","agent":{"name":"test"},"prompt":"t","environment":{"systems":{"network":{"mode":"open"}}}}`))
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()

	gate := pendingGate(t, srv.URL, sp.ID)
	for _, body := range []string{
		`{"decisionId":"` + gate + `","proceed":true}`,
		`{"id":"` + gate + `","approve":true}`,
		`{"id":"` + gate + `"}`,
	} {
		rr, err := http.Post(srv.URL+"/tasks/"+sp.ID+"/resolve", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		rr.Body.Close()
		if rr.StatusCode != http.StatusBadRequest {
			t.Errorf("resolve %s = %d, want 400", body, rr.StatusCode)
		}
	}
	if got := pendingGate(t, srv.URL, sp.ID); got != gate {
		t.Fatalf("after the refused resolves the open gate is %q, want %q still open", got, gate)
	}
}

// pendingGate waits for the task's snapshot to show an open gate, and returns its id.
func pendingGate(t *testing.T, base, id string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g, err := http.Get(base + "/tasks/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var snap task.Snapshot
		decodeResp(t, g, &snap)
		g.Body.Close()
		if len(snap.Pending) > 0 {
			return snap.Pending[0].ID
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s never showed an open gate", id)
	return ""
}

// decodeResp reads a response in the format it DECLARES rather than assuming JSON — the
// daemon renders in the configured default, which is not necessarily the one this test
// would have guessed.
func decodeResp(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type"))
	if !ok {
		f = codec.Default()
	}
	if err := codec.Unmarshal(body, v, f); err != nil {
		t.Fatalf("decoding a %v response: %v\n%s", f, err, body)
	}
}

// streamFmt reads the format an SSE stream declares its frames are in.
func streamFmt(resp *http.Response) codec.Format {
	if v := resp.Header.Get(codec.HeaderFormat); v != "" {
		if f, err := codec.Parse(v); err == nil {
			return f
		}
	}
	return codec.JSON
}

// answering is a driver that has its answer from the start and ends when released: the shape
// of every agent at its terminal frame, which leaves from inside Run with the answer already
// said.
type answering struct{ release chan struct{} }

func (a *answering) Next(ctx context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	select {
	case <-a.release:
	case <-ctx.Done():
	}
	return task.ToolUse{}, false, nil
}

func (a *answering) Result() string { return "the answer" }

// THE ANSWER IS ON THE SNAPSHOT BEFORE THE RUN RETURNS. The terminal frame leaves from inside
// Run, and a reader it brings (viffd's mirror) asks for the snapshot at once; a snapshot that
// learns the answer only when Run returns answers that reader with nothing, and the record ends
// with no result.
func TestTheAnswerIsOnTheSnapshotBeforeTheRunReturns(t *testing.T) {
	a := &answering{release: make(chan struct{})}
	d := NewInProcessHandler(func(l runner.Launch, reg *task.Registry) *task.Task {
		return task.NewTask(l.ID, task.NewSupervisor(workEnv, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, reg), a, okExec{})
	})
	if _, err := d.Launch(runner.Launch{ID: "a1", Agent: &runner.Agent{Name: "test"}, Environment: systems.Environment{Systems: systems.Systems{"network": {{Attributes: map[string]string{"mode": runner.NetworkOpen}}}}}, Prompt: "t"}); err != nil {
		t.Fatal(err)
	}
	snap, ok := d.SnapshotOf("a1")
	if !ok || snap.Status.Terminal() || snap.Result != "the answer" {
		t.Fatalf("while the run goes on the snapshot is %+v (%v), want the driver's answer on it", snap, ok)
	}
	close(a.release)
	deadline := time.Now().Add(5 * time.Second)
	for !snap.Status.Terminal() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		snap, _ = d.SnapshotOf("a1")
	}
	if snap.Status != task.Done || snap.Result != "the answer" {
		t.Fatalf("after the run: %+v", snap)
	}
}
