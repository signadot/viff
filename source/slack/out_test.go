package slack_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/source/slack"
	"github.com/signadot/verse/versed"
)

// outRig is a real verse over a real store, a fake Slack, and the out half between them, reading
// and writing the verse over HTTP as its own principal.
type outRig struct {
	store entity.Store
	fake  *fakeSlack
	out   *slack.Out
}

func newOutRig(t *testing.T) *outRig {
	t.Helper()
	store := entitytest.Store(t)
	ts := httptest.NewServer(versed.NewWithStore(versed.ScriptedBuilder, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	src := newSource(t, f, slack.Config{})
	return &outRig{
		store: store, fake: f,
		out: src.Out(client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:slack-io")),
	}
}

// ask writes an input, as a person or a charter would.
func (r *outRig) ask(t *testing.T, target, name string, input entity.Obj) {
	t.Helper()
	entitytest.Set(t, r.store, entity.RefAt("slack", "Acme", "out", target, name), input.Payload())
}

// status reads what the out half wrote back, or nil.
func (r *outRig) status(t *testing.T, target, name string) entity.Obj {
	t.Helper()
	e, ok, err := r.store.Get(entity.RefAt("slack", "Acme", "out", target, name+slack.StatusSuffix))
	if err != nil {
		t.Fatalf("reading the status: %v", err)
	}
	if !ok {
		return nil
	}
	return entity.ToObj(e.Payload)
}

func (r *outRig) pass(t *testing.T) {
	t.Helper()
	if err := r.out.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
}

// A person is a DM: the out half opens it, says the text there, and writes where it landed.
func TestOutSaysAnInputToAPersonAndWritesItsStatus(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "U123", "hello", entity.Obj{"text": "the review is waiting on you"})
	r.pass(t)

	if r.fake.opened["U123"] != 1 || len(r.fake.posted) != 1 {
		t.Fatalf("opened %v, posted %v: want one DM opened and one message said", r.fake.opened, r.fake.posted)
	}
	if p := r.fake.posted[0]; p["channel"] != "DU123" || p["text"] != "the review is waiting on you" {
		t.Errorf("said %v, want the text in the person's DM", p)
	}
	st := r.status(t, "U123", "hello")
	if st["channel"] != "DU123" || st["ts"] != r.fake.posted[0]["ts"] || st["posted"] == nil {
		t.Errorf("status %v, want where it landed, the message's ts and the fingerprint said", st)
	}
	if st["error"] != nil {
		t.Errorf("a post that worked carries an error: %v", st["error"])
	}
	if link, _ := st["permalink"].(string); !strings.Contains(link, "DU123") {
		t.Errorf("permalink %q", link)
	}
}

// The next pass finds the input in step and says nothing: a restart resumes rather than repeats,
// because the status is the whole of what is remembered.
func TestOutIsInStepOnTheNextPass(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "U123", "hello", entity.Obj{"text": "hi"})
	r.pass(t)
	r.pass(t)
	if len(r.fake.posted) != 1 || len(r.fake.updates) != 0 {
		t.Errorf("posted %d, updated %d: a pass over an input already said said it again",
			len(r.fake.posted), len(r.fake.updates))
	}
}

// A changed input is an update of the message it already is, not a second message.
func TestOutUpdatesTheMessageWhenTheInputChanges(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "U123", "hello", entity.Obj{"text": "hi"})
	r.pass(t)
	first := r.status(t, "U123", "hello")

	r.ask(t, "U123", "hello", entity.Obj{"text": "hi, again"})
	r.pass(t)
	if len(r.fake.posted) != 1 || len(r.fake.updates) != 1 {
		t.Fatalf("posted %d, updated %d: want the one message updated", len(r.fake.posted), len(r.fake.updates))
	}
	if u := r.fake.updates[0]; u["ts"] != first["ts"] || u["text"] != "hi, again" {
		t.Errorf("updated %v, want the message at %v", u, first["ts"])
	}
	if st := r.status(t, "U123", "hello"); st["posted"] == first["posted"] {
		t.Error("the fingerprint did not move with the input")
	}
}

// A channel is said in as it is; no DM is opened.
func TestOutSaysInAChannelDirectly(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "C999", "release", entity.Obj{"text": "staging.3 is out"})
	r.pass(t)
	if len(r.fake.opened) != 0 || len(r.fake.posted) != 1 || r.fake.posted[0]["channel"] != "C999" {
		t.Errorf("opened %v, posted %v: want the channel said in directly", r.fake.opened, r.fake.posted)
	}
}

// A refusal is written on the status, in Slack's words, and the next pass tries again; once it
// works, the error is gone.
func TestOutRecordsAFailureAndRetries(t *testing.T) {
	r := newOutRig(t)
	r.fake.fail["chat.postMessage"] = "not_in_channel"
	r.ask(t, "C999", "release", entity.Obj{"text": "staging.3 is out"})
	r.pass(t)
	if st := r.status(t, "C999", "release"); !strings.Contains(st["error"].(string), "not_in_channel") {
		t.Fatalf("status %v, want Slack's refusal", st)
	}

	delete(r.fake.fail, "chat.postMessage")
	r.pass(t)
	st := r.status(t, "C999", "release")
	if st["ts"] == nil || st["error"] != nil {
		t.Errorf("status %v, want the retry posted and the error gone", st)
	}
}

// What cannot be said is said on the status, and a name ending in the status suffix is never an
// input: a status whose input is gone is left alone.
func TestOutRefusesWhatItCannotSay(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "U123", "empty", entity.Obj{"title": "no text"})
	r.ask(t, "X1", "nowhere", entity.Obj{"text": "hi"})
	r.ask(t, "U123", "gone"+slack.StatusSuffix, entity.Obj{"ts": "1.1"})
	r.pass(t)

	if len(r.fake.posted) != 0 {
		t.Errorf("said %v, want nothing said", r.fake.posted)
	}
	if st := r.status(t, "U123", "empty"); !strings.Contains(st["error"].(string), "text") {
		t.Errorf("status %v, want the missing text named", st)
	}
	if st := r.status(t, "X1", "nowhere"); !strings.Contains(st["error"].(string), "not a Slack id") {
		t.Errorf("status %v, want the target refused", st)
	}
}

// pose stands a question's record where the mirror keeps it, saying what a test needs it to.
func (r *outRig) pose(t *testing.T, id string, record entity.Obj) {
	t.Helper()
	entitytest.Set(t, r.store, mirror.QuestionRef(id), record.Payload())
}

// askButtons writes an input whose buttons answer q with proceed or deny.
func (r *outRig) askButtons(t *testing.T, q string) {
	t.Helper()
	r.ask(t, "C999", "review", entity.Obj{
		"text": "the review is waiting",
		"buttons": []any{
			map[string]any{"label": "Approve", "answer": map[string]any{"question": q, "option": "proceed"}},
			map[string]any{"label": "Deny", "answer": map[string]any{"question": q, "option": "deny"}},
		},
	})
}

// shown is a message's buttons, by label, and the program's lines beneath them.
func shown(t *testing.T, m map[string]any) (buttons map[string]map[string]any, lines string) {
	t.Helper()
	buttons = map[string]map[string]any{}
	blocks, _ := m["blocks"].([]any)
	var said []string
	for _, b := range blocks {
		bm := b.(map[string]any)
		switch bm["type"] {
		case "actions":
			for _, e := range bm["elements"].([]any) {
				em := e.(map[string]any)
				buttons[em["text"].(map[string]any)["text"].(string)] = em
			}
		case "context":
			for _, e := range bm["elements"].([]any) {
				said = append(said, e.(map[string]any)["text"].(string))
			}
		}
	}
	return buttons, strings.Join(said, "\n")
}

// Buttons are laid out as answers: each carries the question and option it answers, and a line
// the writer does not word says which question it is and who it waits on.
func TestOutLaysButtonsOutAsAnswers(t *testing.T) {
	r := newOutRig(t)
	r.pose(t, "q~1", entity.Obj{"status": mirror.QuestionPending,
		mirror.WaitingOnField: []any{"user:scott@example.com", "user:ana@example.com"}})
	r.askButtons(t, "q~1")
	r.pass(t)
	if len(r.fake.posted) != 1 {
		t.Fatalf("posted %v, want one message", r.fake.posted)
	}
	buttons, lines := shown(t, r.fake.posted[0])
	btn := buttons["Approve"]
	if btn["value"] != `{"question":"q~1","option":"proceed"}` || !strings.HasPrefix(btn["action_id"].(string), slack.AnswerAction) {
		t.Errorf("button %v, want it to answer q~1 with proceed", btn)
	}
	if want := "Answers `q~1` · waiting on scott@example.com, ana@example.com"; lines != want {
		t.Errorf("said %q, want %q", lines, want)
	}
	if r.fake.posted[0]["text"] != "the review is waiting" {
		t.Errorf("text %v, want the text kept for notifications", r.fake.posted[0]["text"])
	}
}

// A question that settles redraws the message, which nobody asked for by writing: the buttons go,
// and everyone who can see it is told the verdict, who gave it and what they said with it.
func TestOutRedrawsTheMessageWhenItsQuestionSettles(t *testing.T) {
	r := newOutRig(t)
	r.pose(t, "q~1", entity.Obj{"status": mirror.QuestionPending})
	r.askButtons(t, "q~1")
	r.pass(t)

	r.pose(t, "q~1", entity.Obj{"status": mirror.QuestionAnswered, "verdict": "deny",
		"by": "user:ani@example.com", "say": []any{"not on friday"}})
	r.pass(t)
	if len(r.fake.posted) != 1 || len(r.fake.updates) != 1 {
		t.Fatalf("posted %d, updated %d: want the one message redrawn", len(r.fake.posted), len(r.fake.updates))
	}
	buttons, lines := shown(t, r.fake.updates[0])
	if len(buttons) != 0 {
		t.Errorf("buttons %v on a settled question", buttons)
	}
	if want := "`q~1` answered *deny* by ani@example.com: not on friday"; lines != want {
		t.Errorf("said %q, want %q", lines, want)
	}
	r.pass(t)
	if len(r.fake.updates) != 1 {
		t.Errorf("updated %d times: a settled message was redrawn again", len(r.fake.updates))
	}
}

// What a message says of its question, in each state its record can be in. A verdict is written
// only by the settle, so a record carrying one is answered even while a pending status that
// reached it after says otherwise.
func TestOutSaysWhatBecameOfTheQuestion(t *testing.T) {
	for _, c := range []struct {
		name    string
		record  entity.Obj // nil: no record stands
		buttons bool
		says    string
	}{
		{"not asked", nil, false, "No question `q~1` stands"},
		{"a ballot so far", entity.Obj{"status": mirror.QuestionPending,
			"ballots": map[string]any{"user:ani@example.com": map[string]any{"choice": "proceed"}}},
			true, "Answers `q~1` · so far: ani@example.com proceed"},
		{"answered", entity.Obj{"status": mirror.QuestionAnswered, "verdict": "proceed",
			"by": "user:scott@example.com"}, false, "`q~1` answered *proceed* by scott@example.com"},
		{"a verdict under a pending status", entity.Obj{"status": mirror.QuestionPending,
			"verdict": "proceed", "by": "user:scott@example.com"}, false, "`q~1` answered *proceed* by scott@example.com"},
		{"withdrawn", entity.Obj{"status": mirror.QuestionOrphaned, "why": "the rule stopped selecting it"},
			false, "`q~1` was withdrawn unanswered: the rule stopped selecting it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newOutRig(t)
			if c.record != nil {
				r.pose(t, "q~1", c.record)
			}
			r.askButtons(t, "q~1")
			r.pass(t)
			if len(r.fake.posted) != 1 {
				t.Fatalf("posted %v, want one message", r.fake.posted)
			}
			buttons, lines := shown(t, r.fake.posted[0])
			if (len(buttons) > 0) != c.buttons {
				t.Errorf("buttons %v, want buttons: %v", buttons, c.buttons)
			}
			if lines != c.says {
				t.Errorf("said %q, want %q", lines, c.says)
			}
		})
	}
}

// A message posted before its question stands offers no button, and gains them when it does.
func TestOutOffersTheButtonsOnceTheQuestionStands(t *testing.T) {
	r := newOutRig(t)
	r.askButtons(t, "q~1")
	r.pass(t)
	r.pose(t, "q~1", entity.Obj{"status": mirror.QuestionPending})
	r.pass(t)
	if len(r.fake.updates) != 1 {
		t.Fatalf("updated %d times, want the message redrawn once the question stood", len(r.fake.updates))
	}
	if buttons, _ := shown(t, r.fake.updates[0]); len(buttons) != 2 {
		t.Errorf("buttons %v, want both offered", buttons)
	}
}

// A button that answers nothing is not said, and the status says why.
func TestOutRefusesAButtonThatAnswersNothing(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "U123", "review", entity.Obj{
		"text":    "hi",
		"buttons": []any{map[string]any{"label": "Do it", "url": "https://example.com"}},
	})
	r.pass(t)
	if len(r.fake.posted) != 0 {
		t.Errorf("said %v, want nothing said", r.fake.posted)
	}
	if st := r.status(t, "U123", "review"); !strings.Contains(st["error"].(string), "answer") {
		t.Errorf("status %v, want the button refused", st)
	}
}
