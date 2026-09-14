package versed

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// waitRuleQuestion waits for the rule's pending question about a subject, and for its record.
func waitRuleQuestion(t *testing.T, srv *Server, rule, subject string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, p := range srv.Reg.Pending() {
			if p.Question.Origin == rule && p.Question.Subject == subject {
				if _, ok, _ := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(p.Question.ID))); ok {
					return p.Question.ID
				}
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s never asked about %s", rule, subject)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func installAs(t *testing.T, as, url, charter string) *http.Response {
	t.Helper()
	return postAs(t, as, url+"/charter", "application/tony", charter)
}

// A gate that names its answerers holds its question out to them: davido answers, a stranger
// is refused, and the charter's owner answers what their own rule asked. With two answerers
// and `need: 2` the question waits for both.
func TestAGateHeldOutToItsAnswerers(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)

	resp := installAs(t, "alice@example.com", ts.URL, `{
		name: approve-ask,
		condition: {subject: {system: demo, kind: ask, value: {stage: open}}},
		gates: [{name: approval, mode: ask, answerers: [oscar@example.com]}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	subject := entity.RefAt("demo", "ask", "1")
	entitytest.Set(t, srv.Store, subject, entity.Obj{"stage": "open"}.Payload())
	qid := waitRuleQuestion(t, srv, "approve-ask", subject.String())

	rec, _, _ := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid)))
	grants, _ := entity.ParseGrants(mirror.QuestionRef(qid).Segs(), rec.Payload)
	if len(grants) != 1 || len(grants[0].To[entity.ReadWrite]) != 1 || grants[0].To[entity.ReadWrite][0] != "user:oscar@example.com" {
		t.Fatalf("the question's grants = %+v; want read-write for davido", grants)
	}
	if got := entity.Str(rec.Payload, entity.OwnerField); got != entity.SystemDocket {
		t.Fatalf("the question's record is owned by %q", got)
	}

	resp = answerAs(t, "bob@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a stranger answered: %s", resp.Status)
	}
	resp = answerAs(t, "oscar@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("davido could not answer the question held out to him: %s", resp.Status)
	}
	if q := settledQuestion(t, srv, qid); q["by"] != "oscar@example.com" {
		t.Fatalf("settled by %v", q["by"])
	}

	// The author answers what their own rule asked, without being named.
	second := entity.RefAt("demo", "ask", "2")
	entitytest.Set(t, srv.Store, second, entity.Obj{"stage": "open"}.Payload())
	qid = waitRuleQuestion(t, srv, "approve-ask", second.String())
	resp = answerAs(t, "alice@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the charter's owner could not answer: %s", resp.Status)
	}

	// Two answerers, both needed.
	resp = installAs(t, "alice@example.com", ts.URL, `{
		name: both-ask,
		condition: {subject: {system: demo, kind: both, value: {stage: open}}},
		gates: [{name: approval, mode: ask, need: 2, answerers: [oscar@example.com, ana@example.com]}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install both-ask: %s", resp.Status)
	}
	third := entity.RefAt("demo", "both", "1")
	entitytest.Set(t, srv.Store, third, entity.Obj{"stage": "open"}.Payload())
	qid = waitRuleQuestion(t, srv, "both-ask", third.String())
	resp = answerAs(t, "oscar@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the first of two ballots: %s, want 202 recorded", resp.Status)
	}
	if q := question(t, srv, qid); q["status"] != mirror.QuestionPending {
		t.Fatalf("one of two settled the question: %+v", q)
	}
	resp = answerAs(t, "ana@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the second of two ballots: %s", resp.Status)
	}
	settledQuestion(t, srv, qid)
}

// A gate nobody could settle is refused at install, saying why.
func TestAGateNobodyCouldSettleIsRefusedAtInstall(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, tc := range []struct{ gate, want string }{
		{`{name: g, mode: ask, need: 2, answerers: [oscar@example.com]}`, "nobody can settle"},
		{`{name: g, mode: ask, answerers: [nobody]}`, "names nobody"},
		{`{name: g, mode: ask, answerers: [verse.group.payments]}`, "nothing stands at that address"},
		{`{name: g, mode: ask, need: 3, options: [{id: yes, need: 3}], answerers: [a@x, b@x]}`, "nobody can settle"},
	} {
		resp := installAs(t, "alice@example.com", ts.URL, fmt.Sprintf(`{
			name: refused,
			condition: {subject: {system: demo, kind: ask, value: {stage: open}}},
			gates: [%s],
			action: {patch: {stage: reviewing}},
		}`, tc.gate))
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 == 2 || !strings.Contains(string(body), tc.want) {
			t.Errorf("%s: %s %s; want a refusal saying %q", tc.gate, resp.Status, body, tc.want)
		}
	}
	// A threshold a kind or everyone can meet is not refused: their number is not known.
	resp := installAs(t, "alice@example.com", ts.URL, `{
		name: open-ask,
		condition: {subject: {system: demo, kind: ask, value: {stage: open}}},
		gates: [{name: g, mode: ask, need: 3, answerers: [user]}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a threshold over a kind was refused: %s", resp.Status)
	}
}
