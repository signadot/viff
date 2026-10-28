package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// A NO RUNS THE ELSE, THE SAME WAY: a task's ask row carries an `else` beside its `then`, a
// refusal starts it as whom the task acts for, and the question settles once it has run. The
// `then` stays untouched.
func TestATasksQuestionRunsItsElseOnANo(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	open := map[string]any{"systems": map[string]any{"network": map[string]any{"mode": "open"}}}
	spawn := postTony(t, ops.URL+"/tasks", map[string]any{"id": "ce-1", "spawn": map[string]any{
		"script": "true", "environment": stepEnv,
		"autonomy": []map[string]any{
			{"match": map[string]any{"system": "commands"}, "then": map[string]any{"ask": map[string]any{
				"then": map[string]any{"spawn": map[string]any{"prompt": "log the step", "environment": open}},
				"else": map[string]any{"spawn": map[string]any{"prompt": "say why it was refused", "environment": open}}}}},
			{"then": "ask"},
		}}})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	qid := waitQuestion(t, srv, "ce-1")
	question := mirror.QuestionRef(qid)
	if q, _, _ := srv.Store.Get(question); entity.Get(entity.Get(q.Payload, "else"), "spawn") == nil {
		t.Fatalf("the question does not carry the row's else: %v", entity.ToObj(q.Payload))
	}
	resp := postTony(t, ops.URL+"/tasks/ce-1/resolve", api.Resolve{ID: qid, Choice: "deny", By: "user:scott@example.com"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("refusing = %s", resp.Status)
	}
	id := waitField(t, srv, question, "else-task")
	e, ok, _ := srv.Store.Get(mirror.Ref(id))
	if !ok {
		t.Fatalf("the else of %s never ran as %s", question, id)
	}
	if got := entity.Str(e.Payload, "prompt"); got != "say why it was refused" {
		t.Fatalf("the else runs %q, want the row's else prompt", got)
	}
	waitValue(t, srv, question, "status", mirror.QuestionAnswered)
	if q, _, _ := srv.Store.Get(question); entity.Get(q.Payload, "then-task") != nil {
		t.Fatalf("a no ran the then too: %v", entity.ToObj(q.Payload))
	}
}
