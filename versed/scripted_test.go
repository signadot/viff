package versed

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
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
		for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
			return mirror.IsQuestion(ref)
		}) {
			if entity.ToObj(ent.Payload)["task"] == id {
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

// A scripted spawn that names no vector ASKS.
//
// This is the contract change, pinned rather than described. The scripted driver used to build
// its own Auto vector and ignore the one the spawn carried, so every `driver: scripted` charter
// ran its shell command unattended whatever it had written — and a charter that asked to be
// asked was disobeyed in silence. Now the spawn governs, and a spawn that says nothing gets the
// same answer an `llm` spawn saying nothing gets about running a command: ask.
func TestAScriptedSpawnWithNoVectorAsks(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "unvectored", Prompt: "true"})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	waitQuestion(t, srv, "unvectored")
}

// And a spawn that says `auto` runs unattended, which is how a charter that means it says so —
// one line, and the same line an llm spawn uses.
func TestAScriptedSpawnCanSayAuto(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "vectored", Prompt: "true", Vector: "auto"})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	noQuestionWithin(t, srv, "vectored", 200*time.Millisecond)
	waitTaskStatus(t, srv, "vectored", "done")
}

// A failing command still fails the task, which is what a scripted run has always promised and
// is now the call list's default `fatal` doing the same job. A trigger's onDone firing on a
// command that never worked is the thing neither version allows.
func TestAScriptedCommandThatFailsFailsTheTask(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "broken", Prompt: "exit 3", Vector: "auto"})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	waitTaskStatus(t, srv, "broken", "failed")
}
