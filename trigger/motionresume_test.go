package trigger

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// channel writes an open motion channel by hand — a motion the previous verse left behind.
func channel(t *testing.T, s entity.Store, id string, payload entity.Obj) entity.Ref {
	t.Helper()
	ref := entity.VerseRef(motionSystem, motionChannelKind, id)
	entitytest.Set(t, s, ref, payload.Payload())
	return ref
}

func statusOf(t *testing.T, s entity.Store, ref entity.Ref) entity.Obj {
	t.Helper()
	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		t.Fatalf("reading %s: ok=%v err=%v", ref, ok, err)
	}
	return entity.ToObj(e.Payload)
}

// A who-motion is resumed through the chain it recorded: the engine navigates back to the
// action the ballot is holding up and re-enters the selection, so a pick cast after the
// restart still decides who runs.
func TestResumeReEntersASelection(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	spawned := make(chan launch.Spec, 2)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		spawned <- spec
		return nil
	})
	spawn := &launch.Spec{
		Prompt:   "review $subject",
		Grant:    "read-only",
		Provider: &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "ask"},
	}
	subj := entity.RefAt("demo", "pr", "1")
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    Action{Patch: entity.Obj{"stage": "reviewing"}.Payload(), Spawn: spawn},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	// The subject as the interrupted firing left it: past its latch, so nothing re-fires the
	// rule and the motion below is the only thing standing between the ballot and the work.
	entitytest.Set(t, store, subj, entity.Obj{"stage": "reviewing"}.Payload())
	mid := motionChannelID(providerSpecSeed(subj, spawn.Provider))
	channel(t, store, mid, entity.Obj{
		"axis": "select", "status": "open", "subject": subj.String(), "chain": "review",
		"options": []any{
			map[string]any{"id": "gpt", "need": 1, "choice": "gpt"},
			map[string]any{"id": "claude", "need": 1, "choice": "claude"},
		},
	})

	if n := eng.ResumeMotions(); n != 1 {
		t.Fatalf("resumed %d motions, want 1", n)
	}
	// The pick, cast as an operator reading the docket would cast it.
	entitytest.Set(t, store, entity.RefAt("vote", mid, "scott"),
		entity.Obj{"choice": "claude", "by": "scott"}.Payload())

	select {
	case spec := <-spawned:
		if spec.Doer != "claude" || spec.Driver != "claude" {
			t.Fatalf("spawned %+v, want the doer the ballot named", spec)
		}
		if spec.Chain != "review" || spec.Subject != subj.String() {
			t.Fatalf("the resumed spawn lost its provenance: %+v", spec)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the ballot decided nothing: a resumed motion did not spawn")
	}
}

// A resumed selection honors the clock it was opened under: `within` is stamped on the channel
// as an absolute instant, so a window that expired while the verse was down closes the motion
// on the way back up rather than restarting it. A deadline that reset itself on every restart
// would be no deadline at all on a verse that restarts.
func TestResumeHonorsADeadlineThatPassedWhileDown(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		t.Errorf("a no-pick spawned %s anyway: %+v", id, spec)
		return nil
	})
	spawn := &launch.Spec{
		Prompt: "review $subject", Grant: "read-only",
		Provider: &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "vote", Need: 2, Within: "1h"},
	}
	subj := entity.RefAt("demo", "pr", "1")
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    Action{Patch: entity.Obj{"stage": "reviewing"}.Payload(), Spawn: spawn},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, subj, entity.Obj{"stage": "reviewing"}.Payload())
	mid := motionChannelID(providerSpecSeed(subj, spawn.Provider))
	ref := channel(t, store, mid, entity.Obj{
		"axis": "select", "status": "open", "subject": subj.String(), "chain": "review",
		// Opened an hour ago by a verse that is no longer running.
		"deadline_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		"options": []any{
			map[string]any{"id": "gpt", "need": 2, "choice": "gpt"},
			map[string]any{"id": "claude", "need": 2, "choice": "claude"},
		},
	})

	if n := eng.ResumeMotions(); n != 1 {
		t.Fatalf("resumed %d motions, want 1", n)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if statusOf(t, store, ref)["outcome"] == "no-pick" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the expired selection did not close: %+v", statusOf(t, store, ref))
}

// A motion whose rule left the charter is SETTLED rather than offered. Nothing will act on
// the answer, so a docket that kept listing it would be asking the room to decide something
// no charter reads — the failure a settled question already avoids on the other docket.
func TestResumeOrphansAMotionWhoseRuleIsGone(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	whether := channel(t, store, "w1", entity.Obj{
		"axis": "whether", "status": "open", "subject": "demo:pr:1", "trigger": "removed",
	})
	who := channel(t, store, "s1", entity.Obj{
		"axis": "select", "status": "open", "subject": "demo:pr:1", "chain": "removed",
	})

	if n := eng.ResumeMotions(); n != 0 {
		t.Fatalf("resumed %d motions, want 0 — neither has a rule to resume into", n)
	}
	for _, ref := range []entity.Ref{whether, who} {
		got := statusOf(t, store, ref)
		if got["status"] != motionOrphaned {
			t.Fatalf("%s status = %v, want orphaned: %+v", ref, got["status"], got)
		}
		if got["why"] == "" || got["orphanedAt"] == "" {
			t.Fatalf("%s was settled without saying why or when: %+v", ref, got)
		}
		// Settled, not decided: a motion nobody answered must not read as though somebody had.
		if got["outcome"] != nil {
			t.Fatalf("%s was given an outcome nobody voted for: %+v", ref, got)
		}
	}
}

// A whether-motion whose SUBJECT has moved out of what its rule selects is settled, even
// though the rule is installed and healthy.
//
// This is the staging failure, reduced. A merge proposal's gate stood open on `!quorum 1`; a
// sweep wrote `state: stale` onto the proposal, which is exactly what the rule's
// `!not.has-path state` excludes; verse was redeployed. The rule was re-evaluated, did not
// select the proposal, and never looked at the motion again — so the docket went on offering
// it, somebody voted approve, and the tally never moved. The rule being present is not the
// question; whether it still picks this subject is.
func TestResumeSettlesAMotionItsRuleNoLongerSelects(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	// The staging rule, in its own grammar rather than hand-built: `!not.has-path state` is a
	// tagged predicate about the payload, and spelling it as a Go map is how a test comes to
	// pass because its predicate matches nothing at all.
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: merge-proposal
  condition:
    subject: {system: merge, kind: proposal, value: !not.has-path state}
  gates:
  - {name: merge-to-main, mode: ask, formula: !quorum 1}
  action:
    patch: {state: merged}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	// The guard on the guard: with no `state` written the rule DOES select it, so the settle
	// below is about the sweep's write and not about a predicate that never matched.
	if !eng.stillSelects(eng.triggers[0], entity.RefAt("merge", "proposal", "p0")) {
		entitytest.Set(t, store, entity.RefAt("merge", "proposal", "p0"),
			entity.Obj{"branch": "auto/y"}.Payload())
		if !eng.stillSelects(eng.triggers[0], entity.RefAt("merge", "proposal", "p0")) {
			t.Fatal("the rule selects no undecided proposal either; the predicate is the thing under test")
		}
	}
	// The proposal as the sweep left it: swept stale while its gate stood open.
	subj := entity.RefAt("merge", "proposal", "p1")
	entitytest.Set(t, store, subj, entity.Obj{"state": "stale", "branch": "auto/x"}.Payload())
	ref := channel(t, store, "e7596c16", entity.Obj{
		"axis": "whether", "status": "open", "subject": subj.String(),
		"trigger": "merge-proposal", "gate": "merge-to-main",
		"options": []any{map[string]any{"id": "proceed", "choice": "approve", "need": 1}},
	})
	// And a ballot already cast into it, which is what makes the stale listing expensive: it
	// reads as a decision somebody made.
	entitytest.Set(t, store, entity.RefAt("vote", "e7596c16", "me"),
		entity.Obj{"choice": "approve", "by": "me"}.Payload())

	eng.ResumeMotions()

	got := statusOf(t, store, ref)
	if got["status"] != motionOrphaned {
		t.Fatalf("status = %v, want orphaned — the rule cannot select this subject any more: %+v",
			got["status"], got)
	}
	if why, _ := got["why"].(string); !strings.Contains(why, "merge:proposal:p1") {
		t.Errorf("why = %q, want it to name the subject the rule stopped selecting", why)
	}
	// Settled, not decided: the vote stands in the log, and no outcome was invented from it.
	if got["outcome"] != nil {
		t.Fatalf("a motion nobody carried was given an outcome: %+v", got)
	}
}

// A whether-motion whose rule is still installed is left alone: its awaiter is the firing,
// and the firing comes back with the rule's own replay. Settling it here would orphan a
// question somebody is about to be asked again.
func TestResumeLeavesALiveWhetherMotionOpen(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr"}},
		Gates: []Gate{{Name: "approvals", Mode: "ask", Formula: &Formula{Op: OpAtom, Req: &Requirement{
			Kind: ReqQuorum, Facts: Match{"system": "motion", "kind": "vote"}, N: 2,
		}}}},
		Action: Action{Patch: entity.Obj{"stage": "merged"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	// The subject still standing where the rule can see it: the action never ran, which is what
	// the gate is holding, so nothing has moved it out of the selection.
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"),
		entity.Obj{"stage": "open"}.Payload())
	ref := channel(t, store, "w2", entity.Obj{
		"axis": "whether", "status": "open", "subject": "demo:pr:1", "trigger": "merge",
	})

	eng.ResumeMotions()

	if got := statusOf(t, store, ref); got["status"] != "open" {
		t.Fatalf("status = %v, want still open — the rule that asks it is installed: %+v",
			got["status"], got)
	}
}

// A motion that already committed is not resumed and not settled: terminal-once means the
// decision stands, and re-entering its continuation would be a second run of decided work.
func TestResumeLeavesACommittedMotionAlone(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	ref := channel(t, store, "c1", entity.Obj{
		"axis": "select", "status": "closed", "outcome": "claude", "subject": "demo:pr:1",
		"chain": "gone", // the rule is gone too, and it still must not be touched
	})

	if n := eng.ResumeMotions(); n != 0 {
		t.Fatalf("resumed %d motions, want 0", n)
	}
	got := statusOf(t, store, ref)
	if got["status"] != "closed" || got["outcome"] != "claude" {
		t.Fatalf("a committed motion was disturbed: %+v", got)
	}
}

// A PACKAGE'S MOTION IS NOT BASELINE'S TO SETTLE, and this is the test that was missing.
//
// Every resume test above runs one engine holding its own rule, which is the case that always
// worked. A verse runs several — baseline, and one per installed package — and all of them
// read the same motion slice out of the same store. Baseline swept a package's motions, looked
// up the rule in its own charter, did not find it, and settled a live gate as `the rule that
// opened it is no longer in the charter`. On staging that took a merge gate off the docket
// while `train-run` was installed in verse-dev.merge and running.
func TestBaselineDoesNotSettleAPackagesMotion(t *testing.T) {
	store := entitytest.Store(t)
	pkg := New(store, denyResolver{})
	pkg.Confine(SubtreeAt("verse-dev", "merge"))
	if err := pkg.AddTrigger(Trigger{
		Name:      "train-run",
		Condition: Condition{Subject: Match{"system": "verse-dev", "kind": "merge"}},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	base := New(store, denyResolver{})

	// Stamped, as a motion opened by that engine now is.
	mine := channel(t, store, "p1", entity.Obj{
		"axis": "whether", "status": "open", "subject": "verse-dev:merge:1",
		"trigger": "train-run", "at": "verse-dev.merge",
	})
	// And unstamped, as every motion written before the charter was recorded is — the
	// staging one. Baseline can only tell by asking the engines it runs.
	older := channel(t, store, "p2", entity.Obj{
		"axis": "whether", "status": "open", "subject": "verse-dev:merge:2",
		"trigger": "train-run",
	})
	base.KnowsElsewhere(pkg.Knows)

	base.ResumeMotions()
	for _, ref := range []entity.Ref{mine, older} {
		if got := statusOf(t, store, ref); got["status"] != "open" {
			t.Fatalf("baseline settled %s, a rule it does not hold: %+v", ref, got)
		}
	}
}

// …and with nobody else running, the old reading stands: a rule that is gone is gone, and the
// motion is settled rather than left in front of a room that cannot act on it.
func TestBaselineAloneStillSettlesAGoneRule(t *testing.T) {
	store := entitytest.Store(t)
	base := New(store, denyResolver{})
	ref := channel(t, store, "g1", entity.Obj{
		"axis": "whether", "status": "open", "subject": "demo:pr:1", "trigger": "removed",
	})
	base.ResumeMotions()
	if got := statusOf(t, store, ref); got["status"] != motionOrphaned {
		t.Fatalf("status = %v, want orphaned: %+v", got["status"], got)
	}
}

// A motion says which charter opened it, so a docket entry names a rule rather than half of
// one: `trigger` is unique inside a charter and not across them.
func TestAMotionRecordsItsCharter(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	eng.Confine(SubtreeAt("verse-dev", "merge"))
	m := &motion{
		channel: entity.VerseRef(motionSystem, motionChannelKind, "c1"),
		axis:    "whether", subject: "verse-dev:merge:1", trigger: "train-run",
		options: []motionOption{{id: "proceed", choice: "approve", need: 1}, {id: "deny"}},
	}
	eng.openChannel(m)
	if got := statusOf(t, store, m.channel); got["at"] != "verse-dev.merge" {
		t.Fatalf("at = %v, want the charter that opened it: %+v", got["at"], got)
	}
}

// captureResolver records the questions a firing asks, so a test can read what was published
// about them rather than only whether they were answered.
type captureResolver struct {
	mu   sync.Mutex
	seen []task.GateQuestion
}

func (c *captureResolver) Resolve(_ context.Context, q task.GateQuestion, _ task.ToolUse) (task.Resolution, error) {
	c.mu.Lock()
	c.seen = append(c.seen, q)
	c.mu.Unlock()
	return task.Resolution{Proceed: false, By: "deny"}, nil
}

// A FIRING'S GATE NAMES ITS CHARTER, for the reason a motion does: `gate` and `origin` are
// names, and a name is unique inside a charter rather than across them, so a docket entry
// saying `merge-to-main` on `train-run` left a reader to work out which package that was.
func TestAFiringGateNamesItsCharter(t *testing.T) {
	store := entitytest.Store(t)
	res := &captureResolver{}
	eng := New(store, res)
	eng.Confine(SubtreeAt("verse-dev", "merge"))
	if err := eng.AddTrigger(Trigger{
		Name:      "train-run",
		Condition: Condition{Subject: Match{"system": "verse-dev", "kind": "merge"}},
		Gates:     []Gate{{Name: "merge-to-main", Mode: "ask"}},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("verse-dev", "merge", "1"), entity.Obj{"stage": "cut"}.Payload())

	deadline := time.After(3 * time.Second)
	for {
		res.mu.Lock()
		n := len(res.seen)
		var q task.GateQuestion
		if n > 0 {
			q = res.seen[0]
		}
		res.mu.Unlock()
		if n > 0 {
			if q.At != "verse-dev.merge" {
				t.Fatalf("gate question At = %q, want the charter that asked: %+v", q.At, q)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("the gate never asked")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
