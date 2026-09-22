package slack_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
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
