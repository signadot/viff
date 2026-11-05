package trigger

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// A chain begins at an external write and gains a hop per firing.
//
// This is the property the whole throttle rests on, tested end to end rather than on the map
// alone: a person writes, a rule fires and writes, a second rule fires off THAT and writes. The
// engine did not cause the first commit, so it is depth 0; the two it did cause are 1 and 2.
//
// The two rules here are the convergence note's fwd/back pair with the loop opened out — a→b→c
// rather than a→b→a — so the chain climbs exactly as a runaway would without running forever.
func TestCausalDepthClimbsWithEachHop(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	for _, tr := range []Trigger{
		{
			Name:      "fwd",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "a"}}},
			Action:    Action{Patch: entity.Obj{"stage": "b"}.Payload()},
		},
		{
			Name:      "onward",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "b"}}},
			Action:    Action{Patch: entity.Obj{"stage": "c"}.Payload()},
		},
	} {
		if err := eng.AddTrigger(tr); err != nil {
			t.Fatalf("AddTrigger %s: %v", tr.Name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("demo", "pr", "1")
	_, external, err := store.Write(entity.Write{At: ref.At(), Value: entity.Obj{"stage": "a"}.Payload()})
	if err != nil {
		t.Fatal(err)
	}
	waitForStage(t, store, ref, "c")

	// The write nobody in this engine made begins the chain, and must stay unattributed: it is
	// what makes a person's write, a restart, and a peer's write all read the same way.
	if got := eng.depthOf(external); got != 0 {
		t.Errorf("the external write reads at depth %d; a commit the engine did not cause is depth 0", got)
	}

	fwd, ok := awaitCause(eng, "fwd")
	if !ok {
		t.Fatal("no commit recorded for fwd; the action's write carried no chain")
	}
	if fwd.depth != 1 || fwd.parent != external {
		t.Errorf("fwd wrote {depth %d, parent %d}, want depth 1 off the external commit %d",
			fwd.depth, fwd.parent, external)
	}
	onward, ok := awaitCause(eng, "onward")
	if !ok {
		t.Fatal("no commit recorded for onward; the second hop carried no chain")
	}
	if onward.depth != 2 {
		t.Errorf("onward wrote at depth %d, want 2 — it fired off a commit the engine made", onward.depth)
	}
	if _, isFwds := eng.causeOf(onward.parent); !isFwds {
		t.Error("onward's parent is not a commit this engine caused; the hop was not inherited")
	}
}

// The engine's own bookkeeping stays OUT of every chain, and the charter record is the case that
// would be noticed last: AddTrigger writes one, at install, with no firing anywhere near it. A
// stamp there would give the first firing of every rule a parent it never had.
func TestMachineryWritesCarryNoChain(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	ch, unsub := store.Observe(entity.PrefixPattern(nil, 3), entity.PrefixPattern(nil, 4))
	defer unsub()
	if err := eng.AddTrigger(Trigger{
		Name:      "ship",
		Condition: Condition{Subject: mm("demo.pr.*")},
		Action:    Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}

	// The charter write is the only commit this can be, since the engine is not started.
	select {
	case c := <-ch:
		if got := eng.depthOf(c.Rev); got != 0 {
			t.Errorf("the charter record landed at depth %d; installing a rule is not a firing", got)
		}
		if _, ok := eng.causeOf(c.Rev); ok {
			t.Error("the charter record was placed in a causal chain")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no commit observed for the charter write")
	}
}

// A firing with no engine-caused cause starts a fresh chain rather than inheriting a stale one.
// A completion hook is the case that matters — taskEvent leaves Rev unset, because the cause is
// a task finishing, which is the world coming back — and this pins the arithmetic that makes
// "no parent" and "parent 0" the same answer.
func TestUncausedFiringStartsAFreshChain(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})

	eng.recordCause(100, "hook", 0)
	if got := eng.depthOf(100); got != 1 {
		t.Errorf("a write off no cause landed at depth %d, want 1", got)
	}
	// A parent this engine never recorded is external too — an eviction, a restart, a peer.
	eng.recordCause(200, "later", 199)
	if got := eng.depthOf(200); got != 1 {
		t.Errorf("a write off an unknown parent landed at depth %d, want 1", got)
	}
	// And a known parent is inherited from.
	eng.recordCause(300, "deeper", 100)
	if got := eng.depthOf(300); got != 2 {
		t.Errorf("a write off a depth-1 parent landed at depth %d, want 2", got)
	}
}

// A CHANGE IN VERSE'S OWN HALF IS FOUND IN THE COMMIT THAT CARRIED IT, whichever stream delivered
// it. A subtree watch — what a scoped engine observes — delivers commits with no delta, and
// changedIn must still see a subject under `verse` in one; otherwise every firing about such a
// subject has no parent, begins a fresh chain, and the throttle has no loop to see.
func TestChangedInFindsAVerseHalfSubjectWithoutADelta(t *testing.T) {
	s := entitytest.Store(t)
	ref := entity.VerseRef("task", "instance", "run-1")
	segs := ref.Segs()
	ch, unsub, err := s.WatchSubtree(len(segs), segs[:len(segs)-1]...)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unsub()
	if _, _, err := s.Write(entity.Write{At: ref.At(), Value: entity.Obj{"status": "running"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-ch:
		if !changedIn(c, ref) {
			t.Fatalf("changedIn(%s) = false over Changes %+v", ref, c.Changes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the watch never delivered the write")
	}
}

// Only a write that COMMITTED is recorded. A refused precondition wrote nothing, and Modify
// retries — so a patch under contention issues several writes of which at most one lands, and
// counting the attempts would inflate every chain by however busy the store happened to be.
func TestRefusedWriteRecordsNothing(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	w := eng.caused("claimer", 0)

	claim := entity.RefAt("run", "claim", "review@demo.pr.1")
	won, rev, err := w.Write(entity.Write{
		At: claim.At(), Value: entity.Obj{"by": "first"}.Payload(), When: []entity.Require{entity.Created()},
	})
	if err != nil || !won {
		t.Fatalf("first claim: won=%v err=%v", won, err)
	}
	if got := eng.depthOf(rev); got != 1 {
		t.Fatalf("the committed write landed at depth %d, want 1", got)
	}
	lost, lostRev, err := w.Write(entity.Write{
		At: claim.At(), Value: entity.Obj{"by": "second"}.Payload(), When: []entity.Require{entity.Created()},
	})
	if err != nil || lost {
		t.Fatalf("second claim: won=%v err=%v, want refused", lost, err)
	}
	if lostRev != 0 {
		t.Fatalf("a refused write reported rev %d", lostRev)
	}
	if _, ok := eng.causeOf(0); ok {
		t.Error("a refused write was recorded, under revision 0")
	}
}

// awaitCause is findCause, waited for: the store shows a write the instant it commits, and the
// chain is recorded when the write RETURNS, so a test that has just seen the value may be a
// goroutine ahead of the bookkeeping.
func awaitCause(e *Engine, chain string) (link, bool) {
	deadline := time.After(3 * time.Second)
	for {
		if l, ok := findCause(e, chain); ok {
			return l, true
		}
		select {
		case <-deadline:
			return link{}, false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// findCause returns the recorded link written by the named chain, if any.
func findCause(e *Engine, chain string) (link, bool) {
	e.chainMu.Lock()
	defer e.chainMu.Unlock()
	for _, l := range e.causes {
		if l.chain == chain {
			return l, true
		}
	}
	return link{}, false
}

// waitForStage blocks until the entity reaches a stage, so a test asserts on a settled verse
// rather than on whichever hop happened to have landed.
func waitForStage(t *testing.T, s entity.Store, ref entity.Ref, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		got, _, _ := s.Get(ref)
		if entity.Str(got.Payload, "stage") == want {
			return
		}
		select {
		case <-deadline:
			got, _, _ := s.Get(ref)
			t.Fatalf("%s never reached stage %q; it is %v", ref, want, entity.ToObj(got.Payload))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// THE RUNAWAY STOPS. This is the charter convergence names as the one hazard nothing checks
// (docs/convergence.md#the-cycle-which-is-the-one-that-is-not-checked): two rules each writing
// exactly what the other selects on. Measured there with the backstop at 40, it fired 40 times
// and wrote 40-odd commits; without a budget it did not stop at all.
//
// Two triggers installed, so a chain may commit at most cap+1 = 3 hops. Nothing else bounds
// this: there is no fire budget any more (see countFire), so if the throttle does not stop it,
// the test hangs rather than passing quietly on somebody else's backstop.
func TestRunawayCycleStopsAtTheTriggerCount(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetTripPace(0) // refuse rather than pace: this test is about the chain ENDING
	for _, tr := range []Trigger{
		{
			Name:      "fwd",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "a"}}},
			Action:    Action{Patch: entity.Obj{"stage": "b"}.Payload()},
		},
		{
			Name:      "back",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "b"}}},
			Action:    Action{Patch: entity.Obj{"stage": "a"}.Payload()},
		},
	} {
		if err := eng.AddTrigger(tr); err != nil {
			t.Fatalf("AddTrigger %s: %v", tr.Name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("demo", "pr", "1")
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: entity.Obj{"stage": "a"}.Payload()}); err != nil {
		t.Fatal(err)
	}

	// Wait for the trip, then check it stays stopped rather than merely being slow.
	deadline := time.After(20 * time.Second)
	for eng.tripCount() == 0 {
		select {
		case <-deadline:
			t.Fatalf("the cycle never tripped; it fired %d times and is still going", eng.Fired())
		case <-time.After(10 * time.Millisecond):
		}
	}
	fired := eng.Fired()
	time.Sleep(500 * time.Millisecond)
	if after := eng.Fired(); after != fired {
		t.Errorf("the cycle kept firing after the trip: %d → %d", fired, after)
	}
	// Two triggers, so a chain may commit at most 3 hops — external(0) → fwd(1) → back(2) →
	// fwd(3) — and the firing off depth 3 is the first that must be a repeat that COMMITTED.
	if deepest := deepestCause(eng); deepest > 3 {
		t.Errorf("chain reached depth %d with 2 triggers installed; the bound is cap+1 = 3", deepest)
	}
}

// A PACED CYCLE MUST NOT TRIP. The same fwd/back pair, with an ask gate on each — every lap now
// passes through a decision, which is real time entering the loop, so each firing starts a fresh
// chain and the depth never climbs. This is the shape the design exists to PERMIT: a cycle
// through a gate is a control loop running at the speed of whoever answers it.
//
// It would otherwise loop forever. What stops it is the DECISION — a resolver that answers
// `proceed` a fixed number of times and then `deny` — which is the honest way to bound this
// shape now that there is no fire budget: a paced loop runs for exactly as long as someone keeps
// saying yes. The assertion is that it reaches that bound with no trip, the opposite of the test
// above.
func TestPacedCycleNeverTrips(t *testing.T) {
	store := entitytest.Store(t)
	const laps = 8 // comfortably past the cap of 2, where an unpaced cycle would have tripped
	eng := New(store, &budgetedResolver{allow: laps})
	for _, tr := range []Trigger{
		{
			Name:      "fwd",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "a"}}},
			Gates:     []Gate{{Name: "go", Mode: "ask"}},
			Action:    Action{Patch: entity.Obj{"stage": "b"}.Payload()},
		},
		{
			Name:      "back",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "b"}}},
			Gates:     []Gate{{Name: "go", Mode: "ask"}},
			Action:    Action{Patch: entity.Obj{"stage": "a"}.Payload()},
		},
	} {
		if err := eng.AddTrigger(tr); err != nil {
			t.Fatalf("AddTrigger %s: %v", tr.Name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("demo", "pr", "1")
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: entity.Obj{"stage": "a"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(20 * time.Second)
	for eng.Fired() < laps {
		select {
		case <-deadline:
			t.Fatalf("only %d of %d laps ran (trips: %d)", eng.Fired(), laps, eng.tripCount())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if n := eng.tripCount(); n != 0 {
		t.Errorf("a gated cycle tripped %d times; pacing through a decision must end the chain", n)
	}
	if deepest := deepestCause(eng); deepest != 1 {
		t.Errorf("deepest chain is %d; every gated firing should begin one at 1", deepest)
	}
}

// WIDTH IS NOT DEPTH. A mirror landing many entities is many external writes, each starting its
// own chain — so a rule that fans out over all of them fires once per entity at depth 1 and
// nothing trips, however far past the trigger count the COUNT of firings goes. This is the
// regression the design is most likely to break, since the throttle's whole input is a counter.
func TestBurstOfExternalWritesDoesNotTrip(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "greet",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "new"}}},
		Action:    Action{Patch: entity.Obj{"stage": "seen"}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	const n = 30 // 30× the cap of 1: a depth-counting bug that counted firings would trip here
	for i := 0; i < n; i++ {
		ref := entity.RefAt("demo", "pr", strconv.Itoa(i))
		if _, _, err := store.Write(entity.Write{At: ref.At(), Value: entity.Obj{"stage": "new"}.Payload()}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.After(30 * time.Second)
	for {
		done := 0
		for i := 0; i < n; i++ {
			got, _, _ := store.Get(entity.RefAt("demo", "pr", strconv.Itoa(i)))
			if entity.Str(got.Payload, "stage") == "seen" {
				done++
			}
		}
		if done == n {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d of %d entities were acted on (trips: %d)", done, n, eng.tripCount())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if got := eng.tripCount(); got != 0 {
		t.Errorf("a burst of %d external writes tripped %d times; width is not depth", n, got)
	}
	if deepest := deepestCause(eng); deepest != 1 {
		t.Errorf("deepest chain is %d; each external write should start its own at 1", deepest)
	}
}

// deepestCause returns the greatest depth this engine has recorded.
func deepestCause(e *Engine) int {
	e.chainMu.Lock()
	defer e.chainMu.Unlock()
	worst := 0
	for _, l := range e.causes {
		if l.depth > worst {
			worst = l.depth
		}
	}
	return worst
}

// budgetedResolver answers `proceed` a fixed number of times and then `deny` — a stand-in for a
// person who approves a few laps of a control loop and then stops. It is what bounds a PACED
// cycle in a test: the loop runs at the speed of the decision, so the decision is what ends it.
// (Distinct from countingResolver in fanout_test.go, which approves everything and records the
// questions.)
type budgetedResolver struct {
	mu    sync.Mutex
	allow int
}

func (r *budgetedResolver) Resolve(context.Context, task.GateQuestion, task.ToolUse) (task.Resolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.allow <= 0 {
		return task.Resolution{Proceed: false, By: "test:exhausted"}, nil
	}
	r.allow--
	return task.Resolution{Proceed: true, By: "test"}, nil
}

// THE REPORT IS THE DELIVERABLE. Stopping a runaway without saying which rules were involved
// leaves an operator with a charter that quietly does less than it says. This pins the chain the
// walk reconstructs — in causal order, naming every hop including the firing that was refused —
// and that it says so when it is whole.
func TestTripReportsTheCycleByName(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	var logged []slog.Record
	var logMu sync.Mutex
	eng.SetLogger(slog.New(recordingHandler{fn: func(r slog.Record) {
		logMu.Lock()
		logged = append(logged, r)
		logMu.Unlock()
	}}))
	for _, tr := range []Trigger{
		{
			Name:      "fwd",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "a"}}},
			Action:    Action{Patch: entity.Obj{"stage": "b"}.Payload()},
		},
		{
			Name:      "back",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "b"}}},
			Action:    Action{Patch: entity.Obj{"stage": "a"}.Payload()},
		},
	} {
		if err := eng.AddTrigger(tr); err != nil {
			t.Fatalf("AddTrigger %s: %v", tr.Name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("demo", "pr", "1")
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: entity.Obj{"stage": "a"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(20 * time.Second)
	for eng.tripCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("never tripped")
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond) // let any further trips try to report

	logMu.Lock()
	defer logMu.Unlock()
	var cycles []string
	var complete []bool
	for _, r := range logged {
		// INFO, and the level is pinned deliberately: a tripped cycle is a charter doing
		// something the model allows and the throttle then paces, not something an operator
		// has to act on. Warn is for a write that did not land.
		if r.Level != slog.LevelInfo {
			continue
		}
		var c string
		var done bool
		var isTrip bool
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "cycle":
				c, isTrip = a.Value.String(), true
			case "complete":
				done = a.Value.Bool()
			}
			return true
		})
		if isTrip {
			cycles = append(cycles, c)
			complete = append(complete, done)
		}
	}
	if len(cycles) != 1 {
		t.Fatalf("reported %d cycles, want exactly 1 — a chain is explained once, not per lap: %v", len(cycles), cycles)
	}
	// Every hop, oldest first, ending with the firing that was refused. Both rules appear
	// twice, which IS the finding: one of them ran a second time on the same subject.
	if want := "fwd → back → fwd → back"; cycles[0] != want {
		t.Errorf("cycle = %q, want %q", cycles[0], want)
	}
	if !complete[0] {
		t.Error("the walk reported itself truncated; this chain runs back to an external write")
	}
}

// A truncated walk says so rather than presenting a partial chain as whole — the first name in
// the list is the one a reader goes and looks at, so naming the wrong rule as the origin is the
// expensive way to be wrong. Here the chain's ancestors are evicted out from under it.
func TestTruncatedChainReportsItself(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	// A chain of three, then drop the two oldest — an eviction, or a restart.
	eng.recordCause(10, "a", 0)
	eng.recordCause(11, "b", 10)
	eng.recordCause(12, "c", 11)
	if names, root, ok := eng.walkChain(12); !ok || len(names) != 3 || root != 10 {
		t.Fatalf("intact walk = %v root=%d complete=%v, want [a b c] root=10 complete=true", names, root, ok)
	}
	eng.chainMu.Lock()
	delete(eng.causes, 10)
	eng.chainMu.Unlock()
	names, root, ok := eng.walkChain(12)
	if ok {
		t.Error("a walk that lost its ancestors reported itself complete")
	}
	if len(names) != 2 || names[0] != "b" {
		t.Errorf("walk = %v, want the two hops that survive, oldest first", names)
	}
	if root != 11 {
		t.Errorf("root = %d, want 11 — the oldest hop still recorded", root)
	}
}

// recordingHandler is a slog handler that hands every record to a func, so a test can assert on
// what an operator would actually be told.
type recordingHandler struct{ fn func(slog.Record) }

func (h recordingHandler) Enabled(context.Context, slog.Level) bool      { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error { h.fn(r); return nil }
func (h recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler      { return h }
func (h recordingHandler) WithGroup(string) slog.Handler                 { return h }

// PACING, NOT REFUSING: with D set, a tripped chain does not stop — it settles into a steady
// rate. The runaway here would fire as fast as the store accepts writes; paced at D it advances
// about once per D, which is the "polite steady state" the throttle is for.
//
// Asserted as a RATE rather than a count, because a count would be a race: what matters is that
// the loop keeps going (unlike D=0, which stops it dead) and that it goes at roughly 1/D.
func TestTrippedChainIsPacedNotStopped(t *testing.T) {
	const D = 150 * time.Millisecond
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetTripPace(D)
	for _, tr := range []Trigger{
		{
			Name:      "fwd",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "a"}}},
			Action:    Action{Patch: entity.Obj{"stage": "b"}.Payload()},
		},
		{
			Name:      "back",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "b"}}},
			Action:    Action{Patch: entity.Obj{"stage": "a"}.Payload()},
		},
	} {
		if err := eng.AddTrigger(tr); err != nil {
			t.Fatalf("AddTrigger %s: %v", tr.Name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("demo", "pr", "1")
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: entity.Obj{"stage": "a"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	// Wait for it to be pacing rather than still in its free hops.
	deadline := time.After(20 * time.Second)
	for eng.tripCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("never tripped")
		case <-time.After(5 * time.Millisecond):
		}
	}
	from := eng.Fired()
	start := time.Now()
	time.Sleep(10 * D)
	ran := eng.Fired() - from
	elapsed := time.Since(start)

	if ran == 0 {
		t.Fatalf("a paced chain stopped entirely; D=%v should hold each firing, not refuse it", D)
	}
	// Roughly one firing per D. Generous bounds: this is a rate, measured on a real store, and
	// the point is the ORDER — not free-running, not stopped.
	rate := float64(ran) / elapsed.Seconds()
	want := 1 / D.Seconds()
	if rate > want*3 {
		t.Errorf("paced rate %.1f/s is far above 1/D = %.1f/s — the pace is not being applied", rate, want)
	}
	if rate < want/4 {
		t.Errorf("paced rate %.1f/s is far below 1/D = %.1f/s — the chain is barely advancing", rate, want)
	}
	t.Logf("paced at %.1f firings/s against 1/D = %.1f/s (%d firings in %v)", rate, want, ran, elapsed.Round(time.Millisecond))
}

// A PACED FIRING ABORTS IF ITS CHAIN BREAKS UNDER IT. While a tripped firing waits out D, a write
// to its subject means the world moved: that write re-evaluated the rule and raised whatever
// firing is now correct, so the held one is stale and must not land on top of it.
func TestPacedFiringAbortsWhenTheSubjectMoves(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	subj := entity.RefAt("demo", "pr", "1")

	// It waits the full D when nothing disturbs it.
	start := time.Now()
	if !eng.pauseFor(context.Background(), subj, 80*time.Millisecond) {
		t.Fatal("an undisturbed wait aborted; it should proceed after D")
	}
	if el := time.Since(start); el < 60*time.Millisecond {
		t.Errorf("waited only %v; D was 80ms", el)
	}

	// And gives up the moment its subject is written.
	done := make(chan bool, 1)
	go func() { done <- eng.pauseFor(context.Background(), subj, 10*time.Second) }()
	time.Sleep(50 * time.Millisecond) // let it register
	moved := time.Now()
	eng.subjectMoved(subj)
	select {
	case proceed := <-done:
		if proceed {
			t.Error("the firing proceeded although its subject moved under it")
		}
		if el := time.Since(moved); el > time.Second {
			t.Errorf("took %v to notice the subject moved; it should be immediate", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a paced firing did not notice its subject moving")
	}

	// A write to something else leaves it waiting.
	go func() { done <- eng.pauseFor(context.Background(), subj, 120*time.Millisecond) }()
	time.Sleep(30 * time.Millisecond)
	eng.subjectMoved(entity.RefAt("demo", "pr", "other"))
	if proceed := <-done; !proceed {
		t.Error("a write to a DIFFERENT entity aborted the wait; the chain is per subject")
	}

	// Nothing is left behind: the map holds only what is waiting.
	eng.pacingMu.Lock()
	n := len(eng.pacing)
	eng.pacingMu.Unlock()
	if n != 0 {
		t.Errorf("%d subjects left registered after every wait ended", n)
	}
}
