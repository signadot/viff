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
