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

	"github.com/signadot/verse/codec"
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
	if e, _, _ := store.Get(entity.Ref{System: "demo", Kind: "pr", ID: "4"}); e.Payload["stage"] != "open" {
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
		if e, _, _ := store.Get(entity.Ref{System: "demo", Kind: "pr", ID: id}); e.Payload["stage"] == "shipped" {
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

// An ADDRESS GLOB is the string spelling of a set-valued Subject, and it must dispatch
// identically to the object form — `"demo:pr:*"` is the whole kind, exactly as `verse flow
// demo:pr:*` addresses the whole kind's live processes. It used to parse to a `ref` naming an
// entity literally called "*", so nothing fanned out and `set` created `demo:pr:*` instead.
func TestGlobSubjectFansOut(t *testing.T) {
	store := entity.NewMemStore()
	open := prs(store, "open", "1", "2", "3")

	eng := New(store, process.AutoResolver{})
	var tr Trigger
	if err := codec.Unmarshal([]byte(`{"name":"ship-all",
		"condition":{"when":{"system":"ci","kind":"status"},"subject":"demo:pr:*"},
		"action":{"set":{"stage":"shipped"}}}`), &tr, codec.JSON); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})

	for _, ref := range open {
		waitStage(t, store, ref, "shipped")
	}
	// And no entity called "*" was conjured by the action's `set`.
	if e, ok, _ := store.Get(entity.Ref{System: "demo", Kind: "pr", ID: "*"}); ok {
		t.Fatalf("the glob was named instead of selected: created demo:pr:* = %#v", e.Payload)
	}
}

// An id glob selects a FAMILY within a kind, not the whole kind.
func TestIdGlobSelectsAFamily(t *testing.T) {
	store := entity.NewMemStore()
	prs(store, "open", "hotfix-1", "hotfix-2", "feature-9")

	eng := New(store, process.AutoResolver{})
	var tr Trigger
	if err := codec.Unmarshal([]byte(`{"name":"ship-hotfixes",
		"condition":{"when":{"system":"ci","kind":"status"},"subject":"demo:pr:hotfix-*"},
		"action":{"set":{"stage":"shipped"}}}`), &tr, codec.JSON); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})

	waitStage(t, store, entity.Ref{System: "demo", Kind: "pr", ID: "hotfix-1"}, "shipped")
	waitStage(t, store, entity.Ref{System: "demo", Kind: "pr", ID: "hotfix-2"}, "shipped")
	if e, _, _ := store.Get(entity.Ref{System: "demo", Kind: "pr", ID: "feature-9"}); e.Payload["stage"] != "open" {
		t.Fatalf("the glob overreached beyond its family: %#v", e.Payload)
	}
}

// A CONCRETE address still NAMES its entity, so a `set` brings it into existence — the way a
// trigger publishes a fact nothing has written yet. Routing the string form through the address
// grammar must not quietly turn this into a select-what-exists no-op.
func TestConcreteSubjectNamesAndCreates(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	var tr Trigger
	if err := codec.Unmarshal([]byte(`{"name":"record",
		"condition":{"when":{"system":"ci","kind":"status"},"subject":"audit:record:latest"},
		"action":{"set":{"stage":"noted"}}}`), &tr, codec.JSON); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})
	waitStage(t, store, entity.Ref{System: "audit", Kind: "record", ID: "latest"}, "noted")
}

// A completion trigger's subject pattern FILTERS which flows it acts on: a process whose flow
// is not what the subject names is a no-op — never a misfire onto the wrong entity. This is the
// filter the old `$flow` deref lacked (it advanced whatever the payload carried, unconstrained).
func TestSubjectPatternFiltersTheFlow(t *testing.T) {
	store := entity.NewMemStore()
	other := entity.Ref{System: "other", Kind: "thing", ID: "1"}
	store.Put(other, entity.Payload{"stage": "x"})

	eng := New(store, process.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "reviewed",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "process", "kind": "instance", "fields": map[string]any{"status": "completed"}}),
			Subject: mm("demo:pr:*"), // acts on demo:pr flows only
		},
		Action: Action{Set: map[string]any{"stage": "reviewed"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// A completed process whose flow is NOT a demo:pr — the subject pattern doesn't name it.
	store.Put(entity.Ref{System: "process", Kind: "instance", ID: "rev-1"},
		entity.Payload{"status": "completed", "flow": other.String()})

	eventuallyN(t, "the firing to be observed and discarded", func() bool { return eng.Fired() == 0 })
	time.Sleep(50 * time.Millisecond) // give a wrong action time to land, if it were going to
	if e, _, _ := store.Get(other); e.Payload["stage"] != "x" {
		t.Fatalf("acted on a flow the subject doesn't name: %s = %#v", other, e.Payload)
	}
}
