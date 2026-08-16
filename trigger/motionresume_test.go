package trigger

import (
	"context"
	"strings"
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
	ref := entity.RefAt(motionSystem, motionChannelKind, id)
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
