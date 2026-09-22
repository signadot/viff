package slack_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/source/slack"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/versed"
)

// fakeSocket is Slack's side of Socket Mode: it says hello on each connection, pushes the
// envelopes a test sends, and hears the acknowledgements.
type fakeSocket struct {
	send chan map[string]any
	acks chan string

	mu    sync.Mutex
	conns int
}

func (s *fakeSocket) handle(ws *websocket.Conn) {
	s.mu.Lock()
	s.conns++
	s.mu.Unlock()
	if websocket.JSON.Send(ws, map[string]any{"type": "hello"}) != nil {
		return
	}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			var ack map[string]string
			if websocket.JSON.Receive(ws, &ack) != nil {
				return
			}
			s.acks <- ack["envelope_id"]
		}
	}()
	for {
		select {
		case env := <-s.send:
			if websocket.JSON.Send(ws, env) != nil {
				return
			}
		case <-closed:
			return
		}
	}
}

func (s *fakeSocket) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

// socketRig is a real verse holding a pending question, a fake Slack with its socket, the out half
// to post the buttons and the socket to hear them pressed, and the response_url the replies go to.
type socketRig struct {
	*outRig
	srv     *versed.Server
	sock    *fakeSocket
	replies chan map[string]any
	reply   string // the response_url
}

const clickedQuestion = "verse.trigger.review.review~1"

func newSocketRig(t *testing.T) *socketRig {
	t.Helper()
	store := entitytest.Store(t)
	srv := versed.NewWithStore(versed.ScriptedBuilder, store)
	vs := httptest.NewServer(srv.Handler())
	t.Cleanup(vs.Close)
	addr := strings.TrimPrefix(vs.URL, "http://")

	sock := &fakeSocket{send: make(chan map[string]any, 8), acks: make(chan string, 8)}
	ss := httptest.NewServer(websocket.Handler(sock.handle))
	t.Cleanup(ss.Close)

	r := &socketRig{srv: srv, sock: sock, replies: make(chan map[string]any, 8)}
	rs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.replies <- body
	}))
	t.Cleanup(rs.Close)
	r.reply = rs.URL

	f := newFake()
	f.socketURL = "ws" + strings.TrimPrefix(ss.URL, "http") + "/socket"
	f.users = []map[string]any{
		{"id": "U7", "name": "scott", "profile": map[string]any{"email": "scott@example.com"}},
		{"id": "U8", "name": "ani", "profile": map[string]any{"email": "ani@example.com"}},
		{"id": "U9", "name": "nobody", "profile": map[string]any{}},
	}
	src := newSource(t, f, slack.Config{})
	r.outRig = &outRig{store: store, fake: f, out: src.Out(client.New(addr, "agent:slack-io"))}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); src.Socket(appToken, addr).Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

// pending raises a gate the way a firing does and waits until its question stands.
func (r *socketRig) pending(t *testing.T) {
	t.Helper()
	go func() {
		_, _ = r.srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: clickedQuestion, Origin: "review", Gate: "review", Mode: task.Ask},
			task.ToolUse{Name: "verse.trigger.review"})
	}()
	for range 400 {
		if _, ok, _ := r.store.Get(mirror.QuestionRef(clickedQuestion)); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("question %s never stood", clickedQuestion)
}

// buttons posts an input with an Approve and a Deny button on the question, and answers the value
// each posted button carries, so a click below presses what the out half actually said.
func (r *socketRig) buttons(t *testing.T) map[string]string {
	t.Helper()
	r.ask(t, "U7", "review", entity.Obj{
		"text": "the review is waiting on you",
		"buttons": []any{
			map[string]any{"label": "Approve", "answer": map[string]any{"question": clickedQuestion, "option": "proceed"}},
			map[string]any{"label": "Deny", "answer": map[string]any{"question": clickedQuestion, "option": "deny"}},
		},
	})
	r.pass(t)
	if len(r.fake.posted) != 1 {
		t.Fatalf("posted %v, want the one message", r.fake.posted)
	}
	values := map[string]string{}
	for _, b := range actionsOf(t, r.fake.posted[0]["blocks"]) {
		values[b["text"].(map[string]any)["text"].(string)] = b["value"].(string)
	}
	return values
}

// click sends a block_actions envelope as user pressing a button whose value is value.
func (r *socketRig) click(t *testing.T, envelope, user, value string) {
	t.Helper()
	r.sock.send <- map[string]any{
		"envelope_id": envelope, "type": "interactive",
		"payload": map[string]any{
			"type": "block_actions", "user": map[string]any{"id": user}, "response_url": r.reply,
			"actions": []any{map[string]any{"action_id": slack.AnswerAction + "0", "value": value}},
		},
	}
	select {
	case got := <-r.sock.acks:
		if got != envelope {
			t.Fatalf("acknowledged %q, want %q", got, envelope)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("envelope %s was never acknowledged", envelope)
	}
}

func (r *socketRig) replied(t *testing.T) map[string]any {
	t.Helper()
	select {
	case body := <-r.replies:
		return body
	case <-time.After(5 * time.Second):
		t.Fatal("the click was never replied to")
	}
	return nil
}

func (r *socketRig) question(t *testing.T) entity.Obj {
	t.Helper()
	e, ok, err := r.store.Get(mirror.QuestionRef(clickedQuestion))
	if err != nil || !ok {
		t.Fatalf("reading the question: ok %v, %v", ok, err)
	}
	return entity.ToObj(e.Payload)
}

// actionsOf is the buttons of a posted message's actions block.
func actionsOf(t *testing.T, blocks any) []map[string]any {
	t.Helper()
	list, _ := blocks.([]any)
	for _, b := range list {
		if bm, _ := b.(map[string]any); bm["type"] == "actions" {
			var out []map[string]any
			for _, e := range bm["elements"].([]any) {
				out = append(out, e.(map[string]any))
			}
			return out
		}
	}
	t.Fatalf("no actions block in %v", blocks)
	return nil
}

// A press of a posted button answers its question AS THE PERSON WHO PRESSED IT, and tells them so,
// visibly to them alone.
func TestAClickAnswersTheQuestionAsTheClicker(t *testing.T) {
	r := newSocketRig(t)
	r.pending(t)
	values := r.buttons(t)

	r.click(t, "e1", "U7", values["Approve"])
	reply := r.replied(t)
	if reply["response_type"] != "ephemeral" || reply["replace_original"] != false {
		t.Errorf("reply %v, want an ephemeral reply beside the message", reply)
	}
	if text, _ := reply["text"].(string); !strings.HasPrefix(text, "Answered") {
		t.Errorf("replied %q, want it answered", text)
	}
	q := r.question(t)
	if q["by"] != "user:scott@example.com" {
		t.Errorf("answered by %v, want the person who clicked and never agent:slack-io", q["by"])
	}
}

// The second person to press hears versed's refusal in versed's words, and the first answer stands.
func TestASecondClickHearsVersedsRefusal(t *testing.T) {
	r := newSocketRig(t)
	r.pending(t)
	values := r.buttons(t)

	r.click(t, "e1", "U7", values["Approve"])
	r.replied(t)
	r.click(t, "e2", "U8", values["Deny"])
	text, _ := r.replied(t)["text"].(string)
	if !strings.HasPrefix(text, "Not answered: ") || !strings.Contains(text, "already") {
		t.Errorf("replied %q, want versed's refusal", text)
	}
	if q := r.question(t); q["by"] != "user:scott@example.com" {
		t.Errorf("answered by %v, want the first answer to stand", q["by"])
	}
}

// A person whose email this app may not read is nobody verse knows, so nothing is answered.
func TestAClickerWithNoEmailAnswersNothing(t *testing.T) {
	r := newSocketRig(t)
	r.pending(t)
	values := r.buttons(t)

	r.click(t, "e1", "U9", values["Approve"])
	if text, _ := r.replied(t)["text"].(string); !strings.Contains(text, "no email") {
		t.Errorf("replied %q, want the missing email named", text)
	}
	if q := r.question(t); q["status"] != mirror.QuestionPending {
		t.Errorf("question %v, want it still pending", q)
	}
}

// A button that does not say what it answers is refused, whoever presses it.
func TestAButtonWithNoAnswerIsRefused(t *testing.T) {
	r := newSocketRig(t)
	r.click(t, "e1", "U7", "not an answer")
	if text, _ := r.replied(t)["text"].(string); !strings.Contains(text, "does not say which question") {
		t.Errorf("replied %q", text)
	}
}

// Slack's disconnect is a request to dial again, and the socket does, at once.
func TestTheSocketDialsAgainOnDisconnect(t *testing.T) {
	r := newSocketRig(t)
	r.sock.send <- map[string]any{"type": "disconnect", "reason": "refresh_requested"}
	for range 400 {
		if r.sock.connections() >= 2 {
			if n := r.fake.called("apps.connections.open"); n < 2 {
				t.Errorf("connected again with %d urls asked for, want a fresh one", n)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%d connections, want a second after the disconnect", r.sock.connections())
}
