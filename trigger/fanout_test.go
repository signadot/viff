package trigger

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// prs seeds n demo:pr entities at a stage, returning their refs.
func prs(store entity.Store, stage string, ids ...string) []entity.Ref {
	out := make([]entity.Ref, 0, len(ids))
	for _, id := range ids {
		ref := entity.Ref{System: "demo", Kind: "pr", ID: id}
		store.Put(ref, entity.Payload{"stage": stage})
		out = append(out, ref)
	}
	return out
}

// BROADCAST — the genuinely new dispatch path. A change UNRELATED to the subject fires once,
// and the set-valued Subject fans the action out over every matching entity: one action per
// member, each about exactly one subject.
func TestSetValuedSubjectFansOut(t *testing.T) {
	store := entity.NewMemStore()
	reviewed := prs(store, "reviewed", "1", "2", "3")
	prs(store, "open", "4") // not selected — wrong stage

	eng := New(store, process.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "ship-all-reviewed",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "ci", "kind": "status"}),
			Subject: Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Set: map[string]any{"stage": "shipped"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// ONE change, about nothing in particular — the fan-out comes from the Subject.
	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})

	for _, ref := range reviewed {
		waitStage(t, store, ref, "shipped")
	}
	// The non-matching PR is untouched: the Subject selected a set, not everything.
	if e, _ := store.Get(entity.Ref{System: "demo", Kind: "pr", ID: "4"}); e.Payload["stage"] != "open" {
		t.Fatalf("an unselected entity was acted on: %#v", e.Payload)
	}
	// Budget metering is per entity acted on, so three members are three.
	if f := eng.Fired(); f != 3 {
		t.Fatalf("fired = %d, want 3 (one per fanned-out subject)", f)
	}
}

// Each fanned-out member gets its own process, with $subject bound to THAT member — so a
// broadcast spawn is N processes each about one subject, not one process about a set.
func TestFanOutSpawnsPerMember(t *testing.T) {
	store := entity.NewMemStore()
	prs(store, "reviewed", "1", "2", "3")

	eng := New(store, process.AutoResolver{})
	type spawned struct {
		id   string
		spec launch.Spec
	}
	got := make(chan spawned, 8)
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		got <- spawned{id, spec}
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(id, sup, blockForever{}, noExec{})
	})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "ci", "kind": "status"}),
			Subject: Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Spawn: &launch.Spec{Task: "ship $subject"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})

	seen := map[string]string{} // subject -> task
	ids := map[string]bool{}
	for range 3 {
		select {
		case s := <-got:
			seen[s.spec.Subject] = s.spec.Task
			ids[s.id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of 3 members spawned: %v", len(seen), seen)
		}
	}
	for _, id := range []string{"1", "2", "3"} {
		ref := "demo:pr:" + id
		if seen[ref] != "ship "+ref {
			t.Errorf("member %s: task = %q, want $subject bound to that member", ref, seen[ref])
		}
	}
	if len(ids) != 3 {
		t.Fatalf("members shared a spawn id (%v) — control would be cross-wired", ids)
	}
	// And each is its own flow, addressable as a set.
	if n := len(eng.matchFlow(mustExpr(t, "demo:pr:*"))); n != 3 {
		t.Fatalf("%d live processes for 3 members", n)
	}
}

// A re-fire over the same set must be IDEMPOTENT per member: spawnID is deterministic per
// (trigger, subject), so the second broadcast re-uses each member's id rather than starting a
// duplicate. This is what makes a broadcast safe to re-trigger.
func TestFanOutRefireIsIdempotentPerMember(t *testing.T) {
	store := entity.NewMemStore()
	prs(store, "reviewed", "1", "2")

	eng := New(store, process.AutoResolver{})
	// The spawner runs on an engine goroutine while the test polls, so the counter must be
	// atomic — a plain int here is a data race, not a flake.
	var spawns atomic.Int64
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		spawns.Add(1)
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(id, sup, blockForever{}, noExec{})
	})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "ci", "kind": "status"}),
			Subject: Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Spawn: &launch.Spec{Task: "ship $subject"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ci := entity.Ref{System: "ci", Kind: "status", ID: "build"}
	store.Put(ci, entity.Payload{"state": "green"})
	eventuallyN(t, "2 spawns", func() bool { return spawns.Load() == 2 })

	// Fire again over the same set — same members, same ids, so no new processes.
	store.Put(ci, entity.Payload{"state": "green", "run": "2"})
	time.Sleep(300 * time.Millisecond)
	if n := spawns.Load(); n != 2 {
		t.Fatalf("a re-fire spawned %d processes; want 2 (idempotent per member)", n)
	}
}

// The runaway backstop meters ENTITIES ACTED ON, not firings — a fan-out over more members
// than the budget allows is truncated rather than dropped whole, and stops at the cap.
func TestFanOutBudgetMetersEntities(t *testing.T) {
	store := entity.NewMemStore()
	ids := make([]string, 0, 10)
	for i := range 10 {
		ids = append(ids, fmt.Sprint(i))
	}
	prs(store, "reviewed", ids...)

	eng := New(store, process.AutoResolver{})
	eng.SetMaxFires(4) // fewer than the 10 members
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "ci", "kind": "status"}),
			Subject: Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Set: map[string]any{"stage": "shipped"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})

	time.Sleep(400 * time.Millisecond)
	if f := eng.Fired(); f != 4 {
		t.Fatalf("fired = %d, want exactly the cap of 4 (metered per entity, truncated not dropped)", f)
	}
	shipped := 0
	for _, id := range ids {
		if e, _ := store.Get(entity.Ref{System: "demo", Kind: "pr", ID: id}); e.Payload["stage"] == "shipped" {
			shipped++
		}
	}
	if shipped != 4 {
		t.Fatalf("%d entities advanced, want 4 — a truncated fan-out must still act on what it reserved", shipped)
	}
}

// A set-valued Subject that currently matches nothing is a no-op, not an error: the firing
// simply has no members to be about.
func TestFanOutOverEmptySetIsANoOp(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "ci", "kind": "status"}),
			Subject: Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Set: map[string]any{"stage": "shipped"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})
	time.Sleep(250 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("fired %d times over an empty subject set", f)
	}
}

// Select is sorted, so a truncated fan-out is deterministic rather than picking arbitrary
// members from a map iteration.
func TestSubjectSelectionIsDeterministic(t *testing.T) {
	store := entity.NewMemStore()
	prs(store, "reviewed", "b", "a", "c")
	m := Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": "reviewed"}}
	want := []string{"demo:pr:a", "demo:pr:b", "demo:pr:c"}
	for range 20 { // map iteration order varies per range, so repeat
		refs := m.Select(store)
		for i, r := range refs {
			if r.String() != want[i] {
				t.Fatalf("Select order = %v, want %v", refs, want)
			}
		}
	}
}

// A broadcast raises ONE gate for the firing, not one per member — an operator approving "ship
// every reviewed PR" answers a single question.
func TestFanOutRaisesOneGatePerFiring(t *testing.T) {
	store := entity.NewMemStore()
	prs(store, "reviewed", "1", "2", "3")

	asked := make(chan process.GateQuestion, 8)
	eng := New(store, countingResolver{asked})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "ci", "kind": "status"}),
			Subject: Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": "reviewed"}},
		},
		Gates:  []Gate{{Name: "ship", Mode: "ask"}},
		Action: Action{Set: map[string]any{"stage": "shipped"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})

	// All three members advance off one approval.
	for _, id := range []string{"1", "2", "3"} {
		waitStage(t, store, entity.Ref{System: "demo", Kind: "pr", ID: id}, "shipped")
	}
	if n := len(asked); n != 1 {
		t.Fatalf("a broadcast posed %d gates, want 1 per firing", n)
	}
}

// countingResolver approves everything and records what it was asked.
type countingResolver struct{ asked chan process.GateQuestion }

func (c countingResolver) Resolve(_ context.Context, q process.GateQuestion, _ process.ToolUse) (process.Resolution, error) {
	c.asked <- q
	return process.Resolution{Proceed: true, By: "test"}, nil
}

func eventuallyN(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", why)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
