package versed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

const (
	ttlAsker    = "user:victor@example.com"
	ttlAnswerer = "user:oscar@example.com"
)

// expiryOf waits for an ask to carry its expiry, and answers it.
func expiryOf(t *testing.T, srv *Server, ref entity.Ref) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, waitField(t, srv, ref, askExpiryField))
	if err != nil {
		t.Fatalf("the expiry is not a date: %v", err)
	}
	return at
}

// gone waits for an ask to be removed, the clock having passed its keep. Generous: the timer
// is armed by the watch that sees the expiry written, and a loaded suite delays that watch.
func gone(t *testing.T, srv *Server, ref entity.Ref) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok, _ := srv.Store.Get(ref); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still stands after its keep", ref)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// keeping is a started verse on a clock the test moves, whose settled asks stand for keep (zero:
// the default).
func keeping(t *testing.T, keep time.Duration) (*Server, *httptest.Server, *fakeClock) {
	t.Helper()
	srv := testServer(t)
	clock := clockOf(t, srv)
	srv.AskKeep = keep
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, clock
}

// settle asks, answers yes as the answerer, and waits for the answer on the ask.
func settle(t *testing.T, srv *Server, url, body string) (entity.Ref, string) {
	t.Helper()
	ref := mirror.AskRef(askID(t, ask(t, ttlAsker, url, body)))
	posed(t, srv, ref)
	qid := ref.Name()
	if e, _, _ := srv.Store.Get(ref); entity.Str(e.Payload, askExpiryField) != "" {
		t.Fatal("a pending ask was given an expiry")
	}
	if resp := answerAs(t, ttlAnswerer, url, qid); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	waitField(t, srv, ref, "answer")
	return ref, qid
}

// A SETTLED ASK GOES AFTER ITS KEEP, and its question with it.
func TestASettledAskGoesAfterItsKeep(t *testing.T) {
	srv, ts, clock := keeping(t, 0)
	ref, qid := settle(t, srv, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com], keep: 2s}`)
	if at, want := expiryOf(t, srv, ref), clock.Now().Add(2*time.Second); !at.Equal(want) {
		t.Fatalf("expires %s, want 2s after it settled, %s", at, want)
	}
	clock.Advance(time.Second)
	if _, ok, _ := srv.Store.Get(ref); !ok {
		t.Fatal("the ask went before its keep")
	}
	clock.Advance(time.Second)
	gone(t, srv, ref)
	if _, ok, _ := srv.Store.Get(mirror.QuestionRef(qid)); ok {
		t.Fatal("its question outlived it")
	}
}

// WITHOUT A KEEP, A SETTLED ASK STANDS A WEEK.
func TestASettledAskStandsAWeekByDefault(t *testing.T) {
	srv, ts, clock := keeping(t, 0)
	ref, _ := settle(t, srv, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com]}`)
	if at, want := expiryOf(t, srv, ref), clock.Now().Add(askKeepDefault); !at.Equal(want) {
		t.Fatalf("expires %s, want a week on, %s", at, want)
	}
}

// AN EXPIRY OUTLIVES THE PROCESS THAT ARMED IT: the ask holds it, and the next start arms it again.
func TestAnAsksExpiryHoldsAcrossARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	clock := clockOf(t, first)
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	ref, _ := settle(t, first, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com], keep: 3s}`)
	expiryOf(t, first, ref)
	first.dropExpiry(ref.Name()) // the timer goes with the process
	ts.Close()
	stop()

	// The same clock: the expiry is an instant on the record, and the second verse arms it
	// against the clock as it stands.
	second := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	second.clock = clock
	second.Start(t.Context())
	clock.Advance(3 * time.Second)
	gone(t, second, ref)
}

// KEEP IS A DURATION, checked where it is asked.
func TestAnAsksKeepIsADuration(t *testing.T) {
	_, ts := asking(t)
	for _, keep := range []string{"a week", "-1h", "0s"} {
		resp := ask(t, ttlAsker, ts.URL, `{name: x, question: q, keep: "`+keep+`"}`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("keep %q: %s, want 400", keep, resp.Status)
		}
	}
}

// shortKeep is a started verse whose settled asks stand for keep, on the wall clock.
func shortKeep(t *testing.T, keep time.Duration) (*Server, *httptest.Server) {
	t.Helper()
	srv, ts, _ := keeping(t, keep)
	srv.clock = wallClock{}
	return srv, ts
}

const gatedRule = `{
	name: land, condition: {subject: {path: "demo.pr.*", value: !and [{stage: null}, !not {landed: null}]}},
	gates: [{name: ok, mode: ask}], action: {patch: {landed: true}},
}`

// A RULE'S GATE IS AN ASK, AND GOES LIKE ONE: settled, it stands for the verse's keep, then it is
// removed.
func TestASettledGateGoesAfterTheKeep(t *testing.T) {
	srv, ts, clock := keeping(t, 2*time.Second)
	if resp := installAs(t, "alice@example.com", ts.URL, gatedRule); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	pr := entity.RefAt("demo", "pr", "1")
	owns(t, srv, "user:alice@example.com", nil, pr)
	entitytest.Set(t, srv.Store, pr, entity.Obj{"stage": "open"}.Payload())
	qid := waitRuleQuestion(t, srv, "land", pr.String())
	ref := mirror.QuestionRef(qid)
	// Alice's pr, so hers to answer for: a gate naming nobody is answered by whoever may write
	// what it is about.
	if resp := answerAs(t, "user:alice@example.com", ts.URL, qid); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	expiryOf(t, srv, ref)
	clock.Advance(2 * time.Second)
	gone(t, srv, ref)
}

// A GATE FIRED AGAIN AFTER ITS ASK SETTLED IS A NEW ASK. The answer stands on the ask it answered,
// untouched — its status, its verdict, who gave it and the ballots — until the keep removes it;
// the firing that came back asks a question of its own, under an id of its own, which stands
// while it waits.
func TestAGateFiredAgainIsANewAsk(t *testing.T) {
	srv, ts, clock := keeping(t, 2*time.Second)
	if resp := installAs(t, "alice@example.com", ts.URL, gatedRule); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	pr := entity.RefAt("demo", "pr", "1")
	owns(t, srv, "user:alice@example.com", nil, pr)
	entitytest.Set(t, srv.Store, pr, entity.Obj{"stage": "open"}.Payload())
	qid := waitRuleQuestion(t, srv, "land", pr.String())
	ref := mirror.QuestionRef(qid)
	// Alice's pr, so hers to answer for: a gate naming nobody is answered by whoever may write
	// what it is about.
	if resp := answerAs(t, "user:alice@example.com", ts.URL, qid); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	expiryOf(t, srv, ref)
	decided := func() map[string]any {
		t.Helper()
		q := questionDoc(t, srv, qid)
		out := map[string]any{"ballots": fmt.Sprint(ballotsOf(q))}
		for _, k := range []string{"status", "verdict", "by", "answeredAt", askExpiryField, "askedAgain"} {
			out[k] = q[k]
		}
		return out
	}
	was := decided()
	if was["status"] != mirror.QuestionAnswered || was["verdict"] != "proceed" || was["ballots"] == fmt.Sprint(map[string]string{}) {
		t.Fatalf("the answer is not on the ask: %v", was)
	}

	// Fired again, as a rule does when its latch is taken off.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if e, _, _ := srv.Store.Get(pr); entity.Get(e.Payload, "landed") != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the rule's action never ran")
		}
	}
	entitytest.Set(t, srv.Store, pr, entity.Obj{"stage": "open", "landed": entity.Removed()}.Payload())
	again := waitRuleQuestion(t, srv, "land", pr.String())
	if again == qid {
		t.Fatalf("the gate fired again asked under the settled ask's id, %s", qid)
	}
	if now := decided(); fmt.Sprint(now) != fmt.Sprint(was) {
		t.Fatalf("the settled ask changed when the gate fired again:\n was %v\n now %v", was, now)
	}
	if q := waitQuestionStatus(t, srv, again, mirror.QuestionPending); entity.Get(q, "verdict") != nil || len(ballotsOf(entity.ToObj(q))) != 0 {
		t.Fatalf("the new ask is not a new question: %v", q)
	}

	clock.Advance(2 * time.Second)
	gone(t, srv, ref)
	e, ok, _ := srv.Store.Get(mirror.QuestionRef(again))
	if !ok {
		t.Fatal("the new ask went with the old one's keep")
	}
	if entity.Str(e.Payload, "status") != mirror.QuestionPending || entity.Str(e.Payload, askExpiryField) != "" {
		t.Fatalf("the new ask does not stand while it waits: %v", entity.ToObj(e.Payload))
	}
}

// A SUSPENDED ASK IS KEPT: a suspension withdraws a gate's ask to ask it again when it lifts, so
// it stands past the keep a settled ask goes after, and the lift asks it again unmarked.
func TestASuspendedAskIsKeptPastItsKeep(t *testing.T) {
	srv, ts, clock := keeping(t, time.Second)
	addRule(t, ts, `{
		name: ship,
		condition: {subject: {path: "demo.release.*", value: {stage: open}}},
		gates: [{name: approvals, need: 2}],
		action: {patch: {stage: shipped}},
	}`)
	subject := entity.RefAt("demo", "release", "1")
	entitytest.Set(t, asTester(srv), subject, entity.Obj{"stage": "open"}.Payload())
	id := waitRuleQuestion(t, srv, "ship", subject.String())
	ref := mirror.QuestionRef(id)
	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "*", Subject: subject.String(), Why: "hold it"}); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	rec := waitQuestionStatus(t, srv, id, mirror.QuestionOrphaned)
	if entity.Get(rec, mirror.SuspendedField) == nil {
		t.Fatalf("the withdrawn ask does not say it is suspended: %v", entity.ToObj(rec))
	}
	clock.Advance(3 * time.Second)
	if _, ok, _ := srv.Store.Get(ref); !ok {
		t.Fatal("a suspended ask was removed after the keep")
	}
	if code, _, msg := suspendAs(t, ts, "resume", "", "", api.FlowSelect{Charter: "*", Subject: subject.String()}); code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, msg)
	}
	rec = waitQuestionStatus(t, srv, id, mirror.QuestionPending)
	if entity.Get(rec, mirror.SuspendedField) != nil {
		t.Fatalf("the ask asked again still says it is suspended: %v", entity.ToObj(rec))
	}
}
