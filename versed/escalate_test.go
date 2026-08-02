package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

func escalateAs(t *testing.T, ts *httptest.Server, instID, qid string) *http.Response {
	t.Helper()
	return postTony(t, ts.URL+"/resolvers/instance/"+instID+"/verdicts",
		api.Verdict{ID: qid, Verdict: "escalate", Why: "not mine to call"})
}

// A named binding escalates to the standing one covering the subject, and that one to `⊤`.
//
// Two hops and no counter, which falls out of the ladder rather than being enforced by it: a
// binding cannot hand a question to itself, so the second hop always lands at the docket a
// person reads. Escalating changes who answers and not what is being answered — the question
// id is the same throughout, because the thing being decided did not change.
func TestEscalationReachesTheDocketInTwoHops(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	installResolver(t, ts.URL,
		`{name: security-review, kind: security, byRef: true, method: {driver: url, url: "https://x"}}`).Body.Close()
	pr(t, srv, "1", "payments")

	// Named beats standing, so the run's own resolver owns it first.
	spawnFor(t, ts, "run-1", "change:pr:1", "resolvers:security:security-review")
	qid := waitQuestion(t, srv, "run-1")
	if got := question(t, srv, qid)["resolver"]; got != "resolvers:security:security-review" {
		t.Fatalf("owned by %v before any escalation", got)
	}

	// Hop one: the named layer declines, and the standing binding covering the subject owns it.
	resp := escalateAs(t, ts, resolver.InstanceRef("security-review", "run-1").ID, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("escalate = %s, want 204", resp.Status)
	}
	got := question(t, srv, qid)
	if got["resolver"] != "resolvers:security:payments-review" {
		t.Fatalf("hop one landed on %v, want the standing binding", got["resolver"])
	}
	if got["reownedFrom"] != "resolvers:security:security-review" || got["reownedWhy"] != "escalated" {
		t.Fatalf("the transfer left no trace, or the wrong one: %+v", got)
	}
	if got["status"] != mirror.QuestionPending {
		t.Fatalf("escalating settled the question: %+v", got)
	}

	// Hop two: the standing binding declines, and there is nobody above it. Absent is ⊤.
	resp = escalateAs(t, ts, resolver.InstanceRef("payments-review", "run-1").ID, qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("second escalate = %s, want 204", resp.Status)
	}
	got = question(t, srv, qid)
	if got["resolver"] != nil {
		t.Fatalf("hop two landed on %v, want the docket", got["resolver"])
	}
	// Still open, still the SAME question — the thing being answered never changed.
	if got["status"] != mirror.QuestionPending || got["id"] != qid {
		t.Fatalf("the question changed as it moved: %+v", got)
	}
	// And a person can now answer it, which is what ⊤ means.
	ans := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: qid, Approve: true, By: "human:scott"})
	ans.Body.Close()
	if ans.StatusCode != http.StatusNoContent {
		t.Fatalf("a person answering an escalated question = %s", ans.Status)
	}
}

// A resolver escalates only what is its own, and cannot escalate what is already settled.
func TestEscalatingWhatIsNotYours(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "run-1", "change:pr:1", "")
	spawnFor(t, ts, "run-2", "change:pr:1", "")
	mine := waitQuestion(t, srv, "run-1")
	theirs := waitQuestion(t, srv, "run-2")

	inst := resolver.InstanceRef("payments-review", "run-1").ID
	resp := escalateAs(t, ts, inst, theirs)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("escalating another instance's question = %s, want 403", resp.Status)
	}

	ans := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: mine, Approve: true, By: "human:scott"})
	ans.Body.Close()
	resp = escalateAs(t, ts, inst, mine)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("escalating a settled question = %s, want 409", resp.Status)
	}
}

// A transfer ARRIVES for the layer above, on the stream it is already reading.
//
// Without this an escalated question would appear nowhere and the layer above would learn of
// it only by polling its docket — and the design says there is no second path anywhere, `⊤`
// included.
func TestAnEscalationArrivesOnTheSpine(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	installResolver(t, ts.URL,
		`{name: security-review, kind: security, byRef: true, method: {driver: url, url: "https://x"}}`).Body.Close()
	subject := entity.Ref{System: "change", Kind: "pr", ID: "1"}
	pr(t, srv, "1", "payments")
	mirror.Watch(srv.Store, "resolvers:security:payments-review", "payments-review", subject)

	spawnFor(t, ts, "run-1", subject.String(), "resolvers:security:security-review")
	qid := waitQuestion(t, srv, "run-1")
	escalateAs(t, ts, resolver.InstanceRef("security-review", "run-1").ID, qid).Body.Close()
	time.Sleep(50 * time.Millisecond)

	spine := resolver.InstanceRef("payments-review", subject.String()).ID
	var arrived bool
	for _, ev := range commitsOf(t, ts.URL+"/resolvers/instance/"+spine+"/events?since=0") {
		if ev.Ref == mirror.QuestionRef(qid) && entity.Str(ev.To, "resolver") == "resolvers:security:payments-review" {
			arrived = true
		}
	}
	if !arrived {
		t.Fatal("the escalated question never appeared on the spine of the layer that now owns it")
	}
}
