package trigger

import (
	"context"
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
	ref := entity.Ref{System: motionSystem, Kind: motionChannelKind, ID: id}
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
	subj := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
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
	entitytest.Set(t, store, entity.Ref{System: "vote", Kind: mid, ID: "scott"},
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
	subj := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
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
