package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// shellGate is one gate over the scripted task's one action, with the question it poses.
func shellGate(need int, clock *task.Deadline) []task.Gate {
	return []task.Gate{{
		Name: "shell", Mode: task.Ask, Match: task.GateMatch{Tools: []string{"run_bash"}},
		Need: need, Deadline: clock,
	}}
}

// waitTaskEnded waits for the mirrored task to reach any terminal status.
func waitTaskEnded(t *testing.T, srv *Server, id string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		e, ok, _ := srv.Store.Get(mirror.Ref(id))
		if ok && task.Status(entity.Str(e.Payload, "status")).Terminal() {
			return
		}
		select {
		case <-deadline:
			e, _, _ := srv.Store.Get(mirror.Ref(id))
			t.Fatalf("%s never ended; have %+v", id, entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A task's own gate takes a threshold: the question it poses says so, and the loop does not
// pass until the second ballot. This is the runtime half of the one gate record — a task's
// action gates used to pose two-option questions whatever the record could carry.
func TestARunsGateTakesAThreshold(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postTony(t, ts.URL+"/tasks", api.Spawn{ID: "voted", Prompt: "true", Gates: shellGate(2, nil)})
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("spawn = %s", resp.Status)
	}
	qid := waitQuestion(t, srv, "voted")
	q := question(t, srv, qid)
	opts, _ := q["options"].([]any)
	if len(opts) != 2 {
		t.Fatalf("the record does not carry the gate's options: %+v", q)
	}
	if first, _ := opts[0].(map[string]any); first["id"] != task.Proceed || first["need"] != int64(2) {
		t.Fatalf("proceed is not at the gate's threshold: %+v", q["options"])
	}

	one := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: qid, Choice: task.Proceed, By: "user:a@example.com"})
	one.Body.Close()
	if one.StatusCode != http.StatusAccepted {
		t.Fatalf("the first ballot = %s, want 202 — one of two is not a settle", one.Status)
	}
	if q := question(t, srv, qid); q["status"] != mirror.QuestionPending {
		t.Fatalf("one ballot settled a two-ballot question: %+v", q)
	}
	noTerminalWithin(t, srv, "voted", 100*time.Millisecond)

	two := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: qid, Choice: task.Proceed, By: "user:b@example.com"})
	two.Body.Close()
	if two.StatusCode != http.StatusNoContent {
		t.Fatalf("the second ballot = %s, want 204", two.Status)
	}
	if q := settledQuestion(t, srv, qid); q["verdict"] != task.Proceed {
		t.Fatalf("two ballots settled on %v: %+v", q["verdict"], q)
	}
	waitTaskStatus(t, srv, "voted", string(task.Done))
}

// noTerminalWithin fails if the task ends inside d — a gate that let it through early.
func noTerminalWithin(t *testing.T, srv *Server, id string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		if e, ok, _ := srv.Store.Get(mirror.Ref(id)); ok && task.Status(entity.Str(e.Payload, "status")).Terminal() {
			t.Fatalf("%s ended before its question settled: %+v", id, entity.ToObj(e.Payload))
		}
		select {
		case <-deadline:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A task's own gate takes a deadline, and the clock's default is delivered to the loop as an
// answer: the run that hits an `ask` with nobody attached no longer parks until somebody finds
// it — with `default: deny` the action fails back to the task as a refusal it can re-plan
// around, and the record says the clock decided.
func TestARunsGateDeadlineAnswersTheLoop(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postTony(t, ts.URL+"/tasks", api.Spawn{ID: "unattended", Prompt: "true",
		Gates: shellGate(0, &task.Deadline{Within: "150ms", Default: task.Deny})})
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("spawn = %s", resp.Status)
	}
	qid := waitQuestion(t, srv, "unattended")
	rec, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
	if within, dflt, _ := mirror.DeadlineOf(rec.Payload); within != "150ms" || dflt != task.Deny {
		t.Fatalf("the record does not carry the gate's deadline: %+v", question(t, srv, qid))
	}

	// Nobody answers. The clock does, as itself.
	q := settledQuestion(t, srv, qid)
	if q["by"] != task.ByDeadline || q["verdict"] != task.Deny {
		t.Fatalf("settled by %v on %v, want the deadline on its default: %+v", q["by"], q["verdict"], q)
	}
	// And the loop heard it: the one action was refused, so the run ends rather than standing.
	waitTaskEnded(t, srv, "unattended")
}

// A gate whose question cannot be posed as written refuses the spawn, before anything runs.
func TestASpawnWithAGateThatAsksNobodyIsRefused(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	gates := shellGate(2, nil)
	gates[0].Mode = task.Auto
	resp := postTony(t, ts.URL+"/tasks", api.Spawn{ID: "malformed", Prompt: "true", Gates: gates})
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "gate shell") {
		t.Fatalf("spawn = %s %s, want 400 naming the gate", resp.Status, strings.TrimSpace(string(raw)))
	}
	if _, ok, _ := srv.Store.Get(mirror.Ref("malformed")); ok {
		t.Fatal("a refused spawn was mirrored anyway")
	}
	// The same document over the running task's dial is refused the same way.
	ok := postTony(t, ts.URL+"/tasks", api.Spawn{ID: "running", Prompt: "sleep 5", Vector: "auto"})
	ok.Body.Close()
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/tasks/running/vector",
		strings.NewReader(`{vector: {default: auto, gates: [{name: shell, mode: auto, need: 2}]}, by: "user:scott@example.com"}`))
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(api.PrincipalHeader, tester)
	slid, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("vector: %v", err)
	}
	slid.Body.Close()
	if slid.StatusCode != http.StatusBadRequest {
		t.Fatalf("sliding to a malformed vector = %s, want 400", slid.Status)
	}
}
