package trigger

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/task"
)

// A SPAWN NO RUNNER OFFERS IS REFUSED AT AN INSTALL, where the author is present to hear it,
// as one some runner offers and none takes is (TestASpawnsEnvironmentIsHeldToTheCeilingAtInstall).
// What an install says depends on the call and never on when it is made, so there is nothing
// about the engine's state here to set.
func TestASpawnNoRunnerOffersIsRefusedAtAnInstall(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetOfferCheck(func(l runner.Launch) error {
		return fmt.Errorf("%w: no runner offers a script", ErrNoOffer)
	})
	eng.ObserveScoped(t.Context())
	err := eng.AddTrigger(scriptRule(t))
	if err == nil || !strings.Contains(err.Error(), "no runner offers a script") {
		t.Fatalf("install = %v; want it refused, naming the spawn no runner offers", err)
	}
	if _, stands := eng.Rule("run"); stands {
		t.Fatal("a refused install left the rule standing")
	}
}

// A RULE PLACED FROM ITS RECORD IS HELD FOR A RUNNER, NOT REFUSED: the record was accepted, and
// a restore places the records' rules before the runners have been heard from. It stands by
// name, says what it waits for, and the pass a registration kicks arms it.
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
	placeRecord(t, eng, scriptRule(t))
	waitHeld(t, eng, "run", "waiting for a runner")
	if _, stands := eng.Rule("run"); !stands {
		t.Fatal("a held rule does not stand by name")
	}
	if got := eng.Triggers(); len(got) != 0 {
		t.Fatalf("armed = %v; a held rule is not armed", got)
	}
	// A pass with no runner leaves it held.
	eng.Kick()
	time.Sleep(20 * time.Millisecond)
	if ps := eng.Pendings(); len(ps) != 1 || !strings.Contains(ps[0].Cause, "waiting for a runner") {
		t.Fatalf("pending = %+v after a pass with no runner; want it still held", ps)
	}
	// A runner registers: the pass it kicks arms the rule.
	offered.Store(true)
	eng.Kick()
	waitArming(t, eng, nil)
	waitArmed(t, eng, "run")
}

// scriptRule is a rule whose one spawn is a script.
func scriptRule(t *testing.T) Trigger {
	t.Helper()
	return mustParseCharter(t, `
- name: run
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action: {patch: {stage: started}, spawn: {script: "true"}}
`)
}

// placeRecord writes a rule's record with no install, as a restore finds one: the follow places
// it, vetted as the engine places a rule (RunnersToCome).
func placeRecord(t *testing.T, eng *Engine, rule Trigger) {
	t.Helper()
	if _, err := eng.persist(rule, "", true); err != nil {
		t.Fatalf("writing the record of %s: %v", rule.Name, err)
	}
}

// AN INSTALL ANSWERS ONCE ACCEPTED: written and checked, with its watch opened behind the answer
// at the store's pace. A store that does not open the watch yet does not refuse the install; the
// rule is armed and arming, says what the store is doing, and watches when the store answers.
func TestAnInstallAnswersOnceAcceptedAndArmsBehindIt(t *testing.T) {
	store := phasedStore(t, entitytest.Store(t))
	store.Set(entity.WatchRefused, errors.New("context deadline exceeded"))
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("a store that did not open the watch refused the install: %v", err)
	}
	waitArmingSays(t, eng, "review", "the store refused its watch, and is asked again: ")
	if n := eng.NotesOn(Trigger{Name: "review"}); n.Waits == "" || !n.Arming {
		t.Fatalf("notes = %+v; want the rule arming", n)
	}
	store.Set(entity.WatchOpen, nil)
	waitArmed(t, eng, "review")
	waitArming(t, eng, nil)
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
	placeRecord(t, eng, scriptRule(t))
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
// second some runner refuses is refused, even by a vet that holds for a runner still to come,
// not held for the first — the hold would hide a refusal that waiting will not cure.
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
	err := eng.addTrigger(rule, "", RunnersToCome)
	if err == nil || !strings.Contains(err.Error(), "the ceiling is lower") {
		t.Fatalf("vet = %v; want the second spawn\x27s refusal", err)
	}
	if got := eng.Held(); len(got) != 0 {
		t.Fatalf("pending = %v; a refused rule is not held", got)
	}
}
