package mirror

import (
	"sync"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/task"
)

// leasedHandle is a handle of a task on a runner: each subscribe is a request the test's runner
// accepts or does not, and a stream of its own.
type leasedHandle struct {
	fakeHandle
	mu       sync.Mutex
	asked    []int64         // the cursor of each subscribe, in order
	within   []time.Duration // how long each subscribe was waited for
	closed   []bool          // whether each subscribe's stream was closed by its reader
	accepted []time.Time     // when each accepted subscribe was accepted
	refuse   bool            // the runner accepts no subscribe from here on
	first    []task.Event
	next     []task.Event // the frames of the next subscribe accepted, once
}

func (h *leasedHandle) SubscribeAccepted(since int64, within time.Duration) (<-chan task.Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, since)
	h.within = append(h.within, within)
	h.closed = append(h.closed, false)
	i := len(h.closed) - 1
	unsub := func() {
		h.mu.Lock()
		h.closed[i] = true
		h.mu.Unlock()
	}
	ch := make(chan task.Event, 8)
	if h.refuse {
		close(ch)
		return ch, unsub
	}
	h.accepted = append(h.accepted, time.Now())
	ch <- task.Event{Kind: task.EventAccepted}
	if len(h.asked) == 1 {
		for _, ev := range h.first {
			ch <- ev
		}
	}
	for _, ev := range h.next {
		ch <- ev
	}
	h.next = nil
	return ch, unsub
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
	ch, unsub := subscribe(h, true, 0, 400*time.Millisecond)
	go follow(h, q, ch, unsub, 400*time.Millisecond, nil)
	go record(store, "p", entity.Obj{"status": string(task.Running)}, Meta{Runner: "http://runner.invalid"}, h, q)

	waitFor(t, "three subscribes while the store takes no write", func() bool { return len(h.subscribes()) >= 3 })
}

// A VIFF THAT STOPS STOPS FOLLOWING ITS TASKS, and ends none of them: the viff that comes next
// follows them again. Once Done closes, nothing more is asked of the runner, and the task is not
// given up.
func TestAFollowEndsWithTheViffThatFollows(t *testing.T) {
	h := &leasedHandle{}
	done := make(chan struct{})
	q := newFrames()
	ch, unsub := subscribe(h, true, 0, 400*time.Millisecond)
	returned := make(chan struct{})
	go func() {
		follow(h, q, ch, unsub, 400*time.Millisecond, done)
		close(returned)
	}()
	waitFor(t, "a resubscribe", func() bool { return len(h.subscribes()) >= 2 })
	close(done)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the follow went on after the viff stopped")
	}
	asked := len(h.subscribes())
	time.Sleep(300 * time.Millisecond) // three quarters of a lease: a follow still running asks again
	if n := len(h.subscribes()); n != asked {
		t.Fatalf("%d subscribes after the viff stopped", n-asked)
	}
	for {
		it, ok := q.take()
		if !ok {
			break
		}
		if it.mark == markGaveUp {
			t.Fatal("the follow gave the task up as the viff stopped")
		}
	}
}

// writeCounter is a store that counts the writes made to one task's record.
type writeCounter struct {
	entity.Store
	mu     sync.Mutex
	record int
}

func (c *writeCounter) Write(w entity.Write) (bool, int64, error) {
	if w.At.Ref == Ref("p") && w.At.Access == "" {
		c.mu.Lock()
		c.record++
		c.mu.Unlock()
	}
	return c.Store.Write(w)
}

// A TASK THAT SAYS MORE THAN THE STORE TAKES IS WRITTEN AS THE STORE TAKES IT. Frames queued behind
// one another are one write, the last one's, and the end is on the record without a write for
// each frame before it.
func TestFramesQueuedTogetherAreOneWrite(t *testing.T) {
	store := &writeCounter{Store: entitytest.Store(t)}
	q := newFrames()
	for i := 1; i <= 50; i++ {
		q.put(item{ev: task.Event{Kind: task.EventSay, Text: "working", Cursor: int64(i)}})
	}
	q.put(item{ev: task.Event{Kind: task.EventStatus, Text: string(task.Done), Cursor: 51}})
	q.close()
	record(store, "p", entity.Obj{"status": string(task.Running)}, Meta{Runner: "http://runner.invalid"}, &leasedHandle{}, q)
	if got := entity.Str(mustGet(t, store, "p"), "status"); got != string(task.Done) {
		t.Fatalf("status = %q, want the end on the record", got)
	}
	store.mu.Lock()
	n := store.record
	store.mu.Unlock()
	if n > 5 {
		t.Fatalf("%d writes of the record for 51 frames queued together", n)
	}
}

// A SUBSCRIBE IS WAITED FOR A QUARTER LEASE AT MOST, the time to the next one, and THE STREAM IT
// REPLACES IS CLOSED: an open connection renews nothing, and one left open is a stream the
// runner keeps serving for nobody.
func TestEachSubscribeIsWaitedForAQuarterLeaseAndReplacesTheLast(t *testing.T) {
	store := entitytest.Store(t)
	h := &leasedHandle{}
	Reflect(store, "p", h, Meta{Runner: "http://runner.invalid", Lease: "400ms"})
	waitFor(t, "four subscribes", func() bool { return len(h.subscribes()) >= 4 })
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, w := range h.within {
		if w != 100*time.Millisecond {
			t.Fatalf("subscribe %d was waited for %s, want a quarter of the 400ms lease", i+1, w)
		}
	}
	for i, closed := range h.closed[:len(h.closed)-1] {
		if !closed {
			t.Fatalf("subscribe %d's stream was left open when the next was asked for", i+1)
		}
	}
}

// A RUNNER REACHED AGAIN BEFORE THE WAIT IS OVER SAYS HOW THE TASK ENDED. Its subscribes go
// unanswered for longer than the lease and less than the wait, and the runner ended the task at
// its lease meanwhile: the record takes the runner's end, its reason the runner's, and not the
// mirror's own account of a runner that did not answer.
func TestAHealBeforeTheWaitTakesTheRunnersOwnEnd(t *testing.T) {
	was := waitMargin
	waitMargin = 2 * time.Second
	t.Cleanup(func() { waitMargin = was })

	store := entitytest.Store(t)
	h := &leasedHandle{}
	Reflect(store, "p", h, Meta{Runner: "http://runner.invalid", Lease: "400ms"})
	waitFor(t, "a second subscribe", func() bool { return len(h.subscribes()) >= 2 })
	h.mu.Lock()
	h.refuse = true
	h.mu.Unlock()
	time.Sleep(600 * time.Millisecond) // past the lease, inside the wait
	h.mu.Lock()
	h.refuse = false
	h.next = []task.Event{{Kind: task.EventStatus, Text: string(task.Failed), Orphaned: true, Err: "no reader for 400ms", Cursor: 9}}
	h.mu.Unlock()

	waitFor(t, "the task to end on its record", func() bool { return task.TerminalStatus(entity.Str(mustGet(t, store, "p"), "status")) })
	got := entity.ToObj(mustGet(t, store, "p"))
	if got["status"] != string(task.Failed) || got[OrphanedField] != true || got["err"] != "no reader for 400ms" {
		t.Fatalf("record = %v, want the runner's own end", got)
	}
}
