package trigger

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// A RULE WHOSE SPAWN NO RUNNER OFFERS IS HELD, NOT REFUSED: the runner may register after the
// rule, as every runner does at a restore. It stands by name, says what it waits for, and the
// pass a registration kicks arms it. A spawn some runner offers and none takes is still refused
// (TestASpawnsEnvironmentIsHeldToTheCeilingAtInstall).
func TestARuleHeldForARunnerArmsWhenOneRegisters(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	var offered atomic.Bool
	eng.SetOfferCheck(func(l runner.Launch) error {
		if offered.Load() {
			return nil
		}
		return fmt.Errorf("%w: no runner offers a script", ErrNoOffer)
	})
	eng.ObserveScoped(t.Context())
	rule := mustParseCharter(t, `
- name: run
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {patch: {stage: started}, spawn: {script: "true"}}
`)
	if err := eng.AddTrigger(rule); err != nil {
		t.Fatalf("a spawn no runner offers refused the install: %v", err)
	}
	ps := eng.Pendings()
	if len(ps) != 1 || ps[0].Rule != "run" || ps[0].Arming || !strings.Contains(ps[0].Cause, "waiting for a runner") {
		t.Fatalf("pending = %+v; want run held for a runner", ps)
	}
	if _, stands := eng.Rule("run"); !stands {
		t.Fatal("a held rule does not stand by name")
	}
	if got := eng.Triggers(); len(got) != 0 {
		t.Fatalf("armed = %v; a held rule is not armed", got)
	}
	// A pass with no runner leaves it held.
	eng.Kick()
	time.Sleep(20 * time.Millisecond)
	if ps := eng.Pendings(); len(ps) != 1 {
		t.Fatalf("pending = %+v after a pass with no runner; want it still held", ps)
	}
	// A runner registers: the pass it kicks arms the rule.
	offered.Store(true)
	eng.Kick()
	waitArming(t, eng, nil)
	deadline := time.Now().Add(2 * time.Second)
	for len(eng.Pendings()) > 0 || len(eng.Triggers()) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("pending = %+v, armed = %v; want run armed", eng.Pendings(), eng.Triggers())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// AN INSTALL ANSWERS ONCE ACCEPTED: written and checked, with its watch opened behind the answer
// at the store's pace. A store that does not open the watch yet does not refuse the install; the
// rule is arming, and arms when the store answers.
func TestAnInstallAnswersOnceAcceptedAndArmsBehindIt(t *testing.T) {
	store := &unwatchable{Store: entitytest.Store(t)}
	store.on.Store(true)
	eng := New(store, task.AutoResolver{})
	eng.every = 5 * time.Millisecond
	eng.ObserveScoped(t.Context())
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("a store that did not open the watch refused the install: %v", err)
	}
	waitHeld(t, eng, "review", "the store refused its watch, and is asked again: ")
	if n := eng.NotesOn(Trigger{Name: "review"}); n.Waits == "" {
		t.Fatalf("notes = %+v; want the rule held", n)
	}
	store.on.Store(false)
	waitArmed(t, eng, "review")
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref, "reviewed")
}

// A PENDING RULE THE WORLD COMES TO REFUSE STAYS, WITH THE REFUSAL AS ITS CAUSE: it was
// accepted and its record stands, so the engine keeps it by name and says why it fires on
// nothing, rather than forgetting a rule someone wrote until the next restart. The world may
// change back — the runner that took its spawn registers again — and then it arms.
func TestAPendingRuleRefusedLaterStandsWithTheRefusal(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	var answer atomic.Pointer[error]
	noOffer := fmt.Errorf("%w: no runner offers a script", ErrNoOffer)
	answer.Store(&noOffer)
	eng.SetOfferCheck(func(l runner.Launch) error { return *answer.Load() })
	eng.ObserveScoped(t.Context())
	rule := mustParseCharter(t, `
- name: run
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {patch: {stage: started}, spawn: {script: "true"}}
`)
	if err := eng.AddTrigger(rule); err != nil {
		t.Fatalf("a spawn no runner offers refused the install: %v", err)
	}
	waitHeld(t, eng, "run", "waiting for a runner: ")

	// A runner offers it and refuses it: the rule is refused, and stands.
	refused := fmt.Errorf("no runner takes it: the ceiling is lower")
	answer.Store(&refused)
	eng.Kick()
	waitHeld(t, eng, "run", "refused: ")
	if ps := eng.Pendings(); !strings.Contains(ps[0].Cause, "the ceiling is lower") {
		t.Fatalf("pending = %+v; want the refusal's words", ps)
	}
	if _, stands := eng.Rule("run"); !stands {
		t.Fatal("a refused pending rule does not stand by name")
	}
	if got := eng.Held(); len(got) != 1 || got[0].Name != "run" {
		t.Fatalf("pending = %v; want run, refused", got)
	}

	// The world changes back: it arms.
	var ok error
	answer.Store(&ok)
	eng.Kick()
	waitArmed(t, eng, "run")
}

// waitHeld waits until the rule is pending with a cause that says this.
func waitHeld(t *testing.T, e *Engine, name, cause string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, p := range e.Pendings() {
			if p.Rule == name && strings.Contains(p.Cause, cause) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending = %+v; want %s held: %q", e.Pendings(), name, cause)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitArmed waits until the rule is armed and nothing is pending.
func waitArmed(t *testing.T, e *Engine, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(e.Pendings()) == 0 {
			for _, tr := range e.Triggers() {
				if tr.Name == name {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending = %+v, armed = %v; want %s armed", e.Pendings(), e.Triggers(), name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// EVERY SPAWN IS ASKED OF THE RUNNERS: a rule whose first spawn no runner offers and whose
// second some runner refuses is refused, where the author is, not held for the first — the
// hold would hide a refusal that waiting will not cure.
func TestARefusedSpawnOutranksAnUnofferedOne(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetOfferCheck(func(l runner.Launch) error {
		if l.Script == "first" {
			return fmt.Errorf("%w: no runner offers a script", ErrNoOffer)
		}
		return fmt.Errorf("no runner takes it: the ceiling is lower")
	})
	eng.ObserveScoped(t.Context())
	rule := mustParseCharter(t, `
- name: run
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {seq: [{spawn: {script: "first"}}, {spawn: {script: "second"}}]}
`)
	err := eng.AddTrigger(rule)
	if err == nil || !strings.Contains(err.Error(), "the ceiling is lower") {
		t.Fatalf("install = %v; want the second spawn\x27s refusal", err)
	}
	if got := eng.Held(); len(got) != 0 {
		t.Fatalf("pending = %v; a refused rule is not held", got)
	}
}
