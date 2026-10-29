package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/systems/systemstest"
	"github.com/signadot/verse/task"
)

// registerRunner installs a runner face offering the agents named (and scripts) under name,
// probed, so a charter spawning one of those agents installs and its firing is routed there.
func registerRunner(t *testing.T, ts *httptest.Server, name string, agents ...string) {
	t.Helper()
	release := make(chan struct{})
	build := func(l runner.Launch, _ *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), l.Environment)
		return task.NewTask(l.ID, sup, &blockedDriver{release: release}, okExecutor{})
	}
	face := provenFace(t, ts.URL, offeringAll(runnerd.NewInProcessHandler(build), agents...).HTTPHandler(), func() { close(release) })
	if resp := postTony(t, ts.URL+"/runners", face.reg(name)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("runner add %s = %s", name, resp.Status)
	}
}

// A RUNNER THAT HAS NOT DESCRIBED ITSELF TAKES NO SPAWN, and a spawn does not ask it: the hello,
// the probe at start and `verse runner check` describe a runner. Installed with --no-probe
// against a url nothing answers, it is named in the refusal of a spawn nothing else takes, on the
// ad-hoc path and on the charter's. Installed the same way against a runner that is up, it takes
// spawns once a check has described it, and only what it offers.
func TestARunnerThatHasNotDescribedItselfIsPassedOver(t *testing.T) {
	srv := testServer(t)
	srv.NoLocalRunner = true
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "pending", "url": deadURL, "subject": someSubject(t)}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("--no-probe add = %s", resp.Status)
	}
	const passedOver = "passed over: verse.runners.pending, not described"
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": "p1", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	if msg := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, passedOver) {
		t.Fatalf("a spawn only an undescribed runner might take = %s %q, want 400 naming it", resp.Status, msg)
	}
	h := srv.spawnerFor(srv.Store, "", srv.Eng)("fired", launch.Spec{Script: "t"})
	if h == nil || !strings.Contains(h.Err(), passedOver) {
		t.Fatalf("a charter spawn: %v, want it failed naming the runner passed over", h)
	}

	face := runnerFace(t, ts.URL)
	if resp := postTony(t, ts.URL+"/runners?probe=false", face.reg("late")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("--no-probe add = %s", resp.Status)
	}
	resp = postTony(t, ts.URL+"/tasks", map[string]any{"id": "l0", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	if msg := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "verse.runners.late") {
		t.Fatalf("a spawn before the check = %s %q, want 400 naming the runner not described", resp.Status, msg)
	}
	if e, ok := findRunner(srv.Store, "late"); !ok || len(e.Description().Offers) != 0 {
		t.Fatalf("a spawn described the runner: %+v", e)
	}
	check, err := http.Get(ts.URL + "/runners/late/check")
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, check); check.StatusCode != http.StatusOK {
		t.Fatalf("check = %s: %s", check.Status, body)
	}
	spawnOK(t, ts, map[string]any{"id": "l1", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	// …and what it said is what the next spawn is checked against: an agent it does not offer.
	resp = postTony(t, ts.URL+"/tasks", map[string]any{"id": "l2", "spawn": map[string]any{"agent": map[string]any{"name": "other"}, "prompt": "t"}})
	if msg := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "no runner offers the agent other") {
		t.Fatalf("a spawn no runner's offer takes = %s %q, want 400 saying so", resp.Status, msg)
	}
}

// …AND VERSE ASKS WHEN IT STARTS. A new server over a store holding registrations without
// offers probes them: the one that answers has its offers kept, the one that does not stays as
// it was.
func TestStartProbesRunnersWithoutOffers(t *testing.T) {
	store := entitytest.Store(t)
	first := issuing(t, NewWithStore(ScriptedBuilder, store))
	ts := httptest.NewServer(first.Handler())
	face := runnerFace(t, ts.URL)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	for name, reg := range map[string]map[string]any{"up": face.reg("up"), "down": {"name": "down", "url": deadURL, "subject": someSubject(t)}} {
		if resp := postTony(t, ts.URL+"/runners?probe=false", reg); resp.StatusCode != http.StatusCreated {
			t.Fatalf("--no-probe add %s = %s", name, resp.Status)
		}
	}
	// The first verse stops serving its operators; its keys stay where the runner reads them, as
	// a restarted verse's are, under the same issuer and key.
	defer ts.Close()

	second := issuing(t, NewWithStore(ScriptedBuilder, store))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second.ctx = ctx
	if err := second.probeRunners(); err != nil {
		t.Fatal(err)
	}
	if e, ok := findRunner(store, "up"); !ok || len(e.Description().Offers) == 0 {
		t.Errorf("the runner that answers was not asked at start: %+v", e)
	}
	if e, ok := findRunner(store, "down"); !ok || len(e.Description().Offers) != 0 {
		t.Errorf("the runner that does not answer gained offers: %+v", e)
	}
}
