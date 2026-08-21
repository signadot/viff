package versed

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/trigger"
)

// motions lists what stands on the docket a voter reads — the motion channels, whatever their
// status.
func motions(t *testing.T, srv *Server) map[string]entity.Obj {
	t.Helper()
	out := map[string]entity.Obj{}
	for _, e := range srv.Store.List(func(r entity.Ref) bool {
		return r.Reserved() && r.System() == "motion" && r.Kind() == "channel"
	}) {
		out[e.Ref.ID()] = entity.ToObj(e.Payload)
	}
	return out
}

// waitUntil polls until cond holds, or fails the test.
func waitUntil(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// A WHO-motion outlives the verse that opened it, and the ballot cast after the restart still
// decides who does the work.
//
// This is the case the docket could not survive. A who-motion opens from inside an action,
// AFTER that action has written its latch — so the subject no longer matches the rule, nothing
// re-fires it, and the only thing that ever awaited the ballots was a goroutine in the verse
// that died. The entry stayed on `verse motion` looking answerable, an operator picked a doer,
// and nothing whatsoever happened.
func TestARestartResumesAWhoMotion(t *testing.T) {
	addr := entitytest.Addr(t)
	rule := trigger.Trigger{
		Name:      "review",
		Condition: trigger.Condition{Subject: trigger.Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action: trigger.Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{
				Prompt:   "review $subject",
				Grant:    "read-only",
				Provider: &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "ask"},
			},
		},
	}

	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	if err := first.Eng.AddTrigger(rule); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	pr := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, first.Store, pr, entity.Obj{"stage": "open"}.Payload())
	waitUntil(t, "the who-motion to reach the docket", func() bool { return len(motions(t, first)) == 1 })

	var mid string
	for id, m := range motions(t, first) {
		mid = id
		// The address of the action it is holding up. Without it a restart has no way back to
		// the work the ballot is about — the subject and the capability do not name a rule.
		if m["chain"] != "review" {
			t.Fatalf("the motion does not record the action it blocks: %+v", m)
		}
	}

	stop() // the verse goes down with the selection still open

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())

	// The pick, cast from the docket exactly as `verse motion <id> --vote claude --as scott` does.
	entitytest.Set(t, second.Store, entity.RefAt("vote", mid, "scott"),
		entity.Obj{"choice": "claude", "by": "scott"}.Payload())

	waitUntil(t, "the resumed motion to close on the pick", func() bool {
		return motions(t, second)[mid]["outcome"] == "claude"
	})
	// And the work it was holding up actually starts, under the doer the ballot named — the
	// claim, since a motion that closes and spawns nothing has resumed only on paper.
	waitUntil(t, "the picked doer to be spawned", func() bool {
		for _, e := range second.Store.List(func(r entity.Ref) bool {
			return r.Reserved() && r.System() == mirror.System && r.Kind() == mirror.InstanceKind
		}) {
			if entity.Str(e.Payload, "doer") == "claude" {
				return true
			}
		}
		return false
	})
}

// A WHETHER-motion comes back a different way, and this pins that it comes back at all: its
// awaiter is the firing, the firing never ran its action (that is what the gate is holding),
// so the subject still matches and the rule's own replay re-monitors the same channel over the
// ballots already cast. A quorum half-reached before the restart is a quorum half-reached
// after it.
func TestARestartRemonitorsAWhetherMotion(t *testing.T) {
	addr := entitytest.Addr(t)
	rule := trigger.Trigger{
		Name:      "merge",
		Condition: trigger.Condition{Subject: trigger.Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Gates: []trigger.Gate{{
			Name: "approvals",
			Mode: "ask",
			Formula: &trigger.Formula{Op: trigger.OpAtom, Req: &trigger.Requirement{
				Kind: trigger.ReqQuorum, Facts: trigger.Match{"system": "motion", "kind": "vote"}, N: 2,
			}},
		}},
		Action: trigger.Action{Patch: entity.Obj{"stage": "merged"}.Payload()},
	}

	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	if err := first.Eng.AddTrigger(rule); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	pr := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, first.Store, pr, entity.Obj{"stage": "open"}.Payload())
	waitUntil(t, "the whether-motion to reach the docket", func() bool { return len(motions(t, first)) == 1 })
	var mid string
	for id := range motions(t, first) {
		mid = id
	}
	// One of the two approvals, before the lights go out.
	entitytest.Set(t, first.Store, entity.RefAt("vote", mid, "alice"),
		entity.Obj{"choice": "approve", "by": "alice"}.Payload())
	stop()

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	// The motion is still open and still counting: it must not have been settled by the
	// restart, and the ballot cast before it still counts toward the quorum.
	if got := motions(t, second)[mid]; got["outcome"] != nil {
		t.Fatalf("the restart decided a motion nobody finished voting on: %+v", got)
	}
	entitytest.Set(t, second.Store, entity.RefAt("vote", mid, "bob"),
		entity.Obj{"choice": "approve", "by": "bob"}.Payload())

	waitUntil(t, "the quorum to carry across the restart", func() bool {
		e, ok, _ := second.Store.Get(pr)
		return ok && entity.Str(e.Payload, "stage") == "merged"
	})
}
