package viffd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
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

// A gate that names its answerers holds its question out to them: oscar answers, a stranger
// is refused, and the charter's owner answers what their own rule asked. With two answerers
// and `need: 2` the question waits for both.
func TestAGateHeldOutToItsAnswerers(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)

	resp := installAs(t, "alice@example.com", ts.URL, `{
		name: approve-ask,
		condition: {subject: {path: "demo.ask.*", value: {stage: open}}},
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
		t.Fatalf("the question's grants = %+v; want read-write for oscar", grants)
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
		t.Fatalf("oscar could not answer the question held out to him: %s", resp.Status)
	}
	if q := settledQuestion(t, srv, qid); q["by"] != "user:oscar@example.com" {
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
		condition: {subject: {path: "demo.both.*", value: {stage: open}}},
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
	if q := questionDoc(t, srv, qid); q["status"] != mirror.QuestionPending {
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
		{`{name: g, mode: ask, answerers: [nobody]}`, "a bare word names nothing"},
		{`{name: g, mode: ask, answerers: [mirror.principal.group.payments]}`, "is no person"},
		{`{name: g, mode: ask, answerers: [mirror.covers.review@run-1]}`, "is no person"},
		{`{name: g, mode: ask, answerers: [group:payments]}`, "nothing stands at mirror.principal.group.payments"},
		{`{name: g, mode: ask, answerers: [via:mirror.resolvers.ghost]}`, "nothing stands at mirror.resolvers.ghost"},
		{`{name: g, mode: ask, need: 3, options: [{id: yes, need: 3}], answerers: [a@x, b@x]}`, "nobody can settle"},
	} {
		resp := installAs(t, "alice@example.com", ts.URL, fmt.Sprintf(`{
			name: refused,
			condition: {subject: {path: "demo.ask.*", value: {stage: open}}},
			gates: [%s],
			action: {patch: {stage: reviewing}},
		}`, tc.gate))
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 == 2 || !strings.Contains(string(body), tc.want) {
			t.Errorf("%s: %s %s; want a refusal saying %q", tc.gate, resp.Status, body, tc.want)
		}
	}
	// A threshold a kind, a group or everyone can meet is not refused: their number is not known.
	resp := installAs(t, "alice@example.com", ts.URL, `{
		name: open-ask,
		condition: {subject: {path: "demo.ask.*", value: {stage: open}}},
		gates: [{name: g, mode: ask, need: 3, answerers: [user:*]}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a threshold over a kind was refused: %s", resp.Status)
	}
}

// A QUESTION IS NOT ANSWERABLE UNTIL IT IS POSED. The registry has one the moment the engine
// raises it and the store a moment later, in two writes; an answer that arrived in between was
// checked against the docket's row — every person — and tallied with the default options at one
// apiece. So a stranger's single ballot could settle a gate that named its answerers and needed
// two of them. This answers at the first instant the registry has the question, which is that
// window, and it has to be refused and counted exactly as it would be a minute later.
func TestAQuestionIsNotAnswerableBeforeItIsPosed(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := installAs(t, "alice@example.com", ts.URL, `{
		name: window-ask,
		condition: {subject: {path: "demo.window.*", value: {stage: open}}},
		gates: [{name: approval, mode: ask, need: 2, answerers: [oscar@example.com, ana@example.com]}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	// The registry alone, with no wait for the store: the earliest an answer can be sent.
	raised := func(subject string) string {
		deadline := time.After(3 * time.Second)
		for {
			for _, p := range srv.Reg.Pending() {
				if p.Question.Origin == "window-ask" && p.Question.Subject == subject {
					return p.Question.ID
				}
			}
			select {
			case <-deadline:
				t.Fatalf("window-ask never asked about %s", subject)
			case <-time.After(time.Millisecond):
			}
		}
	}
	for i := 0; i < 8; i++ { // the window is narrow, so it is tried more than once
		subject := entity.RefAt("demo", "window", fmt.Sprint(i))
		entitytest.Set(t, srv.Store, subject, entity.Obj{"stage": "open"}.Payload())
		qid := raised(subject.String())

		resp := answerAs(t, "mallory@example.com", ts.URL, qid)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("round %d: a stranger answered a question held out to two named people: %s", i, resp.Status)
		}
		resp = answerAs(t, "oscar@example.com", ts.URL, qid)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("round %d: the first of two ballots: %s, want 202 recorded", i, resp.Status)
		}
		if q := questionDoc(t, srv, qid); q["status"] != mirror.QuestionPending {
			t.Fatalf("round %d: one ballot of two settled the question: %+v", i, q)
		}
	}
}
