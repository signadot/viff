package io_test

import (
	"context"
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
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:demo-io")
	return &outRig{store: store, sink: sink, place: place,
		out: verseio.NewOut(sink, place, verse, nil)}
}

func (r *outRig) pass(t *testing.T) {
	t.Helper()
	if err := r.out.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
}

func (r *outRig) status(t *testing.T, target, name string) entity.Obj {
	t.Helper()
	e, ok, err := r.store.Get(entity.RefAt("demo", "out", target, name+verseio.StatusSuffix))
	if err != nil {
		t.Fatalf("reading the status: %v", err)
	}
	if !ok {
		return nil
	}
	return entity.ToObj(e.Payload)
}

func ask(t *testing.T, store entity.Store, target, name string, input entity.Obj) {
	t.Helper()
	entitytest.Set(t, store, entity.RefAt("demo", "out", target, name), input.Payload())
}

// Every input under the place reaches the sink, with what the last pass recorded beside it, and
// what the sink answers lands on the status.
func TestAPassAsksTheSinkForEachInputAndRecordsWhatItSays(t *testing.T) {
	r := newOutRig(t)
	r.sink.fields = entity.Obj{"said": "yes"}
	ask(t, r.store, "ana", "hello", entity.Obj{"text": "hi"})
	ask(t, r.store, "bo", "hello", entity.Obj{"text": "hi again"})
	r.pass(t)

	if n := r.sink.count(); n != 2 {
		t.Fatalf("delivered %d, want both inputs", n)
	}
	if got := r.status(t, "ana", "hello")["said"]; got != "yes" {
		t.Errorf("status %v, want what the sink answered", got)
	}
	// And the second pass hands the sink what the first one wrote.
	r.pass(t)
	last := r.sink.seen[len(r.sink.seen)-1]
	if entity.Str(last.Status, "said") != "yes" {
		t.Errorf("the sink was handed status %v, want what it recorded last time", last.Status)
	}
	if entity.Str(last.Value, "text") == "" || last.Target == "" || last.Name == "" {
		t.Errorf("the sink was handed %+v, want the input and where it stands", last)
	}
}

// A sink that says nothing needed doing writes nothing, which is what makes a quiet verse quiet:
// most passes over most inputs are this one.
func TestASinkThatSaysNothingWritesNothing(t *testing.T) {
	r := newOutRig(t)
	ask(t, r.store, "ana", "hello", entity.Obj{"text": "hi"})
	r.pass(t)
	if st := r.status(t, "ana", "hello"); st != nil {
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
	st := r.status(t, "ana", "hello")
	if !strings.Contains(fmt.Sprint(st["error"]), "refused") || st["failedAt"] == nil {
		t.Fatalf("status %v, want the failure and when it began", st)
	}
	began := st["failedAt"]

	r.pass(t) // the same failure again says nothing new
	if got := r.status(t, "ana", "hello")["failedAt"]; got != began {
		t.Errorf("failedAt moved to %v: the same failure was written twice", got)
	}

	r.sink.err = nil
	r.sink.fields = entity.Obj{"said": "yes"}
	r.pass(t)
	if st := r.status(t, "ana", "hello"); st["error"] != nil || st["failedAt"] != nil {
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
