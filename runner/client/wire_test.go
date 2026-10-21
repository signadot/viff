package client_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/systems/systemstest"
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
func remote(t *testing.T, d task.Driver) (*httptest.Server, func() runner.Launch) {
	t.Helper()
	var mu sync.Mutex
	var got runner.Launch
	build := func(l runner.Launch, reg *task.Registry) *task.Task {
		mu.Lock()
		got = l
		mu.Unlock()
		sup := systemstest.Opened(task.NewSupervisor(workEnv, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), workEnv)
		return task.NewTask(l.ID, sup, d, okExec{})
	}
	srv := httptest.NewServer(runnerd.NewInProcessHandler(build).HTTPHandler())
	t.Cleanup(srv.Close)
	return srv, func() runner.Launch {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

// A LAUNCH REACHES A REMOTE RUNNER WHOLE: what the builder of a runner at the far end of a url is
// handed is the launch verse sent, every field of it.
func TestALaunchReachesARemoteRunnerWhole(t *testing.T) {
	srv, handed := remote(t, &doneDriver{steps: 1})
	sent := runner.Launch{
		ID: "review@demo.pr.1",
		Agent: &runner.Agent{Name: "verse", Settings: map[string]string{"model": "m-1"},
			Annotations: map[string]string{"verse.portkey/virtual-key": "route-a"}},
		Environment: systems.Environment{Vars: map[string]string{"BRANCH": "main"}, Systems: systems.Systems{
			// As the environment reads back: a block's attributes and verbs are maps, if empty.
			"slack":   {{Attributes: map[string]string{}, Verbs: map[string][]systems.Shape{"post": {{"channel": "deploys"}}}}},
			"network": {{Attributes: map[string]string{"mode": runner.NetworkOpen}, Verbs: map[string][]systems.Shape{}}},
		}},
		Prompt: "review it", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, Converse: true,
		Answer:     ir.FromMap(map[string]*ir.Node{"verdict": ir.FromString("ok")}),
		Occurrence: 4217,
		Provenance: runner.Provenance{Principal: "user:ana@example.com", Subject: "demo.pr.1", Chain: "review"},
		Credential: "eyJ.token.sig",
	}
	h := client.HTTP(srv.URL).Launch(sent)
	if msg := h.Err(); msg != "" {
		t.Fatalf("the launch was refused: %s", msg)
	}

	got := handed()
	if got.Answer == nil || ir.Get(got.Answer, "verdict") == nil {
		t.Errorf("Answer arrived as %v, want the contract sent", got.Answer)
	}
	got.Answer, sent.Answer = nil, nil
	if !reflect.DeepEqual(got, sent) {
		t.Errorf("the launch arrived as\n%+v\nwant\n%+v", got, sent)
	}
	// And the runner says back which run it is, and who runs it.
	snap, err := client.HTTP(srv.URL).(task.Lister).TaskSnapshot(sent.ID)
	if err != nil || snap.Occurrence != sent.Occurrence || snap.Agent != "verse" {
		t.Errorf("the snapshot says occurrence %d, doer %q (%v); want %d, verse", snap.Occurrence, snap.Agent, err, sent.Occurrence)
	}
	// …and never says the credential back.
	for _, path := range []string{"/tasks", "/tasks/" + sent.ID} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), sent.Credential) {
			t.Errorf("GET %s says the credential back:\n%s", path, body)
		}
	}
}

// A cancel's reason reaches the task it ends, where its charters read it.
func TestACancelsReasonReachesARemoteTask(t *testing.T) {
	srv, _ := remote(t, &paced{n: 500})
	r := client.HTTP(srv.URL)
	h := r.Launch(runner.Launch{ID: "long", Agent: &runner.Agent{Name: "test"}, Prompt: "wait", Environment: openNet})
	if err := h.Cancel("the place is going away", ""); err != nil {
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

// A launch that does not parse is refused, and starts nothing. One with no body is refused too: a
// launch says who does what.
func TestALaunchThatDoesNotParseIsRefused(t *testing.T) {
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
		t.Errorf("a launch that does not parse = %d, want 400", code)
	}
	if code := post(`{id: "typed", agent: {name: a}, prompt: p, occurrence: "many"}`); code != http.StatusBadRequest {
		t.Errorf("a launch whose field is the wrong type = %d, want 400", code)
	}
	tasks, err := client.HTTP(srv.URL).(task.Lister).ListTasks()
	if err != nil || len(tasks) != 0 {
		t.Fatalf("after two refused launches the runner hosts %v (%v), want nothing", tasks, err)
	}
	if code := post(``); code != http.StatusBadRequest {
		t.Errorf("a launch with no body = %d, want 400", code)
	}
}
