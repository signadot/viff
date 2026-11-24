package slack_test

import (
	"context"
	"fmt"
	"go.viff.dev/viff/internal/viffd/viffdtest"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"go.viff.dev/viff/internal/viffd"
	"go.viff.dev/viff/pkg/client"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	viffio "go.viff.dev/viff/pkg/io"
	"go.viff.dev/viff/pkg/io/slack"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
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

// socketRig is a real viff holding a pending question, a fake Slack with its socket, the out half
// to ask it in a channel, and the socket to hear the answer typed in that message's thread.
type socketRig struct {
	*outRig
	srv    *viffd.Server
	sock   *fakeSocket
	kicked chan struct{}
	hints  chan []entity.Ref
	ts     string // the message asking the question, which a reply threads under
	src    *slack.Slack
	viff   *client.Client
	addr   string // the viff instance, for a test that asks it things as somebody
}

const askedQuestion = "mirror.trigger.review.review~1"

func newSocketRig(t *testing.T) *socketRig { return newSocketRigWith(t, nil) }

// newSocketRigWith is newSocketRig for an io that proves itself with bearer, as cmd/iod does with
// -token-file.
func newSocketRigWith(t *testing.T, bearer client.BearerFunc) *socketRig {
	t.Helper()
	store := entitytest.Store(t)
	srv := viffdtest.Server(t, runnerdtest.Scripted, store)
	vs := httptest.NewServer(srv.Handler())
	t.Cleanup(vs.Close)
	addr := strings.TrimPrefix(vs.URL, "http://")

	sock := &fakeSocket{send: make(chan map[string]any, 8), acks: make(chan string, 8)}
	ss := httptest.NewServer(websocket.Handler(sock.handle))
	t.Cleanup(ss.Close)

	r := &socketRig{srv: srv, sock: sock, kicked: make(chan struct{}, 8), hints: make(chan []entity.Ref, 8), addr: addr}
	f := newFake()
	f.socketURL = "ws" + strings.TrimPrefix(ss.URL, "http") + "/socket"
	f.users = []map[string]any{
		{"id": "U7", "name": "walter", "profile": map[string]any{"email": "walter@example.com"}},
		{"id": "U8", "name": "peggy", "profile": map[string]any{"email": "peggy@example.com"}},
		{"id": "U9", "name": "nobody", "profile": map[string]any{}},
	}
	src := newSource(t, f, slack.Config{})
	viff := client.NewAs(addr, "agent:slack-iod", bearer)
	sink := src.Sink(viff)
	sink.As = viffio.As(addr, bearer) // as cmd/iod wires it: an ask for somebody is read as them
	out := viffio.NewOut(sink, src.OutRef(), viff, nil)
	r.outRig = &outRig{store: store, fake: f, sink: sink, out: out}
	r.src, r.viff = src, viff

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s := src.Socket(appToken)
	s.Viff = viff // as cmd/iod wires it: `/viff link` is told to the viff instance as the io itself
	s.Asking = sink.Asked
	s.Answered = func() { r.kicked <- struct{}{} }
	s.Hint = func(_ context.Context, refs ...entity.Ref) { r.hints <- refs }
	go func() { defer close(done); _ = s.Control(ctx, viffio.As(addr, bearer)) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

// asking raises a gate the way a firing does, waits until its question stands, and asks it in a
// channel — so a test replies to the message the out half actually posted.
func (r *socketRig) asking(t *testing.T) {
	t.Helper()
	r.raise(t)
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

// raise raises the gate a firing would, and waits until its question stands.
func (r *socketRig) raise(t *testing.T) {
	t.Helper()
	go func() {
		_, _ = r.srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: askedQuestion, Origin: "review", Gate: "review"},
			task.ToolUse{Name: "mirror.trigger.review"})
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
}

// A QUESTION ASKED IN SOMEBODY'S THREAD is a reply there, and a reply in that thread answers it —
// "yes" included, the word everybody types.
func TestAQuestionAskedInAThreadIsAnsweredThere(t *testing.T) {
	r := newSocketRig(t)
	r.raise(t)
	const thread = "1790000050.000001" // a conversation somebody started, not one of the bot's
	r.ask(t, "C999", "review", entity.Obj{
		"text": "Confirm:", "asks": askedQuestion, "thread": thread,
	})
	r.pass(t)
	if len(r.fake.posted) != 0 || len(r.fake.inThread) != 1 || r.fake.inThread[0]["thread_ts"] != thread {
		t.Fatalf("posted %v at channel level and %v in threads, want one reply in %s", r.fake.posted, r.fake.inThread, thread)
	}
	if st := r.status(t, "review"); st["thread"] != thread {
		t.Errorf("the status does not say which thread it spoke in: %v", st)
	}
	r.ts = thread
	r.reply(t, "e1", "U7", "yes")
	select {
	case <-r.kicked:
	case <-time.After(5 * time.Second):
		t.Fatalf("yes in the thread answered nothing: %v", r.question(t))
	}
	if q := r.question(t); q["verdict"] != "proceed" || q["by"] != "user:walter@example.com" {
		t.Fatalf("yes in the thread answered %v by %v, want proceed by walter", q["verdict"], q["by"])
	}
}

// WHERE THE VIFF VERIFIES PEOPLE, a reply is admitted as the person through their Slack account,
// from this io presenting its own credential, and the ballot says which io it came through and who
// vouched for the person. The same reply from an io with no credential is refused, and the person
// is told.
func TestAReplyIsAdmittedAsThePersonWhereViffVerifiesPeople(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("the-slack-iods-own-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bearer := func(context.Context) (string, error) {
		b, err := os.ReadFile(file)
		return strings.TrimSpace(string(b)), err
	}
	verifying := viffd.Auth{Mode: viffd.AuthRequired, Providers: map[string]viffd.AuthProvider{
		// An issuer nobody reaches: `required` wants one, and nothing here logs in through it.
		"corp":  {Issuer: "https://idp.example.com", ClientID: "viff-cli"},
		"slack": {IOs: []string{"slack"}, Domains: []string{"example.com"}},
	}, Iod: viffd.AuthIod{Static: []viffd.IodStatic{{File: file, Principal: "agent:slack-iod", IO: "slack"}}}}

	r := newSocketRigWith(t, bearer)
	if err := r.srv.SetAuth(verifying); err != nil {
		t.Fatal(err)
	}
	r.asking(t)
	r.reply(t, "e1", "U7", "proceed")
	select {
	case <-r.kicked:
	case <-time.After(5 * time.Second):
		t.Fatalf("the reply answered nothing: %v", r.question(t))
	}
	q := r.question(t)
	if q["verdict"] != "proceed" || q["by"] != "user:walter@example.com" {
		t.Fatalf("answered %v by %v, want proceed by walter", q["verdict"], q["by"])
	}
	e, _, _ := r.store.Get(mirror.QuestionRef(askedQuestion))
	ballot := entity.Get(entity.Get(e.Payload, "ballots"), "user:walter@example.com")
	if entity.Str(ballot, "authenticated") != "slack" || entity.Str(ballot, "via") != "agent:slack-iod" {
		t.Errorf("walter's ballot = %v, want authenticated by slack via this io", ballot)
	}

	// The io with no credential of its own: the person is asserted, and a viff instance that verifies
	// people will not have it.
	bare := newSocketRig(t)
	if err := bare.srv.SetAuth(verifying); err != nil {
		t.Fatal(err)
	}
	bare.asking(t)
	bare.reply(t, "e2", "U7", "proceed")
	if said := bare.heard(t, "U7"); !strings.Contains(said, "Not answered") {
		t.Errorf("an asserted reply was not refused: %q", said)
	}
	if q := bare.question(t); q["verdict"] != nil {
		t.Errorf("an asserted reply answered: %v", q)
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
	if q := r.question(t); q["by"] != "user:walter@example.com" || q["verdict"] != "proceed" {
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

// The second person to answer hears viff's refusal, and hears it alone: it is about them, not
// about the question, which the thread already says was decided.
func TestASecondReplyHearsViffsRefusalAlone(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.reply(t, "e1", "U7", "proceed")
	r.heard(t, "thread")
	<-r.kicked
	r.reply(t, "e2", "U8", "deny")
	if said := r.heard(t, "U8"); !strings.HasPrefix(said, "Not answered: ") || !strings.Contains(said, "already") {
		t.Errorf("said %q to the second answerer, want viff's refusal", said)
	}
	if q := r.question(t); q["by"] != "user:walter@example.com" {
		t.Errorf("answered by %v, want the first answer to stand", q["by"])
	}
}

// A person whose email this app may not read is nobody viff knows, so nothing is answered.
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
	if said["by"] != "user:walter@example.com" {
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
	if got := r.waitForSay(t, "what does this involve?"); got["by"] != "user:walter@example.com" {
		t.Errorf("said by %v, want the person who typed it", got["by"])
	}
	if q := r.question(t); q["status"] != mirror.QuestionPending {
		t.Errorf("question %v, want it still pending", q)
	}
}

// A thread outlives its decision: what is said in it afterwards goes on the question, and somebody
// who addresses the bot is told what became of the thing they were answering.
func TestAThreadGoesOnAfterItsQuestionSettles(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	r.reply(t, "e1", "U7", "proceed")
	r.heard(t, "thread")
	<-r.kicked
	r.pass(t) // the message is redrawn, and the question is settled in the index

	r.event(t, "e2", map[string]any{"type": "app_mention", "channel": "C999", "user": "U8",
		"text": "<@U0BOT> approve", "ts": "1790000099.000200", "thread_ts": r.ts})
	if said := r.heard(t, "U8"); !strings.Contains(said, "settled") {
		t.Errorf("said %q, want it to say the question is settled", said)
	}
	if got := r.waitForSay(t, "approve"); got["by"] != "user:peggy@example.com" {
		t.Errorf("said by %v, want the person who typed it", got["by"])
	}
}

// One message, two events: Slack sends `message` and `app_mention` for the same words. Handling
// both would record what somebody said twice, or record it as talk while answering it.
func TestAMentionIsHandledOnceNotTwice(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)

	for i, kind := range []string{"message", "app_mention"} {
		r.event(t, fmt.Sprintf("e%d", i), map[string]any{"type": kind, "channel": "C999",
			"user": "U7", "text": "<@U0> proceed", "ts": "1790000099.000100", "thread_ts": r.ts})
	}
	r.heard(t, "thread")
	if q := r.question(t); q["verdict"] != "proceed" {
		t.Fatalf("answered %v, want proceed", q["verdict"])
	}
	// The plain copy said nothing on the question, since the mention answered with those words.
	said, _ := r.question(t)[mirror.SaidField].(map[string]any)
	if len(said) != 0 {
		t.Errorf("said %v, want the words answered rather than also recorded as talk", said)
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

// A MESSAGE WHOSE EDIT WAS REFUSED FOR GOOD STILL SHOWS ITS OLD WORDS, and the input is not
// delivered again (git-issue na9fce9dh12krdydqsn0). What a reply in its thread answers is the edit
// Slack shows, so a sink that starts afresh — a restarted io — does not take the refused words as
// the message's (git-issue ny9fce9dh12ks1q9qsn0), and the edit is not sent again.
func TestARefusedEditIsNotRememberedAsShown(t *testing.T) {
	r := newSocketRig(t)
	r.asking(t)
	r.fake.fail["chat.update"] = "msg_too_long"
	r.ask(t, "C999", "review", entity.Obj{
		"text": "the review is waiting on you, and here is a great deal more", "asks": askedQuestion,
	})
	r.pass(t)
	if st := r.status(t, "review"); st["failed"] != viffio.PermanentFailure {
		t.Fatalf("status %v, want the edit refused for good", st)
	}

	fresh := r.src.Sink(r.viff)
	if got := fresh.Asked("C999", r.ts); len(got) != 0 {
		t.Fatalf("a fresh sink already knows %v", got)
	}
	if err := viffio.NewOut(fresh, r.src.OutRef(), r.viff, nil).Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fresh.Asked("C999", r.ts); len(got) != 0 {
		t.Fatalf("after a pass, the thread asks %v, want nothing: Slack does not show the refused edit", got)
	}
	if n := r.fake.called("chat.update"); n != 1 {
		t.Errorf("chat.update asked %d times, want once: the refused edit is not sent again", n)
	}
}

// `/viff link <nonce>` said to the app binds the Slack account to whoever the viff instance gave the nonce
// to, on this io's word; and where slack names no domains, that binding is the only way a reply is
// admitted as the person. The dogfood shape: people asserted (`optional`), bindings by the nonce.
func TestLinkSaidToTheAppBindsTheAccountAndAdmitsTheReply(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("the-slack-iods-own-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bearer := func(context.Context) (string, error) {
		b, err := os.ReadFile(file)
		return strings.TrimSpace(string(b)), err
	}
	r := newSocketRigWith(t, bearer)
	if err := r.srv.SetAuth(viffd.Auth{Mode: viffd.AuthOptional, Providers: map[string]viffd.AuthProvider{
		"slack": {IOs: []string{"slack"}},
	}, Iod: viffd.AuthIod{Static: []viffd.IodStatic{{File: file, Principal: "agent:slack-iod", IO: "slack"}}}}); err != nil {
		t.Fatal(err)
	}
	r.asking(t)

	// Unbound and with no first sight to admit her, walter's reply counts for nothing.
	r.reply(t, "e1", "U7", "proceed")
	if q := r.question(t); q["verdict"] != nil {
		t.Fatalf("an unbound reply answered: %v", q)
	}
	if got := waitSaid(t, r, "U7", "Not answered"); got == "" {
		t.Fatalf("walter was not told the refusal alone: %v", r.fake.ephemerals)
	}
	if q := r.question(t); q["verdict"] != nil {
		t.Fatalf("an unbound reply answered: %v", q)
	}

	// Walter claims a Slack account from the CLI, asserted; the nonce is what the viff instance answers.
	resp, err := client.New(r.addr, "user:walter@example.com").Post("/accounts/slack", nil, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var claim struct {
		Nonce string `tony:"field=nonce"`
	}
	if resp.StatusCode != 201 {
		t.Fatalf("claim = %s", resp.Status)
	}
	if err := client.Decode(resp, &claim); err != nil {
		t.Fatal(err)
	}

	// A wrong nonce from the DM: told alone, nothing bound. The right one, as the slash command
	// with no response_url (the ephemeral stands in): bound, and told.
	r.event(t, "e2", map[string]any{"type": "message", "channel": "D77", "user": "U7", "text": "/viff link nope", "ts": "1790000099.000300"})
	if got := waitSaid(t, r, "U7", "Not linked"); !strings.Contains(got, "no claim has that nonce") {
		t.Fatalf("a wrong nonce: told %q", got)
	}
	r.sock.send <- map[string]any{"envelope_id": "e3", "type": "slash_commands", "payload": map[string]any{
		"command": "/viff", "text": "link " + claim.Nonce, "user_id": "U7", "channel_id": "D77",
	}}
	<-r.sock.acks
	if got := waitSaid(t, r, "U7", "Linked"); !strings.Contains(got, "<@U7> is user:walter@example.com") {
		t.Fatalf("the link: told %q", got)
	}
	rec, ok, err := entity.As(r.store, entity.SystemIdentity).Get(entity.ViffRef("identity", "slack", "account", "U7"))
	if err != nil || !ok || entity.Str(rec.Payload, "principal") != "user:walter@example.com" ||
		entity.Str(rec.Payload, "claimant") != "asserted" || entity.Str(entity.Get(rec.Payload, "proof"), "method") != "heard" {
		t.Fatalf("the binding = %v (ok=%v err=%v)", rec.Payload, ok, err)
	}

	// Now the reply is walter's, through the binding.
	r.reply(t, "e4", "U7", "proceed")
	select {
	case <-r.kicked:
	case <-time.After(5 * time.Second):
		t.Fatalf("the reply after linking answered nothing: %v", r.question(t))
	}
	q := r.question(t)
	ballot := entity.Get(entity.Get(mustGet(t, r.store, mirror.QuestionRef(askedQuestion)), "ballots"), "user:walter@example.com")
	if q["verdict"] != "proceed" || q["by"] != "user:walter@example.com" || entity.Str(ballot, "authenticated") != "slack" {
		t.Fatalf("after linking: answered %v by %v, ballot %v", q["verdict"], q["by"], ballot)
	}
}

// waitSaid waits for something said to user alone containing want (any text when want is "").
func waitSaid(t *testing.T, r *socketRig, user, want string) string {
	t.Helper()
	for range 400 {
		r.fake.mu.Lock()
		for i := len(r.fake.ephemerals) - 1; i >= 0; i-- {
			m := r.fake.ephemerals[i]
			if text, _ := m["text"].(string); m["user"] == user && strings.Contains(text, want) {
				r.fake.mu.Unlock()
				return text
			}
		}
		r.fake.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return ""
}

func mustGet(t *testing.T, store entity.Store, ref entity.Ref) entity.Payload {
	t.Helper()
	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		t.Fatalf("reading %s: ok %v, %v", ref, ok, err)
	}
	return e.Payload
}
