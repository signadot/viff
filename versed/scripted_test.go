package versed

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// waitTaskStatus waits for the mirrored task to reach status, and says what it saw if it does
// not.
func waitTaskStatus(t *testing.T, srv *Server, id, status string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		e, ok, _ := srv.Store.Get(mirror.Ref(id))
		if ok && entity.ToObj(e.Payload)["status"] == status {
			return
		}
		select {
		case <-deadline:
			e, _, _ := srv.Store.Get(mirror.Ref(id))
			t.Fatalf("%s never reached %q; have %+v", id, status, entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// noQuestionWithin fails if a question is raised for the task inside d.
func noQuestionWithin(t *testing.T, srv *Server, id string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		for _, ent := range entity.Below(srv.Store, mirror.IsQuestion, 1, entity.VerseRoot, mirror.AskSystem) {
			if mirror.TaskFrom(ent.Payload) == id {
				t.Fatalf("%s posed a question it should not have: %+v", id, entity.ToObj(ent.Payload))
			}
		}
		select {
		case <-deadline:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A SPAWN THAT NAMES NO POLICY IS GIVEN NO VERB: its first step is refused, and nobody is asked.
// The script a charter writes is one action and no step, so the step here is the task's own
// (stepBuilder); a script's step is a tool it calls.
func TestASpawnWithNoPolicyIsGivenNoVerb(t *testing.T) {
	srv := serverWith(t, stepBuilder(t), entitytest.Store(t))
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "unstated", Spawn: launch.Spec{Script: "true", Environment: openNet}})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	noQuestionWithin(t, srv, "unstated", 300*time.Millisecond)
}

// A script that says `auto` runs unattended, which is how a charter that means it says so: one
// line, and the same line an agent's spawn uses.
func TestAScriptedSpawnCanSayAuto(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "vectored", Spawn: launch.Spec{Script: "true", Environment: openNet, Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}}})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	noQuestionWithin(t, srv, "vectored", 200*time.Millisecond)
	waitTaskStatus(t, srv, "vectored", "done")
}

// A failing script fails the task: a trigger's onDone never fires on a command that did not
// work.
func TestAScriptedCommandThatFailsFailsTheTask(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "broken", Spawn: launch.Spec{Script: "exit 3", Environment: openNet, Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}}})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	waitTaskStatus(t, srv, "broken", "failed")
}
