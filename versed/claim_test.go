package versed

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// THE CLAIM IS THE ANSWER (consequence.go, settle). A question is settled by the first resolution
// written on it as `followed`; what follows it, what is delivered and what the record says are
// that one resolution's, whichever delivery would have taken first, and a crash between the claim
// and the delivery leaves an answer decided and not yet heard, which the next posing delivers.

// claimAsk writes a resolution on a question as its claim, as a settle does before anything runs or
// is delivered: the state a crash there leaves.
func claimAsk(t *testing.T, srv *Server, ref entity.Ref, res task.Resolution) {
	t.Helper()
	if _, _, err := entity.As(srv.Store, entity.SystemDocket).Write(entity.Write{
		At: ref.At(), Value: entity.Obj{"followed": mirror.ClaimRecord(res, time.Now())}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
}

// A CLAIM STANDING IS THE ANSWER, WHATEVER LANDS AFTER IT. bob's no claimed alice's ask, and
// while its delivery is in flight carol's yes finds the claim and is refused, naming bob: it
// delivers nothing, since bob's settle delivers, and runs nothing. Once bob's delivery has failed,
// dave's yes is refused the same way and delivers bob's no in its place: the ask settles on it,
// and the yes's `then` never starts. bob's no again is that answer's own retry, and is told it
// took.
func TestTheClaimSettlesAQuestion(t *testing.T) {
	srv, ts := asking(t)
	const alice, bob, carol, dave = "user:alice@example.com", "user:bob@example.com", "user:carol@example.com", "user:dave@example.com"
	id := askID(t, ask(t, alice, ts.URL, `{name: ship, question: "Ship?", to: [bob@example.com, carol@example.com, dave@example.com],
		then: {spawn: {prompt: "announce it", environment: {systems: {network: {mode: open}}}}}}`))
	ref := mirror.AskRef(id)
	posed(t, srv, ref)
	claimAsk(t, srv, ref, task.Choose(task.Deny, bob))
	// An ask being settled is not changed: a new edit is one the claim could never be
	// delivered to.
	if resp := change(t, alice, ts.URL, id, `{question: "Ship now?"}`); resp.StatusCode != http.StatusConflict || !strings.Contains(bodyOf(t, resp), "being settled by "+bob) {
		t.Fatalf("a change to an ask being settled = %s, want 409 naming bob's claim", resp.Status)
	}

	resp := answerWith(t, carol, ts.URL, id, task.Proceed)
	if body := bodyOf(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "by "+bob) {
		t.Fatalf("carol's yes against bob's claim = %s: %s, want 409 naming bob", resp.Status, body)
	}
	if e, _, _ := srv.Store.Get(ref); entity.Str(e.Payload, "status") != mirror.QuestionPending {
		t.Fatalf("carol's yes delivered bob's no while bob's own delivery was in flight: %v", entity.ToObj(e.Payload))
	}

	srv.undelivered(srv.Store, id, errors.New("the runner did not answer"))
	resp = answerWith(t, dave, ts.URL, id, task.Proceed)
	if body := bodyOf(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "by "+bob) {
		t.Fatalf("dave's yes against bob's undelivered claim = %s: %s, want 409 naming bob", resp.Status, body)
	}
	waitValue(t, srv, ref, "status", mirror.QuestionAnswered)
	e, _, _ := srv.Store.Get(ref)
	rec := entity.ToObj(e.Payload)
	if rec["verdict"] != task.Deny || rec["by"] != bob || rec["answer"] != "no" {
		t.Fatalf("the ask settled as %v %v (%v), want bob's no", rec["verdict"], rec["by"], rec["answer"])
	}
	time.Sleep(100 * time.Millisecond)
	if e, _, _ := srv.Store.Get(ref); entity.Get(e.Payload, "then-task") != nil {
		t.Fatalf("carol's yes ran the then of a question bob's no settled: %v", entity.ToObj(e.Payload))
	}
	if resp := answerWith(t, bob, ts.URL, id, task.Deny); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bob's no again = %s: %s, want 204, its own answer", resp.Status, bodyOf(t, resp))
	}
}

// A CRASH BETWEEN THE CLAIM AND THE DELIVERY LOSES NOTHING. bob's yes claimed alice's ask and the
// verse went down before anything ran or was delivered. The next verse, waiting on the ask again,
// delivers the claimed yes, runs its `then`, and the ask settles as bob answered it; a verse after
// that runs nothing more.
func TestARestartFinishesAClaimedSettle(t *testing.T) {
	raw := entitytest.Store(t)
	first := NewWithStore(ScriptedBuilder, raw)
	t.Cleanup(first.stopAllIOs)
	_, ts := askingOn(t, first)
	const alice, bob = "user:alice@example.com", "user:bob@example.com"
	id := askID(t, ask(t, alice, ts.URL, `{name: ship, question: "Ship?", to: [bob@example.com],
		then: {spawn: {prompt: "announce it", environment: {systems: {network: {mode: open}}}}}}`))
	ref := mirror.AskRef(id)
	posed(t, first, ref)
	claimAsk(t, first, ref, task.Choose(task.Proceed, bob))
	// ... and the delivery had failed once before the crash, which the record says.
	first.undelivered(first.Store, id, errors.New("the verse went down"))
	ts.Close()
	first.stopAllIOs()

	second := NewWithStore(ScriptedBuilder, raw)
	t.Cleanup(second.stopAllIOs)
	second.Start(t.Context())
	waitValue(t, second, ref, "answer", "yes")
	then := thenTaskOf(t, second, ref)
	e, _, _ := second.Store.Get(ref)
	rec := entity.ToObj(e.Payload)
	if rec["status"] != mirror.QuestionAnswered || rec["verdict"] != task.Proceed || rec["by"] != bob {
		t.Fatalf("after the restart the ask says %v %v %v, want answered, proceed, by bob", rec["status"], rec["verdict"], rec["by"])
	}
	if c, _ := rec["followed"].(map[string]any); c["undelivered"] != nil {
		t.Fatalf("the claim still says undelivered: %v", rec["followed"])
	}

	third := NewWithStore(ScriptedBuilder, raw)
	t.Cleanup(third.stopAllIOs)
	third.Start(t.Context())
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := third.Store.Get(ref); entity.Str(e.Payload, "then-task") != then.Ref.Name() {
		t.Fatalf("a later start ran the then again: %q, was %q", entity.Str(e.Payload, "then-task"), then.Ref.Name())
	}
}

// A RESTORE PUTS BACK WHAT WAS ANSWERED YES TO. alice's rule has a gate whose `then` answers a
// question that has since settled; the install checked it against the verse as it stood, and the
// restore does not check it again, so the rule comes back.
func TestARestoreKeepsARuleWhoseThenAnswersASettledAsk(t *testing.T) {
	raw := entitytest.Store(t)
	first := NewWithStore(ScriptedBuilder, raw)
	t.Cleanup(first.stopAllIOs)
	_, ts := askingOn(t, first)
	const alice, bob = "user:alice@example.com", "user:bob@example.com"
	const ship = "verse.trigger.ship.ship~restore"
	answered := raise(t, first, ship, nil)
	// Installed, then given its then: a rule's place acts as whoever owns its record, which the
	// first install writes, so the then is checked as alice on the replacement.
	rule := func(then string) string {
		return `{name: approve-f,
			condition: {subject: {path: "demo.f.*", value: {stage: open}}},
			gates: [{name: approval, mode: ask` + then + `}],
			action: {patch: {stage: reviewing}}}`
	}
	for _, then := range []string{"", `, then: {answer: {ask: "` + ship + `", choice: proceed}}`} {
		resp := installAs(t, alice, ts.URL, rule(then))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("install with then %q: %s: %s", then, resp.Status, bodyOf(t, resp))
		}
		resp.Body.Close()
	}
	if resp := answerWith(t, bob, ts.URL, ship, task.Proceed); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bob settles ship: %s: %s", resp.Status, bodyOf(t, resp))
	}
	<-answered
	ts.Close()
	first.stopAllIOs()

	second := NewWithStore(ScriptedBuilder, raw)
	t.Cleanup(second.stopAllIOs)
	second.Start(t.Context())
	if _, ok := second.Eng.Rule("approve-f"); !ok {
		t.Fatal("the rule whose then answers a settled ask did not come back at the restart")
	}
}

// AN ANSWER NOBODY HEARD STANDS. The claim is the answer, so an answer whose delivery finds nobody
// waiting, the task's decision having closed, is not "no such question": it is recorded, said to
// stand, and the claim is on the record for the next asking to deliver.
func TestAnAnswerNobodyHeardStands(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": "unheard-1", "spawn": map[string]any{"script": "sleep 5", "environment": stepEnv}})
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn = %s: %s", resp.Status, bodyOf(t, resp))
	}
	resp.Body.Close()
	const qid = "unheard-1~closed"
	mirror.PostQuestion(srv.Store, task.Pending{Question: task.GateQuestion{ID: qid, Gate: "write"}}, "unheard-1")
	posed(t, srv, mirror.QuestionRef(qid))
	resp = postTony(t, ts.URL+answerPath(qid), api.Resolve{Choice: "proceed", By: tester})
	var rec api.Recorded
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("an answer the task has no open decision for = %s: %s, want 202 recorded", resp.Status, bodyOf(t, resp))
	}
	decodeResp(t, resp, &rec)
	if rec.Choice != "proceed" || !strings.Contains(rec.Note, "the answer stands") {
		t.Fatalf("recorded = %+v, want proceed with the answer standing", rec)
	}
	e, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
	c := entity.Get(e.Payload, "followed")
	if entity.Str(e.Payload, "status") != mirror.QuestionPending || entity.Str(c, "by") != "user:"+tester || entity.Str(c, "undelivered") == "" {
		t.Fatalf("the record = %v, want pending under the tester's claim, undelivered", entity.ToObj(e.Payload))
	}
}

// AN ANSWER NOBODY HEARD STANDS ON A FIRING'S QUESTION TOO. bob's yes claimed a firing's question
// and its delivery failed, and the firing's waiter is gone with the record still pending: bob's
// yes again is told the answer stands, as a task's question says it, and carol's no is refused,
// naming bob.
func TestAnAnswerNobodyHeardStandsOnAFiring(t *testing.T) {
	srv, ts := askingOn(t, testServer(t))
	const bob, carol = "user:bob@example.com", "user:carol@example.com"
	const id = "verse.trigger.review.review~unheard"
	// Posed with nothing in the registry waiting on it: the state a waiter leaving between the
	// claim and its delivery leaves, before the record says it went.
	mirror.PostQuestion(srv.Store, task.Pending{Question: task.GateQuestion{ID: id, Origin: "review", Gate: "review"}, Tool: "verse.trigger.review"}, "")
	ref := mirror.AskRef(id)
	posed(t, srv, ref)
	claimAsk(t, srv, ref, task.Choose(task.Proceed, bob))
	srv.undelivered(srv.Store, id, errors.New("nothing waits on it"))

	resp := answerWith(t, bob, ts.URL, id, task.Proceed)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bob's yes again, nobody waiting = %s: %s, want 202 recorded", resp.Status, bodyOf(t, resp))
	}
	var rec api.Recorded
	decodeResp(t, resp, &rec)
	if rec.Choice != task.Proceed || !strings.Contains(rec.Note, "the answer stands") {
		t.Fatalf("recorded = %+v, want proceed with the answer standing", rec)
	}
	if resp := answerWith(t, carol, ts.URL, id, task.Deny); resp.StatusCode != http.StatusConflict || !strings.Contains(bodyOf(t, resp), "by "+bob) {
		t.Fatalf("carol's no against bob's claim, nobody waiting = %s, want 409 naming bob", resp.Status)
	}
	e, _, _ := srv.Store.Get(ref)
	if entity.Str(e.Payload, "status") != mirror.QuestionPending || entity.Str(entity.Get(e.Payload, "followed"), "by") != bob {
		t.Fatalf("the record = %v, want pending under bob's claim", entity.ToObj(e.Payload))
	}
}
