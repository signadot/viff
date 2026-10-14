package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// paced takes n steps at a pace, so a task is still running when it is cancelled.
type paced struct{ n int }

func (d *paced) Next(ctx context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	if d.n <= 0 {
		return task.ToolUse{}, false, nil
	}
	d.n--
	select {
	case <-time.After(20 * time.Millisecond):
	case <-ctx.Done():
	}
	return task.ToolUse{Name: "noop"}, true, nil
}

// remote stands up the stock runner over HTTP, and keeps the launch its builder was handed.
func remote(t *testing.T, d task.Driver) (*httptest.Server, func() task.Launch) {
	t.Helper()
	var mu sync.Mutex
	var got task.Launch
	build := func(l task.Launch, reg *task.Registry) *task.Task {
		mu.Lock()
		got = l
		mu.Unlock()
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		return task.NewTask(l.ID, sup, d, okExec{})
	}
	srv := httptest.NewServer(runnerd.NewInProcessHandler(build).HTTPHandler())
	t.Cleanup(srv.Close)
	return srv, func() task.Launch {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

// A LAUNCH REACHES A REMOTE RUNNER WHOLE: what the builder of a runner at the far end of a url is
// handed is the launch verse spawned, less the two fields that are verse's own. The credential,
// the event window and which run it is are among what arrives.
func TestALaunchReachesARemoteRunnerWhole(t *testing.T) {
	srv, handed := remote(t, &doneDriver{steps: 1})
	sent := task.Launch{
		ID: "review@demo.pr.1", Prompt: "review it", Driver: "llm",
		Frame: "you are a reviewer", Retain: 512,
		Tools: []string{"read_file", "run_bash"}, VirtualKey: "vk-123",
		Answer:      ir.FromMap(map[string]*ir.Node{"verdict": ir.FromString("ok")}),
		Calls:       []task.Call{{Tool: "run_bash", Input: map[string]any{"cmd": "true"}}},
		Vector:      &task.Vector{Default: task.Ask},
		Environment: "verse-repo", Scope: "me", Occurrence: 4217,
		Provenance: task.Provenance{Principal: "user:ana@example.com", Subject: "demo.pr.1", Chain: "review"},
	}
	h := client.HTTP(srv.URL).Spawn(sent)
	if msg := h.Err(); msg != "" {
		t.Fatalf("the spawn was refused: %s", msg)
	}

	got := handed()
	for _, c := range []struct {
		field     string
		got, want any
	}{
		{"ID", got.ID, sent.ID}, {"Prompt", got.Prompt, sent.Prompt}, {"Driver", got.Driver, sent.Driver},
		{"Frame", got.Frame, sent.Frame}, {"Tools", got.Tools, sent.Tools},
		{"Retain", got.Retain, sent.Retain}, {"VirtualKey", got.VirtualKey, sent.VirtualKey},
		{"Calls", got.Calls, sent.Calls}, {"Environment", got.Environment, sent.Environment},
		{"Occurrence", got.Occurrence, sent.Occurrence}, {"Provenance", got.Provenance, sent.Provenance},
		{"Scope", got.Scope, ""},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s arrived as %v, want %v", c.field, c.got, c.want)
		}
	}
	if got.Vector == nil || got.Vector.Default != task.Ask {
		t.Errorf("Vector arrived as %+v, want the one sent", got.Vector)
	}
	if got.Answer == nil || ir.Get(got.Answer, "verdict") == nil {
		t.Errorf("Answer arrived as %v, want the contract sent", got.Answer)
	}
	if got.Grant != nil {
		t.Errorf("a launch that named no grant arrived bounded by %+v", got.Grant)
	}
	// And the runner says back which run it is.
	snap, err := client.HTTP(srv.URL).(task.Lister).TaskSnapshot(sent.ID)
	if err != nil || snap.Occurrence != sent.Occurrence {
		t.Errorf("the snapshot says occurrence %d (%v), want %d", snap.Occurrence, err, sent.Occurrence)
	}
}

// A cancel's reason reaches the task it ends, where its charters read it.
func TestACancelsReasonReachesARemoteTask(t *testing.T) {
	srv, _ := remote(t, &paced{n: 500})
	r := client.HTTP(srv.URL)
	h := r.Spawn(task.Launch{ID: "long", Prompt: "wait"})
	if err := h.Cancel("the place is going away"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var snap task.Snapshot
	for i := 0; i < 300; i++ {
		s, err := r.(task.Lister).TaskSnapshot("long")
		if err == nil && s.Status.Terminal() {
			snap = s
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snap.Status != task.Cancelled || !strings.Contains(snap.Err, "the place is going away") {
		t.Fatalf("the task ended %q saying %q, want it cancelled for the reason given", snap.Status, snap.Err)
	}
}

// A spawn that does not parse is refused, and spawns nothing. One with no body is a launch
// that says nothing, and is a spawn.
func TestASpawnThatDoesNotParseIsRefused(t *testing.T) {
	srv, _ := remote(t, &doneDriver{steps: 1})
	post := func(body string) int {
		t.Helper()
		resp, err := http.Post(srv.URL+"/tasks", codec.MediaType(codec.Tony), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(`{id: "broken", prompt: [unclosed`); code != http.StatusBadRequest {
		t.Errorf("a spawn that does not parse = %d, want 400", code)
	}
	if code := post(`{id: "typed", retain: "many"}`); code != http.StatusBadRequest {
		t.Errorf("a spawn whose field is the wrong type = %d, want 400", code)
	}
	tasks, err := client.HTTP(srv.URL).(task.Lister).ListTasks()
	if err != nil || len(tasks) != 0 {
		t.Fatalf("after two refused spawns the runner hosts %v (%v), want nothing", tasks, err)
	}
	if code := post(``); code != http.StatusCreated {
		t.Errorf("a spawn with no body = %d, want 201", code)
	}
}
