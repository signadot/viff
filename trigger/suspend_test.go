package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// suspending is an engine over store with a loaded table of its own and the given suspensions in
// it, each held by holder.
func suspending(t *testing.T, store entity.Store, holder string, shapes ...[3]string) (*Engine, *Suspensions) {
	t.Helper()
	eng := New(store, task.NewRegistry())
	tbl := NewSuspensions()
	tbl.Load(nil)
	eng.SetSuspensions(tbl)
	for _, sh := range shapes {
		s, err := NewSuspension(sh[0], sh[1], sh[2])
		if err != nil {
			t.Fatal(err)
		}
		s.Held = map[string]Holder{holder: {Why: "testing"}}
		tbl.Put(s)
	}
	return eng, tbl
}

// reviewRule is a level rule: every open PR is patched with what it is given.
func reviewRule(name string, patch entity.Obj) Trigger {
	return Trigger{
		Name:      name,
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    Action{Patch: patch.Payload()},
	}
}

// stays asserts an entity's field still reads want after the engine has had time to act.
func stays(t *testing.T, s entity.Store, ref entity.Ref, field, want string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	e, _, _ := s.Get(ref)
	if got := entity.Str(e.Payload, field); got != want {
		t.Fatalf("%s.%s = %q, want it still %q", ref, field, got, want)
	}
}

// A rule suspended whole does not fire, and its commit is skipped before its watermark moves, so
// the replay a lift opens at the head is still admitted. A rule suspended about one subject fires
// about the others.
func TestASuspendedRuleIsSkippedBeforeItsWatermark(t *testing.T) {
	store := entitytest.Store(t)
	eng, tbl := suspending(t, store, "user:tester", [3]string{"", "review", ""}, [3]string{"", "triage", "demo.pr.1"})
	if err := eng.AddTrigger(reviewRule("review", entity.Obj{"stage": "reviewed"})); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddTrigger(reviewRule("triage", entity.Obj{"triaged": "yes"})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	pr1, pr2 := entity.RefAt("demo", "pr", "1"), entity.RefAt("demo", "pr", "2")
	entitytest.Set(t, store, pr1, entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, pr2, entity.Obj{"stage": "open"}.Payload())

	waitField(t, store, pr2, "triaged", "yes") // triage is held about pr1 only
	stays(t, store, pr1, "triaged", "")
	stays(t, store, pr1, "stage", "open")
	stays(t, store, pr2, "stage", "open")
	eng.seenMu.Lock()
	seen := eng.seen["review"]
	eng.seenMu.Unlock()
	if seen != 0 {
		t.Fatalf("review's watermark moved to %d while it was suspended whole", seen)
	}
	s, _ := NewSuspension("", "review", "")
	tbl.Remove(s.ID())
	if !eng.firstSight("review", store.Rev()) {
		t.Fatal("the head is not a first sight for review once lifted: its replay would be refused")
	}
}

// A suspended firing is refused before the cycle throttle, which counts a trip and marks its
// chain reported: the throttle never sees a firing that was not going to happen.
func TestASuspendedFiringCountsNoTrip(t *testing.T) {
	pr1 := entity.RefAt("demo", "pr", "1")
	for _, suspended := range []bool{false, true} {
		store := entitytest.Store(t)
		var shapes [][3]string
		if suspended {
			shapes = append(shapes, [3]string{"*", "", "demo.pr.1"})
		}
		eng, _ := suspending(t, store, "user:tester", shapes...)
		eng.SetTripPace(0)
		trig := reviewRule("review", entity.Obj{"stage": "reviewed"})
		if err := eng.AddTrigger(trig); err != nil {
			t.Fatal(err)
		}
		// A commit this engine wrote deep in a chain, about the subject.
		rev := int64(99)
		eng.chainMu.Lock()
		eng.causes[rev] = link{depth: 5, chain: "review", parent: 98}
		eng.chainMu.Unlock()
		c := entity.Commit{Rev: rev, Changes: []entity.Event{{Ref: pr1}}}
		eng.fireSubjects(context.Background(), trig, c, []entity.Ref{pr1})
		eng.chainMu.Lock()
		trips := eng.trips
		eng.chainMu.Unlock()
		if suspended && trips != 0 {
			t.Fatalf("a suspended firing counted %d trips", trips)
		}
		if !suspended && trips != 1 {
			t.Fatalf("the unsuspended firing counted %d trips; the setup does not trip the throttle", trips)
		}
	}
}

// Every charter-authored write passes through patchEntity, so a firing's write about a suspended
// subject is refused there whatever path reached it. A completion hook's write lands: it records
// what a task did, and what it wakes is held.
func TestASuspendedSubjectRefusesAFiringsWriteAndTakesAHooks(t *testing.T) {
	store := entitytest.Store(t)
	eng, _ := suspending(t, store, "user:tester", [3]string{"*", "", "demo.pr.1"})
	pr1 := entity.RefAt("demo", "pr", "1")
	if err := eng.AddTrigger(reviewRule("review", entity.Obj{"stage": "reviewed"})); err != nil {
		t.Fatal(err)
	}
	for _, chain := range []string{"review", "review.approval.onDenied"} {
		if err := eng.patchEntity(chain, 0, pr1, pr1, entity.Obj{"stage": chain}.Payload()); err == nil {
			t.Fatalf("a write by %s about a suspended subject landed", chain)
		}
	}
	if _, ok, _ := store.Get(pr1); ok {
		t.Fatal("a refused write left the subject written")
	}
	for _, chain := range []string{"review.onDone", "review.seq.1"} {
		if err := eng.patchEntity(chain, 0, pr1, pr1, entity.Obj{"stage": chain}.Payload()); err != nil {
			t.Fatalf("%s's write about a suspended subject was refused: %v", chain, err)
		}
	}
	waitStage(t, store, pr1, "review.seq.1")
}

// A suspension holds its scope's work and nobody else's: the same rule about the same subject
// in an engine whose table holds nothing fires.
func TestAnotherScopesRuleAboutTheSameSubjectFires(t *testing.T) {
	store := entitytest.Store(t)
	mine, _ := suspending(t, store, "user:tester", [3]string{"*", "", "demo.pr.1"})
	mine.SetScope("me")
	theirs, _ := suspending(t, store, "user:tester")
	if err := mine.AddTrigger(reviewRule("review", entity.Obj{"mine": "yes"})); err != nil {
		t.Fatal(err)
	}
	if err := theirs.AddTrigger(reviewRule("review", entity.Obj{"theirs": "yes"})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mine.Start(ctx)
	theirs.Start(ctx)
	pr1 := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, pr1, entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, pr1, "theirs", "yes")
	stays(t, store, pr1, "mine", "")
}

// A holder's suspension covers work only while the holder may suspend it, asked when it would
// apply: bob's `demo.pr.*` holds the PR nobody owns, and stops holding the one alice does.
func TestAWildcardStopsCoveringWhatSomebodyOwns(t *testing.T) {
	raw := entitytest.Store(t)
	store := entity.As(raw, entity.SystemDefault)
	eng, _ := suspending(t, store, "user:bob@example.com", [3]string{"*", "", "demo.pr.*"})
	// Carol's rule, not the system's: a rule the system installed is the system's alone to
	// suspend. Not written down, since this store holds no seed to admit carol's record.
	if err := eng.addTrigger(reviewRule("review", entity.Obj{"stage": "reviewed"}), false, "user:carol@example.com"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	nobodys, alices := entity.RefAt("demo", "pr", "2"), entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, nobodys, entity.Obj{"stage": "open"}.Payload())
	stays(t, store, nobodys, "stage", "open")
	if _, _, err := entity.As(raw, "user:alice@example.com").Write(entity.Write{
		At: alices.At(), Value: entity.Obj{"stage": "open"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	waitStage(t, store, alices, "reviewed")
	stays(t, store, nobodys, "stage", "open")
}

// A rule the system installed is the system's to suspend: a person's suspension of its subject
// does not hold it, and the system's does.
func TestOnlyTheSystemSuspendsTheSystemsRule(t *testing.T) {
	store := entity.As(entitytest.Store(t), entity.SystemDefault)
	eng, tbl := suspending(t, store, "user:bob@example.com", [3]string{"*", "", "demo.pr.2"})
	if err := eng.addTrigger(reviewRule("review", entity.Obj{"stage": "reviewed"}), false, entity.SystemCharter); err != nil {
		t.Fatal(err)
	}
	pr2 := entity.RefAt("demo", "pr", "2")
	if held := eng.holding("review", pr2); len(held) != 0 {
		t.Fatalf("bob's suspension holds the system's rule: %v", held)
	}
	s, _ := NewSuspension("*", "", "demo.pr.2")
	s.Held = map[string]Holder{"user:bob@example.com": {Why: "mine"}, "system:throttle": {Why: "the system's"}}
	tbl.Put(s)
	if held := eng.holding("review", pr2); len(held) != 1 {
		t.Fatalf("the system's suspension does not hold the system's rule: %v", held)
	}
}
