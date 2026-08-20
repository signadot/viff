package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// answerable raises an engine gate and blocks on it the way a firing does, returning the
// channel the resolution comes back on.
func answerable(t *testing.T, srv *Server, id string) chan task.Resolution {
	t.Helper()
	answered := make(chan task.Resolution, 1)
	go func() {
		res, _ := srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: id, Origin: "review", Gate: "review", Mode: task.Ask},
			task.ToolUse{Name: "verse.trigger.review"})
		answered <- res
	}()
	waitPending(t, srv, id)
	return answered
}

// settledQuestion waits for a question's record to SETTLE, and answers it.
//
// The record is written by more than one path — a local run posts it from the registry and again
// from its frame, and the answer is a third writer — so a read taken the instant an answer's
// route returns 204 can catch the record between them, saying `status: pending` with a verdict
// and a `by` already on it. Waiting for the condition is the boundary; a fixed sleep is a margin
// sized for an unloaded machine, and that is what made two tests here flaky under the full suite
// (git-issue nkptp9b3h12ksx3kg9n0).
//
// It waits for a SETTLED status rather than for a particular one, so a test asserting the wrong
// outcome still fails on its own assertion rather than timing out here.
func settledQuestion(t *testing.T, srv *Server, id string) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		q := question(t, srv, id)
		switch q["status"] {
		case mirror.QuestionAnswered, mirror.QuestionOrphaned:
			return q
		}
		select {
		case <-deadline:
			t.Fatalf("question %s never settled: %+v", id, q)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func question(t *testing.T, srv *Server, id string) map[string]any {
	t.Helper()
	e, _, _ := srv.Store.Get(mirror.QuestionRef(id))
	return entity.ToObj(e.Payload)
}

// Two people at a docket answer the same question at once. Exactly one answer applies, and
// BOTH are in the record — a docket that keeps only the winner cannot show that a question
// was contested, which is the thing an auditor would most want to see.
func TestTwoAnswersRaceAndBothAreRecorded(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const id = "verse.trigger.review.review~1"
	answered := answerable(t, srv, id)

	win := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: id, Approve: true, By: "human:scott"})
	win.Body.Close()
	if win.StatusCode != http.StatusNoContent {
		t.Fatalf("first answer = %s, want 204", win.Status)
	}
	lose := postTony(t, ts.URL+"/gate/resolve", api.Resolve{
		ID: id, Approve: false, By: "human:alice", Say: []string{"not this week"},
	})
	lose.Body.Close()
	if lose.StatusCode != http.StatusConflict {
		t.Fatalf("second answer = %s, want 409 — losing a race is an outcome, not a missing question", lose.Status)
	}

	// The task proceeds ONCE, on the winner's verdict. A denial arriving second must not
	// reach the waiter at all: it lost, and a gate that resolved twice is a gate that let a
	// second decision through after the first had already released the work.
	select {
	case res := <-answered:
		if !res.Proceed || res.By != "human:scott" {
			t.Fatalf("the waiter got %+v, want the first answer", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the question never resolved")
	}

	q := question(t, srv, id)
	if q["status"] != mirror.QuestionAnswered || q["by"] != "human:scott" || q["verdict"] != "proceed" {
		t.Fatalf("the record does not name the winner: %+v", q)
	}
	stale, _ := q["stale"].(map[string]any)
	late, ok := stale["human:alice"].(map[string]any)
	if !ok {
		t.Fatalf("the loser left no trace: %+v", q)
	}
	if late["verdict"] != "deny" {
		t.Errorf("the loser's verdict was not kept: %+v", late)
	}
	if late["at"] == nil {
		t.Errorf("the loser's answer has no time on it: %+v", late)
	}
}

// A stale answer to a real question and an answer to no question are different errors. They
// were one 404 before the record existed to tell them apart, and the difference is what an
// operator needs: one means "look for your typo", the other means "somebody beat you to it".
func TestAnsweringNoQuestionIsNotTheSameAsLosing(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: "verse.trigger.nope.nope~9", Approve: true, By: "human:scott"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("answering an unknown id = %s, want 404", resp.Status)
	}
	// And nothing was minted for it. An answer to no question must not conjure the question
	// it was answering, or the docket would grow an entry every time somebody mistyped.
	if _, ok, _ := srv.Store.Get(mirror.QuestionRef("verse.trigger.nope.nope~9")); ok {
		t.Fatal("answering an unknown id created a question record")
	}
}

// Attribution is required rather than defaulted. The server used to fill in `human:cli`,
// which put a name on the record belonging to nobody — and the record is durable now, so an
// unattributable answer is a permanent one.
func TestAnAnswerMustNameItsAnswerer(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const id = "verse.trigger.review.review~1"
	answerable(t, srv, id)

	resp := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: id, Approve: true})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unattributed answer = %s, want 400", resp.Status)
	}
	// And it did not land. Refusing at the edge is only worth anything if the refusal is
	// also a refusal to act.
	if q := question(t, srv, id); q["status"] != mirror.QuestionPending {
		t.Fatalf("a refused answer settled the question anyway: %+v", q)
	}
}

// An answer naming no question is refused before anything is looked up. Without this it
// reaches the registry as an empty id, finds nothing, and comes back 404 — indistinguishable
// from a real id that was mistyped.
func TestAnAnswerMustNameItsQuestion(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postTony(t, ts.URL+"/gate/resolve", api.Resolve{Approve: true, By: "human:scott"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an answer naming no question = %s, want 400", resp.Status)
	}
}

// An answer that could not be DELIVERED is not an answer about a missing question.
//
// It used to be. A runner that was down produced "no such question", which sends an operator
// hunting a typo for a question that is open, published, and waiting — the one case where
// retrying is exactly the right thing to do.
func TestAnUndeliverableAnswerSaysSo(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	srv := testServer(t)
	srv.SetRunner(runner.HTTP(deadURL))
	mirror.PostQuestion(srv.Store, task.Pending{
		Question: task.GateQuestion{ID: "p~d1", Origin: "p", Gate: "write"},
	}, "p")
	entitytest.Set(t, srv.Store, mirror.Ref("p"),
		entity.Obj{"status": "running", "runner": deadURL}.Payload())

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: "p~d1", Approve: true, By: "human:scott"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("answering an open question on an unreachable runner = %s, want 502", resp.Status)
	}

	// And the question is untouched. Nothing is known about whether it was answered, so
	// recording an outcome — settled, or stale — would be recording a guess.
	q := question(t, srv, "p~d1")
	if q["status"] != mirror.QuestionPending {
		t.Fatalf("an undelivered answer changed the record: %+v", q)
	}
	if q["stale"] != nil {
		t.Fatalf("an undelivered answer was recorded as a losing one: %+v", q)
	}
}

// A question whose asker gave up leaves the docket.
//
// The registry drops a question when its last waiter is cancelled — a timed-out request, a
// torn-down task — and OnPost had no counterpart, so the publisher was told about every
// question that arrived and none that left. The record stayed `pending`: offered on a docket,
// answerable-looking, and undeliverable, until something else happened to notice.
func TestAQuestionWhoseAskerGaveUpLeavesTheDocket(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const id = "verse.trigger.review.review~1"
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = srv.Reg.Resolve(ctx,
			task.GateQuestion{ID: id, Origin: "review", Gate: "review", Mode: task.Ask},
			task.ToolUse{Name: "verse.trigger.review"})
	}()
	waitPending(t, srv, id)
	if q := question(t, srv, id); q["status"] != mirror.QuestionPending {
		t.Fatalf("the question was not published as pending: %+v", q)
	}

	cancel() // the firing that was waiting on this answer is gone

	var got map[string]any
	for range 400 {
		got = question(t, srv, id)
		if got["status"] != mirror.QuestionPending {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got["status"] != mirror.QuestionOrphaned {
		t.Fatalf("status = %v, want orphaned — nobody is left to receive an answer: %+v",
			got["status"], got)
	}

	resp, err := http.Get(ts.URL + "/gates")
	if err != nil {
		t.Fatalf("gates: %v", err)
	}
	defer resp.Body.Close()
	var gates []map[string]any
	decodeResp(t, resp, &gates)
	if len(gates) != 0 {
		t.Fatalf("the docket still offers %v", gates)
	}
}

// But a joiner giving up while others still wait takes nothing off the docket. A question
// several callers are blocked on is not retired because one of them left.
func TestOneJoinerLeavingKeepsTheQuestion(t *testing.T) {
	srv := testServer(t)
	const id = "verse.trigger.review.review~1"
	q := task.GateQuestion{ID: id, Origin: "review", Gate: "review", Mode: task.Ask}

	stay, keep := context.WithCancel(context.Background())
	defer keep()
	go func() { _, _ = srv.Reg.Resolve(stay, q, task.ToolUse{Name: "verse.trigger.review"}) }()
	waitPending(t, srv, id)

	leave, give := context.WithCancel(context.Background())
	go func() { _, _ = srv.Reg.Resolve(leave, q, task.ToolUse{Name: "verse.trigger.review"}) }()
	for srv.Reg.WaiterCount(id) < 2 {
		time.Sleep(time.Millisecond)
	}
	give()
	time.Sleep(100 * time.Millisecond)

	if got := question(t, srv, id); got["status"] != mirror.QuestionPending {
		t.Fatalf("status = %v, want still pending — somebody is still blocked on it: %+v",
			got["status"], got)
	}
}

// An answer a broker took for a worker is recorded as ACCEPTED, not as delivered — and
// becomes delivered when the worker acknowledges it.
//
// It was recorded as settled the moment it was queued, so `verse gate`, the snapshot and the
// record all reported the question decided while the worker that must act on it had heard
// nothing. If it never came back the answer evaporated with no trace it was undelivered.
//
// The gate is closed either way: nobody else may answer. What is not yet true is that the
// party who must act knows.
func TestARelayedAnswerIsRecordedAsInFlight(t *testing.T) {
	b := broker.New()
	b.Spawn(task.Launch{ID: "p", Prompt: "work", Driver: "human"}, nil)
	srv := testServer(t)
	srv.SetRunner(b)
	nonce := srv.HostBroker("", b.WorkerHandler())
	mirror.Reflect(srv.Store, "p", b.Attach("p"), mirror.Meta{Runner: b.Ref()})
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	work := httptest.NewServer(srv.BrokerHandler())
	defer work.Close()

	claim := postTony(t, work.URL+"/broker/"+nonce+"/tasks/p/claim", map[string]any{"identity": "human:w"})
	var cl struct {
		Epoch int64 `tony:"field=epoch"`
	}
	decodeResp(t, claim, &cl)
	claim.Body.Close()
	postTony(t, work.URL+"/broker/"+nonce+"/tasks/p/report", map[string]any{
		"epoch":  cl.Epoch,
		"events": []task.Frame{{Kind: task.EvPending, ID: "p~d1", Gate: "write"}},
	}).Body.Close()
	for range 400 {
		if _, ok, _ := srv.Store.Get(mirror.QuestionRef("p~d1")); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	resp := postTony(t, ops.URL+"/gate/resolve", api.Resolve{ID: "p~d1", Approve: true, By: "human:scott"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resolve = %s", resp.Status)
	}

	q := question(t, srv, "p~d1")
	if q["status"] != mirror.QuestionAnswered || q["verdict"] != "proceed" {
		t.Fatalf("the answer was not recorded at all: %+v", q)
	}
	if q["delivered"] != false {
		t.Fatalf("delivered = %v, want false — the worker has not been told: %+v", q["delivered"], q)
	}

	// The worker pulls, then polls past it. That acknowledgement is what makes it delivered.
	pull := func(since int64) []map[string]any {
		t.Helper()
		r, err := http.Get(work.URL + "/broker/" + nonce + "/tasks/p/inbox?since=" +
			itoa(since) + "&epoch=" + itoa(cl.Epoch))
		if err != nil {
			t.Fatalf("inbox: %v", err)
		}
		defer r.Body.Close()
		var out struct {
			Items []map[string]any `tony:"field=items"`
		}
		decodeResp(t, r, &out)
		return out.Items
	}
	items := pull(0)
	if len(items) == 0 {
		t.Fatal("the answer never reached the worker's inbox")
	}
	pull(int64(len(items)))

	var got map[string]any
	for range 400 {
		got = question(t, srv, "p~d1")
		if _, still := got["delivered"]; !still {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the worker acknowledged the answer and the record still says in-flight: %+v", got)
}
