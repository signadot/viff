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
	"github.com/signadot/verse/task"
)

// registerRunner installs a runner face offering the agents named (and scripts) under name,
// probed, so a charter that spawns on `verse.runners.<name>` installs: a spawn naming a runner
// that has not described itself is refused at install.
func registerRunner(t *testing.T, ts *httptest.Server, name string, agents ...string) {
	t.Helper()
	release := make(chan struct{})
	build := func(l runner.Launch, _ *task.Registry) *task.Task {
		sup := task.NewSupervisor(task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		return task.NewTask(l.ID, sup, &blockedDriver{release: release}, okExecutor{})
	}
	face := httptest.NewServer(offeringAll(runnerd.NewInProcessHandler(build), agents...).HTTPHandler())
	t.Cleanup(func() {
		close(release)
		face.Close()
	})
	if resp := postTony(t, ts.URL+"/runners", map[string]any{"name": name, "url": face.URL}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("runner add %s = %s", name, resp.Status)
	}
}

// A RUNNER THAT HAS NOT DESCRIBED ITSELF TAKES NO SPAWN. Installed with --no-probe against a
// url nothing answers, a spawn naming it is refused with the reason, on the ad-hoc path and on
// the charter's. Installed the same way against a runner that is up, the first spawn asks it,
// is checked against what it says, and the registration keeps the answer.
func TestASpawnOnARunnerThatHasNotDescribedItselfIsRefused(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "pending", "url": deadURL}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("--no-probe add = %s", resp.Status)
	}
	resp := postTony(t, ts.URL+"/tasks", map[string]any{
		"id": "p1", "doer": map[string]any{"agent": "fake"}, "prompt": "t", "runner": "verse.runners.pending",
	})
	defer resp.Body.Close()
	if msg := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "has not described itself") {
		t.Fatalf("a spawn on a runner that never answered = %s %q, want 400 saying it has not described itself", resp.Status, msg)
	}
	// The charter path refuses the same spawn as a failed task; here it fails one step earlier,
	// since the rule's view in this harness reads no registration at all (the uninstalled-runner
	// test above), and that refusal names the runner too.
	h := srv.spawnerFor(srv.Store, "", srv.Eng)("fired", launch.Spec{Doer: runner.Doer{Script: "t"}, Runner: "verse.runners.pending"})
	if h == nil || !strings.Contains(h.Err(), "verse.runners.pending") {
		t.Fatalf("a charter spawn on it: %v, want it failed naming the runner", h)
	}

	face := runnerFace(t)
	if resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "late", "url": face.URL}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("--no-probe add = %s", resp.Status)
	}
	if e, ok := findRunner(srv.Store, "late"); !ok || len(e.Offers) != 0 {
		t.Fatalf("a --no-probe registration keeps offers: %+v", e)
	}
	spawnOK(t, ts, map[string]any{"id": "l1", "doer": map[string]any{"agent": "fake"}, "prompt": "t", "runner": "verse.runners.late"})
	if e, ok := findRunner(srv.Store, "late"); !ok || len(e.Offers) == 0 {
		t.Fatalf("the first spawn did not keep what the runner said: %+v", e)
	}
	// …and what it said is what the next spawn is checked against: an agent it does not offer.
	resp = postTony(t, ts.URL+"/tasks", map[string]any{
		"id": "l2", "doer": map[string]any{"agent": "other"}, "prompt": "t", "runner": "verse.runners.late",
	})
	defer resp.Body.Close()
	if msg := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "verse.runners.late") {
		t.Fatalf("a spawn the runner's offer refuses = %s %q, want 400 naming the runner", resp.Status, msg)
	}
}

// …AND VERSE ASKS WHEN IT STARTS. A new server over a store holding registrations without
// offers probes them: the one that answers has its offers kept, the one that does not stays as
// it was.
func TestStartProbesRunnersWithoutOffers(t *testing.T) {
	store := entitytest.Store(t)
	first := NewWithStore(ScriptedBuilder, store)
	ts := httptest.NewServer(first.Handler())
	face := runnerFace(t)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	for name, url := range map[string]string{"up": face.URL, "down": deadURL} {
		if resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": name, "url": url}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("--no-probe add %s = %s", name, resp.Status)
		}
	}
	ts.Close()

	second := NewWithStore(ScriptedBuilder, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second.ctx = ctx
	if err := second.probeRunners(); err != nil {
		t.Fatal(err)
	}
	if e, ok := findRunner(store, "up"); !ok || len(e.Offers) == 0 {
		t.Errorf("the runner that answers was not asked at start: %+v", e)
	}
	if e, ok := findRunner(store, "down"); !ok || len(e.Offers) != 0 {
		t.Errorf("the runner that does not answer gained offers: %+v", e)
	}
}
