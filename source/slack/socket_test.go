package slack_test

import (
	"context"
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
// to ask it in a channel, and the socket to hear the answer typed in that message's thread.
type socketRig struct {
	*outRig
	srv    *versed.Server
	sock   *fakeSocket
	kicked chan struct{}
	hints  chan []entity.Ref
	ts     string // the message asking the question, which a reply threads under
}

const askedQuestion = "verse.trigger.review.review~1"

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

	r := &socketRig{srv: srv, sock: sock, kicked: make(chan struct{}, 8), hints: make(chan []entity.Ref, 8)}
	f := newFake()
	f.socketURL = "ws" + strings.TrimPrefix(ss.URL, "http") + "/socket"
	f.users = []map[string]any{
		{"id": "U7", "name": "scott", "profile": map[string]any{"email": "scott@example.com"}},
		{"id": "U8", "name": "ani", "profile": map[string]any{"email": "ani@example.com"}},
		{"id": "U9", "name": "nobody", "profile": map[string]any{}},
	}
	src := newSource(t, f, slack.Config{})
	out := src.Out(client.New(addr, "agent:slack-io"))
	r.outRig = &outRig{store: store, fake: f, out: out}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s := src.Socket(appToken, addr)
	s.Asking = out.Asked
	s.Answered = func() { r.kicked <- struct{}{} }
	s.Hint = func(_ context.Context, refs ...entity.Ref) { r.hints <- refs }
	go func() { defer close(done); s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

// asking raises a gate the way a firing does, waits until its question stands, and asks it in a
// channel — so a test replies to the message the out half actually posted.
func (r *socketRig) asking(t *testing.T) {
	t.Helper()
	go func() {
		_, _ = r.srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: askedQuestion, Origin: "review", Gate: "review", Mode: task.Ask},
			task.ToolUse{Name: "verse.trigger.review"})
	}()
	stood := false
	for range 400 {
		if _, ok, _ := r.store.Get(mirror.QuestionRef(askedQuestion)); ok {
			stood = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !stood {
		t.Fatalf("question %s never stood", askedQuestion)
	}
	r.ask(t, "C999", "review", entity.Obj{
		"text": "the review is waiting on you", "asks": askedQuestion,
	})
	r.pass(t)
	if len(r.fake.posted) != 1 {
		t.Fatalf("posted %v, want the one message", r.fake.posted)
	}
	r.ts, _ = r.fake.posted[0]["ts"].(string)
	if !strings.Contains(shown(t, r.fake.posted[0]), "Answer in this thread") {
		t.Fatalf("the message does not say how to answer: %q", shown(t, r.fake.posted[0]))
	}
}

// reply says text in the thread of the message asking the question, as user.
func (r *socketRig) reply(t *testing.T, envelope, user, text string) {
	t.Helper()
	r.event(t, envelope, map[string]any{
		"type": "message", "channel": "C999", "user": user, "text": text,
		"ts": "1790000099.000100", "thread_ts": r.ts,
	})
}

// event sends one Events API envelope and waits for the socket to acknowledge it.
func (r *socketRig) event(t *testing.T, envelope string, ev map[string]any) {
	t.Helper()
	r.sock.send <- map[string]any{"envelope_id": envelope, "type": "events_api",
		"payload": map[string]any{"type": "event_callback", "event": ev}}
	select {
	case got := <-r.sock.acks:
		if got != envelope {
			t.Fatalf("acknowledged %q, want %q", got, envelope)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("envelope %s was never acknowledged", envelope)
	}
}

// heard waits for what the socket says back: in the thread for everyone, or to one person alone.
func (r *socketRig) heard(t *testing.T, to string) string {
	t.Helper()
	for range 400 {
		if got := r.fake.saidTo(to, r.ts); got != "" {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("nothing was said to %q", to)
	return ""
}

func (r *socketRig) question(t *testing.T) entity.Obj {
	t.Helper()
	e, ok, err := r.store.Get(mirror.QuestionRef(askedQuestion))
	if err != nil || !ok {
		t.Fatalf("reading the question: ok %v, %v", ok, err)
	}
	return entity.ToObj(e.Payload)
}

// A reply in the thread saying one of the question's options answers it, AS THE PERSON WHO TYPED
// IT, and the thread is told what was decided.
func TestAReplyAnswersTheQuestionAsWhoeverTypedIt(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.reply(t, "e1", "U7", "proceed")
	if said := r.heard(t, "thread"); !strings.Contains(said, "Answered") || !strings.Contains(said, "U7") {
		t.Errorf("said %q in the thread, want the answer and who gave it", said)
	}
	if q := r.question(t); q["by"] != "user:scott@example.com" || q["verdict"] != "proceed" {
		t.Errorf("answered %v by %v, want proceed by the person who typed it", q["verdict"], q["by"])
	}

	// And the message itself is redrawn, asked for at once rather than at the next interval.
	select {
	case <-r.kicked:
	default:
		t.Fatal("an answered reply did not ask for the message to be redrawn")
	}
	r.pass(t)
	if len(r.fake.updates) != 1 {
		t.Fatalf("updated %d times, want the message redrawn", len(r.fake.updates))
	}
	redrawn := shown(t, r.fake.updates[0])
	if strings.Contains(redrawn, "Answer in this thread") || !strings.Contains(redrawn, "answered *proceed*") {
		t.Errorf("redrawn as %q: want the verdict, and no way left to answer", redrawn)
	}
}

// Answering the bot by name is answering: a mention in the thread arrives as its own event, and
// the mention itself is not part of what was said.
func TestAMentionInTheThreadAnswersToo(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.event(t, "e1", map[string]any{"type": "app_mention", "channel": "C999", "user": "U7",
		"text": "<@U0BOT> proceed", "ts": "1790000099.000100", "thread_ts": r.ts})
	r.heard(t, "thread")
	if q := r.question(t); q["verdict"] != "proceed" {
		t.Errorf("answered %v, want proceed", q["verdict"])
	}
}

// Addressed by name, a sentence is read as prose — which is what somebody writing to a program by
// name means it to be. The answer is still one of the ids on the record.
func TestAddressedByNameASentenceIsAnAnswer(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.event(t, "e1", map[string]any{"type": "app_mention", "channel": "C999", "user": "U7",
		"text": "<@U0BOT> approve it pls", "ts": "1790000099.000100", "thread_ts": r.ts})
	r.heard(t, "thread")
	if q := r.question(t); q["verdict"] != "proceed" {
		t.Errorf("answered %v, want proceed", q["verdict"])
	}
}

// The same sentence in the thread at large is talk: people discuss under these messages, and no
// reading of prose should let a conversation answer a gate by accident.
func TestTheSameSentenceInTheThreadIsTalk(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.reply(t, "e1", "U7", "approve it pls")
	r.waitForSay(t, "approve it pls")
	if q := r.question(t); q["status"] != mirror.QuestionPending {
		t.Errorf("question %v, want it still pending", q)
	}
}

// A negation is a sentence this program has no business resolving: "do not ship it" and "ship it"
// differ by one word, and reading them the same answers gates nobody answered.
func TestANegationIsNotResolved(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.event(t, "e1", map[string]any{"type": "app_mention", "channel": "C999", "user": "U7",
		"text": "<@U0BOT> do not approve this yet", "ts": "1790000099.000100", "thread_ts": r.ts})
	if said := r.heard(t, "U7"); !strings.Contains(said, "yes or a no") {
		t.Errorf("said %q, want it to say it cannot tell", said)
	}
	if q := r.question(t); q["status"] != mirror.QuestionPending {
		t.Errorf("question %v, want it still pending", q)
	}
}

// `y` and `n` are the two options a gate has unless it says otherwise.
func TestAReplySayingYAnswersProceed(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.reply(t, "e1", "U7", "y")
	r.heard(t, "thread")
	if q := r.question(t); q["verdict"] != "proceed" {
		t.Errorf("answered %v, want proceed", q["verdict"])
	}
}

// The second person to answer hears verse's refusal, and hears it alone: it is about them, not
// about the question, which the thread already says was decided.
func TestASecondReplyHearsVersesRefusalAlone(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.reply(t, "e1", "U7", "proceed")
	r.heard(t, "thread")
	<-r.kicked
	r.reply(t, "e2", "U8", "deny")
	if said := r.heard(t, "U8"); !strings.HasPrefix(said, "Not answered: ") || !strings.Contains(said, "already") {
		t.Errorf("said %q to the second answerer, want verse's refusal", said)
	}
	if q := r.question(t); q["by"] != "user:scott@example.com" {
		t.Errorf("answered by %v, want the first answer to stand", q["by"])
	}
}

// A person whose email this app may not read is nobody verse knows, so nothing is answered.
func TestAReplyFromSomebodyWithNoEmailAnswersNothing(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.reply(t, "e1", "U9", "proceed")
	if said := r.heard(t, "U9"); !strings.Contains(said, "no email") {
		t.Errorf("said %q, want the missing email named", said)
	}
	if q := r.question(t); q["status"] != mirror.QuestionPending {
		t.Errorf("question %v, want it still pending", q)
	}
}

// A reply that says none of the options decides nothing and is not ignored either: it is talk
// about the question, so it lands ON the question, attributed. Nothing is said back at them for
// talking.
func TestAReplyThatIsNotAnAnswerIsSaidOnTheQuestion(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.reply(t, "e1", "U7", "hold on, checking with ops")
	said := r.waitForSay(t, "hold on, checking with ops")
	if said["by"] != "user:scott@example.com" {
		t.Errorf("said by %v, want the person who typed it", said["by"])
	}
	if got := r.fake.saidTo("thread", r.ts) + r.fake.saidTo("U7", r.ts); got != "" {
		t.Errorf("said %q to somebody talking", got)
	}
	if q := r.question(t); q["status"] != mirror.QuestionPending {
		t.Errorf("question %v, want it still pending", q)
	}
}

// waitForSay waits for text to land on the question as a say, and answers that entry.
func (r *socketRig) waitForSay(t *testing.T, text string) entity.Obj {
	t.Helper()
	for range 400 {
		for _, v := range entity.ToObj(entity.Get(r.question(t).Payload(), mirror.SaidField)) {
			if said, _ := v.(map[string]any); said != nil && said["text"] == text {
				return entity.Obj(said)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%q was never said on the question", text)
	return nil
}

// Addressed to the bot and not an answer: it says so, to the person who said it, naming what the
// question takes. A reply to the thread at large stays unanswered, which is the test above.
func TestTheBotSaysSoWhenItDoesNotUnderstand(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.event(t, "e1", map[string]any{"type": "app_mention", "channel": "C999", "user": "U7",
		"text": "<@U0BOT> what does this involve?", "ts": "1790000099.000100", "thread_ts": r.ts})
	said := r.heard(t, "U7")
	if !strings.Contains(said, "did not get that") || !strings.Contains(said, "proceed") {
		t.Errorf("said %q, want it to say so and name what the question takes", said)
	}
	if !strings.Contains(said, "noted") {
		t.Errorf("said %q, want it to say the words were kept", said)
	}
	if got := r.waitForSay(t, "what does this involve?"); got["by"] != "user:scott@example.com" {
		t.Errorf("said by %v, want the person who typed it", got["by"])
	}
	if q := r.question(t); q["status"] != mirror.QuestionPending {
		t.Errorf("question %v, want it still pending", q)
	}
}

// A reply under a message this program did not post answers nothing.
func TestAReplyInAnotherThreadIsNotAnAnswer(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.event(t, "e1", map[string]any{"type": "message", "channel": "C999", "user": "U7",
		"text": "proceed", "ts": "1790000099.000100", "thread_ts": "1780000000.000001"})
	time.Sleep(100 * time.Millisecond)
	if q := r.question(t); q["status"] != mirror.QuestionPending {
		t.Errorf("question %v, want it still pending", q)
	}
}

// An Events API delivery down the socket is the hint the request url would have sent: the channel
// it names, or a full resync when it names none.
func TestAnEventDownTheSocketIsAHint(t *testing.T) {
	r := newSocketRig(t)
	for _, c := range []struct {
		event map[string]any
		want  string // the hinted ref, "" for a full resync
	}{
		{map[string]any{"type": "message", "channel": "C123", "ts": "1.1", "text": "hi"}, "C123"},
		{map[string]any{"type": "user_change", "user": map[string]any{"id": "U7"}}, ""},
	} {
		r.event(t, "ev", c.event)
		select {
		case refs := <-r.hints:
			switch {
			case c.want == "" && len(refs) != 0:
				t.Errorf("%v hinted %v, want a full resync", c.event["type"], refs)
			case c.want != "" && (len(refs) != 1 || !strings.Contains(refs[0].String(), c.want)):
				t.Errorf("%v hinted %v, want the channel %s", c.event["type"], refs, c.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%v was never hinted", c.event["type"])
		}
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
