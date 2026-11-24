package viffd

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

const (
	thWalter = "user:walter@example.com"
	thIvan   = "user:ivan@example.com"
	thAnn    = "user:ann@example.com"
	thBob    = "user:bob@example.com"
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

// askField waits for an ask's record to carry field — for what a row's `then` came to, with its
// status, since the mark is written before it is made — and answers the record.
// atPath is the value at a dotted path of fields in p, nil where any is absent: `ran.answered`.
func atPath(p entity.Payload, path string) entity.Payload {
	for _, k := range strings.Split(path, ".") {
		p = entity.Get(p, k)
	}
	return p
}

// strAt is the string at a dotted path of fields in p, empty where it is absent or no string.
func strAt(p entity.Payload, path string) string {
	parent, last := "", path
	if i := strings.LastIndex(path, "."); i >= 0 {
		parent, last = path[:i], path[i+1:]
	}
	if parent != "" {
		p = atPath(p, parent)
	}
	return entity.Str(p, last)
}

func askField(t *testing.T, srv *Server, id, field string) entity.Payload {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(mirror.AskRef(id)); ok {
			f := atPath(e.Payload, field)
			follow := strings.HasPrefix(field, "ran.")
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

// A YES CASTS THE ASKER'S BALLOT ON ANOTHER ASK: ship takes two proceeds; bob gives one; walter asks
// ivan whether the UI is ok, and ivan's yes casts walter's, with his words, and ship settles.
func TestAYesCastsTheAskersBallotOnAnother(t *testing.T) {
	srv, ts := asking(t)
	const ship = "mirror.trigger.ship.ship~1"
	raise(t, srv, ship, []task.Option{{ID: "proceed", Need: 2}, {ID: "deny", Need: 1}})
	if resp := answerWith(t, thBob, ts.URL, ship, "proceed"); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bob's ballot: %s: %s", resp.Status, bodyOf(t, resp))
	}

	id := askID(t, ask(t, thWalter, ts.URL, `{name: ui-ok, question: "Is the UI ok to ship?", to: [ivan@example.com],
		on: [{match: {verdict: proceed}, then: {answer: {ask: "`+ship+`", choice: proceed, say: "ivan says the UI is ok"}}}]}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := answerWith(t, thIvan, ts.URL, id, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ivan's yes: %s: %s", resp.Status, bodyOf(t, resp))
	}

	p := askField(t, srv, id, "ran.answered")
	if code, _ := codec.AsInt64(entity.ToObj(atPath(p, "ran.answered"))["status"]); code != http.StatusNoContent {
		t.Fatalf("walter's ballot on ship: %v", entity.ToObj(atPath(p, "ran.answered")))
	}
	q, _, _ := srv.Store.Get(mirror.QuestionRef(ship))
	rec := entity.ToObj(q.Payload)
	if rec["verdict"] != "proceed" || ballotsOf(rec)[thWalter] != "proceed" {
		t.Fatalf("ship settled %v with ballots %v, want proceed with walter's", rec["verdict"], ballotsOf(rec))
	}
	if !strings.Contains(fmtPayload(q.Payload), "ivan says the UI is ok") {
		t.Errorf("walter's ballot does not carry his words: %s", fmtPayload(q.Payload))
	}
}

// A NO CASTS NOTHING: the ballot waits on the yes, and ivan's no leaves ship as it was.
func TestANoCastsNothing(t *testing.T) {
	srv, ts := asking(t)
	const ship = "mirror.trigger.ship.ship~2"
	raise(t, srv, ship, []task.Option{{ID: "proceed", Need: 2}, {ID: "deny", Need: 1}})
	id := askID(t, ask(t, thWalter, ts.URL, `{name: ui-ok, question: "UI ok?", to: [ivan@example.com],
		on: [{match: {verdict: proceed}, then: {answer: {ask: "`+ship+`", choice: proceed}}}]}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := answerWith(t, thIvan, ts.URL, id, "deny"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ivan's no: %s: %s", resp.Status, bodyOf(t, resp))
	}
	askField(t, srv, id, "answer")
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := srv.Store.Get(mirror.AskRef(id)); atPath(e.Payload, "ran.answered") != nil {
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
	const ship = "mirror.trigger.ship.ship~3"
	raise(t, srv, ship, []task.Option{{ID: "proceed", Need: 1}, {ID: "deny", Need: 1}})
	bobs := askID(t, ask(t, "user:alice@example.com", ts.URL, `{name: bobs, question: q, to: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(bobs))
	for then, want := range map[string]int{
		`{answer: {ask: nothing-here, choice: proceed}}`:            http.StatusNotFound,
		`{answer: {ask: "` + bobs + `", choice: proceed}}`:          http.StatusForbidden, // it waits on bob, not walter
		`{answer: {ask: "` + ship + `", choice: maybe}}`:            http.StatusBadRequest,
		`{answer: {ask: "` + ship + `"}}`:                           http.StatusBadRequest,
		`{answer: {ask: "` + ship + `", choice: proceed, why: x}}`:  http.StatusBadRequest,
		`{answer: {ask: "` + ship + `", choice: proceed, edit: 7}}`: http.StatusConflict,
	} {
		resp := ask(t, thWalter, ts.URL, `{name: ui-ok, question: q, to: [ivan@example.com], on: [{then: `+then+`}]}`)
		if resp.StatusCode != want {
			t.Errorf("%s: %s %s, want %d", then, resp.Status, bodyOf(t, resp), want)
		}
	}
	// One walter reads and is not asked: his own, put to ivan.
	mine := askID(t, ask(t, thWalter, ts.URL, `{name: mine, question: q, to: [ivan@example.com]}`))
	posed(t, srv, mirror.AskRef(mine))
	resp := ask(t, thWalter, ts.URL, `{name: ui-ok, question: q, to: [ivan@example.com],
		on: [{match: {verdict: proceed}, then: {answer: {ask: "`+mine+`", choice: proceed}}}]}`)
	if msg := bodyOf(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(msg, "does not wait on") {
		t.Errorf("an answer to walter's own ask: %s %s, want 403, as it does not wait on him", resp.Status, msg)
	}
}

// THE WORDS CONSENTED TO: walter's ballot is for ship as he saw it. Reworded before ivan's yes, ship
// is not answered, and walter's ask says why.
func TestAThenAnswerToAnAskSinceRewordedIsRefused(t *testing.T) {
	srv, ts := asking(t)
	ship := askID(t, ask(t, "user:alice@example.com", ts.URL, `{name: ship, question: "Ship it?", to: [walter@example.com]}`))
	posed(t, srv, mirror.AskRef(ship))
	id := askID(t, ask(t, thWalter, ts.URL, `{name: ui-ok, question: "UI ok?", to: [ivan@example.com],
		on: [{match: {verdict: proceed}, then: {answer: {ask: "`+ship+`", choice: proceed}}}]}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := change(t, "user:alice@example.com", ts.URL, ship, `{question: "Ship it, and the migration?"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("alice's rewording: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := answerWith(t, thIvan, ts.URL, id, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ivan's yes: %s", resp.Status)
	}
	out := entity.ToObj(atPath(askField(t, srv, id, "ran.answered"), "ran.answered"))
	if code, _ := codec.AsInt64(out["status"]); code != http.StatusConflict || !strings.Contains(fmtAny(out["refused"]), "edit") {
		t.Fatalf("ran.answered %v, want refused over the edit", out)
	}
	if e, _, _ := srv.Store.Get(mirror.AskRef(ship)); entity.Str(e.Payload, "status") != mirror.QuestionPending {
		t.Fatalf("ship was answered with words walter never saw: %v", entity.ToObj(e.Payload))
	}
}

// A YES RAISES THE NEXT ASK, and that one's yes casts the ballot: ivan's yes asks ann, due an hour
// from his yes, and ann's yes answers ship for walter.
func TestAYesRaisesTheNextAskAndItsYesAnswers(t *testing.T) {
	srv, ts := asking(t)
	ship := askID(t, ask(t, "user:alice@example.com", ts.URL, `{name: ship, question: "Ship it?", to: [walter@example.com]}`))
	posed(t, srv, mirror.AskRef(ship))
	id := askID(t, ask(t, thWalter, ts.URL, `{name: ui-ok, question: "UI ok?", to: [ivan@example.com],
		on: [{match: {verdict: proceed}, then: {ask: {name: deploy, question: "Deploy it?", to: [ann@example.com], due: 1h,
			on: [{match: {verdict: proceed}, then: {answer: {ask: "`+ship+`", choice: proceed}}}]}}}]}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := answerWith(t, thIvan, ts.URL, id, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ivan's yes: %s", resp.Status)
	}
	asked := entity.ToObj(atPath(askField(t, srv, id, "ran.asked"), "ran.asked"))
	deploy, _ := asked["id"].(string)
	if !strings.HasPrefix(deploy, "deploy-") {
		t.Fatalf("ran.asked %v, want the deploy ask's id", asked)
	}
	d, _, _ := srv.Store.Get(mirror.AskRef(deploy))
	if got := mirror.PrincipalFrom(d.Payload); got != thWalter {
		t.Fatalf("the deploy ask is %q's, want walter's", got)
	}
	if due, err := time.Parse(time.RFC3339, entity.Str(d.Payload, "due")); err != nil || time.Until(due) < 50*time.Minute {
		t.Fatalf("the deploy ask is due %q, want an hour from ivan's yes", entity.Str(d.Payload, "due"))
	}
	if rows := onRows(d.Payload); len(rows) != 1 || mirror.EditOf(atPath(rows[0], "then.answer")) != 1 {
		got := 0
		if len(rows) > 0 {
			got = mirror.EditOf(atPath(rows[0], "then.answer"))
		}
		t.Fatalf("the nested answer consents to edit %d of ship, want 1, filled when walter asked", got)
	}
	posed(t, srv, mirror.AskRef(deploy))
	if resp := answerWith(t, thAnn, ts.URL, deploy, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ann's yes: %s: %s", resp.Status, bodyOf(t, resp))
	}
	askField(t, srv, deploy, "ran.answered")
	if q, _, _ := srv.Store.Get(mirror.AskRef(ship)); entity.Str(q.Payload, "answer") != "yes" || entity.Str(q.Payload, "by") != thWalter {
		t.Fatalf("ship %v, want answered yes by walter", entity.ToObj(q.Payload))
	}
}

// A NESTED ASK IS CHECKED WHERE THE FIRST IS ASKED, as far as the viff can say now.
func TestAThenThatAsksIsCheckedWhereItIsAsked(t *testing.T) {
	_, ts := asking(t)
	for then, want := range map[string]int{
		`{ask: {name: d, question: q, to: [ann]}}`: http.StatusBadRequest, // a bare word names nothing
		`{ask: {name: d, question: q, due: -1h}}`:  http.StatusBadRequest,
		`{ask: {name: d, question: q, serves: x}}`: http.StatusBadRequest,
		`{ask: {name: d, question: q, on: [{match: {verdict: proceed}, then: {answer: {ask: nothing, choice: proceed}}}]}}`: http.StatusNotFound,
	} {
		resp := ask(t, thWalter, ts.URL, `{name: ui-ok, question: q, to: [ivan@example.com], on: [{then: `+then+`}]}`)
		if resp.StatusCode != want {
			t.Errorf("%s: %s %s, want %d", then, resp.Status, bodyOf(t, resp), want)
		}
	}
}

func fmtAny(v any) string {
	s, _ := v.(string)
	return s
}

// A NO MEETS A ROW A YES DOES NOT: ann's no asks the lead instead, as walter; ann's yes on a
// second meets no row and asks nobody.
func TestANoMeetsItsOwnRow(t *testing.T) {
	srv, ts := asking(t)
	const escalate = `on: [{match: {verdict: !not proceed}, then: {ask: {name: ship-lead, question: "Ann said no: ship it?", to: [lead@example.com]}}}]`
	no := askID(t, ask(t, thWalter, ts.URL, `{name: ship, question: "Ship it?", to: [ann@example.com], `+escalate+`}`))
	yes := askID(t, ask(t, thWalter, ts.URL, `{name: ship2, question: "Ship it?", to: [ann@example.com], `+escalate+`}`))
	posed(t, srv, mirror.AskRef(no))
	posed(t, srv, mirror.AskRef(yes))
	if resp := answerWith(t, thAnn, ts.URL, no, "deny"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ann's no: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := answerWith(t, thAnn, ts.URL, yes, "proceed"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ann's yes: %s: %s", resp.Status, bodyOf(t, resp))
	}
	asked := entity.ToObj(atPath(askField(t, srv, no, "ran.asked"), "ran.asked"))
	lead, _ := asked["id"].(string)
	if !strings.HasPrefix(lead, "ship-lead-") {
		t.Fatalf("ran.asked %v, want the lead's ask", asked)
	}
	if e, _, _ := srv.Store.Get(mirror.AskRef(lead)); mirror.PrincipalFrom(e.Payload) != thWalter {
		t.Fatalf("the lead's ask is %q's, want walter's", mirror.PrincipalFrom(e.Payload))
	}
	askField(t, srv, yes, "answer")
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := srv.Store.Get(mirror.AskRef(yes)); atPath(e.Payload, "ran.asked") != nil {
		t.Fatalf("a yes ran the no's row: %v", entity.ToObj(e.Payload))
	}
}

// SILENCE THAT SETTLES ON DENY MEETS THE NO'S ROW: nobody answers by the date, which settles on
// deny unless the asker said otherwise, and the lead is asked.
func TestSilenceMeetsTheNosRow(t *testing.T) {
	srv, ts, clock := keeping(t, 0)
	due := clock.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	id := askID(t, ask(t, thWalter, ts.URL, `{name: ship, question: "Ship it?", to: [ann@example.com], due: "`+due+`",
		on: [{match: {verdict: !not proceed}, then: {ask: {name: ship-lead, question: "Nobody answered: ship it?", to: [lead@example.com]}}}]}`))
	posed(t, srv, mirror.AskRef(id))
	clock.Advance(time.Minute)
	asked := entity.ToObj(atPath(askField(t, srv, id, "ran.asked"), "ran.asked"))
	if lead, _ := asked["id"].(string); !strings.HasPrefix(lead, "ship-lead-") {
		t.Fatalf("ran.asked %v after silence, want the lead's ask", asked)
	}
}

// EVERY ROW IS CHECKED WHEN ASKED, and a refusal names the row; changing the rows asks again.
func TestEveryRowIsCheckedAndChangingThemAsksAgain(t *testing.T) {
	srv, ts := asking(t)
	for body, want := range map[string]int{
		`{name: x, question: q, to: [ann@example.com], on: [{match: {verdict: !not proceed}, then: {answer: {ask: nothing-here, choice: proceed}}}]}`: http.StatusNotFound,
		`{name: x, question: q, to: [ann@example.com], on: [{match: {verdict: !not proceed}, then: {shell: "rm -rf /"}}]}`:                            http.StatusBadRequest,
		`{name: x, question: q, to: [ann@example.com], on: [{match: {verdict: !not proceed}, then: {prompt: a, script: ""}}]}`:                        http.StatusBadRequest,
	} {
		resp := ask(t, thWalter, ts.URL, body)
		if msg := bodyOf(t, resp); resp.StatusCode != want || !strings.Contains(msg, "on[0]") {
			t.Errorf("%s: %s %q, want %d naming the row", body, resp.Status, msg, want)
		}
	}
	id := askID(t, ask(t, thWalter, ts.URL, `{name: ship, question: "Ship it?", to: [ann@example.com]}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := change(t, thWalter, ts.URL, id, `{on: [{match: {verdict: !not proceed}, then: {spawn: {prompt: "tell the channel it was declined", environment: {systems: {network: {mode: open}}}}}}]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("a new on: %s: %s", resp.Status, bodyOf(t, resp))
	}
	e, _, _ := srv.Store.Get(mirror.AskRef(id))
	rows := onRows(e.Payload)
	if got := mirror.EditOf(e.Payload); got != 2 || len(rows) != 1 || atPath(rows[0], "then.spawn.prompt") == nil {
		t.Fatalf("after a new on the ask is at edit %d with on %v, want edit 2 and the new row", got, entity.Get(e.Payload, "on"))
	}
}
