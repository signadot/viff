package io_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/versed"
)

// The out half's machinery, tested with a sink that is nothing like Slack: the pass, the status
// beside each input, the failure written once, and what a sink is handed.

// noted is a sink that writes down what it was asked for, and does whatever a test told it to.
type noted struct {
	mu       sync.Mutex
	seen     []verseio.Input
	fields   entity.Obj // what Deliver answers
	err      error      // …or fails with
	delivers int
}

func (n *noted) Deliver(_ context.Context, in verseio.Input) (entity.Obj, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.delivers++
	n.seen = append(n.seen, in)
	if n.err != nil {
		return nil, n.err
	}
	if n.fields == nil {
		return nil, nil
	}
	out := entity.Obj{}
	for k, v := range n.fields {
		out[k] = v
	}
	return out, nil
}

func (n *noted) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.delivers
}

type outRig struct {
	store entity.Store
	sink  *noted
	out   *verseio.Out
	place entity.Ref
}

func newOutRig(t *testing.T) *outRig {
	t.Helper()
	store := entitytest.Store(t)
	ts := httptest.NewServer(versed.NewWithStore(versed.ScriptedBuilder, store).Handler())
	t.Cleanup(ts.Close)
	place := entity.RefAt("demo", "out")
	sink := &noted{}
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:demo-iod")
	return &outRig{store: store, sink: sink, place: place,
		out: verseio.NewOut(sink, place, verse, nil)}
}

func (r *outRig) pass(t *testing.T) {
	t.Helper()
	if err := r.out.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
}

func (r *outRig) status(t *testing.T, name string) entity.Obj {
	t.Helper()
	e, ok, err := r.store.Get(entity.RefAt("demo", "out", name+verseio.StatusSuffix))
	if err != nil {
		t.Fatalf("reading the status: %v", err)
	}
	if !ok {
		return nil
	}
	return entity.ToObj(e.Payload)
}

// ask writes an input the way a person would: under the place, saying whom it is for.
func ask(t *testing.T, store entity.Store, to, name string, input entity.Obj) {
	t.Helper()
	input["to"] = to
	entitytest.Set(t, store, entity.RefAt("demo", "out", name), input.Payload())
}

// askDeep writes one where a charter's fan-out would land it, which is the shape a person never
// types and the one that has to work.
func askDeep(t *testing.T, store entity.Store, segs []string, to string, input entity.Obj) {
	t.Helper()
	input["to"] = to
	entitytest.Set(t, store, entity.RefAt(append([]string{"demo", "out"}, segs...)...), input.Payload())
}

// Every input under the place reaches the sink, with what the last pass recorded beside it, and
// what the sink answers lands on the status.
func TestAPassAsksTheSinkForEachInputAndRecordsWhatItSays(t *testing.T) {
	r := newOutRig(t)
	r.sink.fields = entity.Obj{"said": "yes"}
	ask(t, r.store, "ana", "hello", entity.Obj{"text": "hi"})
	ask(t, r.store, "bo", "hello-again", entity.Obj{"text": "hi again"})
	r.pass(t)

	if n := r.sink.count(); n != 2 {
		t.Fatalf("delivered %d, want both inputs", n)
	}
	if got := r.status(t, "hello")["said"]; got != "yes" {
		t.Errorf("status %v, want what the sink answered", got)
	}
	// And the second pass hands the sink what the first one wrote.
	r.pass(t)
	last := r.sink.seen[len(r.sink.seen)-1]
	if entity.Str(last.Status, "said") != "yes" {
		t.Errorf("the sink was handed status %v, want what it recorded last time", last.Status)
	}
	if entity.Str(last.Value, "text") == "" || last.To == "" || last.Name == "" {
		t.Errorf("the sink was handed %+v, want the input and where it stands", last)
	}
}

// An input is found wherever it stands, because what writes one decides where: a person types one
// under the place, and a charter's fan-out lands one per person under (rule, subject, key). A
// contract that read the target off a fixed level could be written by hand and by nothing else.
func TestAnInputIsFoundAtAnyDepth(t *testing.T) {
	r := newOutRig(t)
	r.sink.fields = entity.Obj{"said": "yes"}
	deep := []string{"gate-ask", "verse", "ask", "review~1", "user:ana@example.com"}
	askDeep(t, r.store, deep, "user:ana@example.com", entity.Obj{"text": "the review is waiting"})
	r.pass(t)

	if n := r.sink.count(); n != 1 {
		t.Fatalf("delivered %d, want the one input, however deep it stands", n)
	}
	in := r.sink.seen[0]
	if in.To != "user:ana@example.com" || in.Name != "user:ana@example.com" {
		t.Errorf("the sink was handed %+v, want whom it is for and what it is called", in)
	}
	if want := "demo.out.gate-ask.verse.ask"; !strings.Contains(in.At.String(), want) {
		t.Errorf("at %s, want the address it was written at", in.At)
	}
	// And its status stands beside it, at that same depth.
	at := entity.RefAt(append(append([]string{"demo", "out"}, deep[:len(deep)-1]...),
		deep[len(deep)-1]+verseio.StatusSuffix)...)
	if _, ok, err := r.store.Get(at); err != nil || !ok {
		t.Errorf("no status at %s: ok %v, %v", at, ok, err)
	}
}

// A sink that says nothing needed doing writes nothing, which is what makes a quiet verse quiet:
// most passes over most inputs are this one.
func TestASinkThatSaysNothingWritesNothing(t *testing.T) {
	r := newOutRig(t)
	ask(t, r.store, "ana", "hello", entity.Obj{"text": "hi"})
	r.pass(t)
	if st := r.status(t, "hello"); st != nil {
		t.Errorf("status %v, want none written", st)
	}
}

// A status is never an input: it would ask for what it is the answer to.
func TestAStatusIsNeverAnInput(t *testing.T) {
	r := newOutRig(t)
	r.sink.fields = entity.Obj{"said": "yes"}
	ask(t, r.store, "ana", "hello"+verseio.StatusSuffix, entity.Obj{"ts": "1.1"})
	r.pass(t)
	if n := r.sink.count(); n != 0 {
		t.Errorf("delivered %d, want the status left alone", n)
	}
}

// A failure is recorded once, with the instant it began, and cleared by the success that ends it.
func TestAFailureIsWrittenOnceAndClearedByTheNextSuccess(t *testing.T) {
	r := newOutRig(t)
	r.sink.err = fmt.Errorf("the far end refused")
	ask(t, r.store, "ana", "hello", entity.Obj{"text": "hi"})
	r.pass(t)
	st := r.status(t, "hello")
	if !strings.Contains(fmt.Sprint(st["error"]), "refused") || st["failedAt"] == nil {
		t.Fatalf("status %v, want the failure and when it began", st)
	}
	began := st["failedAt"]

	r.pass(t) // the same failure again says nothing new
	if got := r.status(t, "hello")["failedAt"]; got != began {
		t.Errorf("failedAt moved to %v: the same failure was written twice", got)
	}

	r.sink.err = nil
	r.sink.fields = entity.Obj{"said": "yes"}
	r.pass(t)
	if st := r.status(t, "hello"); st["error"] != nil || st["failedAt"] != nil {
		t.Errorf("status %v, want the failure gone with the success that ended it", st)
	}
}

// A kick is a pass now rather than at the next interval, and two kicks before one pass are one
// pass: it asks for the truth to be read, not for a thing to be done.
func TestAKickAsksForOnePass(t *testing.T) {
	r := newOutRig(t)
	ask(t, r.store, "ana", "hello", entity.Obj{"text": "hi"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.out.Kick()
	r.out.Kick()
	go r.out.Run(ctx, time.Hour) // the interval is never reached; only the kicks are

	until(t, func() bool { return r.sink.count() >= 1 }, "the kick was never passed on")
	if n := r.sink.count(); n > 3 {
		t.Errorf("%d deliveries from two kicks and one start", n)
	}
}

func until(t *testing.T, ok func() bool, why string) {
	t.Helper()
	for range 400 {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(why)
}

// A TARGET IS FIXED AT FIRST DELIVERY.
//
// Every sink resolves `to` once and then addresses what it already made — a Slack channel and ts,
// a Linear comment id, both on the status. So a `to` changed afterwards reached nobody: the input
// went on editing the message it first made, at the old target, silently. Found by writing an
// address that was not one (`user:scott@…`, an ellipsis) and watching the DM update anyway.
func TestATargetIsFixedAtFirstDelivery(t *testing.T) {
	r := newOutRig(t)
	r.sink.fields = entity.Obj{"ts": "1.1"}
	ask(t, r.store, "C111", "note", entity.Obj{"text": "hello"})
	r.pass(t)
	if st := r.status(t, "note"); st["to"] != "C111" {
		t.Fatalf("status %v, want who it went to — which is what makes the check below possible", st)
	}
	delivered := r.sink.count()

	// Retargeted: refused, said on the status, and the sink is never asked — so no call is made
	// to the far side about a message it would address by the old target anyway.
	ask(t, r.store, "C222", "note", entity.Obj{"text": "hello"})
	r.pass(t)
	if r.sink.count() != delivered {
		t.Errorf("the sink was asked %d times, want the retarget refused before it",
			r.sink.count()-delivered)
	}
	st := r.status(t, "note")
	err, _ := st["error"].(string)
	if !strings.Contains(err, "C111") || !strings.Contains(err, "C222") {
		t.Errorf("status %v, want both targets named", st)
	}
	if st["to"] != "C111" {
		t.Errorf("status says it went to %v, want the target it was actually delivered to", st["to"])
	}

	// …and putting it back is enough: the refusal is level-triggered like everything else here.
	ask(t, r.store, "C111", "note", entity.Obj{"text": "hello again"})
	r.pass(t)
	if r.sink.count() == delivered {
		t.Error("the sink was not asked again once the target was put back")
	}
	if st := r.status(t, "note"); st["error"] != nil {
		t.Errorf("status %v, want the refusal cleared by the delivery that ended it", st)
	}
}

// AND A TARGET THAT NEVER REACHED ANYBODY MAY STILL BE CORRECTED: a first delivery that failed
// writes no `to`, so a typo'd address is fixed by editing it, which is what anybody would try.
func TestATargetThatNeverDeliveredMayBeCorrected(t *testing.T) {
	r := newOutRig(t)
	r.sink.err = errors.New("no such channel")
	ask(t, r.store, "C-typo", "note", entity.Obj{"text": "hello"})
	r.pass(t)
	if st := r.status(t, "note"); st["to"] != nil {
		t.Fatalf("status %v, want no target recorded for a delivery that did not happen", st)
	}

	r.sink.err = nil
	r.sink.fields = entity.Obj{"ts": "1.1"}
	ask(t, r.store, "C-right", "note", entity.Obj{"text": "hello"})
	r.pass(t)
	st := r.status(t, "note")
	if st["to"] != "C-right" || st["error"] != nil {
		t.Errorf("status %v, want the corrected target delivered and the error gone", st)
	}
}

// A REFUSAL IS PERMANENT: said once, and not asked again while the input stands as it was refused.
// Sending it again on every pass is what tripped GitHub's secondary rate limit for the account the
// io acted as (git-issue na9fce9dh12krdydqsn0).
func TestARefusalIsPermanentUntilTheInputChanges(t *testing.T) {
	r := newOutRig(t)
	r.sink.err = verseio.Permanent(errors.New("422: can not approve your own pull request"))
	ask(t, r.store, "pr.47", "review", entity.Obj{"event": "approve"})
	r.pass(t)
	st := r.status(t, "review")
	if st["failed"] != verseio.PermanentFailure || st["failedOn"] == nil || !strings.Contains(fmt.Sprint(st["error"]), "422") {
		t.Fatalf("status %v, want the refusal recorded as permanent, with the input it refused", st)
	}
	for range 3 {
		r.pass(t)
	}
	if n := r.sink.count(); n != 1 {
		t.Fatalf("asked %d times, want once: a refusal is not asked again", n)
	}

	// Edited: a new asking, delivered once. Refused again with the same words, it is recorded
	// against the new input, or it would be asked on every pass from here on.
	ask(t, r.store, "pr.47", "review", entity.Obj{"event": "request-changes"})
	r.pass(t)
	r.pass(t)
	if n := r.sink.count(); n != 2 {
		t.Fatalf("asked %d times after one edit, want 2", n)
	}
	if on := r.status(t, "review")["failedOn"]; on == st["failedOn"] {
		t.Errorf("failedOn %v, want the edited input's fingerprint", on)
	}

	// Edited and met: the refusal goes with the success that ends it.
	r.sink.err, r.sink.fields = nil, entity.Obj{"review": "7001"}
	ask(t, r.store, "pr.48", "review", entity.Obj{"event": "approve"})
	r.pass(t)
	if st := r.status(t, "review"); st["error"] != nil || st["failed"] != nil || st["failedOn"] != nil {
		t.Errorf("status %v, want the refusal gone with the success that ended it", st)
	}
}

// A failure that is not a refusal — the far side down, a rate limit — is asked again every pass,
// as before.
func TestAFailureThatIsNotARefusalIsAskedAgain(t *testing.T) {
	r := newOutRig(t)
	r.sink.err = errors.New("502 bad gateway")
	ask(t, r.store, "pr.47", "note", entity.Obj{"text": "hi"})
	for range 3 {
		r.pass(t)
	}
	if n := r.sink.count(); n != 3 {
		t.Fatalf("asked %d times over three passes, want 3", n)
	}
	if st := r.status(t, "note"); st["failed"] != nil {
		t.Errorf("status %v, want a transient failure not marked permanent", st)
	}
}

// keeping is a sink that also keeps something current every pass, and notes what it was told of.
type keeping struct {
	noted
	kept []string
}

func (k *keeping) Keep(_ context.Context, in verseio.Input) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.kept = append(k.kept, in.Name)
}

// A KEEPER HEARS OF AN INPUT IT IS NOT ASKED TO DELIVER, so what it keeps current every pass — a
// Slack thread's questions — stays current for a message whose last edit was refused.
func TestAKeeperIsToldOfARefusedInput(t *testing.T) {
	store := entitytest.Store(t)
	ts := httptest.NewServer(versed.NewWithStore(versed.ScriptedBuilder, store).Handler())
	t.Cleanup(ts.Close)
	sink := &keeping{}
	sink.err = verseio.Permanent(errors.New("msg_too_long"))
	out := verseio.NewOut(sink, entity.RefAt("demo", "out"),
		client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:demo-iod"), nil)
	ask(t, store, "C111", "note", entity.Obj{"text": "long"})
	for range 3 {
		if err := out.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.delivers != 1 || len(sink.kept) != 2 {
		t.Errorf("delivered %d and kept %d, want delivered once and kept on the two passes after", sink.delivers, len(sink.kept))
	}
}
