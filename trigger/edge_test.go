package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// ci writes a ci:status entity at a state.
func ci(t *testing.T, store entity.Store, state string) {
	t.Helper()
	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"),
		entity.Obj{"state": state}.Payload())
}

// onWhen builds the canonical edge trigger: on CI going green, if `when` holds, ship every
// reviewed PR.
func onWhen(when *Formula) Trigger {
	return Trigger{
		Name: "ship",
		Condition: Condition{
			On:      MatchWhen(Match{"system": "ci", "kind": "status", "value": map[string]any{"state": "green"}}),
			When:    when,
			Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}
}

// The whole rule in one test: the cause RISES and the subject set is acted on, one firing per
// member — and the entity whose change caused it is not among them.
func TestOnFiresOnTheRisingEdge(t *testing.T) {
	store := entitytest.Store(t)
	reviewed := prs(t, store, "reviewed", "1", "2")
	prs(t, store, "open", "3") // wrong stage — not selected

	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(onWhen(nil)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ci(t, store, "red") // the cause does not hold: nothing rises, nothing fires
	time.Sleep(200 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("fired %d times before the cause held", f)
	}

	ci(t, store, "green") // ¬on(C-1) ∧ on(C)
	for _, ref := range reviewed {
		waitStage(t, store, ref, "shipped")
	}
	if e, _, _ := store.Get(entity.RefAt("demo", "pr", "3")); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("an unselected entity was acted on: %#v", e.Payload)
	}
	// One firing per member, which is what the budget meters.
	if f := eng.Fired(); f != 2 {
		t.Fatalf("fired = %d, want 2 (one per selected subject)", f)
	}
}

// An EDGE DOES NOT RE-FIRE while it stays true. This is the property that makes `on` a cause
// rather than a condition, and the one an author has to know: a predicate already satisfied
// says nothing when it is satisfied again.
func TestOnDoesNotReFireWhileItHolds(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(onWhen(nil)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	prs(t, store, "reviewed", "1")
	ci(t, store, "green")
	waitStage(t, store, entity.RefAt("demo", "pr", "1"), "shipped")

	// A second PR reaches `reviewed` while CI is still green. The predicate never fell, so
	// there is no rising edge and this PR is not shipped.
	prs(t, store, "reviewed", "2")
	// And the cause's own subtree changes again, still green — still no edge.
	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"),
		entity.Obj{"state": "green", "run": 2}.Payload())
	time.Sleep(400 * time.Millisecond)

	two := entity.RefAt("demo", "pr", "2")
	if e, _, _ := store.Get(two); entity.ToObj(e.Payload)["stage"] != "reviewed" {
		t.Fatalf("an edge re-fired while its predicate stayed true: %s = %#v", two, e.Payload)
	}
	if f := eng.Fired(); f != 1 {
		t.Fatalf("fired = %d, want 1", f)
	}

	// Falling and rising again IS a new edge.
	ci(t, store, "red")
	ci(t, store, "green")
	waitStage(t, store, two, "shipped")
}

// `when` is the LEVEL beside the edge: the cause rising is not enough on its own.
func TestOnIsGuardedByWhen(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	when := MatchWhen(Match{"system": "demo", "kind": "notion", "value": map[string]any{"ok": true}})
	if err := eng.AddTrigger(onWhen(when)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	one := entity.RefAt("demo", "pr", "1")
	prs(t, store, "reviewed", "1")

	ci(t, store, "green") // the cause rises, but the condition does not hold
	time.Sleep(300 * time.Millisecond)
	if e, _, _ := store.Get(one); entity.ToObj(e.Payload)["stage"] != "reviewed" {
		t.Fatalf("fired with an unsatisfied `when`: %#v", e.Payload)
	}

	// Make the condition hold, then raise the cause again. Note the condition becoming true is
	// NOT itself a cause — the edge has to rise for the trigger to be asked at all.
	entitytest.Set(t, store, entity.RefAt("demo", "notion", "n"), entity.Obj{"ok": true}.Payload())
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := store.Get(one); entity.ToObj(e.Payload)["stage"] != "reviewed" {
		t.Fatalf("the condition becoming true fired the trigger; only `on` is a cause: %#v", e.Payload)
	}
	ci(t, store, "red")
	ci(t, store, "green")
	waitStage(t, store, one, "shipped")
}

// An `on` trigger does NOT act on the backlog. Installed over a verse where its predicate
// already holds, there is no rising edge to observe — which is exactly the opposite of a
// subject-dispatched trigger, whose level-triggered seed acts on everything already standing.
// Both readings are defensible; the difference is what `on` MEANS, and it is worth pinning.
func TestOnDoesNotFireOnTheBacklog(t *testing.T) {
	store := entitytest.Store(t)
	prs(t, store, "reviewed", "1")
	ci(t, store, "green") // the cause already holds before anything is installed

	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(onWhen(nil)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx) // the mode whose seed replay WOULD fire a subject-dispatched rule
	time.Sleep(500 * time.Millisecond)

	one := entity.RefAt("demo", "pr", "1")
	if e, _, _ := store.Get(one); entity.ToObj(e.Payload)["stage"] != "reviewed" {
		t.Fatalf("an `on` trigger fired on the backlog: %#v", e.Payload)
	}
}

// A cause that PINS THE ENTITY is still read as a predicate over its payload. Naming the id
// is how you say "this CI status, not any of them", and it used to cost the rest of the
// match: a definite system+kind+id was treated as an entity NAMED rather than selected, and a
// named match is a set of one whether or not it exists — so `{state: green}` was dropped and
// the predicate meant "ci:status:release is there at all".
//
// Both faces of that were broken, in opposite directions. As an `on` it never fired: the
// predicate held from the moment the entity appeared, so the edge had risen before anyone
// could see it fall. As a `when` (below) it fired when it should have blocked.
func TestOnWithAPinnedIDReadsItsPredicate(t *testing.T) {
	store := entitytest.Store(t)
	reviewed := prs(t, store, "reviewed", "1")
	rel := entity.RefAt("ci", "status", "release")

	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			On:      MatchWhen(Match{"system": "ci", "kind": "status", "id": "release", "value": map[string]any{"state": "green"}}),
			Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// The entity exists, and the predicate does NOT hold: no edge.
	entitytest.Set(t, store, rel, entity.Obj{"state": "red"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("fired %d times while the pinned cause was red", f)
	}
	// Now it holds — that is the edge.
	entitytest.Set(t, store, rel, entity.Obj{"state": "green"}.Payload())
	waitStage(t, store, reviewed[0], "shipped")
}

// The `when` face of the same thing: a condition pinned to one entity blocks while that
// entity's payload does not satisfy it. Existence is not the predicate.
func TestWhenWithAPinnedIDBlocksOnItsPredicate(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "ci", "kind": "status", "id": "build", "value": map[string]any{"state": "green"}}),
			Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ci(t, store, "red")
	one := prs(t, store, "reviewed", "1")[0]
	time.Sleep(300 * time.Millisecond)
	if e, _, _ := store.Get(one); entity.ToObj(e.Payload)["stage"] != "reviewed" {
		t.Fatalf("shipped while the pinned condition was red: %#v", e.Payload)
	}
	ci(t, store, "green")
	waitStage(t, store, one, "shipped")
}

// A trigger with no `on` keeps the older dispatch exactly — the two rules coexist, which is
// what lets every charter written so far keep running while the new one is exercised.
func TestSubjectDispatchIsUnchangedAlongsideOn(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(onWhen(nil)); err != nil {
		t.Fatalf("AddTrigger(on): %v", err)
	}
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger(subject): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// The subject-dispatched rule fires on the change to its own subject, as it always has.
	one := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, one, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, one, "reviewed")

	// ...and the edge rule then ships it when its own cause rises.
	ci(t, store, "green")
	waitStage(t, store, one, "shipped")
}

// The guard and the selection come from ONE snapshot, taken at the commit the edge rose at.
// Two reads of a live store could disagree — a member arriving between them is in one answer
// and not the other — and the whole point of asking at a revision is that they cannot.
func TestEdgeReadsOneSnapshot(t *testing.T) {
	store := entitytest.Store(t)
	prs(t, store, "reviewed", "1")

	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(onWhen(nil)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ci(t, store, "green")
	waitStage(t, store, entity.RefAt("demo", "pr", "1"), "shipped")

	// pr:2 reaches `reviewed` AFTER the commit the edge rose at. It is not in that snapshot,
	// so it is not in this firing — the selection is as of C, not as of whenever the sweep
	// happened to run.
	prs(t, store, "reviewed", "2")
	time.Sleep(300 * time.Millisecond)
	two := entity.RefAt("demo", "pr", "2")
	if e, _, _ := store.Get(two); entity.ToObj(e.Payload)["stage"] != "reviewed" {
		t.Fatalf("a member added after the edge's commit was acted on: %#v", e.Payload)
	}
}

// A charter that cannot mean what it says fails at INSTALL. `on` is a predicate over state,
// asked at two revisions, and each of these asks it for something state cannot answer.
func TestAddTriggerRejectsBadOn(t *testing.T) {
	cases := map[string]Condition{
		"on with no system": {
			On:      MatchWhen(Match{"kind": "status"}),
			Subject: Match{"system": "demo", "kind": "pr"},
		},
		"on carrying op": {
			On:      MatchWhen(Match{"system": "ci", "kind": "status", "op": "set"}),
			Subject: Match{"system": "demo", "kind": "pr"},
		},
		"on with a subject that cannot select": {
			On:      MatchWhen(Match{"system": "ci", "kind": "status"}),
			Subject: Match{"system": "demo"}, // no kind, no id — selects nothing definite
		},
	}
	for name, cond := range cases {
		t.Run(name, func(t *testing.T) {
			store := entitytest.Store(t)
			eng := New(store, task.AutoResolver{})
			err := eng.AddTrigger(Trigger{Name: "t", Condition: cond, Action: Action{}})
			if err == nil {
				t.Fatal("installed a condition that cannot mean what it says")
			}
			t.Log(err)
		})
	}
}

// `on` survives the wire: it round-trips through the charter codec, and is ABSENT rather than
// null when unset (omitzero is inert on a custom-codec field, which is why Condition's codec
// is hand-written).
func TestConditionOnRoundTrips(t *testing.T) {
	in := Condition{
		On:      MatchWhen(Match{"system": "ci", "kind": "status", "value": map[string]any{"state": "green"}}),
		When:    MatchWhen(Match{"system": "demo", "kind": "notion"}),
		Subject: Match{"system": "demo", "kind": "pr"},
	}
	node, err := in.ToTonyIR()
	if err != nil {
		t.Fatalf("ToTonyIR: %v", err)
	}
	var out Condition
	if err := out.FromTonyIR(node); err != nil {
		t.Fatalf("FromTonyIR: %v", err)
	}
	if out.On == nil || out.On.Req == nil || out.On.Req.Facts.System() != "ci" {
		t.Fatalf("on did not survive the round trip: %#v", out.On)
	}
	if out.When == nil || out.When.Req.Facts.System() != "demo" {
		t.Fatalf("when did not survive alongside on: %#v", out.When)
	}

	bare := Condition{Subject: Match{"system": "demo", "kind": "pr"}}
	n, err := bare.ToTonyIR()
	if err != nil {
		t.Fatalf("ToTonyIR: %v", err)
	}
	if got := entity.Get(n, "on"); got != nil {
		t.Fatalf("a condition with no `on` emitted one: %v", got)
	}
}
