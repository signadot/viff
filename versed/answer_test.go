package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
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
	e, ok, err := srv.Store.Get(mirror.QuestionRef(id))
	// SAY WHICH IT WAS. Dropping these turned every unreadable or absent question into an
	// empty record, so a test asserting on one field failed as "the field is <nil>" — a
	// sentence about the record when the truth was that there was no record, or no read.
	if err != nil {
		t.Fatalf("read question %s: %v", id, err)
	}
	if !ok {
		t.Fatalf("no question stands at %s", id)
	}
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

	win := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: "user:scott@example.com"})
	win.Body.Close()
	if win.StatusCode != http.StatusNoContent {
		t.Fatalf("first answer = %s, want 204", win.Status)
	}
	lose := postTony(t, ts.URL+answerPath(id), api.Resolve{
		Choice: "deny", By: "user:alice@example.com", Say: []string{"not this week"},
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
		if !res.Proceed || res.By != "user:scott@example.com" {
			t.Fatalf("the waiter got %+v, want the first answer", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the question never resolved")
	}

	q := question(t, srv, id)
	if q["status"] != mirror.QuestionAnswered || q["by"] != "user:scott@example.com" || q["verdict"] != "proceed" {
		t.Fatalf("the record does not name the winner: %+v", q)
	}
	stale, _ := q["stale"].(map[string]any)
	late, ok := stale["user:alice@example.com"].(map[string]any)
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

	resp := postTony(t, ts.URL+answerPath("verse.trigger.nope.nope~9"), api.Resolve{Choice: "proceed", By: "user:scott@example.com"})
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

// An answer is BY whoever the request is made as, whether or not its body says so: a name is on
// every request that writes, so there is no answer by nobody to refuse, and none to default.
func TestAnAnswerIsByWhoeverTheRequestIsMadeAs(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const id = "verse.trigger.review.review~1"
	answerable(t, srv, id)

	resp := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("an answer whose body names nobody = %s, want 204 as the request's principal", resp.Status)
	}
	if q := question(t, srv, id); q["by"] != "user:"+tester {
		t.Fatalf("settled by %v, want the principal the request was made as", q["by"])
	}
}

// An answer is about the question its path names, and a body naming another is refused before
// anything is looked up. Otherwise one of the two names is silently the answer's, and whichever
// it is, the request was about a question other than the one it answered.
func TestAnAnswerIsAboutTheQuestionItsPathNames(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const id, other = "verse.trigger.review.review~1", "verse.trigger.review.review~2"
	answerable(t, srv, id)
	answerable(t, srv, other)

	resp := postTony(t, ts.URL+answerPath(id), api.Resolve{ID: other, Choice: "proceed", By: "user:scott@example.com"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an answer whose body names another question = %s, want 400", resp.Status)
	}
	for _, q := range []string{id, other} {
		if got := question(t, srv, q); got["status"] != mirror.QuestionPending {
			t.Fatalf("a refused answer settled %s: %+v", q, got)
		}
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
	resp := postTony(t, ts.URL+answerPath("p~d1"), api.Resolve{Choice: "proceed", By: "user:scott@example.com"})
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

	resp, err := http.Get(ts.URL + "/asks?all=true")
	if err != nil {
		t.Fatalf("docket: %v", err)
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
