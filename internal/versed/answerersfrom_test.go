package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/mirror"
)

// heldOutTo is who a posed question is held out to: its record's read-write grantees.
func heldOutTo(t *testing.T, srv *Server, qid string) []string {
	t.Helper()
	rec, ok, err := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid)))
	if err != nil || !ok {
		t.Fatalf("question %s has no record: %v", qid, err)
	}
	grants, err := entity.ParseGrants(mirror.QuestionRef(qid).Segs(), rec.Payload)
	if err != nil || len(grants) != 1 {
		t.Fatalf("question %s grants = %+v (%v)", qid, grants, err)
	}
	who := append([]string(nil), grants[0].To[entity.ReadWrite]...)
	sort.Strings(who)
	return who
}

// askingRule installs one standing rule that poses every `ask.*` record's question to whoever
// the record names, and latches the record when it is answered.
func askingRule(t *testing.T, need int) (*Server, *httptest.Server) {
	t.Helper()
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	withoutTheDocketDefault(t, srv)
	gate := `{name: answer, answerersFrom: answerers}`
	if need > 1 {
		gate = `{name: answer, answerersFrom: answerers, need: 2}`
	}
	resp := installAs(t, "alice@example.com", ts.URL, `{
		name: ask,
		condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {answer: null}]}},
		gates: [`+gate+`],
		action: {patch: {answer: yes}},
	}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("install: %s: %s", resp.Status, body)
	}
	resp.Body.Close()
	return srv, ts
}

// A RECORD SAYS WHOM IT ASKS. One standing rule poses every ask, and each ask names its own
// answerers in a field the gate reads at the firing — one name, or a list.
func TestAGateAsksWhomTheRecordNames(t *testing.T) {
	srv, _ := askingRule(t, 1)
	one := entity.RefAt("ask", "one")
	entitytest.Set(t, srv.Store, one, entity.Obj{"question": "q", "answerers": "oscar@example.com"}.Payload())
	if got := heldOutTo(t, srv, waitRuleQuestion(t, srv, "ask", one.String())); len(got) != 1 || got[0] != "user:oscar@example.com" {
		t.Fatalf("one name: held out to %v, want oscar alone", got)
	}

	srv2, _ := askingRule(t, 2)
	two := entity.RefAt("ask", "two")
	entitytest.Set(t, srv2.Store, two, entity.Obj{"question": "q",
		"answerers": []any{"oscar@example.com", "peggy@example.com"}}.Payload())
	got := heldOutTo(t, srv2, waitRuleQuestion(t, srv2, "ask", two.String()))
	if len(got) != 2 || got[0] != "user:oscar@example.com" || got[1] != "user:peggy@example.com" {
		t.Fatalf("a list: held out to %v, want both", got)
	}
}

// WHAT A RECORD NAMES MEETS THE CHECKS A GATE'S OWN NAMES MEET AT INSTALL, at the firing: a
// record naming nobody, a name the grammar does not read, or fewer people than the threshold is
// a question nobody could settle, so nothing is asked.
func TestAGateAsksNobodyWhenTheRecordNamesNobodyItCanAsk(t *testing.T) {
	for _, c := range []struct {
		name      string
		need      int
		answerers any
	}{
		{"an empty list", 1, []any{}},
		{"a name the grammar does not read", 1, "has spaces in it"},
		{"one person under a threshold of two", 2, "oscar@example.com"},
	} {
		srv, _ := askingRule(t, c.need)
		bad := entity.RefAt("ask", "bad")
		rec := entity.Obj{"question": "q"}
		if c.answerers != nil {
			rec["answerers"] = c.answerers
		}
		entitytest.Set(t, srv.Store, bad, rec.Payload())
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			for _, p := range srv.Reg.Pending() {
				if p.Question.Subject == bad.String() {
					t.Fatalf("%s: a question was posed, held out to %v", c.name, p.Question.Answerers)
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		// And the rule is live: a record naming enough people it can ask is asked.
		good := entity.RefAt("ask", "good")
		entitytest.Set(t, srv.Store, good, entity.Obj{"question": "q",
			"answerers": []any{"oscar@example.com", "peggy@example.com"}}.Payload())
		waitRuleQuestion(t, srv, "ask", good.String())
	}
}

// A RECORD THAT NAMES NOBODY SAYS NOTHING: the question is open, to whoever the docket admits.
func TestARecordNamingNoAnswerersIsAnOpenQuestion(t *testing.T) {
	srv, _ := askingRule(t, 1)
	open := entity.RefAt("ask", "open")
	entitytest.Set(t, srv.Store, open, entity.Obj{"question": "q"}.Payload())
	qid := waitRuleQuestion(t, srv, "ask", open.String())
	for _, p := range srv.Reg.Pending() {
		if p.Question.ID == qid && len(p.Question.Answerers) != 0 {
			t.Fatalf("an ask naming nobody was held out to %v", p.Question.Answerers)
		}
	}
}

// AND THE PATH IS CHECKED AT INSTALL: a field of the subject's value, dotted.
func TestAnswerersFromNamesAField(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, bad := range []string{"a..b", ".a", "a."} {
		resp := installAs(t, "alice@example.com", ts.URL, `{
			name: ask,
			condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {answer: null}]}},
			gates: [{name: answer, answerersFrom: "`+bad+`"}],
			action: {patch: {answer: yes}},
		}`)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "answerersFrom") {
			t.Errorf("answerersFrom %q: install = %s: %s", bad, resp.Status, body)
		}
	}
}
