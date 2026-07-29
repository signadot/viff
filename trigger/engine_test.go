package trigger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/process"
)

type denyResolver struct{}

func (denyResolver) Resolve(context.Context, process.GateQuestion, process.ToolUse) (process.Resolution, error) {
	return process.Resolution{Proceed: false, By: "deny"}, nil
}

// A denied firing must not spend the fire budget: with maxFires=1, five denied
// firings leave the counter at zero (the backstop bounds actions taken, not
// attempts, so an ask/park-heavy workload can't wedge all firing).
func TestBudgetNotConsumedByDenials(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	eng.SetMaxFires(1)
	eng.AddTrigger(Trigger{
		Name:      "gated",
		Condition: Condition{Subject: Match{"system": "demo"}},
		Gates:     []Gate{{Mode: "ask"}},
		Action:    Action{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	for i := 0; i < 5; i++ {
		entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "x", ID: fmt.Sprint(i)}, entity.Obj{}.Payload())
	}
	time.Sleep(100 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("denied firings consumed budget: fired=%d, want 0", f)
	}
}

func waitStage(t *testing.T, s entity.Store, ref entity.Ref, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if e, ok, _ := s.Get(ref); ok && entity.ToObj(e.Payload)["stage"] == want {
			return
		}
		select {
		case <-deadline:
			e, _, _ := s.Get(ref)
			t.Fatalf("timed out waiting for %s stage=%q; have %+v", ref, want, e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// setStage is an action that advances the subject's stage — standing in for "a
// process did its work and reported back" without needing a real driver.
func setStage(stage string) Action {
	return Action{Patch: entity.Obj{"stage": stage}.Payload()}
}

// The emergent chain turns and *terminates*: an injected pr change flows open →
// reviewed → deployed purely by triggers reacting to state other actions write,
// then stops because nothing matches "deployed". No path was authored.
func TestEmergentChainTurnsAndTerminates(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{}) // Ask gates auto-proceed
	eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	})
	eng.AddTrigger(Trigger{
		Name:      "deploy",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "reviewed"}}},
		Gates:     []Gate{{Mode: "ask"}}, // a human/agent approves the deploy
		Action:    setStage("deployed"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	subj := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, subj, entity.Obj{"stage": "open"}.Payload()) // ignite

	waitStage(t, store, subj, "deployed")

	// Exactly two fires (review, deploy) — the chain converged, no runaway.
	time.Sleep(50 * time.Millisecond) // allow any spurious extra fire to show
	if f := eng.Fired(); f != 2 {
		t.Fatalf("fired = %d, want 2 (review + deploy)", f)
	}
}

// A trigger is reached only by commits touching what it READS — its cause, its condition and
// its subject. That is what the ignition guard used to approximate: the engine's own
// machinery (reflected process:* state, the durable trigger:* charter) was excluded from a
// default match set, so a trigger had to opt in by naming it, and the loop could not ignite on
// itself.
//
// The guard is gone because the rule makes it unnecessary rather than because the property
// was dropped. A trigger names its subtrees and is evaluated at nothing else, so naming a
// machinery system IS the opt-in — there is no default set left to exclude anything from.
func TestATriggerIsReachedOnlyByWhatItReads(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "greedy",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "x"}},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// A process:* change is not in anything this trigger reads, so it never reaches it.
	entitytest.Set(t, store, entity.Ref{System: "process", Kind: "instance", ID: "p1"}, entity.Obj{"status": "running"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("fired on a process:* change it does not read (fired=%d)", f)
	}

	// A change to what it does read wakes it.
	entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "x", ID: "1"}, entity.Obj{}.Payload())
	deadline := time.After(time.Second)
	for eng.Fired() < 1 {
		select {
		case <-deadline:
			t.Fatal("did not fire on a change to what it reads")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A self-perpetuating chain (rule soup / non-termination) is bounded by the budget
// instead of hanging — the ECA backstop. This is a genuine OSCILLATION: each fire flips
// the phase (a→b, b→a), so every write is a real content change. (An idempotent self-write
// would instead be defused by edge-on-real-change and never run away — see
// TestIdempotentWriteDoesNotLoop; the budget is what bounds a real oscillation.)
func TestBudgetBoundsRunaway(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	eng.SetMaxFires(5)
	eng.AddTrigger(Trigger{
		Name:      "ping",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "loop", "value": map[string]any{"phase": "a"}}},
		Action:    Action{Patch: entity.Obj{"phase": "b"}.Payload()},
	})
	eng.AddTrigger(Trigger{
		Name:      "pong",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "loop", "value": map[string]any{"phase": "b"}}},
		Action:    Action{Patch: entity.Obj{"phase": "a"}.Payload()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx) // versed's production path (per-trigger subtree watch)

	entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "loop", ID: "1"}, entity.Obj{"phase": "a"}.Payload()) // ignite

	// It must settle at the budget, not run forever.
	time.Sleep(300 * time.Millisecond)
	if f := eng.Fired(); f != 5 {
		t.Fatalf("fired = %d, want capped at 5", f)
	}
}

// The complement: an idempotent self-write does NOT run away — edge-on-real-change makes a
// set-to-the-same-value inert, so a trigger whose effect re-touches its own cause fires once,
// not forever. This is the substrate mechanism that lets a "hold on reviewed" charter not loop
// without a latch. (Budget is untouched: 1 ≪ 5.)
func TestIdempotentWriteDoesNotLoop(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	eng.SetMaxFires(5)
	eng.AddTrigger(Trigger{
		Name:      "spin",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "loop"}}, // any change re-matches
		Action:    Action{Patch: entity.Obj{"tick": true}.Payload()},           // writes the SAME value back
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "loop", ID: "1"}, entity.Obj{"tick": true}.Payload()) // ignite

	time.Sleep(300 * time.Millisecond)
	if f := eng.Fired(); f != 1 {
		t.Fatalf("fired = %d, want 1 (the idempotent re-write is inert, no loop)", f)
	}
}
