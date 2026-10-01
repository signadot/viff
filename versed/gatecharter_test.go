package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// THE GATE CHARTER: one message per person a question waits on, written by a rule rather than by
// somebody watching the docket.
//
// It is a charter and not code, which is the point of the whole path: the engine already selects
// records, fans out over a list and derives, and a question is a record. What was missing was
// everything under it — `waitingOn` to say whom, `pose` and `about` to say what, and an out half
// whose inputs can be addressed by a fan-out rather than by hand.
//
// The rule below is the shipped one (deploy/charters/gate-ask.tony), tested here against a real
// engine, a real store and a real question.

// gateCharter is the SHIPPED rule, read from where a deployment installs it, with its place
// swapped for one this test owns. Read rather than copied: a test carrying its own copy proves
// that copy works, and the file somebody deploys drifts from it unnoticed.
func gateCharter(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../deploy/charters/verse/workspace/Signadot/gate-ask.tony")
	if err != nil {
		t.Fatalf("reading the shipped charter: %v", err)
	}
	return string(b)
}

// gateOutbox is where the shipped gate charter asks, inside its own package.
var gateOutbox = entity.RefAt("verse-dev", "gate-ask", "out", "slack")

// withSlackIO registers an io reading slack.Signadot.out, which is what the shipped charters'
// outboxes send to: a package declaring one is refused unless an io reads the place it names.
func withSlackIO(t *testing.T, srv *Server) {
	t.Helper()
	entitytest.Set(t, srv.Store, verseio.Ref("slack"), entity.Obj{
		"name": "slack", "root": "slack",
		"out": entity.Obj{"places": []any{"slack.Signadot.out"}, "as": "agent:slack-io"},
	}.Payload())
}

func installCharter(t *testing.T, ts *httptest.Server, at, doc string) {
	t.Helper()
	url := ts.URL + "/charter"
	if at != "" {
		url += "?at=" + at
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/tony")
	resp, err := sendAsTester(req)
	if err != nil {
		t.Fatalf("installing the charter: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		t.Fatalf("installing the charter: %s: %s", resp.Status, readBody(t, resp))
	}
}

// A question posed to two people becomes two messages, one addressed to each, each naming the
// question it is about — and nobody wrote either of them.
func TestTheGateCharterAsksEachPersonAQuestionWaitsOn(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context()) // the engine, which is what runs the rule
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withSlackIO(t, srv)
	installCharter(t, ts, "", gateCharter(t))

	const id = "verse.trigger.review.review~1"
	go func() {
		_, _ = srv.Reg.Resolve(context.Background(),
			task.GateQuestion{
				ID: id, Origin: "review", Gate: "review", Mode: task.Ask,
				Subject:   `demo.pr."1"`,
				Answerers: []string{"user:ana@example.com", "user:bo@example.com"},
			},
			task.ToolUse{Name: "verse.trigger.review"})
	}()

	var asked map[string]entity.Obj
	deadline := time.After(10 * time.Second)
	for {
		asked = asksIn(t, srv)
		if len(asked) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the charter asked %v, want one message each for ana and bo", asked)
		case <-time.After(10 * time.Millisecond):
		}
	}

	for _, who := range []string{"user:ana@example.com", "user:bo@example.com"} {
		msg, ok := asked[who]
		if !ok {
			t.Fatalf("nothing was asked of %s: %v", who, asked)
		}
		if msg["asks"] != id {
			t.Errorf("the message to %s asks %v, want the question it is about", who, msg["asks"])
		}
		// And its one line NAMES what reached them, resolved rather than left as the reference
		// that asked for it: a deref that silently fails would show somebody `$subject.value…`.
		text, _ := msg["text"].(string)
		if !strings.Contains(text, "review") || strings.Contains(text, "$") {
			t.Errorf("the message to %s says %q, want the rule that reached them, resolved", who, text)
		}
	}
}

// And when the question settles, the messages stand: what they SAY is drawn from the record by
// whoever shows them, so a settle redraws rather than deletes, and the charter has nothing to do.
func TestTheGateCharterLeavesSettledQuestionsAlone(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context()) // the engine, which is what runs the rule
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withSlackIO(t, srv)
	installCharter(t, ts, "", gateCharter(t))

	const id = "verse.trigger.review.review~2"
	go func() {
		_, _ = srv.Reg.Resolve(context.Background(),
			task.GateQuestion{
				ID: id, Origin: "review", Gate: "review", Mode: task.Ask,
				Subject: `demo.pr."2"`, Answerers: []string{"user:ana@example.com"},
			},
			task.ToolUse{Name: "verse.trigger.review"})
	}()
	waitPending(t, srv, id)

	var before int
	deadline := time.After(10 * time.Second)
	for before == 0 {
		select {
		case <-deadline:
			t.Fatal("the charter asked nobody")
		case <-time.After(10 * time.Millisecond):
		}
		before = len(asksIn(t, srv))
	}

	// Answered: the question's `waitingOn` goes, so the rule selects it no longer.
	mirror.AnswerQuestion(srv.Store, id, task.Choose("proceed", "user:ana@example.com"))
	time.Sleep(200 * time.Millisecond)
	if after := len(asksIn(t, srv)); after != before {
		t.Errorf("%d messages before the answer and %d after: a settle is a redraw, not a deletion",
			before, after)
	}
}

// asksIn is every message the charter has derived, by whom it is for. It walks what stands under
// the place rather than naming a depth, because where a fan-out lands one is the fan-out's to
// decide — which is the whole reason an input says who it is for rather than standing somewhere
// that says it.
func asksIn(t *testing.T, srv *Server) map[string]entity.Obj {
	t.Helper()
	out := map[string]entity.Obj{}
	e, ok, err := srv.Store.Get(gateOutbox)
	if err != nil || !ok {
		return out
	}
	var walk func(n entity.Payload)
	walk = func(n entity.Payload) {
		o := entity.ToObj(n)
		if to, _ := o["to"].(string); to != "" {
			out[to] = o
			return
		}
		for k := range o {
			if child := entity.Get(n, k); child != nil {
				walk(child)
			}
		}
	}
	walk(e.Payload)
	return out
}

// A question that named nobody waits on whoever the docket admits — `waitingOn: [user:*]`, which
// is every person and so nobody to send a message to. The rule leaves it alone: it is answered
// from the docket, and a message addressed to a wildcard would be a status full of refusals.
func TestTheGateCharterLeavesDocketQuestionsToTheDocket(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	withSlackIO(t, srv)
	installCharter(t, ts, "", gateCharter(t))

	const id = "verse.trigger.review.review~3"
	go func() {
		_, _ = srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: id, Origin: "review", Gate: "review", Mode: task.Ask,
				Subject: `demo.pr."3"`}, // nobody named
			task.ToolUse{Name: "verse.trigger.review"})
	}()
	waitPending(t, srv, id)
	time.Sleep(500 * time.Millisecond)
	if asked := asksIn(t, srv); len(asked) != 0 {
		t.Errorf("the charter asked %v about a question the docket holds", asked)
	}
}
