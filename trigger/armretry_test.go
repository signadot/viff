package trigger

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger/charter"
)

// phasedStore is a store whose rule watches stand in the phase the test sets
// (entitytest.Phased), with the charter's own watch the store's: the follow is what places
// the rules the test arms.
func phasedStore(t *testing.T, s entity.Store) *entitytest.Phased {
	t.Helper()
	ph := entitytest.NewPhased(s)
	ph.Hold = func(prefix []string) bool { return !slices.Equal(prefix, charter.Prefix) }
	return ph
}

// A RESTORED RULE WHOSE WATCH THE STORE REFUSED IS ARMED, AND ARMING: it stands in the charter
// by name, status names it under `arming:` with what the store is doing — refused, and asked
// again — and the asking is the store's, behind the arm. When the store answers it watches, and
// it acts on the backlog it could not see meanwhile. Before this it stood in the charter deaf
// until the next restart, counted as installed by every listing.
func TestARestoredRuleKeepsAskingForItsWatch(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, task.AutoResolver{})
	for _, name := range []string{"review", "other"} {
		if err := eng.AddTrigger(Trigger{
			Name:      name,
			Condition: Condition{Subject: Match{"path": pathOf("demo", name, "*"), "value": map[string]any{"stage": "open"}}},
			Action:    setStage("reviewed"),
		}); err != nil {
			t.Fatalf("AddTrigger %s: %v", name, err)
		}
	}

	store := phasedStore(t, entitytest.StoreAt(t, addr))
	store.Set(entity.WatchRefused, errors.New("context deadline exceeded"))
	back := reopened(t, store)
	waitArming(t, back, []string{"other", "review"})
	if got := back.Held(); len(got) != 0 {
		t.Fatalf("pending = %v; a rule whose watch the store is opening is armed, not held", got)
	}
	if got := back.Triggers(); len(got) != 2 {
		t.Fatalf("armed = %v; want both rules armed, arming", got)
	}
	for _, p := range back.Arming() {
		if !strings.HasPrefix(p.Cause, "the store refused its watch, and is asked again: context deadline") {
			t.Fatalf("arming %s says %q; want the refusal, asked again", p.Rule, p.Cause)
		}
	}
	if n := back.NotesOn(Trigger{Name: "review"}); n.Waits == "" || !n.Arming {
		t.Fatalf("notes = %+v; want the rule arming, with what the store is doing", n)
	}

	// Deaf meanwhile: a subject that matches is not acted on.
	ref := entity.RefAt("demo", "review", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(30 * time.Millisecond)
	if e, _, _ := store.Get(ref); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("a rule with no watch acted: %v", e.Payload)
	}

	// A removal while arming stops the asking, as it stops the watching.
	if !back.RemoveTrigger("other") {
		t.Fatal("other was not in the charter to remove")
	}
	waitArming(t, back, []string{"review"})
	if got := back.Triggers(); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("armed = %v after the removal; want review alone", got)
	}

	// The store answers: the rule watches, arming clears, and the backlog it missed is acted on.
	store.Set(entity.WatchOpen, nil)
	waitStage(t, store, ref, "reviewed")
	waitArming(t, back, nil)
	if got := back.Triggers(); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("armed = %v; want review", got)
	}
}

// WHAT THE STORE IS DOING IS WHAT `arming:` SAYS: a rule whose watch is in line, or being
// opened again after a drop, is named with those words, and leaves the list on its own when
// the watch opens. Nothing moves between the lists.
func TestArmingSaysWhatTheStoreIsDoing(t *testing.T) {
	store := phasedStore(t, entitytest.Store(t))
	store.Set(entity.WatchInLine, nil)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	waitArmingSays(t, eng, "review", "in line at the store")
	if ready := eng.Ready("review"); ready == nil {
		t.Fatal("an armed rule has no Ready to wait on")
	} else {
		select {
		case <-ready:
			t.Fatal("Ready closed while the watch was in line")
		default:
		}
	}
	store.Set(entity.WatchOpen, nil)
	waitArming(t, eng, nil)
	select {
	case <-eng.Ready("review"):
	case <-time.After(2 * time.Second):
		t.Fatal("Ready did not close once the watch opened")
	}
	// A drop: the rule is still armed, and arming again with the drop's words, until it reopens.
	store.Set(entity.WatchReopening, nil)
	waitArmingSays(t, eng, "review", "its watch is being opened again after a drop")
	if got := eng.Held(); len(got) != 0 {
		t.Fatalf("pending = %v; a dropped watch does not move the rule", got)
	}
	store.Set(entity.WatchOpen, nil)
	waitArming(t, eng, nil)
}

// A RULE REMOVED WHILE ARMING LEAVES ITS WAITERS NOTHING TO WAIT FOR: Ready closes on the
// removal, for the one who took it before and for the one who asks after.
func TestReadyClosesWhenARuleIsRemovedWhileArming(t *testing.T) {
	store := phasedStore(t, entitytest.Store(t))
	store.Set(entity.WatchInLine, nil)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	waitArmingSays(t, eng, "review", "in line at the store")
	before := eng.Ready("review")
	if !eng.RemoveTrigger("review") {
		t.Fatal("review was not in the charter to remove")
	}
	select {
	case <-before:
	case <-time.After(2 * time.Second):
		t.Fatal("Ready taken before the removal never closed")
	}
	if after := eng.Ready("review"); after != nil {
		t.Fatal("a removed rule has a Ready to wait on")
	}
}

// A WATCH THE STORE CANNOT OPEN AT ALL leaves nothing to wait for: the rule stands armed, says
// so under `arming:` with the store's words, and Ready closes rather than holding an install's
// wait to its deadline.
func TestAFailedWatchReleasesTheWait(t *testing.T) {
	store := phasedStore(t, entitytest.Store(t))
	store.Set(entity.WatchInLine, nil)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	waitArmingSays(t, eng, "review", "in line at the store")
	ready := eng.Ready("review")
	store.Set(entity.WatchFailed, errors.New("invalid path"))
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("Ready held on a watch the store cannot open")
	}
	waitArmingSays(t, eng, "review", "the store cannot watch it: invalid path")
	if got := eng.Triggers(); len(got) != 1 {
		t.Fatalf("armed = %v; the rule stands, with the store's verdict on its watch", got)
	}
}

// A WAIT FOLLOWS THE RULE THROUGH A MOVE: a rule lifted while its watch was opening is held
// again for the pass and armed afresh, and the wait taken before the lift holds until the
// watch the rule has now is open.
func TestReadyFollowsARuleThroughALift(t *testing.T) {
	store := phasedStore(t, entitytest.Store(t))
	store.Set(entity.WatchInLine, nil)
	eng, tbl := suspending(t, store, "user:tester")
	eng.ObserveScoped(t.Context())
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	waitArmingSays(t, eng, "review", "in line at the store")
	ready := eng.Ready("review")
	s, err := NewSuspension("", "review", "*", false)
	if err != nil {
		t.Fatal(err)
	}
	s.Held = map[string]Holder{"user:tester": {Why: "testing"}}
	tbl.Put(s)
	tbl.Lift(s.ID())
	waitArmingSays(t, eng, "review", "in line at the store")
	select {
	case <-ready:
		t.Fatal("Ready closed on the lift, with the rule's new watch still in line")
	case <-time.After(100 * time.Millisecond):
	}
	store.Set(entity.WatchOpen, nil)
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("Ready did not close once the lifted rule's watch opened")
	}
}

// waitArming waits until the arming rules are exactly these, by name.
func waitArming(t *testing.T, e *Engine, want []string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		var got []string
		for _, p := range e.Arming() {
			got = append(got, p.Rule)
		}
		if len(got) == len(want) {
			same := true
			for i := range got {
				same = same && got[i] == want[i]
			}
			if same {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("arming = %v, want %v", got, want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitArmingSays waits until the rule is arming with words that begin so.
func waitArmingSays(t *testing.T, e *Engine, name, words string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		for _, p := range e.Arming() {
			if p.Rule == name && strings.HasPrefix(p.Cause, words) {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("arming = %+v; want %s arming: %q", e.Arming(), name, words)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
