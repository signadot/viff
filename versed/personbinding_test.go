package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

func personBinding(name, area, who, deadline string) string {
	d := ""
	if deadline != "" {
		d = `deadline: {within: "` + deadline + `"},`
	}
	return `{name: ` + name + `, kind: approvals, ` + d +
		` condition: {subject: {path: "change.pr.*", value: {area: ` + area + `}}},` +
		` method: {driver: person, who: ` + who + `}}`
}

func handOnAs(t *testing.T, as, url, id, say string) *http.Response {
	t.Helper()
	b, err := codec.Marshal(api.Escalate{ID: id, By: as, Say: say}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, url+"/gate/escalate", "application/tony", string(b))
}

// A person is a binding's resolver: the standing door takes the questions its condition
// covers and holds them out to the person, who answers from the docket; nobody else does; no
// instance is minted, since a person does not dial in.
func TestAPersonAsABindingsResolver(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)
	mustInstall(t, ts.URL, personBinding("payments-owner", "payments", "oscar@example.com", ""))

	pr(t, srv, "1", "payments")
	spawnAs(t, "alice@example.com", ts.URL, "run-1", `change.pr."1"`, "")
	qid := waitQuestion(t, srv, "run-1")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _ := entity.Holds(srv.Store, "user:oscar@example.com", entity.ReadWrite, mirror.QuestionRef(qid)); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if q := question(t, srv, qid); q["resolver"] != "verse.resolvers.approvals.payments-owner" {
		t.Fatalf("the question is owned by %v, want the person's binding", q["resolver"])
	}
	if ok, _ := entity.Holds(srv.Store, "user:oscar@example.com", entity.ReadWrite, mirror.QuestionRef(qid)); !ok {
		rec, _, _ := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid)))
		t.Fatalf("davido does not hold read-write on the question his binding took: %v", entity.ToObj(rec.Payload))
	}
	if _, ok, _ := srv.Store.Get(resolver.InstanceRef("payments-owner", "run-1")); ok {
		t.Fatal("an instance was minted for a person")
	}

	resp := answerAs(t, "bob@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a stranger answered: %s", resp.Status)
	}
	resp = answerAs(t, "oscar@example.com", ts.URL, qid)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		rec, _, _ := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid)))
		t.Fatalf("davido could not answer: %s %s\nrecord: %v", resp.Status, body, entity.ToObj(rec.Payload))
	}
	if q := settledQuestion(t, srv, qid); q["by"] != "oscar@example.com" {
		t.Fatalf("settled by %v", q["by"])
	}
}

// A person escalates the way a service does: the question moves to the next rung covering its
// subject, and to the docket when there is none, where the default holds it out again. A
// stranger cannot hand on what is not theirs, and silence past the person's deadline hands it
// on for them.
func TestAPersonEscalates(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withoutTheDocketDefault(t, srv)
	// Two rungs over payments, by name: alpha then zulu; then the docket.
	mustInstall(t, ts.URL, personBinding("alpha", "payments", "oscar@example.com", ""))
	mustInstall(t, ts.URL, personBinding("zulu", "payments", "carol@example.com", "1ms"))

	pr(t, srv, "1", "payments")
	spawnAs(t, "alice@example.com", ts.URL, "run-1", `change.pr."1"`, "")
	qid := waitQuestion(t, srv, "run-1")
	waitOwnedBy(t, srv, qid, "verse.resolvers.approvals.alpha")

	resp := handOnAs(t, "bob@example.com", ts.URL, qid, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a stranger escalated: %s", resp.Status)
	}
	resp = handOnAs(t, "oscar@example.com", ts.URL, qid, "not mine: infra")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("davido could not escalate: %s", resp.Status)
	}
	q := waitOwnedBy(t, srv, qid, "verse.resolvers.approvals.zulu")
	if q["reownedWhy"] != "escalated by oscar@example.com: not mine: infra" {
		t.Fatalf("the handover is not on the record: %v", q["reownedWhy"])
	}
	if ok, _ := entity.Holds(srv.Store, "user:oscar@example.com", entity.ReadWrite, mirror.QuestionRef(qid)); ok {
		t.Fatal("davido still holds the question he handed on")
	}
	if ok, _ := entity.Holds(srv.Store, "user:carol@example.com", entity.ReadWrite, mirror.QuestionRef(qid)); !ok {
		t.Fatal("carol does not hold the question handed to her")
	}

	// carol says nothing; her rung's clock hands it on to the docket when a reader arrives.
	time.Sleep(20 * time.Millisecond)
	docket(t, srv)
	q = waitOwnedBy(t, srv, qid, "")
	if q["reownedWhy"] != "overdue" {
		t.Fatalf("silence did not hand it on: %v", q["reownedWhy"])
	}
	// At the docket the default holds it out again — to nobody here, since the test turned
	// that off — and the author still answers what their work asked.
	resp = answerAs(t, "alice@example.com", ts.URL, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the task's owner could not answer at the docket: %s", resp.Status)
	}
}

// waitOwnedBy waits for a question to be owned by the given binding — "" for the docket, once
// a handover has been recorded.
func waitOwnedBy(t *testing.T, srv *Server, qid, binding string) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		p := question(t, srv, qid)
		owner, _ := p["resolver"].(string)
		if owner == binding && (binding != "" || p["reownedWhy"] != nil) {
			return p
		}
		select {
		case <-deadline:
			t.Fatalf("question %s never became %q's: %+v", qid, binding, p)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
