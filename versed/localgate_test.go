package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/taskd"
)

// askingServer runs local tasks under an Ask vector, so a spawned one blocks on its first
// action and there is a real task gate to answer.
func askingServer(t *testing.T) *Server {
	t.Helper()
	return NewWithStore(askingBuilder(t), entitytest.Store(t))
}

// askingBuilder is askingServer's half that is not the store, for a test that wraps one.
func askingBuilder(t *testing.T) taskd.Builder {
	t.Helper()
	return func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Ask}, reg)
		return task.NewTask(l.ID, sup, &scriptDriver{command: l.Prompt}, &shellExec{root: t.TempDir()})
	}
}

// waitQuestion waits for a pending question raised by taskID and returns its id.
func waitQuestion(t *testing.T, srv *Server, taskID string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
			return ref.System == mirror.QuestionSystem && ref.Kind == mirror.QuestionKind
		}) {
			p := entity.ToObj(ent.Payload)
			if p["task"] == taskID && p["status"] == mirror.QuestionPending {
				return ent.Ref.ID
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s never posed a question", taskID)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// The LOCAL leg of `POST /tasks/{id}/resolve`, which had no test.
//
// It is the leg that used to be forwarded rather than handled: a local task's answer went
// to taskd's own HTTP face, which reads `{decisionId, proceed}` — so an api.Resolve body
// arrived as an empty decision id and answered nothing. The brokered leg was covered and the
// local one was not, which is exactly why the difference could sit there.
//
// What this pins is that both legs of this route now speak one body. `verse task approve`
// on a local task must answer it, and the task must go on.
func TestTaskResolveAnswersALocalGate(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "local-1", Prompt: "true"})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	qid := waitQuestion(t, srv, "local-1")

	resp := postTony(t, ops.URL+"/tasks/local-1/resolve",
		api.Resolve{ID: qid, Approve: true, By: "human:scott"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resolving a LOCAL task gate = %s, want 204 — the body did not reach the decision", resp.Status)
	}

	// And it actually released the task, rather than merely being accepted. A 204 from a
	// route that answered nothing is the failure this test exists to catch.
	deadline := time.After(3 * time.Second)
	for {
		e, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
		if entity.ToObj(e.Payload)["status"] == mirror.QuestionAnswered {
			return
		}
		select {
		case <-deadline:
			e, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
			t.Fatalf("the question never settled: %+v", entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}
