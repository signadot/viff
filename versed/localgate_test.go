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
	"github.com/signadot/verse/process"
	"github.com/signadot/verse/process/processd"
)

// askingServer runs local processes under an Ask vector, so a spawned one blocks on its first
// action and there is a real process gate to answer.
func askingServer(t *testing.T) *Server {
	t.Helper()
	return NewWithStore(askingBuilder(t), entitytest.Store(t))
}

// askingBuilder is askingServer's half that is not the store, for a test that wraps one.
func askingBuilder(t *testing.T) processd.Builder {
	t.Helper()
	return func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Ask}, reg)
		return process.NewProcess(l.ID, sup, &scriptDriver{command: l.Task}, &shellExec{root: t.TempDir()})
	}
}

// waitQuestion waits for a pending question raised by procID and returns its id.
func waitQuestion(t *testing.T, srv *Server, procID string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
			return ref.System == mirror.QuestionSystem && ref.Kind == mirror.QuestionKind
		}) {
			p := entity.ToObj(ent.Payload)
			if p["process"] == procID && p["status"] == mirror.QuestionPending {
				return ent.Ref.ID
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s never posed a question", procID)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// The LOCAL leg of `POST /processes/{id}/resolve`, which had no test.
//
// It is the leg that used to be forwarded rather than handled: a local process's answer went
// to processd's own HTTP face, which reads `{decisionId, proceed}` — so an api.Resolve body
// arrived as an empty decision id and answered nothing. The brokered leg was covered and the
// local one was not, which is exactly why the difference could sit there.
//
// What this pins is that both legs of this route now speak one body. `verse process approve`
// on a local process must answer it, and the process must go on.
func TestProcessResolveAnswersALocalGate(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/processes", api.Spawn{ID: "local-1", Task: "true"})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	qid := waitQuestion(t, srv, "local-1")

	resp := postTony(t, ops.URL+"/processes/local-1/resolve",
		api.Resolve{ID: qid, Approve: true, By: "human:scott"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resolving a LOCAL process gate = %s, want 204 — the body did not reach the decision", resp.Status)
	}

	// And it actually released the process, rather than merely being accepted. A 204 from a
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
