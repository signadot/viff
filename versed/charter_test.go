package versed

import (
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// A restart comes back running the charter it went down with. Start reinstalls the rules
// standing in the verse, so the second server observes what the first was told to.
//
// Two servers over ONE logd is the restart: the first one's memory is gone, and only what
// the store holds crosses the gap.
func TestStartRestoresTheCharter(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	if err := first.Eng.AddTrigger(trigger.Trigger{
		Name: "review",
		Condition: trigger.Condition{
			When: trigger.MatchWhen(trigger.Match{"system": "demo", "kind": "pr",
				"fields": map[string]any{"stage": "open"}}),
		},
		Action: trigger.Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	store := entitytest.StoreAt(t, addr)
	second := NewWithStore(ScriptedBuilder, store)
	second.Start(t.Context())

	if got := second.Eng.Triggers(); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("charter after restart = %v; want the rule the first server was given", got)
	}
	// And it is a live rule, not just a listed one: the restored charter observes.
	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	deadline := time.After(2 * time.Second)
	for {
		if e, ok, _ := store.Get(ref); ok && entity.Str(e.Payload, "stage") == "reviewed" {
			return
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(ref)
			t.Fatalf("%s did not advance after the restart; have %v", ref, e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Goals come back too, and with them the charter they own. Start reinstalls every goal still
// being pursued, so a restart resumes pursuing rather than leaving a record that says
// "active" with nothing acting on it.
func TestStartRestoresGoals(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	if err := first.Eng.AddGoal(trigger.GoalDef{
		Name:    "ship",
		Subject: "demo:pr:1",
		DoneWhen: &trigger.Formula{Op: trigger.OpAtom, Req: &trigger.Requirement{
			Kind: trigger.ReqFact,
			Facts: trigger.Match{"system": "ci", "kind": "status",
				"fields": map[string]any{"state": "green"}},
		}},
		Triggers: []trigger.Trigger{{
			Name: "ship-review",
			Condition: trigger.Condition{
				When: trigger.MatchWhen(trigger.Match{"system": "demo", "kind": "pr",
					"fields": map[string]any{"stage": "open"}}),
			},
			Action: trigger.Action{Patch: entity.Obj{"stage": "reviewing"}.Payload()},
		}},
	}); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}

	store := entitytest.StoreAt(t, addr)
	second := NewWithStore(ScriptedBuilder, store)
	second.Start(t.Context())

	if got := second.Eng.Goals(); len(got) != 1 || got[0].Name != "ship" {
		t.Fatalf("goals after restart = %v; want the goal still being pursued", got)
	}
	if got := second.Eng.Triggers(); len(got) != 1 || got[0].Name != "ship-review" {
		t.Fatalf("charter after restart = %v; want the goal's owned rule, restored with it", got)
	}
	// The watching came back with the doing: the witness settles the goal.
	entitytest.Set(t, store, entity.Ref{System: "ci", Kind: "status", ID: "build"},
		entity.Obj{"state": "green"}.Payload())
	deadline := time.After(3 * time.Second)
	for {
		e, _, _ := store.Get(entity.Ref{System: "goal", Kind: "instance", ID: "ship"})
		if entity.Str(e.Payload, "outcome") == "done" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the restored goal did not latch; have %v", e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
