package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

// mustInstall fails loudly rather than leaving a test to wonder why nothing owns its question:
// an add that is refused returns a body saying why, and swallowing it costs an afternoon.
func mustInstall(t *testing.T, base, doc string) {
	t.Helper()
	resp := installResolver(t, base, doc)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("installing %s: %s: %s", doc, resp.Status, strings.TrimSpace(string(raw)))
	}
}

func exclusiveBinding(name, area string) string {
	return `{name: ` + name + `, kind: security, exclusive: true, deadline: {within: "1h"},` +
		` condition: {subject: {system: change, kind: pr, value: {area: ` + area + `}}},` +
		` method: {driver: url, url: "https://x"}}`
}

// An exclusive binding's question is not a person's to answer — until they say they are
// answering over it, which is a different act with a different record.
func TestExclusiveRefusesAnotherAnswerer(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	mustInstall(t, ts.URL, exclusiveBinding("payments-review", "payments"))
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "run-1", "change:pr:1", "")
	qid := waitQuestion(t, srv, "run-1")

	// Seen, though. Exclusive governs whose answer counts, never who may look: the question
	// is on the docket a person reads, exactly as any other is.
	found := false
	for _, g := range docket(t, srv) {
		if g["id"] == qid {
			found = true
		}
	}
	if !found {
		t.Fatal("an exclusive binding hid its question from the docket")
	}

	resp := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: qid, Approve: true, By: "human:scott"})
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a person answered an exclusive question: %s %s", resp.Status, body)
	}
	// Named, so the refusal says who to go and ask rather than only that this was not allowed.
	if !strings.Contains(body, "verse:resolvers:security:payments-review") {
		t.Fatalf("the refusal does not name the owner: %s", body)
	}
	if q := question(t, srv, qid); q["status"] != mirror.QuestionPending {
		t.Fatalf("the refused answer settled the question anyway: %+v", q)
	}

	// The owner's own verdict is taken, through the same path.
	inst := resolver.InstanceRef("payments-review", "run-1").ID()
	ok := postTony(t, ts.URL+"/resolvers/instance/"+inst+"/verdicts",
		api.Verdict{ID: qid, Verdict: "proceed"})
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusNoContent {
		t.Fatalf("the owner could not answer its own question: %s", ok.Status)
	}
	q := question(t, srv, qid)
	if q["by"] != "verse:resolvers:security:payments-review" || q["overrode"] != nil {
		t.Fatalf("the owner's own answer was recorded as an override: %+v", q)
	}
}

// The override is the honest half: `exclusive` is not real against whoever holds this API, so
// answering over it is possible and leaves both parties on the record.
func TestOverrideNamesBothParties(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	mustInstall(t, ts.URL, exclusiveBinding("payments-review", "payments"))
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "run-1", "change:pr:1", "")
	qid := waitQuestion(t, srv, "run-1")

	resp := postTony(t, ts.URL+"/gate/resolve",
		api.Resolve{ID: qid, Approve: true, By: "human:scott", Override: true})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the override was refused: %s", resp.Status)
	}
	q := settledQuestion(t, srv, qid)
	if q["by"] != "human:scott" || q["overrode"] != "verse:resolvers:security:payments-review" {
		t.Fatalf("the record does not name both parties: %+v", q)
	}
	if q["status"] != mirror.QuestionAnswered || q["verdict"] != "proceed" {
		t.Fatalf("the overridden answer did not settle the question: %+v", q)
	}
}

// Advisory is the default and is unchanged: the first answer wins, whoever it is from.
func TestAdvisoryTakesTheFirstAnswer(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "run-1", "change:pr:1", "")
	qid := waitQuestion(t, srv, "run-1")

	resp := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: qid, Approve: true, By: "human:scott"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a person could not answer an advisory question: %s", resp.Status)
	}
	q := question(t, srv, qid)
	if q["by"] != "human:scott" || q["overrode"] != nil {
		t.Fatalf("an advisory answer was treated as an override: %+v", q)
	}
}
