package versed

import (
	"net/http"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// A NO RUNS THE ELSE, THE SAME WAY: a rule's gate carries an `else` beside its `then`, a refusal
// starts it as the rule, and the question settles once it has run. The `then` stays untouched.
func TestARulesGateRunsItsElseOnANo(t *testing.T) {
	srv, ts := asking(t)
	const alice = "user:alice@example.com"
	resp := installAs(t, alice, ts.URL, `{
		name: approve-f,
		condition: {subject: {path: "demo.f.*", value: {stage: open}}},
		gates: [{name: approval, mode: ask,
			then: {spawn: {prompt: "announce the approval", environment: {systems: {network: {mode: open}}}}},
			else: {spawn: {prompt: "say why it was refused", environment: {systems: {network: {mode: open}}}}}}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	subject := entity.RefAt("demo", "f", "1")
	entitytest.Set(t, srv.Store, subject, entity.Obj{"stage": "open"}.Payload())
	qid := waitRuleQuestion(t, srv, "approve-f", subject.String())
	question := mirror.QuestionRef(qid)
	posed(t, srv, question)
	if q, _, _ := srv.Store.Get(question); entity.Get(entity.Get(q.Payload, "else"), "spawn") == nil {
		t.Fatalf("the question does not carry the gate's else: %v", entity.ToObj(q.Payload))
	}
	if resp := answerWith(t, alice, ts.URL, qid, "deny"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice's no: %s: %s", resp.Status, bodyOf(t, resp))
	}
	id := waitField(t, srv, question, "else-task")
	e, ok, _ := srv.Store.Get(mirror.Ref(id))
	if !ok {
		t.Fatalf("the else of %s never ran as %s", question, id)
	}
	if got := entity.Str(e.Payload, "prompt"); got != "say why it was refused" {
		t.Fatalf("the else runs %q, want the gate's else prompt", got)
	}
	waitValue(t, srv, question, "status", mirror.QuestionAnswered)
	if q, _, _ := srv.Store.Get(question); entity.Get(q.Payload, "then-task") != nil {
		t.Fatalf("a no ran the then too: %v", entity.ToObj(q.Payload))
	}
}
