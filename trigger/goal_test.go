package trigger

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/example/infra/verse/entity"
)

// Unmet returns exactly the leaf requirements that do not currently hold — the "waiting on"
// complement of Eval's witness.
func TestFormulaUnmet(t *testing.T) {
	store := entity.NewMemStore()
	f := &Formula{Op: OpUntil, Args: []*Formula{
		{Op: OpAtom, Req: &Requirement{Kind: ReqFact, Facts: FactSel{System: "ci", Kind: "status", Where: map[string]string{"state": "green"}}}},
		{Op: OpAtom, Req: &Requirement{Kind: ReqDeadline, Within: "4h"}}, // omitted from Unmet (a time derivation)
	}}

	// Nothing satisfied → the fact leaf is unmet; the deadline leaf is never listed.
	un := f.Unmet(store)
	if len(un) != 1 || un[0].Kind != ReqFact {
		t.Fatalf("unmet = %v, want [fact]", un)
	}
	if lbl := un[0].Label(); lbl != "fact:ci:status" {
		t.Fatalf("label = %q, want fact:ci:status", lbl)
	}

	// Satisfy the fact → no unmet leaves.
	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})
	if un := f.Unmet(store); len(un) != 0 {
		t.Fatalf("unmet after satisfying = %v, want none", un)
	}
}

// A goal whose doneWhen is a fact: before the fact it is active/blocked with a waitingOn
// list and no done latch; once the fact appears it latches goal:done exactly once, marks the
// instance terminal, and reaps its owned trigger.
func TestGoalDoneLatch(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	err := eng.AddGoal(GoalDef{
		Name:    "ship",
		Subject: "change:pr:42",
		DoneWhen: &Formula{Op: OpAtom, Req: &Requirement{
			Kind: ReqFact, Facts: FactSel{System: "ci", Kind: "status", Where: map[string]string{"state": "green"}},
		}},
		Triggers: []Def{{
			Name:    "ship-review",
			Matches: []EventMatch{{System: "change", Fields: map[string]any{"stage": "open"}}},
			Action:  ActionSpec{Set: map[string]any{"stage": "reviewing"}},
		}},
	})
	if err != nil {
		t.Fatalf("AddGoal: %v", err)
	}

	// The owned trigger is installed (the doing).
	if _, ok := eng.Def("ship-review"); !ok {
		t.Fatal("owned trigger not installed")
	}

	// Not yet done: no latch, status blocked, waitingOn names the fact.
	if existsWithin(store, goalDoneRef("ship"), 100*time.Millisecond) {
		t.Fatal("goal latched done before the fact held")
	}
	g, _ := store.Get(goalRef("ship"))
	if g.Payload["terminal"] != false {
		t.Fatalf("terminal = %v, want false", g.Payload["terminal"])
	}
	if s, _ := g.Payload["status"].(string); s != "blocked" {
		t.Fatalf("status = %q, want blocked", s)
	}
	if w, _ := g.Payload["waitingOn"].([]string); len(w) != 1 || w[0] != "fact:ci:status" {
		t.Fatalf("waitingOn = %v, want [fact:ci:status]", g.Payload["waitingOn"])
	}

	// Satisfy doneWhen → done latch, terminal, owned trigger reaped.
	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})
	if !existsWithin(store, goalDoneRef("ship"), 2*time.Second) {
		t.Fatal("goal did not latch done after doneWhen held")
	}
	done, _ := store.Get(goalDoneRef("ship"))
	if done.Payload["goal"] != "ship" || done.Payload["subject"] != "change:pr:42" {
		t.Fatalf("done fact = %+v", done.Payload)
	}
	// The instance is terminal with status done.
	waitForStatus(t, store, "ship", "done")
	if g, _ := store.Get(goalRef("ship")); g.Payload["terminal"] != true {
		t.Fatalf("terminal = %v, want true", g.Payload["terminal"])
	}
	// The owned trigger is reaped (no orphaned charter).
	if _, ok := eng.Def("ship-review"); ok {
		t.Fatal("owned trigger not reaped on terminal")
	}
	// No failure latch on the success path.
	if _, ok := store.Get(goalFailedRef("ship")); ok {
		t.Fatal("unexpected fail latch on the done path")
	}
}

// A goal whose deadline elapses with doneWhen unmet latches goal:failed exactly once and
// marks the instance unachievable.
func TestGoalDeadlineFail(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	if err := eng.AddGoal(GoalDef{
		Name:     "flaky",
		Subject:  "change:pr:7",
		Deadline: "40ms",
		DoneWhen: &Formula{Op: OpAtom, Req: &Requirement{
			Kind: ReqFact, Facts: FactSel{System: "ci", Kind: "status", Where: map[string]string{"state": "green"}},
		}},
	}); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}

	if !existsWithin(store, goalFailedRef("flaky"), 2*time.Second) {
		t.Fatal("goal did not latch failed after the deadline elapsed")
	}
	fail, _ := store.Get(goalFailedRef("flaky"))
	if fail.Payload["reason"] != "deadline" {
		t.Fatalf("fail fact = %+v, want reason=deadline", fail.Payload)
	}
	waitForStatus(t, store, "flaky", "unachievable")
	if _, ok := store.Get(goalDoneRef("flaky")); ok {
		t.Fatal("unexpected done latch on the fail path")
	}
}

// Done wins a done/deadline tie: a goal whose doneWhen already holds before the deadline
// latches done, never failed — even with a short deadline armed.
func TestGoalDoneWinsDeadline(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	// doneWhen already satisfied at install.
	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	if err := eng.AddGoal(GoalDef{
		Name:     "already",
		Deadline: "20ms",
		DoneWhen: &Formula{Op: OpAtom, Req: &Requirement{
			Kind: ReqFact, Facts: FactSel{System: "ci", Kind: "status", Where: map[string]string{"state": "green"}},
		}},
	}); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	if !existsWithin(store, goalDoneRef("already"), 2*time.Second) {
		t.Fatal("did not latch done for an already-satisfied goal")
	}
	// Give the (short) deadline time to elapse; it must not have latched failed.
	time.Sleep(60 * time.Millisecond)
	if _, ok := store.Get(goalFailedRef("already")); ok {
		t.Fatal("fail latched despite doneWhen holding — done should win the tie")
	}
}

// commitGoal is terminal-once: a first "done" latches and freezes, and a later contradicting
// "unachievable" is a no-op — the outcome stays done and no fail fact is written.
func TestGoalTerminalOnce(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	g := &Goal{Def: GoalDef{Name: "once", Subject: "change:pr:1"}}
	store.Put(goalRef("once"), entity.Payload{"name": "once", "status": "active", "terminal": false})

	eng.commitGoal(g, "done", []entity.Ref{{System: "ci", Kind: "status", ID: "x"}})
	if inst, _ := store.Get(goalRef("once")); inst.Payload["outcome"] != "done" || inst.Payload["terminal"] != true {
		t.Fatalf("first commit: %+v, want outcome=done terminal=true", inst.Payload)
	}
	if _, ok := store.Get(goalDoneRef("once")); !ok {
		t.Fatal("done fact not written")
	}

	// A contradicting second commit must not change the latched outcome or write a fail fact.
	eng.commitGoal(g, "unachievable", nil)
	if inst, _ := store.Get(goalRef("once")); inst.Payload["outcome"] != "done" {
		t.Fatalf("terminal-once violated: outcome now %v", inst.Payload["outcome"])
	}
	if _, ok := store.Get(goalFailedRef("once")); ok {
		t.Fatal("a frozen-out commit still wrote a fail fact")
	}
}

// Concurrent add / drive / list / steer / remove across many goals — run under -race to
// exercise the goalsMu→trigMu→flowsMu lock ordering and the terminal-once commit for hangs,
// deadlocks, and data races.
func TestGoalConcurrentLifecycle(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		name := "g" + strconv.Itoa(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = eng.AddGoal(GoalDef{
				Name:    name,
				Subject: "change:pr:" + strconv.Itoa(i),
				DoneWhen: &Formula{Op: OpAtom, Req: &Requirement{
					Kind: ReqFact, Facts: FactSel{System: "ci", Kind: name, Where: map[string]string{"state": "green"}},
				}},
				Deadline: "3s",
			})
			// Concurrent readers/steerers racing the lifecycle.
			eng.Goals()
			eng.PauseGoal(name)
			eng.SteerGoal(name, "hi", "test")
			if i%3 == 0 {
				eng.RemoveGoal(name) // remove some mid-flight
				return
			}
			// Drive the rest to done via their (distinct) fact subtree.
			store.Put(entity.Ref{System: "ci", Kind: name, ID: "build"}, entity.Payload{"state": "green"})
		}()
	}
	wg.Wait()

	// The goals we drove (not removed) must all reach done within the deadline window.
	for i := 0; i < n; i++ {
		if i%3 == 0 {
			continue
		}
		if !existsWithin(store, goalDoneRef("g"+strconv.Itoa(i)), 3*time.Second) {
			t.Fatalf("goal g%d never latched done", i)
		}
	}
}

// waitForStatus polls the goal:instance status until it equals want (or times out).
func waitForStatus(t *testing.T, s entity.Store, name, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if g, ok := s.Get(goalRef(name)); ok {
			if got, _ := g.Payload["status"].(string); got == want {
				return
			}
		}
		select {
		case <-deadline:
			g, _ := s.Get(goalRef(name))
			t.Fatalf("status never reached %q; last = %+v", want, g.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
