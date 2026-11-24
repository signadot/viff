package trigger

import (
	"context"
	"testing"
	"time"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/task"
)

// vanish marks an entity as on its way out, through the ordinary write path.
func vanish(t *testing.T, store entity.Store, ref entity.Ref) {
	t.Helper()
	e, _, _ := store.Get(ref)
	cur := e.Payload
	if cur == nil {
		cur = ir.Null()
	}
	next, err := tony.Patch(cur, entity.VanishPatch(time.Now()))
	if err != nil {
		t.Fatalf("vanish %s: %v", ref, err)
	}
	entitytest.Set(t, store, ref, next)
}

// A rule does not act on an entity on its way out. It would patch it, spawn about it and raise
// a gate on it — all work about something that will not be there.
func TestSelectionSkipsAVanishingEntity(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	live := entity.RefAt("demo", "pr", "live")
	going := entity.RefAt("demo", "pr", "going")
	entitytest.Set(t, store, live, entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, going, entity.Obj{"stage": "open"}.Payload())
	vanish(t, store, going)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    Action{Patch: entity.Obj{"stage": "reviewing"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("demo", "pr", "poke"), entity.Obj{}.Payload())
	waitStage(t, store, live, "reviewing")
	time.Sleep(200 * time.Millisecond)
	if got := entity.Str(mustGet(t, store, going), "stage"); got != "open" {
		t.Fatalf("acted on an entity that is leaving: stage = %q", got)
	}
}

// Naming the marker is the opt-in — which is what lets the REAPER be an ordinary trigger:
// select what is leaving, and remove it. `!delete null` is the removal, at the root exactly as
// at a field; a bare null there is the payload null, an entity holding nothing.
func TestReaperIsAnOrdinaryTrigger(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	going := entity.RefAt("demo", "pr", "going")
	staying := entity.RefAt("demo", "pr", "staying")
	entitytest.Set(t, store, going, entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, staying, entity.Obj{"stage": "open"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name: "reap",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"),
			"value": map[string]any{"@sys": map[string]any{"meta": map[string]any{"vanishing": nil}}}}},
		Action: Action{Patch: entity.Removed()}, // `!delete null` at the root removes the entity
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	vanish(t, store, going)

	deadline := time.After(3 * time.Second)
	for {
		if _, ok, _ := store.Get(going); !ok {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the reaper did not remove %s", going)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, ok, _ := store.Get(staying); !ok {
		t.Fatalf("the reaper removed an entity that was not leaving: %s", staying)
	}
}

// The disappearance is observable as a RISING edge, which is the whole reason the intent is
// written down: `on` says "this now exists", and there is no way to say the negative.
func TestVanishingIsARisingEdge(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	going := entity.RefAt("demo", "pr", "going")
	audit := entity.RefAt("audit", "record", "latest")
	entitytest.Set(t, store, going, entity.Obj{"stage": "open"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name: "note-departures",
		Condition: Condition{
			On: MatchWhen(Match{"path": pathOf("demo", "pr", "*"),
				"value": map[string]any{"@sys": map[string]any{"meta": map[string]any{"vanishing": nil}}}}),
			Subject: mm(audit.String()),
		},
		Action: Action{Patch: entity.Obj{"saw": "a departure"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// Ordinary writes to the subtree are not the edge.
	entitytest.Set(t, store, going, entity.Obj{"stage": "reviewed"}.Payload())
	time.Sleep(300 * time.Millisecond)
	if _, ok, _ := store.Get(audit); ok {
		t.Fatal("fired before anything was vanishing")
	}

	vanish(t, store, going) // ¬on(C-1) ∧ on(C)
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := store.Get(audit); ok && entity.Str(e.Payload, "saw") == "a departure" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the vanishing edge did not fire")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// And the REMOVAL itself is inert: the predicate falls from true to false, and nothing
	// fires on a falling edge. The reactive moment was the intent.
	entitytest.Set(t, store, audit, entity.Obj{"saw": "nothing since"}.Payload())
	mustWriteNil(t, store, going)
	time.Sleep(400 * time.Millisecond)
	if got := entity.Str(mustGet(t, store, audit), "saw"); got != "nothing since" {
		t.Fatalf("the removal itself fired something: saw = %q", got)
	}
}

func mustWriteNil(t *testing.T, store entity.Store, ref entity.Ref) {
	t.Helper()
	if _, _, err := store.Write(entity.Write{At: ref.At()}); err != nil {
		t.Fatalf("remove %s: %v", ref, err)
	}
}

// A NAMED subject is a set of one whether or not the entity exists — except when it is
// leaving. Without this a rule reacting to a departure acts on the departing entity, and since
// a patch over an absent entity CREATES, a removal landing first meant the rule RESURRECTED
// it: back as an ordinary live entity, stripped to whatever the rule wrote, with no marker.
//
// Measured before the fix: `exists=true payload=map[farewell:noted]`.
func TestARuleCannotResurrectWhatIsLeaving(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name: "farewell",
		Condition: Condition{
			On: MatchWhen(Match{"path": pathOf("demo", "pr", "*"),
				"value": map[string]any{"@sys": map[string]any{"meta": map[string]any{"vanishing": nil}}}}),
			Subject: mm(ref.String()), // named — and leaving
		},
		Action: Action{Patch: entity.Obj{"farewell": "noted"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	vanish(t, store, ref)
	if _, _, err := store.Write(entity.Write{At: ref.At()}); err != nil { // the removal
		t.Fatalf("remove: %v", err)
	}
	time.Sleep(700 * time.Millisecond)

	if e, ok, _ := store.Get(ref); ok {
		t.Fatalf("a rule brought back an entity that had left: %v", entity.ToObj(e.Payload))
	}
	// A name that has NEVER existed is still nameable — the create amendment is intact.
	fresh := entity.RefAt("demo", "report", "1")
	if got := (mm("demo.report.1")).Select(store); len(got) != 1 || got[0] != fresh {
		t.Fatalf("naming a never-written entity selected %v, want a set of one", got)
	}
}
