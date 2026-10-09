package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/taskd"
)

// A task on a PER-SPAWN runner can be cancelled.
//
// It could not, and the shape is the one this file's neighbour describes for gates: the task
// is listed, `verse ps` shows it running, and every attempt to stop it answers 404 — so the
// obvious reading is a typo in the id, when the actual cause is that the control path looked
// for the task in the wrong place. Asked directly, the runner holding it cancels happily.
//
// taskControl called s.runner.Attach — this verse's DEFAULT runner — while the whole point of
// `runner:` on a spawn is that the work went somewhere else. Every task a charter spawns onto
// a pool was therefore uncontrollable: no cancel, no say, no vector slide. The events route
// had already learned this ("wherever it runs, not wherever the default runner is") and
// control was left behind, which is why reading a task's stream worked while stopping it did
// not.
func TestControlOnPerSpawnRunner(t *testing.T) {
	// A face that keeps working until it is stopped, so cancel has something to do.
	build := func(l task.Launch, reg *task.Registry) *task.Task {
		return task.NewTask(l.ID, task.NewSupervisor(&task.Vector{Default: task.Auto}, reg),
			&foreverDriver{}, okExecutor{})
	}
	face := httptest.NewServer(taskd.New(build).Handler())
	t.Cleanup(face.Close)

	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	postTony(t, ts.URL+"/runners", map[string]any{
		"name": "code", "url": face.URL,
	}).Body.Close()

	// Stopped from the far side whatever happens here. A foreverDriver task outlives a
	// FAILING assertion, and httptest.Close waits on outstanding requests — so without this
	// the test reports "blocked in Close after 5 seconds" instead of the assertion that
	// actually failed, which is how a five-second check becomes a ten-minute one. The
	// neighbouring gate test guards the same hazard the same way.
	var spawned string
	t.Cleanup(func() {
		if spawned != "" {
			postTony(t, face.URL+"/tasks/"+spawned+"/cancel", map[string]any{}).Body.Close()
		}
	})

	resp := postTony(t, ts.URL+"/tasks", map[string]any{
		"prompt": "work", "runner": "verse.runners.code", "name": "onrunner",
	})
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

	// Cancel THROUGH VERSE, which is the path an operator has.
	cancel := postTony(t, ts.URL+"/tasks/"+id+"/cancel", map[string]any{})
	defer cancel.Body.Close()
	if cancel.StatusCode == http.StatusNotFound {
		t.Fatalf("cancel = 404: verse looked for the task on its default runner, not the one "+
			"hosting it (%s)", face.URL)
	}
	if cancel.StatusCode != http.StatusNoContent && cancel.StatusCode != http.StatusOK {
		t.Fatalf("cancel = %s", cancel.Status)
	}

	// And it took: the runner holding it reports a terminal status.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st := statusOn(t, face.URL, id); task.Status(st).Terminal() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never reached a terminal status after cancel", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// statusOn reads a task's status from the runner hosting it.
func statusOn(t *testing.T, base, id string) string {
	t.Helper()
	resp, err := http.Get(base + "/tasks/" + id)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var inst task.Instance
	decodeResp(t, resp, &inst)
	return string(inst.Status)
}

// foreverDriver keeps proposing work, so a task only ends when something stops it.
type foreverDriver struct{}

func (foreverDriver) Next(_ context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	time.Sleep(5 * time.Millisecond) // slow enough that the test is not racing the loop
	return task.ToolUse{Name: "noop"}, true, nil
}
