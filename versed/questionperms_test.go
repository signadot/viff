package versed

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// withoutTheDocketDefault removes the seed's grant on questions, so that only a co-owner or
// the resolver a question is held out to may answer: the model with the open default off,
// which is what makes the co-ownership edge and the resolver's grant visible.
func withoutTheDocketDefault(t *testing.T, srv *Server) {
	t.Helper()
	questions := entity.VerseRef(mirror.AskSystem)
	if err := entity.SetChildGrants(srv.Store, questions, entity.Levels{entity.Read: {entity.Everyone}}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(srv.Store, questions, nil); err != nil {
		t.Fatal(err)
	}
}

func answerAs(t *testing.T, as, url, id string) *http.Response {
	t.Helper()
	b, err := codec.Marshal(api.Resolve{Choice: "proceed", By: as}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, url+answerPath(id), "application/tony", string(b))
}

func spawnAs(t *testing.T, as, url, id, subject string) {
	t.Helper()
	body := fmt.Sprintf(`{id: %q, spawn: {script: "true", subject: %q, environment: %s, autonomy: %s}}`, id, subject, stepEnvTony, stepAsksTony)
	resp := postAs(t, as, url+"/tasks", "application/tony", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn %s: %s", id, resp.Status)
	}
}

// A question a task raises is co-owned by the task's owner through the link the mirror
// records, and by nobody else without a grant: the owner answers what their own work asked,
// and a stranger — a person, once the docket's open default is off — is refused.
func TestATasksOwnerAnswersWhatItAsked(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)

	pr(t, srv, "1", "payments")
	spawnAs(t, "alice@example.com", ts.URL, "run-1", `change.pr."1"`)
	qid := waitQuestion(t, srv, "run-1")

	rec, ok, err := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid)))
	if err != nil || !ok || entity.Str(rec.Payload, entity.UnderField) != mirror.Ref("run-1").String() {
		t.Fatalf("the question's record does not link it under its task: %v (ok=%v err=%v)", rec.Payload, ok, err)
	}
	if got := entity.Str(rec.Payload, entity.OwnerField); got != entity.SystemDocket {
		t.Fatalf("the question's record is owned by %q; it is the system's, on a record of its own", got)
	}

	resp := answerAs(t, "bob@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a stranger answered: %s", resp.Status)
	}
	if q := questionDoc(t, srv, qid); q["status"] != mirror.QuestionPending {
		t.Fatalf("the refused answer settled the question: %+v", q)
	}
	resp = answerAs(t, "alice@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the task's owner could not answer: %s", resp.Status)
	}
	if q := settledQuestion(t, srv, qid); q["by"] != "user:alice@example.com" {
		t.Fatalf("settled by %v", q["by"])
	}
}

// A charter's owner answers what their rule asked: a firing's question is linked under the
// rule's record, and the rule is the installer's.
func TestACharterOwnerAnswersWhatItsRuleAsked(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)

	resp := postAs(t, "alice@example.com", ts.URL+"/charter", "application/tony", `{
		name: approve-ask,
		condition: {subject: {path: "demo.ask.*", value: {stage: open}}},
		gates: [{name: approval, mode: ask}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	entitytest.Set(t, srv.Store, entity.RefAt("demo", "ask", "1"), entity.Obj{"stage": "open"}.Payload())

	var qid string
	deadline := time.After(3 * time.Second)
	for qid == "" {
		for _, p := range srv.Reg.Pending() {
			if p.Question.Origin == "approve-ask" {
				if _, ok, _ := srv.Store.Get(mirror.QuestionRef(p.Question.ID)); ok {
					qid = p.Question.ID
				}
			}
		}
		select {
		case <-deadline:
			t.Fatal("the rule never asked")
		case <-time.After(5 * time.Millisecond):
		}
	}
	rec, ok, _ := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid)))
	if !ok || entity.Str(rec.Payload, entity.UnderField) != mirror.RuleRef("", "approve-ask").String() {
		t.Fatalf("the question is not linked under its rule: %v", rec.Payload)
	}

	resp = answerAs(t, "bob@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		rec, _, _ := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid)))
		above, _, _ := srv.Store.Get(entity.PermsRef(entity.VerseRef(mirror.AskSystem)))
		t.Fatalf("a stranger answered a charter's question: %s\nrecord: %v\nabove: %v", resp.Status, entity.ToObj(rec.Payload), entity.ToObj(above.Payload))
	}
	resp = answerAs(t, "alice@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the charter's owner could not answer: %s", resp.Status)
	}
	if q := settledQuestion(t, srv, qid); q["by"] != "user:alice@example.com" || q["status"] != mirror.QuestionAnswered {
		t.Fatalf("settled: %+v", q)
	}
}
