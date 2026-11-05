package trigger

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/internal/trigger/charter"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/task"
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
		s, err := NewSuspension(sh[0], sh[1], sh[2], false)
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
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
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

// A suspension's subject is the selector `verse flow --subject` lists by — `*` open in any segment,
// covering at or beneath — and its charter is one address, or with recursive that address and
// every charter beneath it. Two spellings of one path are one record, and the record spells every
// part: an empty subject is refused rather than read as every one. A subject is a path, so work
// about no subject stands under none, `*` included.
func TestASuspensionIsAddressedAsAFlowIs(t *testing.T) {
	sus := func(charter, rule, subject string, recursive bool) Suspension {
		t.Helper()
		s, err := NewSuspension(charter, rule, subject, recursive)
		if err != nil {
			t.Fatalf("NewSuspension(%q, %q, %q, %v): %v", charter, rule, subject, recursive, err)
		}
		return s
	}
	pr1, pr2, issue1 := entity.RefAt("demo", "pr", "1"), entity.RefAt("demo", "pr", "2"), entity.RefAt("demo", "issue", "1")
	deep := entity.RefAt("github", "signadot", "verse", "pr", "42")
	for _, c := range []struct {
		subject string
		covers  []entity.Ref
		not     []entity.Ref
	}{
		{"demo.pr.1", []entity.Ref{pr1}, []entity.Ref{pr2, issue1}},
		{"demo.*.1", []entity.Ref{pr1, issue1}, []entity.Ref{pr2}},
		{"*.pr.*", []entity.Ref{pr1, pr2}, []entity.Ref{issue1}},
		{"github.*.verse.pr.*", []entity.Ref{deep}, []entity.Ref{pr1, entity.RefAt("github", "signadot", "verse", "pr")}},
		{"*", []entity.Ref{pr1, deep}, []entity.Ref{{}}},
	} {
		s := sus("*", "", c.subject, false)
		for _, ref := range c.covers {
			if !s.CoversSubject(ref) {
				t.Errorf("%s does not cover %s", s.ID(), ref)
			}
		}
		for _, ref := range c.not {
			if s.CoversSubject(ref) {
				t.Errorf("%s covers %s", s.ID(), ref)
			}
		}
	}

	for _, same := range [][2]string{{`demo.pr."1"`, "demo.pr.1"}} {
		a, b := sus("*", "", same[0], false), sus("*", "", same[1], false)
		if a.ID() != b.ID() {
			t.Errorf("%q and %q are one selector, but two records: %s, %s", same[0], same[1], a.ID(), b.ID())
		}
	}
	// A trailing `*` asks for a depth the shorter path does not, at any length: a short path names
	// the node at it.
	for _, other := range [][2]string{{"a.b.c.d.*", "a.b.c.d"}, {"demo.pr.*", "demo.pr"}, {"*.*.*", "*"}} {
		if a, b := sus("*", "", other[0], false), sus("*", "", other[1], false); a.ID() == b.ID() {
			t.Errorf("%s asks for a depth %s does not, but both are %s", other[0], other[1], a.ID())
		}
	}

	drift := sus("verse-dev.drift", "", "*", true)
	if drift.ID() != "verse-dev.drift.**:*@*" {
		t.Errorf("a recursive charter's id = %s", drift.ID())
	}
	for _, at := range [][]string{{"verse-dev", "drift"}, {"verse-dev", "drift", "x"}} {
		if !drift.CoversCharter(at, "claim") {
			t.Errorf("%s does not cover the charter at %v", drift.ID(), at)
		}
	}
	if drift.CoversCharter([]string{"verse-dev"}, "claim") || sus("verse-dev.drift", "", "*", false).CoversCharter([]string{"verse-dev", "drift", "x"}, "claim") {
		t.Error("a charter covers what stands above it, or a charter not recursive covers what is beneath it")
	}
	back, err := SuspensionOf(drift.Payload())
	if err != nil || back.ID() != drift.ID() {
		t.Errorf("the record read back as %s (%v), want %s", back.ID(), err, drift.ID())
	}
	if !sus("verse-dev.drift", "claim", "demo.pr.1", false).Within(drift) || drift.Within(sus("verse-dev.drift", "", "*", false)) {
		t.Error("Within does not follow recursive")
	}
	if !sus("*", "", "demo.pr.1", false).Within(sus("*", "", "demo.*", false)) {
		t.Error("demo.pr.1 is not within demo.*")
	}

	// A row's charter, printed as --charter, reads back as the suspension it came from.
	for _, flag := range []string{"verse-dev.drift", "verse-dev.drift:claim", ":review", ":", "*", "*:review"} {
		address, rule := ParseCharterFlag(flag)
		if got := sus(address, rule, "*", false).CharterFlag(); got != flag {
			t.Errorf("--charter %s reads back as %s", flag, got)
		}
	}

	if place, ok := sus("*", "", "demo.*.1", false).SubjectPlace(); !ok || place != entity.RefAt("demo") {
		t.Errorf("demo.*.1's subject place = %s %v, want its literal head demo", place, ok)
	}
	if _, ok := sus("*", "", "*.pr.1", false).SubjectPlace(); ok {
		t.Error("a selector open from its first segment names a subject place")
	}

	for _, bad := range []struct {
		charter, subject string
		recursive        bool
	}{
		{"verse-dev.drift", "", false},          // an empty subject is not every subject
		{"verse-dev.*", "*", false},             // beneath is recursive, not a wildcard
		{"", "*", true},                         // baseline and beneath is every charter
		{"*", "*", true},                        // every charter has nothing beneath it
		{"*", "demo.pr.1[0]", false},            // an element of a payload is not a subject
		{"*", "demo.pr.1 {stage: open}", false}, // nor is a predicate over one
	} {
		if s, err := NewSuspension(bad.charter, "", bad.subject, bad.recursive); err == nil {
			t.Errorf("NewSuspension(%q, %q, recursive %v) accepted as %s", bad.charter, bad.subject, bad.recursive, s.ID())
		}
	}
}

// A rule suspended whole does not fire, and its commit is skipped before its watermark moves, so
// the replay a lift opens at the head is still admitted. A rule suspended about one subject fires
// about the others.
func TestASuspendedRuleIsSkippedBeforeItsWatermark(t *testing.T) {
	store := entitytest.Store(t)
	eng, tbl := suspending(t, store, "user:tester", [3]string{"", "review", "*"}, [3]string{"", "triage", "demo.pr.1"})
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
	var seen int64
	if s := eng.seen["review"]; s != nil {
		seen = s.high
	}
	eng.seenMu.Unlock()
	if seen != 0 {
		t.Fatalf("review's watermark moved to %d while it was suspended whole", seen)
	}
	s, _ := NewSuspension("", "review", "*", false)
	tbl.Lift(s.ID())
	if first, _ := eng.firstSight("review", store.Rev()); !first {
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
	// An OPEN verse, since the whole case is about a place nobody owns: what an untaken place
	// permits is the root record's to say, and a store with none admits nobody at all.
	raw := entitytest.OpenStore(t)
	store := entity.As(raw, entity.SystemDefault)
	eng, _ := suspending(t, store, "user:bob@example.com", [3]string{"*", "", "demo.pr.*"})
	// Alice's rule, not the system's: a rule the system installed is the system's alone to
	// suspend, and a rule acts as whoever owns it, so it is hers to patch her own PR with. Not
	// written down, since this store holds none of versed's seed rows to admit her record — so
	// the record is put there, a rule nobody owns acting as nobody.
	if err := eng.addTrigger(reviewRule("review", entity.Obj{"stage": "reviewed"}), "user:alice@example.com", RunnersChecked); err != nil {
		t.Fatal(err)
	}
	if err := entity.Establish(store, charter.RuleRef("", "review"), "user:alice@example.com", entity.Ref{}, nil); err != nil {
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
	if err := eng.addTrigger(reviewRule("review", entity.Obj{"stage": "reviewed"}), entity.SystemCharter, RunnersChecked); err != nil {
		t.Fatal(err)
	}
	pr2 := entity.RefAt("demo", "pr", "2")
	if held := eng.holding("review", pr2); len(held) != 0 {
		t.Fatalf("bob's suspension holds the system's rule: %v", held)
	}
	s, _ := NewSuspension("*", "", "demo.pr.2", false)
	s.Held = map[string]Holder{"user:bob@example.com": {Why: "mine"}, "system:throttle": {Why: "the system's"}}
	tbl.Put(s)
	if held := eng.holding("review", pr2); len(held) != 1 {
		t.Fatalf("the system's suspension does not hold the system's rule: %v", held)
	}
}

// heldHandle is a task that runs until it is told to finish, counting the holds it is sent.
type heldHandle struct {
	ch     chan task.Event
	mu     sync.Mutex
	pauses int
}

func (h *heldHandle) Subscribe() (<-chan task.Event, func())           { return h.ch, func() {} }
func (h *heldHandle) SubscribeSince(int64) (<-chan task.Event, func()) { return h.ch, func() {} }
func (h *heldHandle) Pause(string) error                               { h.mu.Lock(); h.pauses++; h.mu.Unlock(); return nil }
func (h *heldHandle) Resume(string) error                              { return nil }
func (h *heldHandle) Cancel(string, string) error                      { return nil }
func (h *heldHandle) Steer(string, string, string) error               { return nil }
func (h *heldHandle) SetAutonomy(task.Autonomy, string) error          { return nil }
func (h *heldHandle) Result() string                                   { return "" }
func (h *heldHandle) Err() string                                      { return "" }
func (*heldHandle) Refused() map[string]int                            { return nil }
func (*heldHandle) Agent() (string, map[string]string)                 { return "", nil }

// finish ends the task done.
func (h *heldHandle) finish() {
	h.ch <- task.Event{Kind: task.EventStatus, Text: string(task.Done)}
}

// spawned is a spawner of heldHandles that remembers them by id.
type spawned struct {
	mu sync.Mutex
	by map[string]*heldHandle
}

func (s *spawned) spawn(id string, _ launch.Spec) task.Handle {
	h := &heldHandle{ch: make(chan task.Event, 1)}
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
				Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review"},
				OnDone: &Action{
					Patch: entity.Obj{"stage": "reviewed"}.Payload(),
					Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "merge"},
				},
			},
		},
		{
			Name:      "pick",
			Condition: Condition{Subject: prSubject("picking")},
			Action: Action{
				Patch: entity.Obj{"stage": "picked"}.Payload(),
				Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "pick"},
				OnDone: &Action{
					Patch: entity.Obj{"stage": "chosen"}.Payload(),
					Spawn: &launch.Spec{Prompt: "act", Agent: &runner.Agent{Name: "verse"}},
				},
			},
		},
		{
			Name:      "triage",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "issue", "*"), "value": map[string]any{"stage": "open"}}},
			Action: Action{
				Patch:  entity.Obj{"stage": "triaging"}.Payload(),
				Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "triage"},
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
		s, _ := NewSuspension("*", "", subj, false)
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

	s, _ := NewSuspension("*", "", "demo.pr.1", false)
	tbl.Lift(s.ID())
	sp.wait(t, "review.onDone@demo.pr.1")
	if n := eng.Parked(); n != 1 {
		t.Fatalf("%d launches parked after demo.pr.1's lift, want demo.pr.2's still held", n)
	}
}

// holdSpawner is spawned with one id held at the spawner until release is closed, and reports
// through entered when it gets there: a lift stopped in the middle of starting a parked launch.
func holdSpawner(sp *spawned, id string, entered, release chan struct{}) Spawner {
	return func(got string, spec launch.Spec) task.Handle {
		if got == id {
			close(entered)
			<-release
		}
		return sp.spawn(got, spec)
	}
}

// THE INTERLEAVING A LIFT IS ORDERED FOR. A task about a suspended PR finishes, its hook marks the
// PR reviewed and parks the notification it would start. The lift is caught starting that launch:
// the suspension is draining, so when a sibling PR's commit re-evaluates the merge rule, the rule
// fires about the sibling and is refused about the PR whose launch is not yet live. Once the lift
// has removed the entry and armed the rule again, it acts on the PR — even though the sibling's
// evaluation moved its watermark to the head its replay arrives at.
func TestDrainingRefusesAFiringWhileAParkedLaunchStarts(t *testing.T) {
	store := entitytest.Store(t)
	eng, tbl := suspending(t, store, "user:tester")
	sp := &spawned{by: map[string]*heldHandle{}}
	entered, release := make(chan struct{}), make(chan struct{})
	eng.SetSpawner(holdSpawner(sp, "review.onDone@demo.pr.1", entered, release))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	for _, trig := range []Trigger{
		{
			Name:      "review",
			Condition: Condition{Subject: prSubject("open")},
			Action: Action{
				Patch:  entity.Obj{"stage": "reviewing"}.Payload(),
				Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review"},
				OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload(), Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "notify"}},
			},
		},
		{
			Name:      "merge",
			Condition: Condition{Subject: prSubject("reviewed")},
			Action:    Action{Patch: entity.Obj{"stage": "merging"}.Payload(), Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "merge"}},
		},
	} {
		if err := eng.AddTrigger(trig); err != nil {
			t.Fatalf("AddTrigger %s: %v", trig.Name, err)
		}
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	review := sp.wait(t, "review@demo.pr.1")
	s, _ := NewSuspension("*", "", "demo.pr.1", false)
	s.Held = map[string]Holder{"user:tester": {Why: "hold"}}
	tbl.Put(s)
	review.finish()
	waitStage(t, store, prRef("1"), "reviewed")
	stays(t, store, prRef("1"), "stage", "reviewed") // merge is refused about it while suspended

	lifted := make(chan struct{})
	go func() { tbl.Lift(s.ID()); close(lifted) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the lift never started the parked launch")
	}
	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "reviewed"}.Payload())
	sp.wait(t, "merge@demo.pr.2")
	time.Sleep(200 * time.Millisecond)
	if sp.get("merge@demo.pr.1") != nil {
		t.Fatal("merge fired about the PR whose parked launch was still starting")
	}
	close(release)
	<-lifted
	sp.wait(t, "review.onDone@demo.pr.1")
	sp.wait(t, "merge@demo.pr.1")
}

// A firing's question is posed under a context of its own beneath the mootness context: a
// suspension withdraws it and the lift asks it again under the same id, and when the rule stops
// selecting the subject during a suspension the firing ends and the lift asks nothing.
func TestAPoseIsWithdrawnByEitherAndAskedAgainAfterOne(t *testing.T) {
	store := entitytest.Store(t)
	eng, tbl := suspending(t, store, "user:tester")
	reg := eng.resolver.(*task.Registry)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	if err := eng.AddTrigger(Trigger{
		Name:      "ship",
		Condition: Condition{Subject: prSubject("open")},
		Gates:     []Gate{{Name: "ok", Mode: "ask"}},
		Action:    Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	pending := func() []string {
		var ids []string
		for _, p := range reg.Pending() {
			ids = append(ids, p.Question.ID)
		}
		return ids
	}
	waitPending := func(n int) []string {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			if ids := pending(); len(ids) == n {
				return ids
			}
			select {
			case <-deadline:
				t.Fatalf("pending = %v, want %d", pending(), n)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	asked := waitPending(1)[0]
	s, _ := NewSuspension("*", "", "demo.pr.1", false)
	s.Held = map[string]Holder{"user:tester": {Why: "hold"}}

	tbl.Put(s)
	waitPending(0)
	tbl.Lift(s.ID())
	if again := waitPending(1)[0]; again != asked {
		t.Fatalf("the lift asked %s, want the same question %s", again, asked)
	}

	tbl.Put(s)
	waitPending(0)
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "closed"}.Payload())
	time.Sleep(200 * time.Millisecond)
	tbl.Lift(s.ID())
	time.Sleep(300 * time.Millisecond)
	if ids := pending(); len(ids) != 0 {
		t.Fatalf("the lift asked %v about a subject the rule no longer selects", ids)
	}
	if n := eng.Fired(); n != 0 {
		t.Fatalf("the moot firing acted %d times", n)
	}
}

// A resumed rule sees what an install sees: a level rule still matching acts once, one whose
// condition came and went during the suspension does nothing, and an `on` edge that rose
// meanwhile is not seen.
func TestALiftActsOnTheWorldAsItStands(t *testing.T) {
	store := entitytest.Store(t)
	eng, tbl := suspending(t, store, "user:tester", [3]string{"", "review", "*"}, [3]string{"", "start", "*"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	rel := entity.RefAt("ci", "status", "release")
	job := entity.RefAt("demo", "job", "1")
	entitytest.Set(t, store, rel, entity.Obj{"state": "red"}.Payload())
	for _, trig := range []Trigger{
		reviewRule("review", entity.Obj{"stage": "reviewed"}),
		{
			Name: "start",
			Condition: Condition{
				On:      MatchWhen(Match{"path": pathOf("ci", "status", "release"), "value": map[string]any{"state": "green"}}),
				Subject: Match{"path": pathOf("demo", "job", "*"), "value": map[string]any{"stage": "waiting"}},
			},
			Action: Action{Patch: entity.Obj{"stage": "started"}.Payload()},
		},
	} {
		if err := eng.AddTrigger(trig); err != nil {
			t.Fatalf("AddTrigger %s: %v", trig.Name, err)
		}
	}

	entitytest.Set(t, store, job, entity.Obj{"stage": "waiting"}.Payload())
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, rel, entity.Obj{"state": "green"}.Payload())
	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "closed"}.Payload())
	stays(t, store, prRef("1"), "stage", "open")

	for _, rule := range []string{"review", "start"} {
		s, _ := NewSuspension("", rule, "*", false)
		tbl.Lift(s.ID())
	}
	waitStage(t, store, prRef("1"), "reviewed")
	stays(t, store, prRef("2"), "stage", "closed")
	stays(t, store, job, "stage", "waiting")
	if n := eng.Fired(); n != 1 {
		t.Fatalf("the lift acted %d times, want once — for the PR still open", n)
	}
}

// A RULE PATCHES A ROW THAT HOLDS SOMEBODY ELSE'S PLACE. A patch reads the node, changes it and
// writes the whole of it back; read as the engine — the system, which sees everything — what it
// wrote back carried carol's closed place, and a write that reaches into a place its writer may
// not write is refused. So the read is the rule's, and the place is left as it stands.
func TestARulePatchesARowThatHoldsSomebodyElsesPlace(t *testing.T) {
	raw := entitytest.OpenStore(t)
	store := entity.As(raw, entity.SystemDefault)
	eng := New(store, task.NewRegistry())
	alice, carol := "user:alice@example.com", "user:carol@example.com"
	if err := eng.addTrigger(reviewRule("review", entity.Obj{"stage": "reviewed"}), alice, RunnersChecked); err != nil {
		t.Fatal(err)
	}
	if err := entity.Establish(store, charter.RuleRef("", "review"), alice, entity.Ref{}, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// The row is BOB's, and alice's rule holds read-write on it by his grant — not ownership,
	// which would descend into every place in it. Carol keeps something in the row, closed to
	// everyone: bob co-owns it, alice and her rule hold nothing there.
	bob := "user:bob@example.com"
	pr, inner := entity.RefAt("demo", "pr", "1"), entity.RefAt("demo", "pr", "1", "c")
	if _, _, err := entity.As(raw, bob).Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "draft"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, entity.RefAt("demo"), []entity.Grant{
		{At: []string{"demo"}, Below: true, To: map[entity.Level][]string{entity.Read: {entity.Everyone}, entity.ReadWrite: {alice}}},
		{At: pr.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {carol}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := entity.As(raw, carol).Write(entity.Write{At: inner.At(), Value: entity.Obj{"secret": "hers"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, inner, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(store, alice, entity.Read, inner); ok {
		t.Fatal("the fixture is wrong: alice reads carol's place")
	}
	if _, _, err := entity.As(raw, bob).Write(entity.Write{At: pr.At("stage"), Value: entity.Val("open")}); err != nil {
		t.Fatal(err)
	}
	waitStage(t, store, pr, "reviewed")
	if e, _, _ := store.Get(inner); entity.Str(e.Payload, "secret") != "hers" {
		t.Errorf("carol's place did not survive the patch: %v", e.Payload)
	}
}
