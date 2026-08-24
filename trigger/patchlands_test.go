package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A RULE'S PATCH DOES NOT TAKE A CONTAINER AWAY. `patch: {votes: {bo: yes}}` over a subject
// whose `votes` holds an array used to write `{bo: yes}` where the array stood — every ballot
// gone, the firing reporting success, nothing logged (git-issue mbjpdr6sh12ksa5zgsn0).
//
// The patch fails the firing now, which is what a patch that could not be established already
// does: the subject is left exactly where it was, so the rule still selects it and the next
// commit tries again rather than latching over a write that did not happen.
func TestAPatchDoesNotReplaceTheContainerItLandsOn(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "tally",
		Condition: Condition{Subject: Match{
			"system": "demo", "kind": "pr",
			"value": map[string]any{"stage": "counting"},
		}},
		Action: Action{Patch: entity.Obj{
			"stage": "counted",
			"votes": entity.Obj{"bo": "yes"},
		}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("demo", "pr", "1")
	before := entity.Obj{
		"stage": "counting",
		"votes": []any{entity.Obj{"by": "ana"}, entity.Obj{"by": "bo"}},
	}.Payload()
	entitytest.Set(t, store, ref, before)

	// WAIT FOR THE FIRING, so this cannot pass by the rule never running — which is the way a
	// test of a refusal fails silently.
	deadline := time.After(5 * time.Second)
	for eng.Fired() == 0 {
		select {
		case <-deadline:
			t.Fatal("the rule never fired, so nothing about the patch was exercised")
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(200 * time.Millisecond) // and long enough for a write to have landed

	got, ok, err := store.Get(ref)
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	votes := entity.Get(got.Payload, "votes")
	if votes == nil || len(votes.Values) != 2 {
		t.Fatalf("the ballots did not survive the firing: %v", entity.ToObj(got.Payload))
	}
	if s := entity.Str(got.Payload, "stage"); s != "counting" {
		t.Fatalf("stage = %q; a refused patch must leave the subject where it was, so the "+
			"rule still selects it", s)
	}
}

// AND A PATCH STILL BUILDS STRUCTURE, which is the half that must not be refused: a nested key
// a subject does not carry yet is what a derive writes, and there is nothing to lose.
func TestAPatchStillCreatesANestedKey(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "note",
		Condition: Condition{Subject: Match{
			"system": "demo", "kind": "pr",
			"value": map[string]any{"stage": "shipping"},
		}},
		Action: Action{Patch: entity.Obj{
			"stage": "shipped",
			"notes": entity.Obj{"release": entity.Obj{"shipped": "v1"}},
		}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("demo", "pr", "2")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "shipping"}.Payload())

	deadline := time.After(5 * time.Second)
	for {
		got, _, _ := store.Get(ref)
		if entity.Str(got.Payload, "stage") == "shipped" {
			notes := entity.Get(got.Payload, "notes")
			if notes == nil {
				t.Fatalf("the nested key was not built: %v", entity.ToObj(got.Payload))
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("the rule never landed its patch")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
