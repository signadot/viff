package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// reasonRunner is a runner face that ends every task it is given with a failure, and says why
// ONLY on its event stream: its snapshot reports the terminal status with no reason, as a
// runner whose account of a finished task is thinner than its plane does. It is the shape of
// the first real codex run, whose terminal frame carried the quota refusal while `verse task
// get` printed a bare `failed`.
func reasonRunner(t *testing.T, verseURL, reason string) *face {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /describe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", codec.MediaType(codec.Tony))
		w.Write([]byte("{version: 2, offers: [{agent: {name: codex}, systems: {network: {builds: {mode: [open, none, allow]}}}}]}\n"))
	})
	mux.HandleFunc("POST /tasks", func(w http.ResponseWriter, r *http.Request) {
		var l runner.Launch
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := codec.Unmarshal(body, &l, codec.Tony); err != nil {
			t.Errorf("the launch does not parse: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", codec.MediaType(codec.Tony))
		w.Write([]byte("[]\n"))
	})
	mux.HandleFunc("GET /tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", codec.MediaType(codec.Tony))
		b, _ := codec.Marshal(task.Snapshot{ID: r.PathValue("id"), Status: task.Failed}, codec.Tony)
		w.Write(b)
	})
	mux.HandleFunc("GET /tasks/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		sse, err := codec.NewSSE(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		_ = sse.Send(task.Event{Kind: task.EventStatus, Task: r.PathValue("id"), Text: string(task.Running)})
		_ = sse.Send(task.Event{Kind: task.EventStatus, Task: r.PathValue("id"), Text: string(task.Failed), Err: reason})
		sse.Flush()
	})
	return provenFace(t, verseURL, mux, func() {})
}

// A FAILED TASK SAYS WHY, where a task is read. The reason a runner puts on its terminal frame
// is the record's: `task get` and the list answer it off the mirror, whatever runner ran it. And the
// record says what the task was and which driver ran it, the two facts a reader of a failure
// wants next, without a trip to the runner.
func TestAFailedTaskOnANamedRunnerSaysWhy(t *testing.T) {
	const reason = "codex: the turn failed: Quota exceeded. Check your plan and billing details."
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	face := reasonRunner(t, ts.URL, reason)
	postTony(t, ts.URL+"/runners", map[string]any{"name": "codex", "url": face.URL, "subject": face.Subject}).Body.Close()

	id := spawnOK(t, ts, map[string]any{"id": "run-quota", "spawn": map[string]any{"agent": map[string]any{"name": "codex"}, "prompt": "triage the issue"}})
	waitTerminal(t, srv.Store, id)

	rec := instanceOf(t, srv, id)
	if rec["status"] != string(task.Failed) || rec["err"] != reason {
		t.Fatalf("the record of a failed task: status %v, err %v; want failed with the frame's reason", rec["status"], rec["err"])
	}
	if agent, _ := rec["agent"].(map[string]any); rec["prompt"] != "triage the issue" || agent["name"] != "codex" {
		t.Errorf("the record says prompt %v, agent %v; want what the spawn said", rec["prompt"], rec["agent"])
	}

	var got api.Task
	decodeResp(t, getAs(t, tester, ts.URL+"/tasks/"+id), &got)
	if got.Status != task.Failed || got.Err != reason || got.Prompt != "triage the issue" || got.Who() != "codex" {
		t.Errorf("task get = %+v; want the failure's reason, the prompt and the agent", got)
	}
	var rows []api.Task
	decodeResp(t, getAs(t, tester, ts.URL+"/tasks"), &rows)
	var row *api.Task
	for i := range rows {
		if rows[i].ID == id {
			row = &rows[i]
		}
	}
	if row == nil || row.Err != reason {
		t.Errorf("the list's row for the failed task = %+v; want its reason", row)
	}
}
