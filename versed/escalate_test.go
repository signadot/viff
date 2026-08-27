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
	spawnFor(t, ts, "run-1", `change.pr."1"`, "verse.resolvers.security.security-review")
	qid := waitQuestion(t, srv, "run-1")
	if got := waitOwned(t, srv, qid)["resolver"]; got != "verse.resolvers.security.security-review" {
		t.Fatalf("owned by %v before any escalation", got)
	}

	// Hop one: the named layer declines, and the standing binding covering the subject owns it.
	resp := escalateAs(t, ts, waitInstance(t, srv, "security-review", "run-1"), qid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("escalate = %s, want 204", resp.Status)
	}
	got := question(t, srv, qid)
	if got["resolver"] != "verse.resolvers.security.payments-review" {
		t.Fatalf("hop one landed on %v, want the standing binding", got["resolver"])
	}
	if got["reownedFrom"] != "verse.resolvers.security.security-review" || got["reownedWhy"] != "escalated" {
		t.Fatalf("the transfer left no trace, or the wrong one: %+v", got)
	}
	if got["status"] != mirror.QuestionPending {
		t.Fatalf("escalating settled the question: %+v", got)
	}

	// Hop two: the standing binding declines, and there is nobody above it. Absent is ⊤.
	resp = escalateAs(t, ts, waitInstance(t, srv, "payments-review", "run-1"), qid)
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
	spawnFor(t, ts, "run-1", `change.pr."1"`, "")
	spawnFor(t, ts, "run-2", `change.pr."1"`, "")
	mine := waitQuestion(t, srv, "run-1")
	theirs := waitQuestion(t, srv, "run-2")

	inst := waitInstance(t, srv, "payments-review", "run-1")
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
	subject := entity.RefAt("change", "pr", "1")
	pr(t, srv, "1", "payments")
	mirror.Watch(srv.Store, "verse.resolvers.security.payments-review", "payments-review", subject)

	spawnFor(t, ts, "run-1", subject.String(), "verse.resolvers.security.security-review")
	qid := waitQuestion(t, srv, "run-1")
	escalateAs(t, ts, waitInstance(t, srv, "security-review", "run-1"), qid).Body.Close()
	time.Sleep(50 * time.Millisecond)

	spine := resolver.InstanceRef("payments-review", mirror.OriginKey(subject)).ID()
	var arrived bool
	for _, ev := range commitsOf(t, ts.URL+"/resolvers/instance/"+spine+"/events?since=0") {
		if ev.Ref == mirror.QuestionRef(qid) && entity.Str(ev.To, "resolver") == "verse.resolvers.security.payments-review" {
			arrived = true
		}
	}
	if !arrived {
		t.Fatal("the escalated question never appeared on the spine of the layer that now owns it")
	}
}

func standingWithDeadline(name, area, within string) string {
	return `{name: ` + name + `, kind: security, condition: {subject: {system: change, kind: pr,` +
		` value: {area: ` + area + `}}}, deadline: {within: "` + within + `"},` +
		` method: {driver: url, url: "https://x"}}`
}

// waitArmed waits for a posed question to carry its owner's deadline, and returns it.
//
// Arming is a SECOND write: posting the question publishes it, and stamping `escalatesAt`
// happens after, because the instant is computed from a binding that has to be looked up
// first (see the `arm` hook in server.go). So a reader that waits for the question and then
// reads the clock is reading between two writes, and the answer is legitimately nil — which
// is what made this test fail under -race and pass otherwise. It is the arming that is being
// asserted, so it is the arming that has to be waited for; the message is the assertion it
// replaces, and a genuine failure to arm still reaches it as a timeout.
func waitArmed(t *testing.T, srv *Server, qid string) map[string]any {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		got := question(t, srv, qid)
		if got["escalatesAt"] != nil {
			return got
		}
		select {
		case <-deadline:
			t.Fatalf("the deadline was never armed: %+v", got)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Silence does what an `escalate` verdict does, more slowly.
//
// The expiry is evaluated when a READER arrives rather than by a clock of its own, because the
// layer above is the only party it is for and it learns by reading. A sweeper would compute
// the same transfer slightly earlier, for nobody.
func TestSilencePastTheDeadlineEscalates(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	installResolver(t, ts.URL,
		`{name: security-review, kind: security, byRef: true, deadline: {within: "300ms"}, `+
			`method: {driver: url, url: "https://x"}}`).Body.Close()
	pr(t, srv, "1", "payments")

	spawnFor(t, ts, "run-1", `change.pr."1"`, "verse.resolvers.security.security-review")
	qid := waitQuestion(t, srv, "run-1")

	// Armed, and absolute: a duration on the binding becomes an instant on the question,
	// so a restart cannot slide it.
	got := waitArmed(t, srv, qid)
	// Inside it, nothing moves however much anyone reads.
	docket(t, srv)
	if q := waitOwned(t, srv, qid); q["resolver"] != "verse.resolvers.security.security-review" {
		t.Fatalf("a question inside its deadline moved to %v", q["resolver"])
	}

	time.Sleep(350 * time.Millisecond)
	docket(t, srv) // a reader arrives, and that is when it is evaluated

	got = question(t, srv, qid)
	if got["resolver"] != "verse.resolvers.security.payments-review" {
		t.Fatalf("overdue, and owned by %v — want the layer above", got["resolver"])
	}
	if got["reownedWhy"] != "overdue" || got["reownedFrom"] != "verse.resolvers.security.security-review" {
		t.Fatalf("the transfer says the wrong thing moved it: %+v", got)
	}
	if got["id"] != qid || got["status"] != mirror.QuestionPending {
		t.Fatalf("the question changed as it moved: %+v", got)
	}
	// The new owner has no deadline of its own, so the clock is cleared rather than
	// inherited — otherwise it would be overdue forever against a deadline nobody set.
	if got["escalatesAt"] != nil {
		t.Fatalf("the old owner's clock outlived it: %+v", got)
	}
	// And reading again does not move it again.
	docket(t, srv)
	if q := question(t, srv, qid); q["resolver"] != "verse.resolvers.security.payments-review" {
		t.Fatalf("a second read moved it to %v", q["resolver"])
	}
}

// A binding that answers before its deadline never escalates at all.
func TestAnsweringBeforeTheDeadline(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standingWithDeadline("payments-review", "payments", "300ms")).Body.Close()
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "run-1", `change.pr."1"`, "")
	qid := waitQuestion(t, srv, "run-1")

	resp := postTony(t, ts.URL+"/resolvers/instance/"+waitInstance(t, srv, "payments-review", "run-1")+"/verdicts",
		api.Verdict{ID: qid, Verdict: "proceed"})
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("the verdict was refused: %s", resp.Status)
	}
	// ANSWERED BEFORE THE CLOCK, waited for rather than assumed. The record is written by more
	// than one path and a read taken the instant this route returns can catch it mid-flight; a
	// test that slept through that window was asserting about whichever version it happened to
	// see (git-issue nkptp9b3h12ksx3kg9n0).
	settledQuestion(t, srv, qid)
	// Now let the deadline pass, and sweep. The property is that a question answered in time is
	// not moved by its own clock.
	time.Sleep(350 * time.Millisecond)
	docket(t, srv)

	got := question(t, srv, qid)
	if got["reownedWhy"] != nil {
		t.Fatalf("an answered question was moved by its own deadline: %+v", got)
	}
	if got["by"] != "verse.resolvers.security.payments-review" {
		t.Fatalf("answered by %v, want the binding that answered in time", got["by"])
	}
}

// A handover must LOSE to an answer. escalateOverdue snapshots the pending questions and then
// re-owns them one at a time, so an answer landing during that loop would otherwise be merged
// over: a record saying both "decided by X" and "X let its deadline pass", handing the layer
// above a question somebody had already closed.
//
// The write is conditional on the question still being pending rather than re-read before it,
// because a read before it is the same race with a smaller window.
func TestAnAnswerBeatsAHandover(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	installResolver(t, ts.URL,
		`{name: security-review, kind: security, byRef: true, deadline: {within: "10ms"}, `+
			`method: {driver: url, url: "https://x"}}`).Body.Close()
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "run-1", `change.pr."1"`, "verse.resolvers.security.security-review")
	qid := waitQuestion(t, srv, "run-1")
	time.Sleep(30 * time.Millisecond) // overdue

	// Settled first, then the sweep arrives and must decline to move it.
	resp := postTony(t, ts.URL+"/resolvers/instance/"+
		resolver.InstanceRef("security-review", "run-1").ID()+"/verdicts",
		api.Verdict{ID: qid, Verdict: "proceed"})
	resp.Body.Close()
	docket(t, srv)

	got := question(t, srv, qid)
	if got["reownedWhy"] != nil {
		t.Fatalf("a settled question was handed on anyway: %+v", got)
	}
	if got["by"] != "verse.resolvers.security.security-review" || got["status"] != mirror.QuestionAnswered {
		t.Fatalf("the answer did not survive: %+v", got)
	}
}
