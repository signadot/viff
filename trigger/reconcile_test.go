package trigger

import (
	"fmt"
	"strings"
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
	offered := false
	eng.SetOfferCheck(func(l runner.Launch) error {
		if offered {
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
	offered = true
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
	store := &unwatchable{Store: entitytest.Store(t), on: true}
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
	waitArming(t, eng, []string{"review"})
	if n := eng.NotesOn(Trigger{Name: "review"}); !n.Arming || n.Waits == "" {
		t.Fatalf("notes = %+v; want the rule arming", n)
	}
	store.on = false
	waitArming(t, eng, nil)
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref, "reviewed")
}
