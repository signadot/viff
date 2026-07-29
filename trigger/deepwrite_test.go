package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/process"
)

// A trigger reads the ENTITY, not the write that touched it. A when over two fields must
// fire when one of them is written on its own — the entity satisfies it either way, and
// how it came to be true is not the trigger's business.
//
// This is the whole reason an event carries level state: before it did, this fired on a
// whole-entity write and stayed silent on the deep write that reached the identical state.
// Nothing errored — the trigger simply never ran.
func TestDeepWriteFiresAWholeEntityTrigger(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{Subject: Match{
			"system": "demo", "kind": "pr",
			"value": map[string]any{"stage": "reviewed", "env": "prod"},
		}},
		Action: Action{},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	if _, err := store.Write(entity.Write{
		At:    entity.Path{Ref: ref},
		Value: entity.Obj{"stage": "open", "env": "prod"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if eng.Fired() != 0 {
		t.Fatalf("fired %d times before the entity matched", eng.Fired())
	}

	// Reach the matching state by writing ONE field.
	if _, err := store.Write(entity.Write{At: ref.At("stage"), Value: ir.FromString("reviewed")}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(5 * time.Second)
	for eng.Fired() == 0 {
		select {
		case <-deadline:
			got, _, _ := store.Get(ref)
			t.Fatalf("a deep write did not fire the trigger; the entity is %v", entity.ToObj(got.Payload))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// The same, on the PRODUCTION path: a per-trigger subtree watch (ObserveScoped), which is
// what versed runs. The level state has to come from the same place there — the subtree
// document, seeded by the watch's initial State — and a field written before the trigger was
// installed is part of it.
//
// The binding is the point, not just the firing. An action resolves its $refs against the
// payload that fired it, so a partial payload does not fail loudly: the trigger fires, the
// reference resolves to nothing, and what lands (or what an agent is asked to do) says
// "$note" where the note should be.
func TestDeepWriteUnderSubtreeWatchCarriesTheWholeEntity(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "announce",
		Condition: Condition{Subject: Match{
			"system": "demo", "kind": "task",
			"value": map[string]any{"stage": "ready"},
		}},
		Action: Action{Patch: entity.Obj{"echo": "$note"}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}

	ref := entity.Ref{System: "demo", Kind: "task", ID: "t1"}
	// Written BEFORE the watch opens — so only the seed can account for it.
	entitytest.Set(t, store, ref, entity.Obj{"stage": "queued", "note": "ship it"}.Payload())

	eng.ObserveScoped(t.Context())

	if _, err := store.Write(entity.Write{At: ref.At("stage"), Value: ir.FromString("ready")}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(5 * time.Second)
	for {
		got, _, _ := store.Get(ref)
		echo := entity.ToObj(got.Payload)["echo"]
		if echo == "ship it" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("echo = %v, want %q; the firing saw %v", echo, "ship it", entity.ToObj(got.Payload))
		case <-time.After(10 * time.Millisecond):
		}
	}
}
