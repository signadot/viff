package task

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// Concurrent blocking Resolve calls are answered by ID, out of order, without
// mismatch — the decision-addressed seam.
func TestRegistryConcurrentResolveByID(t *testing.T) {
	r := NewRegistry()
	d1 := GateQuestion{ID: "a#d1", Origin: "a", Gate: "g"}
	d2 := GateQuestion{ID: "b#d1", Origin: "b", Gate: "g"}
	res1 := make(chan Resolution, 1)
	res2 := make(chan Resolution, 1)
	go func() { rr, _ := r.Resolve(context.Background(), d1, ToolUse{Name: "t1"}); res1 <- rr }()
	go func() { rr, _ := r.Resolve(context.Background(), d2, ToolUse{Name: "t2"}); res2 <- rr }()

	waitFor(t, func() bool { return len(r.Pending()) == 2 })

	// answer out of order
	if !r.Answer("b#d1", Resolution{Proceed: true, By: "human"}) {
		t.Fatal("answer b failed")
	}
	if !r.Answer("a#d1", Resolution{Proceed: false, By: "human"}) {
		t.Fatal("answer a failed")
	}

	r1 := <-res1
	r2 := <-res2
	if r1.Proceed || r1.Question.ID != "a#d1" {
		t.Fatalf("a resolved wrong: %+v", r1)
	}
	if !r2.Proceed || r2.Question.ID != "b#d1" {
		t.Fatalf("b resolved wrong: %+v", r2)
	}
	if len(r.Pending()) != 0 {
		t.Fatalf("pending should be empty: %v", r.Pending())
	}
	if r.Answer("unknown", Resolution{Proceed: true}) {
		t.Fatal("answering an unknown ID should return false")
	}
}

// A re-posted question is JOINED, not duplicated: one docket entry, and one answer releases
// every caller waiting on it. This is what makes a deterministic gate id safe — a poser that
// re-asks about the same subject asks the standing question again rather than filling the
// docket with copies of it.
func TestRegistryJoinsAQuestionAlreadyPending(t *testing.T) {
	r := NewRegistry()
	const id = "mirror.trigger.ship.ship~deadbeef"
	q := GateQuestion{ID: id, Origin: "ship", Gate: "ship", Subject: `demo.pr."1"`}

	var posts atomic.Int32
	r.OnPost(func(Pending) { posts.Add(1) })

	const n = 4
	res := make(chan Resolution, n)
	for range n {
		go func() {
			got, err := r.Resolve(context.Background(), q, ToolUse{Name: "mirror.trigger.ship"})
			if err != nil {
				t.Errorf("Resolve: %v", err)
			}
			res <- got
		}()
	}
	// All four are waiting on ONE question. Wait on the waiter count, not on Pending(): a
	// joiner that has not arrived yet would otherwise be answered before it joined, and would
	// then post the id afresh — correct behaviour (the question it wanted was already gone)
	// but not what this test is about.
	waitFor(t, func() bool { return r.WaiterCount(id) == n })
	if p := r.Pending(); len(p) != 1 {
		t.Fatalf("%d questions on the docket for one id: %+v", len(p), p)
	}

	if !r.Answer(id, Resolution{Proceed: true, By: "alice"}) {
		t.Fatal("Answer found no pending question")
	}
	for range n {
		select {
		case got := <-res:
			if !got.Proceed || got.By != "alice" {
				t.Fatalf("waiter got %+v, want alice/proceed", got)
			}
			if got.Question.Subject != `demo.pr."1"` {
				t.Fatalf("waiter lost the question: %+v", got.Question)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a joined waiter was never released — the answer did not broadcast")
		}
	}
	if p := r.Pending(); len(p) != 0 {
		t.Fatalf("answered question still pending: %+v", p)
	}
	if posts.Load() != 1 {
		t.Fatalf("the question was announced %d times, want 1 — a join is not a new question", posts.Load())
	}
}

// One waiter giving up does not take the question away from the others. Without the waiter
// count, a cancelled caller would delete a question the rest are still blocked on, and they
// would wait forever on something no longer answerable.
func TestRegistryCancelledWaiterLeavesTheQuestionStanding(t *testing.T) {
	r := NewRegistry()
	const id = "mirror.trigger.ship.ship~cafe"
	q := GateQuestion{ID: id, Origin: "ship"}

	stay := make(chan Resolution, 1)
	go func() {
		got, _ := r.Resolve(context.Background(), q, ToolUse{Name: "t"})
		stay <- got
	}()
	waitFor(t, func() bool { return r.WaiterCount(id) == 1 })

	leaving, cancel := context.WithCancel(context.Background())
	left := make(chan struct{})
	go func() {
		_, _ = r.Resolve(leaving, q, ToolUse{Name: "t"})
		close(left)
	}()
	waitFor(t, func() bool { return r.WaiterCount(id) == 2 })
	cancel()
	<-left

	if p := r.Pending(); len(p) != 1 {
		t.Fatalf("pending = %d after one waiter cancelled, want 1 (the other is still asking)", len(p))
	}
	if !r.Answer(id, Resolution{Proceed: true, By: "bob"}) {
		t.Fatal("the question was taken off the docket by a departing waiter")
	}
	select {
	case got := <-stay:
		if !got.Proceed {
			t.Fatalf("remaining waiter got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remaining waiter never released")
	}
}

// AN ANSWER TO AN EARLIER EDIT IS REFUSED where the answer is delivered, and a revised question
// keeps whoever waits on it.
func TestDeliverRefusesAnEarlierEdit(t *testing.T) {
	r := NewRegistry()
	q := GateQuestion{ID: "ask-1", Edit: 1}
	got := make(chan Resolution, 1)
	go func() {
		res, _ := r.Resolve(context.Background(), q, ToolUse{})
		got <- res
	}()
	waitFor(t, func() bool { return len(r.Pending()) == 1 })
	q.Edit = 2
	if !r.Revise(q) {
		t.Fatal("revising a pending question was refused")
	}
	if err := r.Deliver("ask-1", Resolution{Choice: Proceed, By: "user:a", Edit: 1}); err != ErrStaleEdit {
		t.Fatalf("an answer to edit 1 of an ask at edit 2: %v, want ErrStaleEdit", err)
	}
	if err := r.Deliver("ask-1", Resolution{Choice: Proceed, By: "user:a", Edit: 2}); err != nil {
		t.Fatalf("an answer to the edit standing: %v", err)
	}
	select {
	case res := <-got:
		if res.Question.Edit != 2 {
			t.Fatalf("the waiter was answered about edit %d, want 2", res.Question.Edit)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter was not released")
	}
}
