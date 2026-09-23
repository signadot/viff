package linear_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/io/linear"
	"github.com/signadot/verse/versed"
)

// The second out half, and the reason there is one: everything below runs on the same io.Out as
// Slack's, against a system with none of Slack's shapes — no channel, no message ts, an identifier
// that has to be resolved before anything can be said, and a comment rather than a message.

type outRig struct {
	store entity.Store
	fake  *fakeLinear
	out   *verseio.Out
}

func newOutRig(t *testing.T) *outRig {
	t.Helper()
	store := entitytest.Store(t)
	ts := httptest.NewServer(versed.NewWithStore(versed.ScriptedBuilder, store).Handler())
	t.Cleanup(ts.Close)
	f := newFake()
	f.team("ENG", "Engineering")
	src := newSource(t, f, linear.Config{Teams: []string{"ENG"}})
	places := src.OutRefs()
	if len(places) != 1 {
		t.Fatalf("out places %v, want one per team", places)
	}
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:linear-io")
	return &outRig{store: store, fake: f,
		out: verseio.NewOut(src.Sink(), places[0], verse, nil)}
}

func (r *outRig) ask(t *testing.T, issue, name string, input entity.Obj) {
	t.Helper()
	input["to"] = issue
	entitytest.Set(t, r.store, entity.RefAt("linear", "ENG", "out", name), input.Payload())
}

func (r *outRig) pass(t *testing.T) {
	t.Helper()
	if err := r.out.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
}

func (r *outRig) status(t *testing.T, name string) entity.Obj {
	t.Helper()
	e, ok, err := r.store.Get(entity.RefAt("linear", "ENG", "out", name+verseio.StatusSuffix))
	if err != nil {
		t.Fatalf("reading the status: %v", err)
	}
	if !ok {
		return nil
	}
	return entity.ToObj(e.Payload)
}

// An input is a comment on the issue its target names, and the status says which comment it is.
func TestAnInputBecomesACommentOnItsIssue(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "ENG-42", "review", entity.Obj{"text": "verse says this is waiting on a decision"})
	r.pass(t)

	if len(r.fake.comments) != 1 {
		t.Fatalf("comments %v, want one", r.fake.comments)
	}
	// The identifier a person types was resolved to the uuid the mutation takes.
	if c := r.fake.comments[0]; c["issue"] != "uuid-ENG-42" || c["body"] != "verse says this is waiting on a decision" {
		t.Errorf("commented %v, want it on the issue, as written", c)
	}
	st := r.status(t, "review")
	if st["comment"] == nil || st["posted"] == nil || st["error"] != nil {
		t.Errorf("status %v, want the comment, its fingerprint and no error", st)
	}
	if url, _ := st["url"].(string); !strings.Contains(url, "linear.app") {
		t.Errorf("url %q, want where a reader finds it", url)
	}
}

// The next pass over the same text says nothing: the fingerprint on the status is what makes a
// pass cheap, and there is no ts here to compare instead.
func TestACommentAlreadySayingItIsLeftAlone(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "ENG-42", "review", entity.Obj{"text": "waiting"})
	r.pass(t)
	r.pass(t)
	if len(r.fake.comments) != 1 {
		t.Errorf("comments %v: it commented twice about one input", r.fake.comments)
	}
}

// A changed text edits the comment rather than leaving a second one, so a thread stays as long as
// the conversation rather than as long as the number of passes.
func TestAChangedInputEditsTheComment(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "ENG-42", "review", entity.Obj{"text": "waiting"})
	r.pass(t)
	first := r.status(t, "review")

	r.ask(t, "ENG-42", "review", entity.Obj{"text": "answered: proceed"})
	r.pass(t)
	if len(r.fake.comments) != 1 {
		t.Fatalf("comments %v, want the one edited", r.fake.comments)
	}
	if r.fake.comments[0]["body"] != "answered: proceed" {
		t.Errorf("comment %v, want what the input now says", r.fake.comments[0])
	}
	st := r.status(t, "review")
	if st["comment"] != first["comment"] {
		t.Errorf("comment moved from %v to %v: it left a second one", first["comment"], st["comment"])
	}
	if st["posted"] == first["posted"] {
		t.Error("the fingerprint did not move with the text")
	}
}

// An issue this credential cannot see is a failure on the status, in the connector's words, and
// the next pass tries again — the same shape as a Slack refusal, which is the framework's doing.
func TestAnIssueItCannotSeeIsRecordedAndRetried(t *testing.T) {
	r := newOutRig(t)
	r.fake.noIssue = true
	r.ask(t, "ENG-9999", "review", entity.Obj{"text": "waiting"})
	r.pass(t)
	st := r.status(t, "review")
	if err, _ := st["error"].(string); !strings.Contains(err, "no issue ENG-9999") {
		t.Fatalf("status %v, want the issue named", st)
	}

	r.fake.noIssue = false
	r.pass(t)
	st = r.status(t, "review")
	if st["comment"] == nil || st["error"] != nil {
		t.Errorf("status %v, want the retry commented and the error gone", st)
	}
}

// An input with nothing to say is refused rather than posted empty.
func TestAnInputWithNoTextIsRefused(t *testing.T) {
	r := newOutRig(t)
	r.ask(t, "ENG-42", "review", entity.Obj{"title": "no text"})
	r.pass(t)
	if len(r.fake.comments) != 0 {
		t.Errorf("commented %v, want nothing said", r.fake.comments)
	}
	if st := r.status(t, "review"); !strings.Contains(entityStr(st, "error"), "text") {
		t.Errorf("status %v, want the missing text named", st)
	}
}

func entityStr(o entity.Obj, field string) string {
	s, _ := o[field].(string)
	return s
}
