package versed

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

const (
	thScott = "user:scott@example.com"
	thDavid = "user:david@example.com"
	thAnn   = "user:ann@example.com"
	thBob   = "user:bob@example.com"
)

// answerWith answers an ask as who, with choice.
func answerWith(t *testing.T, as, url, id, choice string) *http.Response {
	t.Helper()
	b, err := codec.Marshal(api.Resolve{Choice: choice, By: as}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, url+answerPath(id), "application/tony", string(b))
}

// askField waits for an ask's record to carry field — for what a `then` or an `else` came to, with its status,
// since the mark is written before it is made — and answers the record.
func askField(t *testing.T, srv *Server, id, field string) entity.Payload {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(mirror.AskRef(id)); ok {
			f := entity.Get(e.Payload, field)
			follow := strings.HasPrefix(field, "then-") || strings.HasPrefix(field, "else-")
			made := !follow || entity.Get(f, "status") != nil
			if f != nil && made {
				return e.Payload
			}
		}
		select {
		case <-deadline:
			e, _, _ := srv.Store.Get(mirror.AskRef(id))
			t.Fatalf("ask %s never carried %s: %v", id, field, entity.ToObj(e.Payload))
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A YES CASTS THE ASKER'S BALLOT ON ANOTHER ASK: ship takes two proceeds; bob gives one; scott asks
// david whether the UI is ok, and david's yes casts scott's, with his words, and ship settles.
func TestAYesCastsTheAskersBallotOnAnother(t *testing.T) {
	srv, ts := asking(t)
	const ship = "verse.trigger.ship.ship~1"
	raise(t, srv, ship, []task.Option{{ID: "proceed", Need: 2}, {ID: "deny", Need: 1}})
	if resp := answerWith(t, thBob, ts.URL, ship, "proceed"); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bob's ballot: %s: %s", resp.Status, bodyOf(t, resp))
	}

	id := askID(t, ask(t, thScott, ts.URL, `{name: ui-ok, question: "Is the UI ok to ship?", answerers: [david@example.com],
		then: {answer: {ask: "`+ship+`", choice: proceed, say: "david says the UI is ok"}}}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := answerWith(t, thDavid, ts.URL, id, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("david's yes: %s: %s", resp.Status, bodyOf(t, resp))
	}

	p := askField(t, srv, id, "then-answered")
	if code, _ := codec.AsInt64(entity.ToObj(entity.Get(p, "then-answered"))["status"]); code != http.StatusNoContent {
		t.Fatalf("scott's ballot on ship: %v", entity.ToObj(entity.Get(p, "then-answered")))
	}
	q, _, _ := srv.Store.Get(mirror.QuestionRef(ship))
	rec := entity.ToObj(q.Payload)
	if rec["verdict"] != "proceed" || ballotsOf(rec)[thScott] != "proceed" {
		t.Fatalf("ship settled %v with ballots %v, want proceed with scott's", rec["verdict"], ballotsOf(rec))
	}
	if !strings.Contains(fmtPayload(q.Payload), "david says the UI is ok") {
		t.Errorf("scott's ballot does not carry his words: %s", fmtPayload(q.Payload))
	}
}

// A NO CASTS NOTHING: the ballot waits on the yes, and david's no leaves ship as it was.
func TestANoCastsNothing(t *testing.T) {
	srv, ts := asking(t)
	const ship = "verse.trigger.ship.ship~2"
	raise(t, srv, ship, []task.Option{{ID: "proceed", Need: 2}, {ID: "deny", Need: 1}})
	id := askID(t, ask(t, thScott, ts.URL, `{name: ui-ok, question: "UI ok?", answerers: [david@example.com],
		then: {answer: {ask: "`+ship+`", choice: proceed}}}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := answerWith(t, thDavid, ts.URL, id, "deny"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("david's no: %s: %s", resp.Status, bodyOf(t, resp))
	}
	askField(t, srv, id, "answer")
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := srv.Store.Get(mirror.AskRef(id)); entity.Get(e.Payload, "then-answered") != nil {
		t.Fatalf("a no made the answer: %v", entity.ToObj(e.Payload))
	}
	if q, _, _ := srv.Store.Get(mirror.QuestionRef(ship)); len(ballotsOf(entity.ToObj(q.Payload))) != 0 {
		t.Fatalf("ship has ballots after a no: %v", entity.ToObj(q.Payload))
	}
}

// AN ANSWER IS CHECKED WHERE IT IS ASKED FOR: it names an ask that stands and waits on its asker,
// and an option of that ask's own, in the shape an answer takes.
func TestAThenThatAnswersIsCheckedWhereItIsAsked(t *testing.T) {
	srv, ts := asking(t)
	const ship = "verse.trigger.ship.ship~3"
	raise(t, srv, ship, []task.Option{{ID: "proceed", Need: 1}, {ID: "deny", Need: 1}})
	bobs := askID(t, ask(t, "user:alice@example.com", ts.URL, `{name: bobs, question: q, answerers: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(bobs))
	for then, want := range map[string]int{
		`{answer: {ask: nothing-here, choice: proceed}}`:            http.StatusNotFound,
		`{answer: {ask: "` + bobs + `", choice: proceed}}`:          http.StatusForbidden, // it waits on bob, not scott
		`{answer: {ask: "` + ship + `", choice: maybe}}`:            http.StatusBadRequest,
		`{answer: {ask: "` + ship + `"}}`:                           http.StatusBadRequest,
		`{answer: {ask: "` + ship + `", choice: proceed, why: x}}`:  http.StatusBadRequest,
		`{answer: {ask: "` + ship + `", choice: proceed, edit: 7}}`: http.StatusConflict,
	} {
		resp := ask(t, thScott, ts.URL, `{name: ui-ok, question: q, answerers: [david@example.com], then: `+then+`}`)
		if resp.StatusCode != want {
			t.Errorf("%s: %s %s, want %d", then, resp.Status, bodyOf(t, resp), want)
		}
	}
	// One scott reads and is not asked: his own, put to david.
	mine := askID(t, ask(t, thScott, ts.URL, `{name: mine, question: q, answerers: [david@example.com]}`))
	posed(t, srv, mirror.AskRef(mine))
	resp := ask(t, thScott, ts.URL, `{name: ui-ok, question: q, answerers: [david@example.com],
		then: {answer: {ask: "`+mine+`", choice: proceed}}}`)
	if msg := bodyOf(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(msg, "does not wait on") {
		t.Errorf("an answer to scott's own ask: %s %s, want 403, as it does not wait on him", resp.Status, msg)
	}
}

// THE WORDS CONSENTED TO: scott's ballot is for ship as he saw it. Reworded before david's yes, ship
// is not answered, and scott's ask says why.
func TestAThenAnswerToAnAskSinceRewordedIsRefused(t *testing.T) {
	srv, ts := asking(t)
	ship := askID(t, ask(t, "user:alice@example.com", ts.URL, `{name: ship, question: "Ship it?", answerers: [scott@example.com]}`))
	posed(t, srv, mirror.AskRef(ship))
	id := askID(t, ask(t, thScott, ts.URL, `{name: ui-ok, question: "UI ok?", answerers: [david@example.com],
		then: {answer: {ask: "`+ship+`", choice: proceed}}}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := change(t, "user:alice@example.com", ts.URL, ship, `{question: "Ship it, and the migration?"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("alice's rewording: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := answerWith(t, thDavid, ts.URL, id, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("david's yes: %s", resp.Status)
	}
	out := entity.ToObj(entity.Get(askField(t, srv, id, "then-answered"), "then-answered"))
	if code, _ := codec.AsInt64(out["status"]); code != http.StatusConflict || !strings.Contains(fmtAny(out["refused"]), "edit") {
		t.Fatalf("then-answered %v, want refused over the edit", out)
	}
	if e, _, _ := srv.Store.Get(mirror.AskRef(ship)); entity.Str(e.Payload, "status") != mirror.QuestionPending {
		t.Fatalf("ship was answered with words scott never saw: %v", entity.ToObj(e.Payload))
	}
}

// A YES RAISES THE NEXT ASK, and that one's yes casts the ballot: david's yes asks ann, due an hour
// from his yes, and ann's yes answers ship for scott.
func TestAYesRaisesTheNextAskAndItsYesAnswers(t *testing.T) {
	srv, ts := asking(t)
	ship := askID(t, ask(t, "user:alice@example.com", ts.URL, `{name: ship, question: "Ship it?", answerers: [scott@example.com]}`))
	posed(t, srv, mirror.AskRef(ship))
	id := askID(t, ask(t, thScott, ts.URL, `{name: ui-ok, question: "UI ok?", answerers: [david@example.com],
		then: {ask: {name: deploy, question: "Deploy it?", answerers: [ann@example.com], due: 1h,
			then: {answer: {ask: "`+ship+`", choice: proceed}}}}}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := answerWith(t, thDavid, ts.URL, id, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("david's yes: %s", resp.Status)
	}
	asked := entity.ToObj(entity.Get(askField(t, srv, id, "then-asked"), "then-asked"))
	deploy, _ := asked["id"].(string)
	if !strings.HasPrefix(deploy, "deploy-") {
		t.Fatalf("then-asked %v, want the deploy ask's id", asked)
	}
	d, _, _ := srv.Store.Get(mirror.AskRef(deploy))
	if got := entity.Str(d.Payload, "asker"); got != thScott {
		t.Fatalf("the deploy ask is %q's, want scott's", got)
	}
	if due, err := time.Parse(time.RFC3339, entity.Str(d.Payload, "due")); err != nil || time.Until(due) < 50*time.Minute {
		t.Fatalf("the deploy ask is due %q, want an hour from david's yes", entity.Str(d.Payload, "due"))
	}
	if got := mirror.EditOf(entity.Get(entity.Get(d.Payload, "then"), "answer")); got != 1 {
		t.Fatalf("the nested answer consents to edit %d of ship, want 1, filled when scott asked", got)
	}
	posed(t, srv, mirror.AskRef(deploy))
	if resp := answerWith(t, thAnn, ts.URL, deploy, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ann's yes: %s: %s", resp.Status, bodyOf(t, resp))
	}
	askField(t, srv, deploy, "then-answered")
	if q, _, _ := srv.Store.Get(mirror.AskRef(ship)); entity.Str(q.Payload, "answer") != "yes" || entity.Str(q.Payload, "by") != thScott {
		t.Fatalf("ship %v, want answered yes by scott", entity.ToObj(q.Payload))
	}
}

// A NESTED ASK IS CHECKED WHERE THE FIRST IS ASKED, as far as the verse can say now.
func TestAThenThatAsksIsCheckedWhereItIsAsked(t *testing.T) {
	_, ts := asking(t)
	for then, want := range map[string]int{
		`{ask: {name: d, question: q, answerers: [ann]}}`:                                http.StatusBadRequest, // a bare word names nothing
		`{ask: {name: d, question: q, due: -1h}}`:                                        http.StatusBadRequest,
		`{ask: {name: d, question: q, serves: x}}`:                                       http.StatusBadRequest,
		`{ask: {name: d, question: q, then: {answer: {ask: nothing, choice: proceed}}}}`: http.StatusNotFound,
	} {
		resp := ask(t, thScott, ts.URL, `{name: ui-ok, question: q, answerers: [david@example.com], then: `+then+`}`)
		if resp.StatusCode != want {
			t.Errorf("%s: %s %s, want %d", then, resp.Status, bodyOf(t, resp), want)
		}
	}
}

func fmtAny(v any) string {
	s, _ := v.(string)
	return s
}

// A NO RUNS THE ELSE, and a yes the then: ann's no asks the lead instead, as scott; ann's yes on
// a second asks nobody.
func TestANoRunsTheElse(t *testing.T) {
	srv, ts := asking(t)
	const escalate = `else: {ask: {name: ship-lead, question: "Ann said no: ship it?", answerers: [lead@example.com]}}`
	no := askID(t, ask(t, thScott, ts.URL, `{name: ship, question: "Ship it?", answerers: [ann@example.com], `+escalate+`}`))
	yes := askID(t, ask(t, thScott, ts.URL, `{name: ship2, question: "Ship it?", answerers: [ann@example.com], `+escalate+`}`))
	posed(t, srv, mirror.AskRef(no))
	posed(t, srv, mirror.AskRef(yes))
	if resp := answerWith(t, thAnn, ts.URL, no, "deny"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ann's no: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := answerWith(t, thAnn, ts.URL, yes, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ann's yes: %s: %s", resp.Status, bodyOf(t, resp))
	}
	asked := entity.ToObj(entity.Get(askField(t, srv, no, "else-asked"), "else-asked"))
	lead, _ := asked["id"].(string)
	if !strings.HasPrefix(lead, "ship-lead-") {
		t.Fatalf("else-asked %v, want the lead's ask", asked)
	}
	if e, _, _ := srv.Store.Get(mirror.AskRef(lead)); entity.Str(e.Payload, "asker") != thScott {
		t.Fatalf("the lead's ask is %q's, want scott's", entity.Str(e.Payload, "asker"))
	}
	askField(t, srv, yes, "answer")
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := srv.Store.Get(mirror.AskRef(yes)); entity.Get(e.Payload, "else-asked") != nil {
		t.Fatalf("a yes ran the else: %v", entity.ToObj(e.Payload))
	}
	if e, _, _ := srv.Store.Get(mirror.AskRef(no)); entity.Get(e.Payload, "then-asked") != nil || entity.Get(e.Payload, "then-answered") != nil {
		t.Fatalf("a no ran a then: %v", entity.ToObj(e.Payload))
	}
}

// SILENCE THAT SETTLES ON NO RUNS THE ELSE: nobody answers by the date, which is a no unless the
// asker said otherwise, and the lead is asked.
func TestSilenceRunsTheElse(t *testing.T) {
	srv, ts, clock := keeping(t, 0)
	due := clock.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	id := askID(t, ask(t, thScott, ts.URL, `{name: ship, question: "Ship it?", answerers: [ann@example.com], due: "`+due+`",
		else: {ask: {name: ship-lead, question: "Nobody answered: ship it?", answerers: [lead@example.com]}}}`))
	posed(t, srv, mirror.AskRef(id))
	clock.Advance(time.Minute)
	asked := entity.ToObj(entity.Get(askField(t, srv, id, "else-asked"), "else-asked"))
	if lead, _ := asked["id"].(string); !strings.HasPrefix(lead, "ship-lead-") {
		t.Fatalf("else-asked %v after silence, want the lead's ask", asked)
	}
}

// AN ELSE IS CHECKED AS A THEN IS, and says which it is when it is refused; changing it asks again.
func TestAnElseIsCheckedAndChangingItAsksAgain(t *testing.T) {
	srv, ts := asking(t)
	for body, want := range map[string]int{
		`{name: x, question: q, answerers: [ann@example.com], else: {answer: {ask: nothing-here, choice: proceed}}}`: http.StatusNotFound,
		`{name: x, question: q, answerers: [ann@example.com], else: {shell: "rm -rf /"}}`:                            http.StatusBadRequest,
		`{name: x, question: q, answerers: [ann@example.com], else: {prompt: a, script: ""}}`:                        http.StatusBadRequest,
	} {
		resp := ask(t, thScott, ts.URL, body)
		if msg := bodyOf(t, resp); resp.StatusCode != want || !strings.Contains(msg, "else") {
			t.Errorf("%s: %s %q, want %d naming the else", body, resp.Status, msg, want)
		}
	}
	id := askID(t, ask(t, thScott, ts.URL, `{name: ship, question: "Ship it?", answerers: [ann@example.com]}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := change(t, thScott, ts.URL, id, `{else: {spawn: {prompt: "tell the channel it was declined", environment: {systems: {network: {mode: open}}}}}}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("a new else: %s: %s", resp.Status, bodyOf(t, resp))
	}
	e, _, _ := srv.Store.Get(mirror.AskRef(id))
	if got := mirror.EditOf(e.Payload); got != 2 || entity.Str(entity.Get(entity.Get(e.Payload, "else"), "spawn"), "prompt") == "" {
		t.Fatalf("after a new else the ask is at edit %d with else %v, want edit 2 and the new else", got, entity.Get(e.Payload, "else"))
	}
}
