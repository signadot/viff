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
	"github.com/signadot/verse/resolver"
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
//
// It used to hand-build an Ask supervisor because ScriptedBuilder forced Auto and there was no
// way to ask a scripted task for a question. There is now: a spawn that names no vector asks,
// so this is the ordinary builder with a root to run in.
func askingBuilder(t *testing.T) taskd.Builder {
	t.Helper()
	return func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		return NewScriptedTask(l, t.TempDir(), reg)
	}
}

// waitQuestion waits for a pending question raised by taskID and returns its id.
func waitQuestion(t *testing.T, srv *Server, taskID string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
			return ref.System() == mirror.QuestionSystem && ref.Kind() == mirror.QuestionKind
		}) {
			p := entity.ToObj(ent.Payload)
			if p["task"] == taskID && p["status"] == mirror.QuestionPending {
				return ent.Ref.ID()
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s never posed a question", taskID)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitOwned waits for a question to be OWNED, not merely posed.
//
// The two are separate writes: the question is posted `pending` and the standing layer takes it
// a moment later. A test that reads `resolver` the instant waitQuestion returns is racing that
// second write, and on a loaded machine it loses — which is the same shape 602d25e3 fixed for
// the tests that read the instant a route answered 204 (git-issue nkptp9b3h12ksx3kg9n0).
func waitOwned(t *testing.T, srv *Server, qid string) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		p := question(t, srv, qid)
		if p["resolver"] != nil {
			return p
		}
		select {
		case <-deadline:
			t.Fatalf("question %s was never owned: %+v", qid, p)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitInstance waits for a resolver INSTANCE to be minted and returns its id.
//
// A question and the instance beside it are two writes: mirror.Watch mints the instance when the
// question is posed, and a test that builds an instance id and addresses it the moment
// waitQuestion returns is racing that second write. Every route under
// /resolvers/instance/{id} answers 404 until it lands, so the race shows up as "no such
// instance" rather than as a wait (git-issue nkptp9b3h12ksx3kg9n0).
func waitInstance(t *testing.T, srv *Server, name, origin string) string {
	t.Helper()
	ref := resolver.InstanceRef(name, origin)
	deadline := time.After(5 * time.Second)
	for {
		if _, ok, _ := srv.Store.Get(ref); ok {
			return ref.ID()
		}
		select {
		case <-deadline:
			t.Fatalf("instance %s was never minted", ref.ID())
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
		api.Resolve{ID: qid, Choice: "proceed", By: "human:scott"})
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
