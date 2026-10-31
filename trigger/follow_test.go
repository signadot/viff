package trigger

import (
	"errors"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger/charter"
)

// writeRule writes a rule's record as an install does, through the store alone.
func writeRule(t *testing.T, store entity.Store, tr Trigger) {
	t.Helper()
	doc, err := tr.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	entitytest.Set(t, store, charter.RuleRef("", tr.Name), charter.Payload(doc, "", "").Payload())
}

// writeStratum writes a level's record through the store alone.
func writeStratum(t *testing.T, store entity.Store, s Stratum) {
	t.Helper()
	doc, err := s.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	entitytest.Set(t, store, charter.StratumRef("", s.Name), charter.Payload(doc, "", "").Payload())
}

// waitGone waits until no rule of the name stands in the charter.
func waitGone(t *testing.T, eng *Engine, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, stands := eng.Rule(name); !stands {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still stands in the charter", name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// THE RECORD IS THE RULE. A rule's record written through the store, by nothing of the engine's,
// is a rule the engine runs; the record deleted is the rule gone; a record written over under
// the name is the rule replaced.
func TestTheRecordIsTheRule(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())
	restored(t, eng)

	writeRule(t, store, Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	})
	waitArmed(t, eng, "review")
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref, "reviewed")

	// Written over: the rule under the name is the record's.
	writeRule(t, store, Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("triaged"),
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if tr, ok := eng.Trigger("review"); ok && tr.Action.Patch != nil && entity.Str(tr.Action.Patch, "stage") == "triaged" {
			break
		}
		if time.Now().After(deadline) {
			tr, _ := eng.Rule("review")
			t.Fatalf("the rule did not follow its record: %+v", tr.Action)
		}
		time.Sleep(5 * time.Millisecond)
	}
	ref2 := entity.RefAt("demo", "pr", "2")
	entitytest.Set(t, store, ref2, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref2, "triaged")

	// Deleted: the rule is gone, and fires on nothing.
	if _, _, err := store.Write(entity.Write{At: charter.RuleRef("", "review").At()}); err != nil {
		t.Fatal(err)
	}
	waitGone(t, eng, "review")
	ref3 := entity.RefAt("demo", "pr", "3")
	entitytest.Set(t, store, ref3, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(50 * time.Millisecond)
	if e, _, _ := store.Get(ref3); entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("a removed rule acted: %v", e.Payload)
	}
}

// AN INSTALL RETURNS WITH THE ENGINE HOLDING WHAT IT WROTE, and a removal with it gone: the
// caller is answered once the follow has delivered the record, not once the write landed.
func TestAnInstallReturnsWithTheRuleStanding(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())
	restored(t, eng)
	tr := Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}
	for i := 0; i < 20; i++ {
		if err := eng.InstallTrigger(tr); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
		if _, stands := eng.Rule("review"); !stands {
			t.Fatalf("install %d returned with the rule not standing", i)
		}
		if !eng.RemoveTrigger("review") {
			t.Fatalf("removal %d: the rule did not stand to remove", i)
		}
		if _, stands := eng.Rule("review"); stands {
			t.Fatalf("removal %d returned with the rule standing", i)
		}
	}
	if eng.RemoveTrigger("review") {
		t.Fatal("a name nothing holds was removed")
	}
}

// A RECORD WHOSE WORDS MOVED IS NOT PLACED AGAIN: the same rule with its comments edited is
// written, and the engine's copy follows the record with nothing placed — where a rule whose
// value changed is placed afresh, and replays.
func TestARecordRewordedIsNotPlacedAgain(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())
	restored(t, eng)
	ruleFrom := func(src string) Trigger {
		node, err := codec.ParseNode([]byte(src), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		var tr Trigger
		if err := codec.FromNode(node, &tr); err != nil {
			t.Fatal(err)
		}
		return tr
	}
	placed := func() uint64 {
		eng.trigMu.Lock()
		defer eng.trigMu.Unlock()
		return eng.placed
	}
	body := "name: seen\ncondition:\n  subject: {path: \"demo.pr.*\", value: {stage: open}}\naction:\n  patch: {seen: true}\n"
	if err := eng.InstallTrigger(ruleFrom("# the first reason\n" + body)); err != nil {
		t.Fatal(err)
	}
	waitArmed(t, eng, "seen")
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitBool(t, store, ref, "seen", true)
	was := placed()

	reworded := ruleFrom("# a better reason\n" + body)
	if err := eng.InstallTrigger(reworded); err != nil {
		t.Fatal(err)
	}
	if got := placed(); got != was {
		t.Fatalf("the rule was placed again for a rewording (%d placings, was %d)", got, was)
	}
	if got, _ := eng.Rule("seen"); !SameText(got, reworded) {
		t.Fatal("the engine's copy did not follow the record's words")
	}
	if _, armed := eng.Trigger("seen"); !armed {
		t.Fatal("the rule is not armed after a rewording")
	}

	// Changed in value: placed afresh, and the subject that still matches is acted on again.
	changed := ruleFrom(body)
	changed.Action.Patch = setStage("reviewed").Patch
	if err := eng.InstallTrigger(changed); err != nil {
		t.Fatal(err)
	}
	if got := placed(); got == was {
		t.Fatal("a rule whose value changed was not placed again")
	}
	waitStage(t, store, ref, "reviewed")
}

// A RULE'S RECORD BEFORE ITS LEVEL'S IS HELD FOR IT, and armed when the level's record is in:
// the two are records, and a restore's picture holds them in no order.
func TestARuleArmsWhenItsLevelsRecordIsIn(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())
	restored(t, eng)
	writeRule(t, store, Trigger{
		Name:      "r",
		Stratum:   "observe",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    seenAction(),
	})
	waitHeld(t, eng, "r", "waiting for stratum observe")
	writeStratum(t, store, Stratum{Name: "observe", Level: 10, Match: []Match{mm("gh")}})
	waitArmed(t, eng, "r")
	if got := eng.Strata(); len(got) != 1 || got[0].Name != "observe" {
		t.Fatalf("strata = %+v", got)
	}
	// And the level's record deleted is the level gone.
	if _, _, err := store.Write(entity.Write{At: charter.StratumRef("", "observe").At()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(eng.Strata()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the level did not follow its record's deletion: %+v", eng.Strata())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A RECORD THE VETTING REFUSES IS PENDING WITH THE REFUSAL, its record standing: written some
// way other than an install, which refuses where the author is present, it fires on nothing
// and says why, and a record written over it that passes is armed.
func TestARefusedRecordIsPendingWithTheRefusal(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())
	restored(t, eng)
	writeRule(t, store, Trigger{
		Name:      "idle",
		Condition: Condition{Subject: mm("demo.pr.*")},
	})
	waitHeld(t, eng, "idle", "refused: ")
	waitHeld(t, eng, "idle", "does nothing")
	writeRule(t, store, Trigger{
		Name:      "idle",
		Condition: Condition{Subject: mm("demo.pr.*")},
		Action:    setStage("reviewed"),
	})
	waitArmed(t, eng, "idle")
}

// A RESTORE SAYS WHAT THE STORE IS DOING. The charter is read from its records through a
// watch, and until the watch is open nothing is restored and Restored is not closed: a verse
// under load at a restart says so (FollowState) rather than coming back running no rules. An
// empty charter restores, and says nothing is wrong, because nothing is.
func TestARestoreSaysWhatTheStoreIsDoing(t *testing.T) {
	addr := entitytest.Addr(t)
	first := New(entitytest.StoreAt(t, addr), task.AutoResolver{})
	if err := first.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*")}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatal(err)
	}

	store := entitytest.NewPhased(entitytest.StoreAt(t, addr))
	store.Set(entity.WatchRefused, errors.New("context deadline exceeded"))
	eng := New(store, task.AutoResolver{})
	if eng.Restored() != nil {
		t.Fatal("an engine nobody started has a restore to wait on")
	}
	eng.ObserveScoped(t.Context())
	select {
	case <-eng.Restored():
		t.Fatal("restored over a store that refused the charter's watch")
	case <-time.After(100 * time.Millisecond):
	}
	if st := eng.FollowState(); st.Phase != entity.WatchRefused || st.Err == nil || st.Err.Error() != "context deadline exceeded" {
		t.Fatalf("the restore says %q; want the refusal", st)
	}
	if got := charterOf(eng); len(got) != 0 {
		t.Fatalf("charter = %v over a store that has not answered", got)
	}
	store.Set(entity.WatchOpen, nil)
	restored(t, eng)
	if st := eng.FollowState(); st.Phase != entity.WatchOpen {
		t.Fatalf("the restore says %q once the store answered", st)
	}
	waitArmed(t, eng, "review")

	// Nothing to restore is restored at once.
	empty := New(entitytest.Store(t), task.AutoResolver{})
	empty.ObserveScoped(t.Context())
	restored(t, empty)
	if got := charterOf(empty); len(got) != 0 {
		t.Fatalf("charter = %v over an empty store", got)
	}
}

// A SCOPE'S ENGINE PLACES ITS OWN RECORDS AND PASSES OVER BASELINE'S, which it reads through:
// baseline's charter running in every live scope is a question about what a scope means, and a
// restore is not where it gets decided.
func TestAScopeFollowsOnlyItsOwnRecords(t *testing.T) {
	store := entitytest.Store(t)
	base := New(store, task.AutoResolver{})
	if err := base.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*")}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatal(err)
	}
	scoped := store.Scope("me")
	eng := New(scoped, task.AutoResolver{})
	eng.SetScope("me")
	eng.ObserveScoped(t.Context())
	restored(t, eng)
	if got := charterOf(eng); len(got) != 0 {
		t.Fatalf("the scope's charter = %v; baseline's rule was placed in the scope", got)
	}
	if err := eng.AddTrigger(Trigger{
		Name:      "mine",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "bug", "*")}},
		Action:    setStage("triaged"),
	}); err != nil {
		t.Fatal(err)
	}
	waitArmed(t, eng, "mine")
	// Baseline removing its rule does not touch the scope's own under that name.
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*")}},
		Action:    setStage("mine"),
	}); err != nil {
		t.Fatal(err)
	}
	waitArmed(t, eng, "review")
	if !base.RemoveTrigger("review") {
		t.Fatal("baseline's rule did not stand to remove")
	}
	time.Sleep(100 * time.Millisecond)
	if tr, ok := eng.Rule("review"); !ok || entity.Str(tr.Action.Patch, "stage") != "mine" {
		t.Fatalf("the scope's own rule did not survive baseline's removal: %+v, %v", tr, ok)
	}
}
