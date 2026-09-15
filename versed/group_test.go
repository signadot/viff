package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

func groupAs(t *testing.T, as, url, name string, members ...string) *http.Response {
	t.Helper()
	b, err := codec.Marshal(api.Group{Name: name, Members: members}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, url+"/groups", "application/tony", string(b))
}

// A gate held out to a group is answered by its members, `need` of them; a stranger is refused;
// and a group is its maker's to change, not anybody's.
func TestAGateHeldOutToAGroup(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)

	resp := groupAs(t, "walter@example.com", ts.URL, "payments", "oscar@example.com", "ana@example.com")
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("group add: %s", resp.Status)
	}
	if got := owner(t, srv, entity.GroupRef("payments")); got != "user:walter@example.com" {
		t.Fatalf("the group is owned by %q", got)
	}
	resp = groupAs(t, "bob@example.com", ts.URL, "payments", "bob@example.com")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a stranger rewrote the group: %s", resp.Status)
	}

	resp = installAs(t, "alice@example.com", ts.URL, `{
		name: group-ask,
		condition: {subject: {path: [demo, ask, "*"], value: {stage: open}}},
		gates: [{name: approval, mode: ask, need: 2, answerers: [group:payments]}],
		action: {patch: {stage: reviewing}},
	}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	subject := entity.RefAt("demo", "ask", "1")
	entitytest.Set(t, srv.Store, subject, entity.Obj{"stage": "open"}.Payload())
	qid := waitRuleQuestion(t, srv, "group-ask", subject.String())

	resp = answerAs(t, "bob@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a stranger answered a question held out to a group: %s", resp.Status)
	}
	resp = answerAs(t, "oscar@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("the first member's ballot: %s, want 202", resp.Status)
	}
	resp = answerAs(t, "ana@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the second member's ballot: %s", resp.Status)
	}
	if q := settledQuestion(t, srv, qid); q["status"] != mirror.QuestionAnswered {
		t.Fatalf("settled: %+v", q)
	}

	// The listing and the record.
	resp = getAs(t, "bob@example.com", ts.URL+"/groups")
	body := readBody(t, resp)
	if !strings.Contains(body, "payments") || !strings.Contains(body, "oscar@example.com") {
		t.Fatalf("groups: %s", body)
	}
}

// A binding whose resolver is a group holds its questions out to the members.
func TestAGroupAsABindingsResolver(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)
	resp := groupAs(t, "walter@example.com", ts.URL, "reviewers", "oscar@example.com")
	resp.Body.Close()
	mustInstall(t, ts.URL, `{name: review-group, kind: approvals,
		condition: {subject: {path: [change, pr, "*"], value: {area: payments}}},
		method: {driver: group, group: group:reviewers}}`)
	pr(t, srv, "1", "payments")
	spawnAs(t, "alice@example.com", ts.URL, "run-1", `change.pr."1"`, "")
	qid := waitQuestion(t, srv, "run-1")
	waitOwnedBy(t, srv, qid, "verse.resolvers.approvals.review-group")
	resp = answerAs(t, "bob@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a stranger answered: %s", resp.Status)
	}
	resp = answerAs(t, "oscar@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a member could not answer: %s", resp.Status)
	}
}
