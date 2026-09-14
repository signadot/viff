package trigger

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
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

// heldHandle is a task that runs until it is told to finish, counting the holds it is sent.
type heldHandle struct {
	ch     chan task.Frame
	mu     sync.Mutex
	pauses int
}

func (h *heldHandle) Subscribe() (<-chan task.Frame, func())           { return h.ch, func() {} }
func (h *heldHandle) SubscribeSince(int64) (<-chan task.Frame, func()) { return h.ch, func() {} }
func (h *heldHandle) Pause() error                                     { h.mu.Lock(); h.pauses++; h.mu.Unlock(); return nil }
func (h *heldHandle) Resume() error                                    { return nil }
func (h *heldHandle) Cancel(string) error                              { return nil }
func (h *heldHandle) Steer(string, string) error                       { return nil }
func (h *heldHandle) SetVector(*task.Vector, string) error             { return nil }
func (h *heldHandle) Result() string                                   { return "" }
func (h *heldHandle) Err() string                                      { return "" }

// finish ends the task done.
func (h *heldHandle) finish() { h.ch <- task.Frame{Kind: task.EvStatus, Text: string(task.Done)} }

// spawned is a spawner of heldHandles that remembers them by id.
type spawned struct {
	mu sync.Mutex
	by map[string]*heldHandle
}

func (s *spawned) spawn(id string, _ launch.Spec) task.Handle {
	h := &heldHandle{ch: make(chan task.Frame, 1)}
	s.mu.Lock()
	s.by[id] = h
	s.mu.Unlock()
	return h
}

func (s *spawned) get(id string) *heldHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.by[id]
}

func (s *spawned) wait(t *testing.T, id string) *heldHandle {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if h := s.get(id); h != nil {
			return h
		}
		select {
		case <-deadline:
			t.Fatalf("%s never spawned", id)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A task finishing under a suspension lands its hook — the hook's patch is on the subject — and
// parks what the hook would start, without a selection posed for it and without the engine's reap
// loop waiting on it: another task in the engine finishes and hooks meanwhile. The lift starts the
// parked launch.
func TestAHookLandsAndItsLaunchParks(t *testing.T) {
	store := entitytest.Store(t)
	eng, tbl := suspending(t, store, "user:tester")
	sp := &spawned{by: map[string]*heldHandle{}}
	eng.SetSpawner(sp.spawn)
	reg := eng.resolver.(*task.Registry)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	for _, trig := range []Trigger{
		{
			Name:      "review",
			Condition: Condition{Subject: prSubject("open")},
			Action: Action{
				Patch: entity.Obj{"stage": "reviewing"}.Payload(),
				Spawn: &launch.Spec{Prompt: "review"},
				OnDone: &Action{
					Patch: entity.Obj{"stage": "reviewed"}.Payload(),
					Spawn: &launch.Spec{Prompt: "merge"},
				},
			},
		},
		{
			Name:      "pick",
			Condition: Condition{Subject: prSubject("picking")},
			Action: Action{
				Patch: entity.Obj{"stage": "picked"}.Payload(),
				Spawn: &launch.Spec{Prompt: "pick", Grant: "read-only"},
				OnDone: &Action{
					Patch: entity.Obj{"stage": "chosen"}.Payload(),
					Spawn: &launch.Spec{Prompt: "act", Grant: "read-only",
						Provider: &launch.Provider{Select: "ask", Eligible: []string{"a", "b"}}},
				},
			},
		},
		{
			Name:      "triage",
			Condition: Condition{Subject: Match{"system": "demo", "kind": "issue", "value": map[string]any{"stage": "open"}}},
			Action: Action{
				Patch:  entity.Obj{"stage": "triaging"}.Payload(),
				Spawn:  &launch.Spec{Prompt: "triage"},
				OnDone: &Action{Patch: entity.Obj{"stage": "triaged"}.Payload()},
			},
		},
	} {
		if err := eng.AddTrigger(trig); err != nil {
			t.Fatalf("AddTrigger %s: %v", trig.Name, err)
		}
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "picking"}.Payload())
	review := sp.wait(t, "review@demo.pr.1")
	pick := sp.wait(t, "pick@demo.pr.2")

	for _, subj := range []string{"demo.pr.1", "demo.pr.2"} {
		s, _ := NewSuspension("*", "", subj)
		s.Held = map[string]Holder{"user:tester": {Why: "hold"}}
		tbl.Put(s)
	}
	review.finish()
	pick.finish()
	waitStage(t, store, prRef("1"), "reviewed")
	waitStage(t, store, prRef("2"), "chosen")

	entitytest.Set(t, store, entity.RefAt("demo", "issue", "1"), entity.Obj{"stage": "open"}.Payload())
	sp.wait(t, "triage@demo.issue.1").finish()
	waitStage(t, store, entity.RefAt("demo", "issue", "1"), "triaged")

	if sp.get("review.onDone@demo.pr.1") != nil {
		t.Fatal("a hook's spawn about a suspended subject started")
	}
	if n := eng.Parked(); n != 2 {
		t.Fatalf("%d launches parked, want the two hooks'", n)
	}
	if p := reg.Pending(); len(p) != 0 {
		t.Fatalf("a hook under a suspension posed a selection: %+v", p)
	}

	s, _ := NewSuspension("*", "", "demo.pr.1")
	tbl.Remove(s.ID())
	sp.wait(t, "review.onDone@demo.pr.1")
	if n := eng.Parked(); n != 1 {
		t.Fatalf("%d launches parked after demo.pr.1's lift, want demo.pr.2's still held", n)
	}
}
