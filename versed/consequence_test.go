package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// WHAT FOLLOWS A QUESTION RUNS AS WHOEVER ASKED, BEFORE THE ANSWER GOES BACK TO THEM — for a
// task's question as for a person's ask (consequence.go). These pin what a person's ask tests
// (askcharter_test.go, askthen_test.go) do not reach.

// waitValue waits for a field of a record to hold a value.
func waitValue(t *testing.T, srv *Server, ref entity.Ref, field, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(ref); ok && entity.Str(e.Payload, field) == want {
			return
		}
		select {
		case <-deadline:
			e, _, _ := srv.Store.Get(ref)
			t.Fatalf("%s never carried %s: %q; it says %v", ref, field, want, entity.ToObj(e.Payload))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A TASK'S QUESTION RUNS ITS THEN AS WHOM THE TASK ACTS FOR. A run spawned by hand asks before its
// step, with a spawn to follow the yes written on the row's ask; the yes starts the task as the
// person who ran it, about the question, and the run hears its answer.
func TestATasksQuestionRunsItsThenForWhomItActsFor(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	spawn := postTony(t, ops.URL+"/tasks", map[string]any{"id": "cq-1", "spawn": map[string]any{
		"script": "true", "environment": stepEnv,
		"autonomy": []map[string]any{
			{"match": map[string]any{"system": "commands"}, "then": map[string]any{"ask": map[string]any{
				"then": map[string]any{"spawn": map[string]any{"prompt": "log the step", "environment": map[string]any{"systems": map[string]any{"network": map[string]any{"mode": "open"}}}}}}}},
			{"then": "ask"},
		}}})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	qid := waitQuestion(t, srv, "cq-1")
	question := mirror.QuestionRef(qid)
	if q, _, _ := srv.Store.Get(question); entity.Get(entity.Get(q.Payload, "then"), "spawn") == nil {
		t.Fatalf("the question does not carry the row's then: %v", entity.ToObj(q.Payload))
	}
	resp := postTony(t, ops.URL+"/tasks/cq-1/resolve", api.Resolve{ID: qid, Choice: "proceed", By: "user:scott@example.com"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resolving = %s", resp.Status)
	}
	then := thenTaskOf(t, srv, question)
	if got := entity.Str(then.Payload, "flow"); got != question.String() {
		t.Fatalf("the then is about %q, want the question", got)
	}
	if got := entity.Str(then.Payload, "actsFor"); !strings.Contains(got, tester) {
		t.Fatalf("the then acts for %q, want whom the run acts for (%s)", got, tester)
	}
	waitValue(t, srv, question, "status", mirror.QuestionAnswered)
}

// A RULE'S GATE STARTS NOTHING OF ITS OWN: what a rule does about a yes is its action, and about
// a no its gate's onDenied. A gate writing `then` or `else` is refused at install, naming the field.
func TestARulesGateTakesNoThenOrElse(t *testing.T) {
	_, ts := asking(t)
	for _, field := range []string{"then", "else"} {
		resp := installAs(t, "user:alice@example.com", ts.URL, `{name: r,
			condition: {subject: {path: "demo.e.*", value: {stage: open}}},
			gates: [{name: approval, mode: ask, `+field+`: {spawn: {prompt: "announce", environment: {systems: {network: {mode: open}}}}}}],
			action: {patch: {stage: reviewing}}}`)
		body := bodyOf(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("a gate with %s: install = %s, want refused: %s", field, resp.Status, body)
		} else if !strings.Contains(body, field) {
			t.Errorf("a gate with %s: refused without naming it: %s", field, body)
		}
	}
}

// A TASK'S ROW THEN IS CHECKED AT THE SPAWN, as whom the task acts for: a run by hand acts for
// its runner, so a then naming $principal on its own task is fine; a then naming nothing to run
// is refused before anything starts.
func TestARowsThenIsCheckedAtTheSpawn(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	resp := postTony(t, ops.URL+"/tasks", map[string]any{"id": "cq-2", "spawn": map[string]any{
		"script": "true", "environment": stepEnv,
		"autonomy": []map[string]any{
			{"match": map[string]any{"system": "commands"}, "then": map[string]any{"ask": map[string]any{"then": map[string]any{"notify": "x"}}}},
			{"then": "ask"},
		}}})
	body := bodyOf(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "not something a question runs") {
		t.Fatalf("spawn = %s: %s, want refused for the then", resp.Status, body)
	}
}
