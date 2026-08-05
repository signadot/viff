package runner_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/taskd"
)

// a driver that takes `steps` no-op actions then completes.
type doneDriver struct{ steps int }

func (d *doneDriver) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	if d.steps <= 0 {
		return task.ToolUse{}, false, nil // complete
	}
	d.steps--
	return task.ToolUse{Name: "noop"}, true, nil
}

type okExec struct{}

func (okExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu}, nil, nil
}

// The engine drives a REMOTE taskd entirely over HTTP: httpRunner.Spawn issues the
// spawn, remoteHandle.Subscribe streams the SSE read plane (cursored), and the task
// runs to a terminal status — no in-process handle. This is the Phase-3 proof of the
// base-URL decoupling.
func TestHTTPRunnerDrivesRemoteTaskd(t *testing.T) {
	build := func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		return task.NewTask(l.ID, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(taskd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	h := pol.Spawn(task.Launch{ID: "t1", Prompt: "do a thing", Driver: "scripted"}, nil)

	ch, unsub := h.Subscribe()
	defer unsub()

	var sawCursor, sawExec, completed bool
	timeout := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break loop
			}
			if ev.Cursor > 0 {
				sawCursor = true
			}
			if ev.Kind == task.EvExec {
				sawExec = true
			}
			if ev.Kind == task.EvStatus && ev.Text == "done" {
				completed = true
				break loop
			}
		case <-timeout:
			t.Fatal("timed out waiting for the remote task to complete")
		}
	}
	if !completed {
		t.Fatal("never saw a completed status over the wire")
	}
	if !sawCursor {
		t.Fatal("events carried no cursor")
	}
	if !sawExec {
		t.Fatal("never saw the action execute remotely")
	}
}

// A remote runner enumerates its tasks so versed can union them into `task list`/
// `get`: ListTasks/TaskSnapshot are a thin client over the remote's GET /tasks.
func TestHTTPRunnerListsRemoteTasks(t *testing.T) {
	build := func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		return task.NewTask(l.ID, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(taskd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	pol.Spawn(task.Launch{ID: "r1", Prompt: "remote task", Driver: "scripted"}, nil)

	// task.Lister by NAME, not a structural assertion written out here. Spelling the
	// signature inline is what let a widened one report false instead of failing to build.
	lister, ok := pol.(task.Lister)
	if !ok {
		t.Fatal("httpRunner should implement task.Lister")
	}

	insts, err := lister.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks against a live runner = %v, want no error", err)
	}
	var found bool
	for _, m := range insts {
		if m.ID == "r1" {
			found = true
		}
	}
	if !found {
		t.Fatal("remote task r1 not in the runner's ListTasks")
	}

	snap, err := lister.TaskSnapshot("r1")
	if err != nil || snap.Prompt != "remote task" {
		t.Fatalf("TaskSnapshot(r1) = %+v, %v; want the remote task", snap, err)
	}
	// An id the runner has never heard of is ErrNoSuchTask — the one error that means
	// "look for your typo", and the only one `task get` may answer 404 to.
	if _, err := lister.TaskSnapshot("nope"); !errors.Is(err, task.ErrNoSuchTask) {
		t.Fatalf("TaskSnapshot of an unknown id = %v, want ErrNoSuchTask", err)
	}
}

// A runner that REFUSES a spawn must not read as one that took it. The status used to be
// discarded, so 400/403/501 all came back as a live remote handle: versed mirrored a
// task:instance for work that was never started and the flow waited on it forever, so the
// refusal surfaced as a hang. A runner declining a launch it cannot host is the whole
// mechanism by which a runner gets to refuse, and it is inert unless this reads it.
func TestHTTPRunnerSpawnReadsARefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"bad request", http.StatusBadRequest, "unknown driver \"llm\""},
		{"not implemented", http.StatusNotImplemented, "this runner does not host brokered work"},
		{"no body", http.StatusForbidden, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, tc.body, tc.code)
			}))
			defer srv.Close()

			h := runner.HTTP(srv.URL).Spawn(task.Launch{ID: "x", Prompt: "t", Driver: "scripted"}, nil)

			// The refusal has to arrive as a terminal on the plane the flow already reads,
			// which is what FailedHandle is for.
			ch, _ := h.Subscribe()
			ev, open := <-ch
			if !open {
				t.Fatal("a refused spawn produced no frame at all; the flow waits forever")
			}
			if ev.Kind != task.EvStatus || ev.Text != string(task.Failed) {
				t.Fatalf("frame = %+v, want a failed status — the refusal read as an accepted spawn", ev)
			}
			if ev.Err == "" {
				t.Fatal("the failure carries no reason, so the operator is told only that it failed")
			}
			if tc.body != "" && !strings.Contains(ev.Err, tc.body) {
				t.Fatalf("reason = %q, want the runner's own words (%q)", ev.Err, tc.body)
			}
		})
	}
}

// And the read plane: a list that could not be taken must not read as an empty one. nil could
// not carry the difference, so an unreachable runner's tasks were absent from the unified
// list and the response was a clean 200 that was simply short.
func TestHTTPRunnerListReportsWhyItIsEmpty(t *testing.T) {
	// Unreachable: a server that is listening at bind time and gone at call time — the
	// runner that was torn down, which is the ordinary case, not an exotic one.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	if _, err := runner.HTTP(deadURL).(task.Lister).ListTasks(); !errors.Is(err, task.ErrUnreachable) {
		t.Fatalf("a dead runner's list = %v, want ErrUnreachable", err)
	}

	// Answering, but not as a runner: a broker's WORKER leg serves claim/inbox/report and no
	// GET /tasks, so a url pointed at one 404s. That is a different fact from unreachable
	// — something is there, and it is not this contract.
	worker := httptest.NewServer(http.NewServeMux())
	defer worker.Close()
	if _, err := runner.HTTP(worker.URL).(task.Lister).ListTasks(); !errors.Is(err, task.ErrUnsupported) {
		t.Fatalf("a non-runner url's list = %v, want ErrUnsupported", err)
	}

	// And the single-task read tells the same three apart. This one always DID check its
	// status, so no refusal ever passed for an answer — but every failure collapsed into one
	// `false`, which the caller could only report as "no such task".
	if _, err := runner.HTTP(deadURL).(task.Lister).TaskSnapshot("p"); !errors.Is(err, task.ErrUnreachable) {
		t.Fatalf("a dead runner's snapshot = %v, want ErrUnreachable — a task on an "+
			"unreachable runner is not a task that does not exist", err)
	}

	// Answering with something undecodable is also not an empty verse: discarding this is how
	// a format drift on either end reads as a runner with nothing running.
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not":"a list"`))
	}))
	defer junk.Close()
	if _, err := runner.HTTP(junk.URL).(task.Lister).ListTasks(); err == nil {
		t.Fatal("an undecodable body listed as no tasks at all")
	}
}

// Phase 4 resume: a task:instance left stuck "running" (verse crashed mid-run),
// tagged with a remote runner, is re-attached on restart — the replay from the remote
// (which kept running and finished) converges the entity to its terminal status. This is
// what versed.reconcileTasks does: resume, not orphan, when the work outlived verse.
func TestResumeReAttachesToRemoteTask(t *testing.T) {
	build := func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		return task.NewTask(l.ID, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(taskd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	pol.Spawn(task.Launch{ID: "t1", Prompt: "task", Driver: "scripted"}, nil) // runs on the remote to completion
	time.Sleep(300 * time.Millisecond)                                        // let it finish (retained in the ring)

	// "verse restart": a fresh store with a STALE running entity tagged with the runner.
	store := entitytest.Store(t)
	entitytest.Set(t, store, mirror.Ref("t1"), entity.Obj{"status": "running", "runner": srv.URL}.Payload())

	// resume: re-attach and re-mirror (the core of reconcileTasks).
	mirror.Reflect(store, "t1", pol.Attach("t1"), mirror.Meta{Runner: srv.URL})

	deadline := time.After(5 * time.Second)
	for {
		e, _, _ := store.Get(mirror.Ref("t1"))
		if s, _ := entity.ToObj(e.Payload)["status"].(string); s == string(task.Done) {
			return // converged: resume recovered the terminal from the remote
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(mirror.Ref("t1"))
			t.Fatalf("entity never converged to completed; status=%v", entity.ToObj(e.Payload)["status"])
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A gate on a REMOTE task is answerable. Without remoteHandle.Resolve this handle was not
// a gateResolver at all, so every route into it refused with a message about the question —
// a task on another host could be blocked on a decision that no operator could give.
//
// It also pins the vocabulary: the handle sends api.Resolve (`id`/`approve`), and the remote
// runtime understands it. When those two disagree the answer decodes to an empty id, answers
// nothing, and comes back as though the decision were unknown.
func TestHTTPRunnerAnswersARemoteGate(t *testing.T) {
	build := func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		// Ask, and resolved by the daemon's own registry — so the task really blocks and
		// really waits for an answer to arrive over HTTP.
		sup := task.NewSupervisor(&task.Vector{Default: task.Ask}, reg)
		return task.NewTask(l.ID, sup, &doneDriver{steps: 1}, okExec{})
	}
	srv := httptest.NewServer(taskd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	h := pol.Spawn(task.Launch{ID: "t-gate", Prompt: "do a thing", Driver: "scripted"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	rh, ok := h.(interface {
		Resolve(decision string, proceed bool, say, by string) error
	})
	if !ok {
		t.Fatal("a remote handle cannot answer a gate, so a remote task can block forever")
	}

	var decision string
	timeout := time.After(5 * time.Second)
	for decision == "" {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended before the task gated")
			}
			if ev.Kind == task.EvPending {
				decision = ev.ID
			}
		case <-timeout:
			t.Fatal("the remote task never raised a gate")
		}
	}

	if err := rh.Resolve(decision, true, "go ahead", "human:scott"); err != nil {
		t.Fatalf("answering the remote gate %s = %v", decision, err)
	}
	// Reported success is not delivery. The task has to actually go on, and the answer has
	// to arrive attributed — an answer that lost its `by` on the wire is an unattributable
	// decision recorded as though it were attributed.
	timeout = time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended without a decision")
			}
			if ev.Kind == task.EvDecision && ev.ID == decision {
				if !ev.Proceed {
					t.Fatalf("the remote read a deny from an approve: %+v", ev)
				}
				if ev.By != "human:scott" {
					t.Fatalf("by = %q, want human:scott — attribution was dropped on the wire", ev.By)
				}
				return
			}
		case <-timeout:
			t.Fatal("the remote task never acted on the answer")
		}
	}
}

// And the older spelling still works, because things dial in against this face. A client
// sending {decisionId, proceed} must still be understood, or adding the new vocabulary would
// have been a break dressed as an addition.
func TestTaskdStillReadsTheOlderResolveSpelling(t *testing.T) {
	build := func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Ask}, reg)
		return task.NewTask(l.ID, sup, &doneDriver{steps: 1}, okExec{})
	}
	srv := httptest.NewServer(taskd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	h := pol.Spawn(task.Launch{ID: "t-old", Prompt: "do a thing", Driver: "scripted"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	var decision string
	timeout := time.After(5 * time.Second)
	for decision == "" {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended before the task gated")
			}
			if ev.Kind == task.EvPending {
				decision = ev.ID
			}
		case <-timeout:
			t.Fatal("the remote task never raised a gate")
		}
	}

	body, err := codec.Marshal(map[string]any{
		"decisionId": decision, "proceed": true, "by": "human:legacy",
	}, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(srv.URL+"/tasks/t-old/resolve", codec.MediaType(codec.Tony), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the older spelling = %s, want 204", resp.Status)
	}
}

// The contract as documented mixes the two spellings — `{id, proceed}` — and things dial in
// against the document, not against the struct. Read as two fixed vocabularies that combination
// yields a DENY: the id is recognised and the verdict field beside it is not, so an approval
// arrives as a refusal and the task stops. It has to be read as an approval.
func TestTaskdReadsTheDocumentedMixedSpelling(t *testing.T) {
	build := func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Ask}, reg)
		return task.NewTask(l.ID, sup, &doneDriver{steps: 1}, okExec{})
	}
	srv := httptest.NewServer(taskd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	h := pol.Spawn(task.Launch{ID: "t-mixed", Prompt: "do a thing", Driver: "scripted"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	var decision string
	timeout := time.After(5 * time.Second)
	for decision == "" {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended before the task gated")
			}
			if ev.Kind == task.EvPending {
				decision = ev.ID
			}
		case <-timeout:
			t.Fatal("the remote task never raised a gate")
		}
	}

	body, err := codec.Marshal(map[string]any{"id": decision, "proceed": true, "by": "human:doc"}, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(srv.URL+"/tasks/t-mixed/resolve", codec.MediaType(codec.Tony), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the documented spelling = %s, want 204", resp.Status)
	}

	timeout = time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended without a decision")
			}
			if ev.Kind == task.EvDecision && ev.ID == decision {
				if !ev.Proceed {
					t.Fatal("`{id, proceed: true}` was read as a deny — an approval became a refusal")
				}
				return
			}
		case <-timeout:
			t.Fatal("the remote task never acted on the answer")
		}
	}
}
