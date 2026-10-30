package versed

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/task"
)

// A TASK'S LAUNCH IS ON ITS RECORD, and a restart that resumes the task reads it back: what it
// was given, the runner it went to, and the credential's address, so a `then` it raises after
// the restart is held within it as before; and the resumed record still says so.
func TestARestartReadsATasksLaunchBackOffItsRecord(t *testing.T) {
	srv := testServer(t)
	entitytest.Set(t, srv.Store, mirror.Ref("remote-1"), entity.Obj{
		"status":      string(task.Running),
		"runner":      "http://elsewhere.invalid", // remote: resumed, believed alive
		"runnerName":  "verse.runners.gpu",
		"environment": entity.Obj{"systems": entity.Obj{"question": entity.Obj{}}},
		"virtualKey":  "acme.keys.review",
	}.Payload())

	srv.reconcileTasks()

	got := srv.hostedOf("remote-1").launched
	if got == nil {
		t.Fatal("the resumed task's launch was not read back off its record")
	}
	if _, ok := got.environment.Systems["question"]; !ok || got.runner != "verse.runners.gpu" || got.virtualKey != "acme.keys.review" {
		t.Errorf("the resumed launch is %+v, want the record's", *got)
	}
	if got := waitField(t, srv, mirror.Ref("remote-1"), "virtualKey"); got != "acme.keys.review" {
		t.Errorf("the resumed record's virtualKey is %q, want it carried", got)
	}
	rec, _, _ := srv.Store.Get(mirror.Ref("remote-1"))
	if env := mirror.EnvironmentOf(rec.Payload); env == nil || env.Systems["question"] == nil {
		t.Errorf("the resumed record's environment is %v, want it carried", env)
	}
}

// A TASK'S SPAWN WRITES ITS LAUNCH ON ITS RECORD: the environment it was given, its systems
// alone, since its vars are data handed to the script and the record is no place for them.
func TestASpawnWritesItsLaunchOnTheRecord(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": "launched", "spawn": map[string]any{
		"script": "true", "environment": map[string]any{"vars": map[string]any{"TOKEN": "s3cret"},
			"systems": map[string]any{"question": map[string]any{}, "network": map[string]any{"mode": "open"}}},
	}})
	if out := readBody(t, resp); resp.StatusCode/100 != 2 {
		t.Fatalf("spawn = %s: %s", resp.Status, out)
	}
	waitTaskStatus(t, srv, "launched", "done")
	rec, _, _ := srv.Store.Get(mirror.Ref("launched"))
	if env := mirror.EnvironmentOf(rec.Payload); env == nil || env.Systems["question"] == nil {
		t.Errorf("the record's environment is %v, want the spawn's", entity.ToObj(rec.Payload)["environment"])
	}
	if env, _ := entity.ToObj(rec.Payload)["environment"].(map[string]any); env["vars"] != nil {
		t.Errorf("the record's environment carries the spawn's vars: %v", env)
	}
}

// WHAT VERSE HOSTED OF A TASK IS DROPPED A RETENTION AFTER THE TASK ENDS, and a later run's under
// the same id is not.
func TestWhatVerseHostedOfATaskIsDroppedAfterItEnds(t *testing.T) {
	srv := testServer(t)
	srv.HostedRetention = time.Hour
	clock := clockOf(t, srv)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": "brief", "spawn": map[string]any{
		"script": "true", "environment": map[string]any{"systems": map[string]any{"network": map[string]any{"mode": "open"}}},
	}})
	if out := readBody(t, resp); resp.StatusCode/100 != 2 {
		t.Fatalf("spawn = %s: %s", resp.Status, out)
	}
	waitTaskStatus(t, srv, "brief", "done")
	// The record says done before the end arms the retention, so the clock is moved until the
	// timer has been armed and passed.
	waitRetired(t, srv, clock, "brief")

	first := srv.host("again", hostedTask{scope: "first"})
	srv.retire("again", first)
	srv.host("again", hostedTask{scope: "second"})
	clock.Advance(2 * srv.HostedRetention)
	if sc, ok := srv.hostedScope("again"); !ok || sc != "second" {
		t.Errorf("a later run's entry is %q (%v), want it kept past the first run's retention", sc, ok)
	}
}

// A TASK RESUMED AT A RESTART IS DROPPED A RETENTION AFTER IT ENDS, as one launched here is: it is
// hosted again before it is reflected, so its end retires that entry.
func TestWhatVerseHostedOfAResumedTaskIsDroppedAfterItEnds(t *testing.T) {
	remote := remoteRuntime(t, 5)
	client.HTTP(remote.URL).Launch(runner.Launch{ID: "resumed", Script: "true"})
	srv := testServer(t)
	srv.HostedRetention = time.Hour
	clock := clockOf(t, srv)
	entitytest.Set(t, srv.Store, mirror.Ref("resumed"), entity.Obj{
		"status": string(task.Running), "runner": remote.URL,
		"environment": entity.Obj{"systems": entity.Obj{"question": entity.Obj{}}},
	}.Payload())

	srv.reconcileTasks()

	waitTaskStatus(t, srv, "resumed", "done")
	waitRetired(t, srv, clock, "resumed")
}

// waitRetired moves the clock past the retention until what verse hosted of id is gone. The
// record says the task ended before its end arms the retention, so one advance may come before
// the timer is set.
func waitRetired(t *testing.T, srv *Server, clock *fakeClock, id string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		clock.Advance(2 * srv.HostedRetention)
		if _, ok := srv.hosted.Load(id); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("what verse hosted of %s was kept past its end and retention", id)
		}
	}
}

// A RUN'S END RETIRES THAT RUN'S ENTRY, read when it was reflected: a later run hosted under the
// same id while the first was still going is not dropped when the first ends.
func TestARunsEndLeavesALaterRunsEntry(t *testing.T) {
	remote := remoteRuntime(t, 5)
	client.HTTP(remote.URL).Launch(runner.Launch{ID: "twice", Script: "true"})
	srv := testServer(t)
	srv.HostedRetention = time.Hour
	clock := clockOf(t, srv)

	first := srv.host("twice", hostedTask{scope: "first"})
	srv.reflect(srv.Store, "twice", first, client.HTTP(remote.URL).Attach("twice"), mirror.Meta{Runner: remote.URL})
	srv.host("twice", hostedTask{scope: "second"})

	waitTaskStatus(t, srv, "twice", "done")
	clock.Advance(2 * srv.HostedRetention)
	if sc, ok := srv.hostedScope("twice"); !ok || sc != "second" {
		t.Errorf("the later run's entry is %q (%v), want it kept past the first run's end", sc, ok)
	}
}

// A SPAWN REFUSED BEFORE ITS LAUNCH HOSTED NOTHING, and its end retires nothing: a run standing
// under the same id keeps its entry.
func TestARefusedSpawnLeavesTheRunStandingUnderItsID(t *testing.T) {
	srv := testServer(t)
	noLocalRunner(t, srv) // and no runner is registered, so nothing takes the spawn
	srv.HostedRetention = 20 * time.Millisecond
	srv.host("standing", hostedTask{scope: "live"})

	h := srv.spawnerFor(srv.Store, "", srv.Eng)("standing", launch.Spec{Script: "true"})
	if h == nil || !strings.Contains(h.Err(), "no runner offers scripts") {
		t.Fatalf("a spawn no runner takes: %v, want it failed saying no runner offers it", h)
	}
	time.Sleep(10 * srv.HostedRetention)
	if sc, ok := srv.hostedScope("standing"); !ok || sc != "live" {
		t.Errorf("the standing run's entry is %q (%v), want it kept past a refused spawn's end", sc, ok)
	}
}
