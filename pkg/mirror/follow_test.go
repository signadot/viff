package mirror

import (
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// leasedHandle is a handle of a task on a runner: each subscribe is a request the test's runner
// accepts or does not, and a stream of its own.
type leasedHandle struct {
	fakeHandle
	mu       sync.Mutex
	asked    []int64     // the cursor of each subscribe, in order
	accepted []time.Time // when each accepted subscribe was accepted
	refuse   bool        // the runner accepts no subscribe from here on
	first    []task.Event
}

func (h *leasedHandle) SubscribeAccepted(since int64) (<-chan task.Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, since)
	ch := make(chan task.Event, 8)
	if h.refuse {
		close(ch)
		return ch, func() {}
	}
	h.accepted = append(h.accepted, time.Now())
	ch <- task.Event{Kind: task.EventAccepted}
	if len(h.asked) == 1 {
		for _, ev := range h.first {
			ch <- ev
		}
	}
	return ch, func() {}
}

func (h *leasedHandle) subscribes() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64(nil), h.asked...)
}

func waitFor(t *testing.T, why string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", why)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A stream that stays open is asked for again four times a lease, from the cursor the last one
// reached: an open connection renews nothing, and a subscribe the runner accepts does.
func TestTheMirrorSubscribesAgainFourTimesALease(t *testing.T) {
	store := entitytest.Store(t)
	h := &leasedHandle{first: []task.Event{{Kind: task.EventSay, Text: "working", Cursor: 7}}}
	t0 := time.Now()
	Reflect(store, "p", h, Meta{Runner: "http://runner.invalid", Lease: "400ms"})
	waitFor(t, "four subscribes", func() bool { return len(h.subscribes()) >= 4 })
	if took := time.Since(t0); took < 250*time.Millisecond {
		t.Fatalf("four subscribes in %s, want one every quarter of the 400ms lease", took)
	}
	for i, since := range h.subscribes()[1:] {
		if since != 7 {
			t.Fatalf("subscribe %d asked from cursor %d, want 7, where the last one reached", i+2, since)
		}
	}
	e, _, _ := store.Get(Ref("p"))
	got := entity.ToObj(e.Payload)
	if got["status"] != string(task.Running) || got["observed"] == false || got["lease"] != "400ms" {
		t.Fatalf("record = %v, want it running, observed, with its lease", got)
	}
}

// The mirror stops waiting on a runner a lease and a margin after the last subscribe the runner
// accepted, and ends the task on its record as failed and orphaned. Until then the record says
// the stream is lost.
func TestTheMirrorStopsWaitingAMarginAfterTheLease(t *testing.T) {
	was := waitMargin
	waitMargin = 200 * time.Millisecond
	t.Cleanup(func() { waitMargin = was })

	store := entitytest.Store(t)
	h := &leasedHandle{}
	Reflect(store, "p", h, Meta{Runner: "http://runner.invalid", Lease: "400ms"})
	waitFor(t, "a second subscribe", func() bool { return len(h.subscribes()) >= 2 })
	h.mu.Lock()
	h.refuse = true
	last := h.accepted[len(h.accepted)-1]
	h.mu.Unlock()

	status := func() map[string]any {
		e, _, _ := store.Get(Ref("p"))
		return entity.ToObj(e.Payload)
	}
	waitFor(t, "the record to say the stream is lost", func() bool { return status()["observed"] == false })
	if got := status(); got["status"] != string(task.Running) {
		t.Fatalf("record = %v, want the task still running while the mirror waits", got)
	}
	waitFor(t, "the task to end on its record", func() bool { return task.TerminalStatus(entity.Str(mustGet(t, store, "p"), "status")) })
	if waited := time.Since(last); waited < 600*time.Millisecond {
		t.Fatalf("the mirror stopped waiting %s after the last accepted subscribe, want the 400ms lease and the 200ms margin", waited)
	}
	got := status()
	if got["status"] != string(task.Failed) || got[OrphanedField] != true || got["err"] != OrphanedUnanswered {
		t.Fatalf("record = %v, want it failed and orphaned, the runner not having answered", got)
	}
}

func mustGet(t *testing.T, store entity.Store, id string) entity.Payload {
	t.Helper()
	e, _, err := store.Get(Ref(id))
	if err != nil {
		t.Fatal(err)
	}
	return e.Payload
}

// slowStore is a store whose writes wait for the test once it is held.
type slowStore struct {
	entity.Store
	mu   sync.Mutex
	hold chan struct{}
}

func (s *slowStore) Write(w entity.Write) (bool, int64, error) {
	s.mu.Lock()
	hold := s.hold
	s.mu.Unlock()
	if hold != nil {
		<-hold
	}
	return s.Store.Write(w)
}

// A write the store is slow to take does not hold up the next subscribe: the lease is renewed
// by a clock, apart from the record.
func TestASlowStoreDoesNotHoldUpTheNextSubscribe(t *testing.T) {
	store := &slowStore{Store: entitytest.Store(t)}
	h := &leasedHandle{first: []task.Event{{Kind: task.EventSay, Text: "working", Cursor: 3}}}
	hold := make(chan struct{})
	store.mu.Lock()
	store.hold = hold
	store.mu.Unlock()
	t.Cleanup(func() { close(hold) })

	q := newFrames()
	ch, unsub := subscribe(h, true, 0)
	go follow(h, q, ch, unsub, 400*time.Millisecond)
	go record(store, "p", entity.Obj{"status": string(task.Running)}, Meta{Runner: "http://runner.invalid"}, h, q)

	waitFor(t, "three subscribes while the store takes no write", func() bool { return len(h.subscribes()) >= 3 })
}
