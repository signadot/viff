package viffd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/systems/systemstest"
	"go.viff.dev/viff/pkg/task"
)

// A gate raised on a PER-SPAWN runner can be answered.
//
// It could be listed and not answered, which is the worst shape a defect can take here: the
// docket showed the question, the operator answered it, and viff said "no such question" — so
// the obvious reading was a typo, and the actual cause was that nothing on the answer path
// could see a task that had gone somewhere by name.
//
// Both halves of the lookup consulted `s.runner`, this viff instance's DEFAULT runner, while the whole
// point of `runner:` on a spawn is that the work went elsewhere. Listing worked because it reads
// the mirror; answering did not because it read a field. So this test spawns onto a runner that
// is explicitly NOT the default, and answers.
func TestGateOnPerSpawnRunnerIsAnswerable(t *testing.T) {
	// A face that asks before it acts, so there is a real question to answer. Its registry is
	// the one that must receive the answer, and it is not this viff instance's.
	build := func(l runner.Launch, reg *task.Registry) *task.Task {
		gating := task.Autonomy{{Name: "act", Match: &task.Match{System: "commands", Verb: "run"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}}
		// ONE action, then done. A driver that keeps proposing would park on a second gate
		// after this test answers the first, and a task that never reaches a terminal holds
		// the mirror's stream open — so the test would hang in httptest.Close rather than
		// fail. Same hazard runnerFace guards with its release channel.
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, gating, reg), l.Environment)
		sup.SetStepOf(commandStep)
		return task.NewTask(l.ID, sup, &oneAction{}, okExecutor{})
	}
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	face := provenFace(t, ts.URL, offeringAll(runnerd.NewInProcessHandler(build), "fake").HTTPHandler(), func() {})
	// Cleanups run last-registered-first, so this drains before the face closes. Without it a
	// FAILING run leaves the gate unanswered, the task parked, and the mirror's stream open —
	// and httptest.Close waits on outstanding requests, so the test would report a timeout
	// instead of the assertion that actually failed. Answered straight at the runner, which is
	// the path this test is not testing.
	var spawned string // set once the id is known; the cleanup closes over it
	t.Cleanup(func() {
		if spawned == "" {
			return
		}
		if q := pendingOn(t, face.Bare, spawned); q != "" {
			postTony(t, face.Bare+"/tasks/"+spawned+"/resolve", map[string]any{
				"id": q, "choice": "proceed", "by": "cleanup",
			}).Body.Close()
		}
	})

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "code", "url": face.URL, "subject": face.Subject,
	}).Body.Close()

	// Spawned onto that runner BY NAME. The viff instance's own default runner is the in-process one
	// and knows nothing about this task, which is the condition being tested.
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"name": "onrunner", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "work", "environment": stepEnv}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn = %s", resp.Status)
	}
	var sp map[string]any
	decodeResp(t, resp, &sp)
	id, _ := sp["id"].(string)
	if id == "" {
		t.Fatal("no task id")
	}
	spawned = id

	// Wait for the gate to be posed on the far side, then answer it through viff.
	var qid string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if q := pendingOn(t, face.Bare, id); q != "" {
			qid = q
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if qid == "" {
		t.Fatal("the runner never posed a gate; nothing to answer")
	}
	// And for viff to hold it, as the docket an operator answers from does: the question is
	// written after the runner poses it, and an answer racing that write is answering nothing.
	waitQuestionStatus(t, srv, qid, mirror.QuestionPending)

	ans := postTony(t, ts.URL+answerPath(qid), map[string]any{
		"choice": "proceed", "by": tester,
	})
	defer ans.Body.Close()
	if ans.StatusCode == http.StatusNotFound {
		t.Fatalf("answering a gate on a per-spawn runner = 404 no such question — viff can "+
			"list it and not answer it (question %s)", qid)
	}
	if ans.StatusCode != http.StatusNoContent {
		t.Fatalf("answer = %s, want 204", ans.Status)
	}
}

// pendingOn asks a runner face directly what question it currently has open for a task —
// deliberately not through viff, so the test's precondition does not depend on the path it is
// testing.
func pendingOn(t *testing.T, base, id string) string {
	t.Helper()
	resp, err := http.Get(base + "/tasks/" + id)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var inst task.Snapshot
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type"))
	if !ok {
		f = codec.Default()
	}
	if codec.Unmarshal(body, &inst, f) != nil || len(inst.Pending) == 0 {
		return ""
	}
	return inst.Pending[0].ID
}

// oneAction proposes a single gated action and then reports the work done.
type oneAction struct{ done bool }

func (d *oneAction) Next(_ context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	if d.done {
		return task.ToolUse{}, false, nil
	}
	d.done = true
	return task.ToolUse{Name: "noop"}, true, nil
}

// A task's read plane can be attached wherever it runs.
//
// Same root cause as the gate above and a wider blast radius: `streamEvents` reached for
// `s.runner` — the DEFAULT runner — so `viff task attach` on work sent somewhere by name
// answered `404 page not found`. A resolver reads this same stream (see the note on
// streamEvents), so supervision of remote work went with it.
func TestAttachToTaskOnPerSpawnRunner(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	face := runnerFace(t, ts.URL)

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "code", "url": face.URL, "subject": face.Subject,
	}).Body.Close()

	resp := postTony(t, ts.URL+"/tasks", map[string]any{"name": "streamed", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "work", "environment": stepEnv}})
	var sp map[string]any
	decodeResp(t, resp, &sp)
	id, _ := sp["id"].(string)
	if id == "" {
		t.Fatal("no task id")
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/tasks/"+id+"/events", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ev, err := ts.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer ev.Body.Close()
	if ev.StatusCode == http.StatusNotFound {
		t.Fatal("attaching to a task on a per-spawn runner = 404 — viff can list the task " +
			"and not watch it, which takes a resolver's supervision with it")
	}
	if ev.StatusCode != http.StatusOK {
		t.Fatalf("attach = %s, want 200", ev.Status)
	}
	// It is a live stream, so reading a single frame is enough: the point is that it opened
	// against the right runner rather than 404ing against the wrong one.
	buf := make([]byte, 1)
	if _, err := ev.Body.Read(buf); err != nil {
		t.Fatalf("the stream opened and produced nothing: %v", err)
	}
}

// commandStep classifies the one action the test drivers propose, `noop` or `run_bash`, as a
// command run: the step a gate on `commands.run` asks about.
func commandStep(tu task.ToolUse) (task.Step, bool) {
	cmd, _ := tu.Input["command"].(string)
	return task.NewStep(task.StepCommandsRun, map[string]string{"command": cmd}), true
}
