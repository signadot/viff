package versed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
	"github.com/signadot/verse/process/processd"
	"github.com/signadot/verse/runner"
)

// a runner face an operator can actually install: the verse-facing contract, served by the
// same processd handler `/broker/local` serves.
func runnerFace(t *testing.T) *httptest.Server {
	t.Helper()
	build := func(l process.Launch, _ *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(l.ID, sup, &blockedDriver{}, okExecutor{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// a driver that never finishes on its own, so a process stays live long enough to be work that
// is ON a runner when the runner is removed.
//
// It keeps asking for another step rather than blocking inside Next, because a cancel is
// noticed at the loop's BOUNDARY: a driver parked in Next is one the loop never gets back to,
// so cancelling it would produce no terminal at all. This is the shape of the real thing — work
// that is going, and stops at the next hinge.
type blockedDriver struct{}

func (d *blockedDriver) Next(ctx context.Context, _ process.StepResult) (process.ToolUse, bool, error) {
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Millisecond):
	}
	return process.ToolUse{Name: "noop"}, true, nil
}

type okExecutor struct{}

func (okExecutor) Execute(_ context.Context, tu process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	return process.StepResult{Use: tu}, nil, nil
}

// An operator can install one, list it, and read it back. The address is the one they typed.
func TestRunnerAddListShow(t *testing.T) {
	srv := testServer(t)
	face := runnerFace(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "kind": "pool", "url": face.URL,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s, want 201", resp.Status)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if got["ref"] != "runners:pool:gpu" {
		t.Fatalf("ref = %v, want runners:pool:gpu", got["ref"])
	}
	// The probe ran and said what it found, rather than the install being a silent write.
	if got["reach"] != "a runner" {
		t.Fatalf("reach = %v, want \"a runner\" — a live runner face read as something else", got["reach"])
	}

	lr, err := ts.Client().Get(ts.URL + "/runners")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list []map[string]any
	decodeResp(t, lr, &list)
	if len(list) != 1 || list[0]["ref"] != "runners:pool:gpu" || list[0]["url"] != face.URL {
		t.Fatalf("list = %+v, want the one installed runner with its url", list)
	}

	sr, err := ts.Client().Get(ts.URL + "/runners/pool/gpu")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var one map[string]any
	decodeResp(t, sr, &one)
	if one["url"] != face.URL {
		t.Fatalf("show = %+v, want the url", one)
	}
}

// The checks that are real from the first commit: an address that cannot be addressed, and a
// url that is not one. Each refusal quotes what it refused, so the operator sees their typo.
func TestRunnerAddRefusesWhatItCannotHonour(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"no url", map[string]any{"name": "gpu", "kind": "pool"}, "url is required"},
		{"url does not parse", map[string]any{
			"name": "gpu", "kind": "pool", "url": "gpu-pool:9797",
		}, "gpu-pool:9797"},
		{"no kind", map[string]any{"name": "gpu", "url": "http://x:1"}, "kind is required"},
		{"slash in name", map[string]any{
			"name": "a/b", "kind": "pool", "url": "http://x:1",
		}, "may not contain /"},
		{"unknown field", map[string]any{
			"name": "gpu", "kind": "pool", "ulr": "http://x:1",
		}, "has no field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := postTony(t, ts.URL+"/runners?probe=false", tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("add = %s, want 400", resp.Status)
			}
			msg := readBody(t, resp)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("refusal = %q, want it to name %q", msg, tc.want)
			}
		})
	}
}

// The probe's two dispositions, and the reason they differ: whether waiting could change the
// answer. A url that answers as something ELSE is answering now — a broker's worker leg is
// exactly this — while a url nothing answers is the ordinary state of a runner deployed
// alongside the entity that names it.
func TestRunnerAddProbeRefusesOnlyWhatWaitingCannotFix(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Answering, and not as a runner: a mux with no GET /processes, which is the shape of a
	// broker's worker leg (claim/inbox/report).
	worker := httptest.NewServer(http.NewServeMux())
	defer worker.Close()
	resp := postTony(t, ts.URL+"/runners", map[string]any{
		"name": "wrong", "kind": "pool", "url": worker.URL,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("add of a non-runner url = %s, want 400", resp.Status)
	}
	if msg := readBody(t, resp); !strings.Contains(msg, "not a runner") {
		t.Fatalf("refusal = %q, want it to say what it found", msg)
	}

	// Nothing answering: installed, with the warning on the response — the operator running
	// the command is the one who can act on it.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	resp = postTony(t, ts.URL+"/runners", map[string]any{
		"name": "pending", "kind": "pool", "url": deadURL,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add of an unreachable url = %s, want 201 — refusing it would make deployment "+
			"order-dependent for a fact that fixes itself", resp.Status)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if got["reach"] != "unreachable" || got["warning"] == nil {
		t.Fatalf("add = %+v, want it installed and SAYING it could not be reached", got)
	}

	// And --no-probe installs the one the probe would have refused.
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{
		"name": "anyway", "kind": "pool", "url": worker.URL,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("--no-probe add = %s, want 201: a check with no way past it is a trap", resp.Status)
	}
}

// check reports the three apart, on demand, without recording any of it.
func TestRunnerCheckReportsAndRecordsNothing(t *testing.T) {
	srv := testServer(t)
	face := runnerFace(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "kind": "pool", "url": face.URL,
	}).Body.Close()

	resp, err := ts.Client().Get(ts.URL + "/runners/pool/gpu/check")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if got["reach"] != "a runner" {
		t.Fatalf("check = %+v, want reach \"a runner\"", got)
	}
	// Answered live, printed, discarded: the entity is what it was.
	e, ok, _ := srv.Store.Get(runner.Ref("pool", "gpu"))
	if !ok {
		t.Fatal("the runner entity vanished")
	}
	rec := entity.ToObj(e.Payload)
	if _, stored := rec["reach"]; stored {
		t.Fatalf("the store recorded health: %+v — a reachability column that is remembered is "+
			"one that can be stale in the direction that costs", rec)
	}
}

// Removal never guesses what to do with running work. It refuses until told which, and the
// message names both flags — because the difference is an intent verse cannot read from a
// delete, and the operator is present.
func TestRunnerRemoveRefusesUntilToldWhich(t *testing.T) {
	srv := testServer(t)
	face := runnerFace(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "kind": "pool", "url": face.URL,
	}).Body.Close()

	// A process running ON it. Spawned through the runner face and mirrored the way a spawn
	// does, which is what makes it work this removal has to answer for.
	p := runner.HTTP(face.URL)
	p.Spawn(process.Launch{ID: "w1", Task: "long work", Driver: "scripted"}, nil)
	setInstance(t, srv, "w1", entity.Obj{
		"status": "running", "runner": face.URL, "runnerName": "runners:pool:gpu",
	})

	resp, err := ts.Client().Do(mustReq(t, http.MethodDelete, ts.URL+"/runners/pool/gpu"))
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("remove with work on it = %s, want 409", resp.Status)
	}
	msg := readBody(t, resp)
	for _, want := range []string{"--cancel", "--leave", "runners:pool:gpu"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal = %q, want it to name %q", msg, want)
		}
	}

	// --leave removes it and the work keeps running, still reachable on the url stamped on it.
	resp, err = ts.Client().Do(mustReq(t, http.MethodDelete, ts.URL+"/runners/pool/gpu?leave=true"))
	if err != nil {
		t.Fatalf("remove --leave: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove --leave = %s, want 200 — --leave must always succeed, or a runner with "+
			"one long-lived process on it could never be removed", resp.Status)
	}
	if _, ok, _ := srv.Store.Get(runner.Ref("pool", "gpu")); ok {
		t.Fatal("--leave did not remove the entity")
	}
	if _, err := p.(process.Lister).ProcessSnapshot("w1"); err != nil {
		t.Fatalf("the work stopped being reachable when the entity went: %v — the url is stamped "+
			"on the process precisely so removal cannot strand it", err)
	}
}

// And a removal with nothing running on it needs no flag: there is nothing to be wrong about.
func TestRunnerRemoveIdleNeedsNoFlag(t *testing.T) {
	srv := testServer(t)
	face := runnerFace(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "kind": "pool", "url": face.URL,
	}).Body.Close()

	resp, err := ts.Client().Do(mustReq(t, http.MethodDelete, ts.URL+"/runners/pool/gpu"))
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove of an idle runner = %s, want 200", resp.Status)
	}
}

// --cancel ends the work with a reason, and reports what it could NOT reach rather than
// counting it cancelled. An already-gone runner is why an operator removes the entity half the
// time, so that is the ordinary case: saying "cancelled 3" when three are unreachable is the
// same lie as recording a queued answer as delivered.
func TestRunnerRemoveCancelReportsWhatItCouldNotReach(t *testing.T) {
	srv := testServer(t)
	face := runnerFace(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "gpu", "kind": "pool", "url": face.URL,
	}).Body.Close()

	p := runner.HTTP(face.URL)
	h := p.Spawn(process.Launch{ID: "c1", Task: "long work", Driver: "scripted"}, nil)
	setInstance(t, srv, "c1", entity.Obj{
		"status": "running", "runner": face.URL, "runnerName": "runners:pool:gpu",
	})
	ch, unsub := h.Subscribe()
	defer unsub()

	resp, err := ts.Client().Do(mustReq(t, http.MethodDelete,
		ts.URL+"/runners/pool/gpu?cancel=gpu+pool+retired"))
	if err != nil {
		t.Fatalf("remove --cancel: %v", err)
	}
	var got map[string]any
	decodeResp(t, resp, &got)
	if ids, _ := got["cancelled"].([]any); len(ids) != 1 || ids[0] != "c1" {
		t.Fatalf("remove --cancel = %+v, want it to report c1 cancelled", got)
	}
	if _, claimed := got["unreachable"]; claimed {
		t.Fatalf("remove --cancel = %+v, want nothing reported unreachable", got)
	}
	// The work really ended, and its flow can see that it did.
	waitForStatus(t, ch, process.Cancelled)

	// Now the case that decides how honest this is: the runner is already gone. Its own url,
	// not the live one — two entities sharing a url are one physical runner, which is what
	// makes a rename an add and a remove of the same place.
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()
	postTony(t, ts.URL+"/runners?probe=false", map[string]any{
		"name": "ghost", "kind": "pool", "url": goneURL,
	}).Body.Close()
	setInstance(t, srv, "g1", entity.Obj{
		"status": "running", "runner": goneURL, "runnerName": "runners:pool:ghost",
	})

	resp, err = ts.Client().Do(mustReq(t, http.MethodDelete,
		ts.URL+"/runners/pool/ghost?cancel=teardown"))
	if err != nil {
		t.Fatalf("remove --cancel of a gone runner: %v", err)
	}
	decodeResp(t, resp, &got)
	if ids, _ := got["unreachable"].([]any); len(ids) != 1 || ids[0] != "g1" {
		t.Fatalf("remove --cancel of a gone runner = %+v, want g1 reported as unreachable "+
			"rather than counted as cancelled", got)
	}
	if ids, _ := got["cancelled"].([]any); len(ids) != 0 {
		t.Fatalf("remove --cancel = %+v, want nothing counted cancelled", got)
	}
}

func setInstance(t *testing.T, srv *Server, id string, rec entity.Obj) {
	t.Helper()
	if _, _, err := srv.Store.Write(entity.Write{
		At: mirror.Ref(id).At(), Value: rec.Payload(),
	}); err != nil {
		t.Fatalf("writing the process:instance for %s: %v", id, err)
	}
}

func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return req
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	return string(b)
}

func waitForStatus(t *testing.T, ch <-chan process.Frame, want process.Status) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatalf("the stream ended without a %s terminal", want)
			}
			if ev.Kind == process.EvStatus && ev.Text == string(want) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %s terminal — the work never learned it was ended", want)
		}
	}
}
