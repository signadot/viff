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
			Subject: trigger.Match{"system": "demo", "kind": "pr",
				"value": map[string]any{"stage": "open"}},
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
