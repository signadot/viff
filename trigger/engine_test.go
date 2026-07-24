package trigger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/example/infra/verse/entity"
	"github.com/example/infra/verse/process"
)

type denyResolver struct{}

func (denyResolver) Resolve(context.Context, process.GateQuestion, process.ToolUse) (process.Resolution, error) {
	return process.Resolution{Proceed: false, By: "deny"}, nil
}

// A denied firing must not spend the fire budget: with maxFires=1, five denied
// firings leave the counter at zero (the backstop bounds actions taken, not
// attempts, so an ask/park-heavy workload can't wedge all firing).
func TestBudgetNotConsumedByDenials(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	eng.SetMaxFires(1)
	eng.Add(Trigger{
		Name:    "gated",
		Matches: []EventMatch{{System: "demo"}},
		Gates:   []Gate{{Mode: process.Ask}},
		Action:  func(entity.Event, *Engine) {},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	for i := 0; i < 5; i++ {
		store.Put(entity.Ref{System: "demo", Kind: "x", ID: fmt.Sprint(i)}, nil)
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
		if e, ok := s.Get(ref); ok && e.Payload["stage"] == want {
			return
		}
		select {
		case <-deadline:
			e, _ := s.Get(ref)
			t.Fatalf("timed out waiting for %s stage=%q; have %+v", ref, want, e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// setStage is an action that advances the subject's stage — standing in for "a
// process did its work and reported back" without needing a real driver.
func setStage(stage string) Action {
	return func(ev entity.Event, e *Engine) {
		e.Store().Put(ev.Ref, entity.Payload{"stage": stage})
	}
}

// The emergent chain turns and *terminates*: an injected pr change flows open →
// reviewed → deployed purely by triggers reacting to state other actions write,
// then stops because nothing matches "deployed". No path was authored.
func TestEmergentChainTurnsAndTerminates(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{}) // Ask gates auto-proceed
	eng.Add(Trigger{
		Name:    "review",
		Matches: []EventMatch{{System: "demo", Kind: "pr", Fields: map[string]any{"stage": "open"}}},
		Action:  setStage("reviewed"),
	})
	eng.Add(Trigger{
		Name:    "deploy",
		Matches: []EventMatch{{System: "demo", Kind: "pr", Fields: map[string]any{"stage": "reviewed"}}},
		Gates:   []Gate{{Mode: process.Ask}}, // a human/agent approves the deploy
		Action:  setStage("deployed"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	subj := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	store.Put(subj, entity.Payload{"stage": "open"}) // ignite

	waitStage(t, store, subj, "deployed")

	// Exactly two fires (review, deploy) — the chain converged, no runaway.
	time.Sleep(50 * time.Millisecond) // allow any spurious extra fire to show
	if f := eng.Fired(); f != 2 {
		t.Fatalf("fired = %d, want 2 (review + deploy)", f)
	}
}

// The ignition guard: a reflected process:* change does NOT wake a default trigger,
// so the loop cannot self-ignite on its own machinery — but ordinary changes do.
func TestIgnitionGuardSkipsProcessNamespace(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	fired := make(chan entity.Ref, 4)
	eng.Add(Trigger{
		Name:    "greedy",
		Matches: []EventMatch{{}}, // matches everything...
		Action:  func(ev entity.Event, e *Engine) { fired <- ev.Ref },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// a process:* change must be ignored by the (non-opted-in) greedy trigger
	store.Put(entity.Ref{System: "process", Kind: "instance", ID: "p1"}, entity.Payload{"status": "running"})
	select {
	case r := <-fired:
		t.Fatalf("greedy fired on a process:* change (%s) — ignition guard failed", r)
	case <-time.After(200 * time.Millisecond):
	}

	// an ordinary change still wakes it
	store.Put(entity.Ref{System: "demo", Kind: "x", ID: "1"}, nil)
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("greedy did not fire on an ordinary change")
	}
}

// A self-perpetuating trigger (rule soup / non-termination) is bounded by the
// budget instead of hanging — the ECA backstop.
func TestBudgetBoundsRunaway(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	eng.SetMaxFires(5)
	eng.Add(Trigger{
		Name:    "spin",
		Matches: []EventMatch{{System: "demo", Kind: "loop"}}, // any change to demo:loop:* re-matches
		Action: func(ev entity.Event, e *Engine) {
			e.Store().Put(ev.Ref, entity.Payload{"tick": true}) // writes another matching change
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	store.Put(entity.Ref{System: "demo", Kind: "loop", ID: "1"}, entity.Payload{"tick": true}) // ignite

	// It must settle at the budget, not run forever.
	time.Sleep(300 * time.Millisecond)
	if f := eng.Fired(); f != 5 {
		t.Fatalf("fired = %d, want capped at 5", f)
	}
}
