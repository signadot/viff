package trigger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

type denyResolver struct{}

func (denyResolver) Resolve(context.Context, task.GateQuestion, task.ToolUse) (task.Resolution, error) {
	return task.Resolution{Proceed: false, By: "deny"}, nil
}

// A DENIED FIRING IS NOT A FIRING. Five denied gates leave the counter at zero: what the
// counter reports is entities ACTED ON, not attempts, so an ask/park-heavy workload does not
// read as a busy engine.
//
// This used to be a statement about the fire budget — that denials could not exhaust it and
// wedge all firing. The budget is gone (see countFire), and the property it was protecting is
// the one asserted here, which outlives it: a question that was answered "no" left no mark.
func TestDenialsDoNotCountAsFirings(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
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
		t.Fatalf("denied firings were counted: fired=%d, want 0", f)
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
// task did its work and reported back" without needing a real driver.
func setStage(stage string) Action {
	return Action{Patch: entity.Obj{"stage": stage}.Payload()}
}

// The emergent chain turns and *terminates*: an injected pr change flows open →
// reviewed → deployed purely by triggers reacting to state other actions write,
// then stops because nothing matches "deployed". No path was authored.
func TestEmergentChainTurnsAndTerminates(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{}) // Ask gates auto-proceed
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
// machinery (reflected task:* state, the durable trigger:* charter) was excluded from a
// default match set, so a trigger had to opt in by naming it, and the loop could not ignite on
// itself.
//
// The guard is gone because the rule makes it unnecessary rather than because the property
// was dropped. A trigger names its subtrees and is evaluated at nothing else, so naming a
// machinery system IS the opt-in — there is no default set left to exclude anything from.
func TestATriggerIsReachedOnlyByWhatItReads(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
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

	// A task:* change is not in anything this trigger reads, so it never reaches it.
	entitytest.Set(t, store, entity.Ref{System: "task", Kind: "instance", ID: "p1"}, entity.Obj{"status": "running"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("fired on a task:* change it does not read (fired=%d)", f)
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

// A self-perpetuating chain (rule soup / non-termination) settles instead of hanging. This is a
// genuine OSCILLATION: each fire flips the phase (a→b, b→a), so every write is a real content
// change. (An idempotent self-write would instead be defused by edge-on-real-change and never
// run away — see TestIdempotentWriteDoesNotLoop.)
//
// NOTHING BOUNDS THIS BUT THE THROTTLE. It used to be the fire budget, and this test asserted
// the chain ran to exactly maxFires — a number, reached without anything having understood why.
// The budget is gone (see countFire); the throttle recognizes the loop by its causal depth and
// stops it at cap+1 = 3 firings. So if the throttle regresses, this test does not settle at a
// larger number: it hangs, which is the honest failure.
func TestRunawayOscillationIsStoppedByTheThrottle(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	// Refuse rather than pace. Under the default pace this would also settle at 3 within the
	// window below, but for the wrong reason — the fourth firing merely not released yet — and
	// a test that cannot tell "stopped" from "not yet" is not asserting what it says.
	eng.SetTripPace(0)
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

	// It must settle, and settle because the throttle saw the cycle.
	time.Sleep(300 * time.Millisecond)
	if f := eng.Fired(); f != 3 {
		t.Fatalf("fired = %d, want 3 — two triggers, so a chain commits at most cap+1 hops", f)
	}
	if n := eng.tripCount(); n != 1 {
		t.Fatalf("trips = %d, want 1: the oscillation must be stopped by the throttle", n)
	}
}

// The complement: an idempotent self-write does NOT run away — edge-on-real-change makes a
// set-to-the-same-value inert, so a trigger whose effect re-touches its own cause fires once,
// not forever. This is the substrate mechanism that lets a "hold on reviewed" charter not loop
// without a latch. It fires once and the throttle never has to be asked — which is the point:
// the ordinary converging rule must not look like a runaway (see throttled on why the bound is
// `>` and not `>=`, a distinction this shape is exactly what caught).
func TestIdempotentWriteDoesNotLoop(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
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
	if n := eng.tripCount(); n != 0 {
		t.Fatalf("trips = %d, want 0: a rule that settles by writing the same value is not a runaway", n)
	}
}
