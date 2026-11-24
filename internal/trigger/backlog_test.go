package trigger

import (
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/task"
)

// waitEcho polls for the value an action's patch wrote, which is the only proof that
// carries both facts at once: that the firing happened, and that the payload it fired on was
// the whole entity (an unresolved `.[note]` fails the firing instead of writing).
func waitEcho(t *testing.T, s entity.Store, ref entity.Ref, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		got, _, _ := s.Get(ref)
		if echo, _ := entity.ToObj(got.Payload)["echo"].(string); echo == want {
			return
		}
		select {
		case <-deadline:
			got, _, _ := s.Get(ref)
			t.Fatalf("echo never reached %q on %s; the entity is %v", want, ref, entity.ToObj(got.Payload))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func echoTrigger(name string) Trigger {
	return Trigger{
		Name: name,
		Condition: Condition{Subject: Match{
			"path":  pathOf("demo", "task", "*"),
			"value": map[string]any{"stage": "ready"},
		}},
		Action: Action{Patch: entity.Obj{"echo": ".[note]"}.Payload()},
	}
}

// A trigger installed over a POPULATED viff acts on the backlog that already matches it.
// The subtree watch replays the slice at the open, so what fires is "what matches now", not
// "what someone re-touches from here on" — a rule is a statement about the world, and the
// world includes what was written before anyone was listening.
//
// Without the replay, a task written before its trigger existed sits there forever, matching
// perfectly and firing nothing, until something re-writes it to announce a state it has held
// all along. That re-write is a workaround for the substrate, not work anyone wanted to do.
func TestTriggerFiresOnTheBacklogAtInstall(t *testing.T) {
	store := entitytest.Store(t)

	// The viff instance is already populated: two tasks match, one does not.
	ready1 := entity.RefAt("demo", "task", "t1")
	ready2 := entity.RefAt("demo", "task", "t2")
	queued := entity.RefAt("demo", "task", "t3")
	entitytest.Set(t, store, ready1, entity.Obj{"stage": "ready", "note": "one"}.Payload())
	entitytest.Set(t, store, ready2, entity.Obj{"stage": "ready", "note": "two"}.Payload())
	entitytest.Set(t, store, queued, entity.Obj{"stage": "queued", "note": "three"}.Payload())

	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(echoTrigger("announce")); err != nil {
		t.Fatal(err)
	}
	eng.ObserveScoped(t.Context())

	waitEcho(t, store, ready1, "one")
	waitEcho(t, store, ready2, "two")

	// The one that does not match is left alone: a replay is a diff of the slice against
	// nothing, not a firing of everything in it.
	if got, _, _ := store.Get(queued); entity.ToObj(got.Payload)["echo"] != nil {
		t.Fatalf("%s = %v; the replay fired a trigger whose when it does not satisfy",
			queued, entity.ToObj(got.Payload))
	}
}

// The same for a trigger added AFTER observation started — the two install orders open the
// watch from different places (ObserveScoped for what was already there, AddTrigger for
// what comes later) and a backlog that only one of them replays would be the subtler bug.
func TestTriggerAddedLaterFiresOnTheBacklog(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())

	ref := entity.RefAt("demo", "task", "t1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "ready", "note": "late"}.Payload())

	if err := eng.AddTrigger(echoTrigger("announce")); err != nil {
		t.Fatal(err)
	}
	waitEcho(t, store, ref, "late")
}
