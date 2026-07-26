package processd

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/process"
)

// seqDriver emits one action per step, then reports done — no model.
type seqDriver struct {
	steps []process.ToolUse
	i     int
}

func (d *seqDriver) Next(_ context.Context, _ process.StepResult) (process.ToolUse, bool, error) {
	if d.i < len(d.steps) {
		tu := d.steps[d.i]
		d.i++
		return tu, true, nil
	}
	return process.ToolUse{}, false, nil
}

type okExec struct{}

func (okExec) Execute(_ context.Context, tu process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	return process.StepResult{Use: tu, Output: "ok"}, nil, nil
}

// fakeBuild makes a hosted process whose single action hits an ASK gate, so the
// resolve/read-plane paths are exercised without a live model.
func fakeBuild(id, task, _ string, reg *process.Registry, _ process.ContentReader) *process.Process {
	vec := &process.Vector{
		Default: process.Auto,
		Gates:   []process.Gate{{Name: "shell", Mode: process.Ask, Match: process.GateMatch{Verbs: []process.Verb{process.Exec}}}},
	}
	sup := process.NewSupervisor(vec, reg)
	sup.SetDeriver(process.BasicDeriver{})
	tu := process.ToolUse{Name: "run", Effects: []process.Effect{{Verb: process.Exec, Resource: process.Resource{Kind: process.KindProcess, Ref: "x"}}}}
	return process.NewProcess(id, sup, &seqDriver{steps: []process.ToolUse{tu}}, okExec{})
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(New(fakeBuild).Handler())
}

// Spawn a process, watch its pending gate over SSE, resolve it by id, and see it
// complete — the full observe→resolve loop over HTTP.
func TestDaemonSpawnObserveResolve(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// spawn
	resp, err := http.Post(srv.URL+"/processes", "application/json", strings.NewReader(`{"task":"t"}`))
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
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/processes/"+sp.ID+"/events", nil)
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
		var ev process.Frame
		if codec.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev, frameFmt) != nil {
			continue
		}
		if ev.Kind == process.EvPending {
			decisionID = ev.ID
			break
		}
	}
	if decisionID == "" {
		t.Fatal("never saw a pending decision on the stream")
	}

	// resolve it by id
	rr, err := http.Post(srv.URL+"/processes/"+sp.ID+"/resolve", "application/json",
		strings.NewReader(`{"decisionId":"`+decisionID+`","proceed":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if rr.StatusCode != http.StatusNoContent {
		t.Fatalf("resolve status = %d", rr.StatusCode)
	}
	rr.Body.Close()

	// the process should reach completion
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g, _ := http.Get(srv.URL + "/processes/" + sp.ID)
		var snap struct {
			Status string `tony:"field=status"`
		}
		decodeResp(t, g, &snap)
		g.Body.Close()
		if snap.Status == "completed" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("process did not complete after resolve")
}

// Resolving an unknown decision id is a 404, not a silent success.
func TestDaemonResolveUnknown(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/processes", "application/json", strings.NewReader(`{"task":"t"}`))
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()

	rr, err := http.Post(srv.URL+"/processes/"+sp.ID+"/resolve", "application/json",
		strings.NewReader(`{"decisionId":"nope","proceed":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Body.Close()
	if rr.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-decision resolve status = %d, want 404", rr.StatusCode)
	}
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
