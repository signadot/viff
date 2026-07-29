package process

import (
	"context"
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
	d1 := GateQuestion{ID: "a#d1", Origin: "a", Mode: Ask, Gate: "g"}
	d2 := GateQuestion{ID: "b#d1", Origin: "b", Mode: Ask, Gate: "g"}
	res1 := make(chan Resolution, 1)
	res2 := make(chan Resolution, 1)
	go func() { rr, _ := r.Resolve(context.Background(), d1, ToolUse{Name: "t1"}); res1 <- rr }()
	go func() { rr, _ := r.Resolve(context.Background(), d2, ToolUse{Name: "t2"}); res2 <- rr }()

	waitFor(t, func() bool { return len(r.Pending()) == 2 })

	// answer out of order
	if !r.Answer("b#d1", Resolution{Proceed: true, By: "human", ChildMode: ChildAutonomous}) {
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
	if !r2.Proceed || r2.Question.ID != "b#d1" || r2.ChildMode != ChildAutonomous {
		t.Fatalf("b resolved wrong: %+v", r2)
	}
	if len(r.Pending()) != 0 {
		t.Fatalf("pending should be empty: %v", r.Pending())
	}
	if r.Answer("unknown", Resolution{Proceed: true}) {
		t.Fatal("answering an unknown ID should return false")
	}
}

// The ChildMode chosen when answering a CallAgent ASK reaches the spawn.
func TestChildModeFromAskAnswer(t *testing.T) {
	reg := NewRegistry()
	vec := &Vector{Default: Auto, Gates: []Gate{
		{Name: "delegate", Mode: Ask, Match: GateMatch{Verbs: []Verb{CallAgent}}},
	}}
	parent := NewProcess("p", NewSupervisor(vec, reg), &callThenDone{}, &recExec{})

	gotMode := ChildCareful // sentinel (not the expected answer)
	spawned := make(chan struct{}, 1)
	parent.SetSpawn(func(pProc *Process, mode ChildMode, req *CallAgentRequest) (*Process, error) {
		gotMode = mode
		spawned <- struct{}{}
		return pProc.NewChild("kid", mode, req, &scriptDriver{}, &recExec{})
	})

	done := make(chan struct{})
	go func() { parent.Run(context.Background()); close(done) }()

	// the CallAgent's ASK gate posts a pending decision; answer it "autonomous"
	waitFor(t, func() bool { return len(reg.Pending()) == 1 })
	p := reg.Pending()[0]
	if p.Question.Origin != "p" {
		t.Fatalf("decision origin = %q, want p", p.Question.Origin)
	}
	reg.Answer(p.Question.ID, Resolution{Proceed: true, By: "human", ChildMode: ChildAutonomous})

	<-spawned
	<-done
	if gotMode != ChildAutonomous {
		t.Fatalf("child spawned with mode %s, want autonomous", gotMode)
	}
}

// A re-posted question is JOINED, not duplicated: one docket entry, and one answer releases
// every caller waiting on it. This is what makes a deterministic gate id safe — a poser that
// re-asks about the same subject asks the standing question again rather than filling the
// docket with copies of it.
func TestRegistryJoinsAQuestionAlreadyPending(t *testing.T) {
	r := NewRegistry()
	const id = "trigger:ship:ship~deadbeef"
	q := GateQuestion{ID: id, Origin: "ship", Mode: Ask, Gate: "ship", Subject: "demo:pr:1"}

	posts := 0
	r.OnPost(func(Pending) { posts++ })

	const n = 4
	res := make(chan Resolution, n)
	for range n {
		go func() {
			got, err := r.Resolve(context.Background(), q, ToolUse{Name: "trigger:ship"})
			if err != nil {
				t.Errorf("Resolve: %v", err)
			}
			res <- got
		}()
	}
	// All four are waiting on ONE question.
	waitFor(t, func() bool { return len(r.Pending()) == 1 && posts == 1 })

	if !r.Answer(id, Resolution{Proceed: true, By: "alice"}) {
		t.Fatal("Answer found no pending question")
	}
	for range n {
		select {
		case got := <-res:
			if !got.Proceed || got.By != "alice" {
				t.Fatalf("waiter got %+v, want alice/proceed", got)
			}
			if got.Question.Subject != "demo:pr:1" {
				t.Fatalf("waiter lost the question: %+v", got.Question)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a joined waiter was never released — the answer did not broadcast")
		}
	}
	if p := r.Pending(); len(p) != 0 {
		t.Fatalf("answered question still pending: %+v", p)
	}
	if posts != 1 {
		t.Fatalf("the question was announced %d times, want 1 — a join is not a new question", posts)
	}
}

// One waiter giving up does not take the question away from the others. Without the waiter
// count, a cancelled caller would delete a question the rest are still blocked on, and they
// would wait forever on something no longer answerable.
func TestRegistryCancelledWaiterLeavesTheQuestionStanding(t *testing.T) {
	r := NewRegistry()
	const id = "trigger:ship:ship~cafe"
	q := GateQuestion{ID: id, Origin: "ship", Mode: Ask}

	stay := make(chan Resolution, 1)
	go func() {
		got, _ := r.Resolve(context.Background(), q, ToolUse{Name: "t"})
		stay <- got
	}()
	waitFor(t, func() bool { return len(r.Pending()) == 1 })

	leaving, cancel := context.WithCancel(context.Background())
	left := make(chan struct{})
	go func() {
		_, _ = r.Resolve(leaving, q, ToolUse{Name: "t"})
		close(left)
	}()
	waitFor(t, func() bool { return len(r.Pending()) == 1 })
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
