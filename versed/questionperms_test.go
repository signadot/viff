package versed

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// withoutTheDocketDefault removes the seed's grant on questions, so that only a co-owner or
// the binding a question is held out to may answer: the model with the open default off,
// which is what makes the co-ownership edge and the binding's grant visible.
func withoutTheDocketDefault(t *testing.T, srv *Server) {
	t.Helper()
	questions := entity.VerseRef(mirror.QuestionSystem, mirror.QuestionKind)
	if err := entity.SetChildGrants(srv.Store, questions, entity.Levels{entity.Read: {entity.Everyone}}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(srv.Store, questions, nil); err != nil {
		t.Fatal(err)
	}
}

func answerAs(t *testing.T, as, url, id string) *http.Response {
	t.Helper()
	b, err := codec.Marshal(api.Resolve{ID: id, Choice: "proceed", By: as}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, url+"/gate/resolve", "application/tony", string(b))
}

func spawnAs(t *testing.T, as, url, id, subject, resolverRef string) {
	t.Helper()
	body := fmt.Sprintf(`{id: %q, prompt: "true", subject: %q, resolver: %q}`, id, subject, resolverRef)
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
	spawnAs(t, "alice@example.com", ts.URL, "run-1", `change.pr."1"`, "")
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
	if q := question(t, srv, qid); q["status"] != mirror.QuestionPending {
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

// A question is held out to the binding it is bound to, by the binding's address: the
// binding's own verdict is admitted, made as the binding through its instance, and the grant
// is on the question's record where anyone can read who may answer.
func TestABoundQuestionIsHeldOutToItsBinding(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)

	mustInstall(t, ts.URL, exclusiveBinding("payments-review", "payments"))
	pr(t, srv, "1", "payments")
	spawnAs(t, "alice@example.com", ts.URL, "run-1", `change.pr."1"`, "")
	qid := waitQuestion(t, srv, "run-1")
	binding := "verse.resolvers.payments-review"

	// The record is posed with the default's `read`, and the binding's `read-write` lands on
	// the same line a moment later.
	deadline := time.Now().Add(3 * time.Second)
	var grants []entity.Grant
	for time.Now().Before(deadline) {
		if rec, ok, _ := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid))); ok {
			if grants, _ = entity.ParseGrants(mirror.QuestionRef(qid).Segs(), rec.Payload); len(grants) > 0 && len(grants[0].To[entity.ReadWrite]) > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(grants) != 1 || len(grants[0].To[entity.ReadWrite]) != 1 || grants[0].To[entity.ReadWrite][0] != "via:"+binding ||
		len(grants[0].To[entity.Read]) != 1 || grants[0].To[entity.Read][0] != entity.Everyone {
		t.Fatalf("the question's grants = %+v, want read-write for its binding beside read for everyone", grants)
	}
	if ok, _ := entity.Holds(srv.Store, "via:"+binding, entity.ReadWrite, mirror.QuestionRef(qid)); !ok {
		t.Fatal("the binding does not hold read-write on the question held out to it")
	}
	if ok, _ := entity.Holds(srv.Store, "user:carol@example.com", entity.ReadWrite, mirror.QuestionRef(qid)); ok {
		t.Fatal("somebody else holds what the binding was granted")
	}

	inst := waitInstance(t, srv, "payments-review", "run-1")
	resp := postTony(t, ts.URL+"/resolvers/instance/"+inst+"/verdicts", api.Verdict{ID: qid, Verdict: "proceed"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the binding's verdict was refused: %s", resp.Status)
	}
	if q := settledQuestion(t, srv, qid); q["by"] != "via:"+binding {
		t.Fatalf("settled by %v", q["by"])
	}
}

// The runner working a task holds its grant on the task and nothing on the question the task
// raised — the executor may not resolve its own gate — and a runner's name is a runner's,
// since the kind is in the name, however it later turns up on the api. The task's owner answers, and the answer reaches the
// worker.
func TestARunnerMayNotAnswerItsOwnQuestion(t *testing.T) {
	srv := testServer(t)
	b := srv.UseBroker("http://verse.test:9090")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	worker := httptest.NewServer(b.WorkerHandler())
	defer worker.Close()

	resp := postAs(t, "alice@example.com", ts.URL+"/tasks", "application/json",
		`{"prompt":"deploy","driver":"human","runner":"broker"}`)
	var sp struct {
		ID string `tony:"field=id"`
	}
	decodeResp(t, resp, &sp)
	resp.Body.Close()

	resp, err := worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/claim", "application/json",
		strings.NewReader(`{"identity":"runner:bot"}`))
	if err != nil {
		t.Fatal(err)
	}
	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, resp, &claim)
	resp.Body.Close()
	qid := sp.ID + "~d1"
	resp, err = worker.Client().Post(worker.URL+"/tasks/"+sp.ID+"/report", "application/json",
		strings.NewReader(fmt.Sprintf(`{"epoch":%d,"events":[{"kind":"pending","id":%q,"gate":"deploy","text":"prod"}]}`, claim.Epoch, qid)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitDocket(t, srv, qid)

	resp = answerAs(t, "runner:bot", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the runner answered its own question: %s", resp.Status)
	}
	if _, listed, _ := srv.Store.Get(entity.UserRef("bot")); listed {
		t.Fatal("the runner's name was listed as a user when it turned up on the api")
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
		above, _, _ := srv.Store.Get(entity.PermsRef(entity.VerseRef(mirror.QuestionSystem, mirror.QuestionKind)))
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
