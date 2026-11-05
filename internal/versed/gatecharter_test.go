package versed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	verseio "github.com/signadot/verse/pkg/io"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// THE GATE CHARTER: one message per person a question waits on, written by a rule rather than by
// somebody watching the docket.
//
// It is a charter and not code, which is the point of the whole path: the engine already selects
// records, fans out over a list and derives, and a question is a record. What was missing was
// everything under it — `waitingOn` to say whom, `pose` and `about` to say what, and an out half
// whose inputs can be addressed by a fan-out rather than by hand.
//
// The rule below is the example a deployment copies (docs/reference/examples/gate-ask), tested
// here against a real engine, a real store and a real question.

// gateCharter is the EXAMPLE rule, read from where a deployment copies it, with its place swapped
// for one this test owns. Read rather than copied: a test carrying its own copy proves that copy
// works, and the file somebody copies drifts from it unnoticed.
func gateCharter(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../docs/reference/examples/gate-ask/gate-ask.tony")
	if err != nil {
		t.Fatalf("reading the example charter: %v", err)
	}
	return strings.ReplaceAll(string(b), "slack.example.out", "demo.out")
}

// withSlackIO registers an io reading slack.example.out, which is what the example packages'
// outboxes send to: a package declaring one is refused unless an io reads the place it names.
func withSlackIO(t *testing.T, srv *Server) {
	t.Helper()
	entitytest.Set(t, srv.Store, verseio.Ref("slack"), entity.Obj{
		"name": "slack", "root": "slack",
		"out": entity.Obj{"places": []any{"slack.example.out"}, "as": "agent:slack-iod"},
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
	installCharter(t, ts, "", gateCharter(t))

	const id = "verse.trigger.review.review~1"
	go func() {
		_, _ = srv.Reg.Resolve(context.Background(),
			task.GateQuestion{
				ID: id, Origin: "review", Gate: "review",
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
		// that asked for it: a deref that silently fails would show somebody `$[subject.value…]`.
		text, _ := msg["text"].(string)
		if !strings.Contains(text, "review") || strings.Contains(text, "$") {
			t.Errorf("the message to %s says %q, want the rule that reached them, resolved", who, text)
		}
	}
}

// A PERSON'S ASK REACHES WHOM IT ASKS, through the charter the deployment installs as the system:
// a rule acts as its installer, and a person's ask is read by whom it asks, so only the system's
// rule sees them all. No rule raised it, so the line names who asked.
func TestTheGateCharterAsksWhomAPersonsAskNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.tony")
	if err := os.WriteFile(path, []byte(gateCharter(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t)
	srv.SystemCharter = path
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	id := askID(t, ask(t, "user:victor@example.com", ts.URL,
		`{name: land, question: "Merge verse/ask?", to: [ana@example.com]}`))
	var asked map[string]entity.Obj
	deadline := time.After(10 * time.Second)
	for len(asked) == 0 {
		select {
		case <-deadline:
			t.Fatal("the charter asked nobody about victor's ask")
		case <-time.After(10 * time.Millisecond):
		}
		asked = asksIn(t, srv)
	}
	msg, ok := asked["user:ana@example.com"]
	if !ok || len(asked) != 1 {
		t.Fatalf("the charter asked %v, want ana alone", asked)
	}
	// ONE message: the rules for a rule's question and a person's ask never both select one ask,
	// or she is told twice, once by a line naming no rule at all.
	time.Sleep(200 * time.Millisecond)
	if n := messagesIn(t, srv); n != 1 {
		t.Fatalf("%d messages about victor's ask, want one", n)
	}
	if msg["asks"] != id {
		t.Errorf("the message to ana asks %v, want victor's ask", msg["asks"])
	}
	if text, _ := msg["text"].(string); !strings.Contains(text, "user:victor@example.com") || strings.Contains(text, "$") {
		t.Errorf("the message to ana says %q, want who asked, resolved", text)
	}
}

// And when the question settles, the messages stand: what they SAY is drawn from the record by
// whoever shows them, so a settle redraws rather than deletes, and the charter has nothing to do.
func TestTheGateCharterLeavesSettledQuestionsAlone(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context()) // the engine, which is what runs the rule
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installCharter(t, ts, "", gateCharter(t))

	const id = "verse.trigger.review.review~2"
	go func() {
		_, _ = srv.Reg.Resolve(context.Background(),
			task.GateQuestion{
				ID: id, Origin: "review", Gate: "review",
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
	e, ok, err := srv.Store.Get(entity.RefAt("demo", "out"))
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

// AN ASK FOR A CHANNEL is one message in the channel its `slack` label names, and a DM to nobody:
// not to `user:*`, whom a gate asks when anyone may answer, and not to a person it names either.
// Two asks to one channel are two messages.
func TestTheGateCharterPutsALabelledAskToItsChannel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.tony")
	if err := os.WriteFile(path, []byte(gateCharter(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t)
	srv.SystemCharter = path
	srv.Start(t.Context())

	raise := func(id string, answerers ...string) {
		go func() {
			_, _ = srv.Reg.Resolve(context.Background(),
				task.GateQuestion{ID: id, Origin: "train-run", Gate: "merge-to-main",
					Answerers: answerers, Labels: map[string]string{"slack": "C123"}},
				task.ToolUse{Name: "verse.trigger.train-run"})
		}()
	}
	raise("verse.trigger.train-run.merge~1", "user:*")
	raise("verse.trigger.train-run.merge~2", "user:ana@example.com")

	want := map[string]bool{"verse.trigger.train-run.merge~1": true, "verse.trigger.train-run.merge~2": true}
	var got []entity.Obj
	deadline := time.After(10 * time.Second)
	for len(got) < 2 {
		select {
		case <-deadline:
			t.Fatalf("the charter put %v to the channel, want both asks", got)
		case <-time.After(20 * time.Millisecond):
		}
		got = messagesTo(t, srv)
	}
	time.Sleep(200 * time.Millisecond)
	got = messagesTo(t, srv)
	if len(got) != 2 {
		t.Fatalf("%d messages, want one per ask, to the channel alone: %v", len(got), got)
	}
	for _, m := range got {
		if m["to"] != "C123" || !want[fmt.Sprint(m["asks"])] {
			t.Errorf("a message %v, want each ask put to C123", m)
		}
		delete(want, fmt.Sprint(m["asks"]))
	}
}

// A PERSON'S ASK IS DM'D WHATEVER ITS LABELS: a channel's message is read as the io, and a person's
// ask is read only by its asker and whom it asks.
func TestAPersonsAskLabelledForAChannelIsStillDMd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.tony")
	if err := os.WriteFile(path, []byte(gateCharter(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t)
	srv.SystemCharter = path
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	id := askID(t, ask(t, "user:victor@example.com", ts.URL,
		`{name: land, question: "Merge?", to: [ana@example.com], labels: {slack: C123}}`))
	var got []entity.Obj
	for deadline := time.Now().Add(10 * time.Second); len(got) == 0; got = messagesTo(t, srv) {
		if time.Now().After(deadline) {
			t.Fatal("the charter said nothing about victor's ask")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if got = messagesTo(t, srv); len(got) != 1 || got[0]["to"] != "user:ana@example.com" || got[0]["asks"] != id {
		t.Fatalf("messages %v, want one to ana", got)
	}
}

// messagesTo is every message the charter has derived, each as written.
func messagesTo(t *testing.T, srv *Server) []entity.Obj {
	t.Helper()
	var out []entity.Obj
	e, ok, err := srv.Store.Get(entity.RefAt("demo", "out"))
	if err != nil || !ok {
		return nil
	}
	var walk func(p entity.Payload)
	walk = func(p entity.Payload) {
		o := entity.ToObj(p)
		if to, _ := o["to"].(string); to != "" {
			out = append(out, o)
			return
		}
		for k := range o {
			if child := entity.Get(p, k); child != nil {
				walk(child)
			}
		}
	}
	walk(e.Payload)
	return out
}

// messagesIn counts the messages the charter has derived, whoever each is for.
func messagesIn(t *testing.T, srv *Server) int {
	t.Helper()
	e, ok, err := srv.Store.Get(entity.RefAt("demo", "out"))
	if err != nil || !ok {
		return 0
	}
	n := 0
	var walk func(p entity.Payload)
	walk = func(p entity.Payload) {
		o := entity.ToObj(p)
		if to, _ := o["to"].(string); to != "" {
			n++
			return
		}
		for k := range o {
			if child := entity.Get(p, k); child != nil {
				walk(child)
			}
		}
	}
	walk(e.Payload)
	return n
}

// A question that named nobody waits on whoever the docket admits — `waitingOn: [user:*]`, which
// is every person and so nobody to send a message to. The rule leaves it alone: it is answered
// from the docket, and a message addressed to a wildcard would be a status full of refusals.
func TestTheGateCharterLeavesDocketQuestionsToTheDocket(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installCharter(t, ts, "", gateCharter(t))

	const id = "verse.trigger.review.review~3"
	go func() {
		_, _ = srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: id, Origin: "review", Gate: "review",
				Subject: `demo.pr."3"`}, // nobody named
			task.ToolUse{Name: "verse.trigger.review"})
	}()
	waitPending(t, srv, id)
	time.Sleep(500 * time.Millisecond)
	if asked := asksIn(t, srv); len(asked) != 0 {
		t.Errorf("the charter asked %v about a question the docket holds", asked)
	}
}

// systemGated is a verse running the shipped charter as the system, as a deployment installs it:
// only the system's rule reads every ask, a person's and an onward one included.
func systemGated(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "system.tony")
	if err := os.WriteFile(path, []byte(gateCharter(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t)
	srv.SystemCharter = path
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

// messagesAbout is every message the charter wrote about the question id, waiting until there is
// at least one.
func messagesAbout(t *testing.T, srv *Server, id string) []entity.Obj {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		var out []entity.Obj
		if e, ok, err := srv.Store.Get(entity.RefAt("demo", "out")); err == nil && ok {
			var walk func(p entity.Payload)
			walk = func(p entity.Payload) {
				o := entity.ToObj(p)
				if to, _ := o["to"].(string); to != "" {
					if o["asks"] == id {
						out = append(out, o)
					}
					return
				}
				for k := range o {
					if child := entity.Get(p, k); child != nil {
						walk(child)
					}
				}
			}
			walk(e.Payload)
		}
		if len(out) > 0 {
			return out
		}
		select {
		case <-deadline:
			t.Fatalf("the charter wrote nothing about %s", id)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A TASK'S QUESTION REACHES WHOM IT NAMES, under whatever id its runner chose: it is from the
// task, and the line names the task. One whose id verse did not mint was reached by no rule while
// who raised it was read off the id.
func TestTheGateCharterAsksWhomATasksQuestionNames(t *testing.T) {
	srv, _ := systemGated(t)
	const id, taskID = "runner-chosen-7", "run-42"
	mirror.PostQuestion(srv.Store, task.Pending{
		Question: task.GateQuestion{ID: id, Gate: "push-review", Answerers: []string{"user:ana@example.com"}},
		Tool:     "git.push",
	}, taskID)
	msgs := messagesAbout(t, srv, id)
	time.Sleep(200 * time.Millisecond)
	if msgs = messagesAbout(t, srv, id); len(msgs) != 1 || msgs[0]["to"] != "user:ana@example.com" {
		t.Fatalf("the charter wrote %v about the task's question, want one message to ana", msgs)
	}
	if text, _ := msgs[0]["text"].(string); !strings.Contains(text, taskID) || strings.Contains(text, "$") {
		t.Errorf("the message to ana says %q, want the task that asked, resolved", text)
	}
}

// AN ONWARD ASK SAYS WHOM IT ASKS FOR: the resolver raised it, for the party it serves, and the
// line says both. Once: an onward ask is from a principal as a person's ask is, and the rule for a
// person's ask leaves it to this one.
func TestTheGateCharterSaysWhomAnOnwardAskIsFor(t *testing.T) {
	srv, ts := systemGated(t)
	if resp := postAs(t, "user:alice@example.com", ts.URL+"/resolvers", "application/tony",
		`{name: helper, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if code, msg := grantStatus(t, "user:bob@example.com", ts.URL, "helper", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	served := askID(t, ask(t, "carol@example.com", ts.URL, `{name: land, question: "land it?", to: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(served))
	onward := askID(t, postAsResolver(t, "user:alice@example.com", "helper", ts.URL+"/asks",
		`{name: up, question: "should bob land it?", to: [mgr@example.com], serves: `+served+`}`))
	posed(t, srv, mirror.AskRef(onward))

	msgs := messagesAbout(t, srv, onward)
	time.Sleep(200 * time.Millisecond)
	if msgs = messagesAbout(t, srv, onward); len(msgs) != 1 || msgs[0]["to"] != "user:mgr@example.com" {
		t.Fatalf("the charter wrote %v about the onward ask, want one message to mgr", msgs)
	}
	text, _ := msgs[0]["text"].(string)
	if !strings.Contains(text, "via:verse.resolvers.helper") || !strings.Contains(text, "user:bob@example.com") ||
		strings.Contains(text, "$") {
		t.Errorf("the message to mgr says %q, want the resolver and whom it asks for, resolved", text)
	}
}
