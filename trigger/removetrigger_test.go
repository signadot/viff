package trigger

import (
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A removed trigger stops firing. In subtree mode a trigger's observation is its own
// goroutines — not a shared dispatch loop reading the charter — so removing it from the
// charter is only half the removal: the watch has to be cancelled too, or the trigger keeps
// acting on the verse with nothing listing it any more (`verse charter` shows it gone while
// its effects continue).
func TestRemovedTriggerStopsFiring(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	})

	eng.ObserveScoped(t.Context()) // versed's production path (per-trigger subtree watch)

	// It observes while installed.
	first := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, first, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, first, "reviewed")

	if !eng.RemoveTrigger("review") {
		t.Fatal("RemoveTrigger reported the trigger did not exist")
	}
	fired := eng.Fired()

	// And not after. A fresh subject, so nothing about this depends on the first one.
	second := entity.RefAt("demo", "pr", "2")
	entitytest.Set(t, store, second, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(300 * time.Millisecond)

	if e, _, _ := store.Get(second); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("%s stage = %v after its trigger was removed; the subtree watcher outlived the "+
			"charter entry", second, entity.ToObj(e.Payload)["stage"])
	}
	if f := eng.Fired(); f != fired {
		t.Fatalf("fired = %d, was %d before the removal", f, fired)
	}
}

// Removing one trigger stops ONLY that trigger's watchers: each holds a cancel of its own,
// so a shared ancestor context would take the whole charter down with the first removal.
func TestRemovingOneTriggerLeavesTheOthersObserving(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	})
	eng.AddTrigger(Trigger{
		Name:      "triage",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "issue", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("triaged"),
	})

	eng.ObserveScoped(t.Context())

	if !eng.RemoveTrigger("review") {
		t.Fatal("RemoveTrigger reported the trigger did not exist")
	}

	issue := entity.RefAt("demo", "issue", "1")
	entitytest.Set(t, store, issue, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, issue, "triaged")

	pr := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, pr, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := store.Get(pr); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("%s stage = %v; the removed trigger still fires", pr, entity.ToObj(e.Payload)["stage"])
	}
}

// Re-adding a removed trigger observes again — the cancel is per installation, so the
// remove/add cycle a dynamic charter does (rebind a trigger) is not a one-way door.
func TestReAddedTriggerObservesAgain(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	trig := Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}
	eng.AddTrigger(trig)

	eng.ObserveScoped(t.Context())

	eng.RemoveTrigger("review")
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("re-add after remove: %v", err)
	}

	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref, "reviewed")
}
