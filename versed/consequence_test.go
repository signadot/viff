package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// WHAT FOLLOWS A QUESTION RUNS AS WHOEVER ASKED, BEFORE THE ANSWER GOES BACK TO THEM — for a
// rule's question and a task's as for a person's ask (consequence.go). These pin the two kinds a
// person's ask tests (askcharter_test.go, askthen_test.go) do not reach.

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

// A RULE'S GATE RUNS ITS THEN AS THE RULE. alice's rule asks before it reviews a change, with a
// spawn to follow the yes; the question carries the then, the yes starts the task as the rule —
// the charter's owner owns it, it acts for nobody, and it is about the question — and the firing
// goes on to its action.
func TestARulesGateRunsItsThenAsTheRule(t *testing.T) {
	srv, ts := asking(t)
	const alice = "user:alice@example.com"
	resp := installAs(t, alice, ts.URL, `{
		name: approve-c,
		condition: {subject: {path: "demo.c.*", value: {stage: open}}},
		gates: [{name: approval, mode: ask, then: {spawn: {prompt: "announce the approval", environment: {systems: {network: {mode: open}}}}}}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	subject := entity.RefAt("demo", "c", "1")
	entitytest.Set(t, srv.Store, subject, entity.Obj{"stage": "open"}.Payload())
	qid := waitRuleQuestion(t, srv, "approve-c", subject.String())
	question := mirror.QuestionRef(qid)
	posed(t, srv, question)
	if q, _, _ := srv.Store.Get(question); entity.Get(entity.Get(q.Payload, "then"), "spawn") == nil {
		t.Fatalf("the question does not carry the gate's then: %v", entity.ToObj(q.Payload))
	}
	if resp := answerAs(t, alice, ts.URL, qid); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice's yes: %s: %s", resp.Status, bodyOf(t, resp))
	}
	then := thenTaskOf(t, srv, question)
	if got := entity.Str(then.Payload, "flow"); got != question.String() {
		t.Fatalf("the then is about %q, want the question", got)
	}
	if got := entity.Str(then.Payload, "actsFor"); got != "" {
		t.Fatalf("the then acts for %q, want nobody: a rule acts for nobody", got)
	}
	if got := entity.Str(then.Payload, "prompt"); got != "announce the approval" {
		t.Fatalf("the then runs %q, want the gate's prompt", got)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		o, _ := entity.OwnerOf(srv.Store, then.Ref)
		if o == alice {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the then is %q's, want the charter owner's, whom the rule acts as", o)
		}
	}
	// And the firing heard the answer: the question settles only once its delivery took.
	waitValue(t, srv, question, "status", mirror.QuestionAnswered)
}

// THE FIRING GOES ON ONCE THE THEN HAS RUN, NOT ONCE IT HAS SETTLED. A then that asks raises an
// ask that stands pending, as the rule, and the question settles — its answer delivered to the
// firing — while it stands.
func TestARulesGateThenThatAsksDoesNotHoldTheFiring(t *testing.T) {
	srv, ts := asking(t)
	const alice = "user:alice@example.com"
	resp := installAs(t, alice, ts.URL, `{
		name: approve-d,
		condition: {subject: {path: "demo.d.*", value: {stage: open}}},
		gates: [{name: approval, mode: ask, then: {ask: {name: tell, question: "Tell the team?", to: [bob@example.com]}}}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	subject := entity.RefAt("demo", "d", "1")
	entitytest.Set(t, srv.Store, subject, entity.Obj{"stage": "open"}.Payload())
	qid := waitRuleQuestion(t, srv, "approve-d", subject.String())
	if resp := answerAs(t, alice, ts.URL, qid); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice's yes: %s: %s", resp.Status, bodyOf(t, resp))
	}
	waitValue(t, srv, mirror.QuestionRef(qid), "status", mirror.QuestionAnswered)
	asked := entity.ToObj(entity.Get(askField(t, srv, qid, "then-asked"), "then-asked"))
	tell, _ := asked["id"].(string)
	if !strings.HasPrefix(tell, "tell-") {
		t.Fatalf("then-asked %v, want the tell ask's id", asked)
	}
	e, _, _ := srv.Store.Get(mirror.AskRef(tell))
	if got := entity.Str(e.Payload, "status"); got != mirror.QuestionPending {
		t.Fatalf("the then's ask is %q, want pending: the firing did not wait for it", got)
	}
	if got := mirror.PrincipalFrom(e.Payload); !strings.HasPrefix(got, "via:") {
		t.Fatalf("the then's ask is from %q, want via: the rule", got)
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

// A GATE'S THEN IS CHECKED AT INSTALL, AS THE RULE: a row naming $principal where the rule acts for
// nobody, and a key that is not something a question runs, are refused with the author present.
func TestAGatesThenIsCheckedAtInstall(t *testing.T) {
	_, ts := asking(t)
	for name, c := range map[string]struct{ then, says string }{
		"$principal on the then's task": {`{spawn: {script: "true", environment: {systems: {commands: {run: {}}}}, autonomy: [{match: {system: commands, verb: run}, then: {ask: {answerers: [$principal]}}}, {then: ask}]}}`, "acts for nobody"},
		"a key an ask does not run":     {`{notify: "#releases"}`, "not something a question runs"},
		"two things to run":             {`{spawn: {script: "true"}, ask: {question: "x?"}}`, "names one thing to run"},
	} {
		resp := installAs(t, "user:alice@example.com", ts.URL, `{name: r,
			condition: {subject: {path: "demo.e.*", value: {stage: open}}},
			gates: [{name: approval, mode: ask, then: `+c.then+`}],
			action: {patch: {stage: reviewing}}}`)
		body := bodyOf(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: install = %s, want refused: %s", name, resp.Status, body)
		} else if !strings.Contains(body, c.says) {
			t.Errorf("%s: refused for another reason: %s", name, body)
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
