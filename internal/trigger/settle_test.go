package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/task"
)

// settleWorld is an engine observing a store with one rule installed and a subject at a stage the
// rule does not fire about, which is what a start finds: claims and records, and no task.
func settleWorld(t *testing.T, rule Trigger, stage string) (*Engine, entity.Store, *spawned) {
	t.Helper()
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	sp := &spawned{by: map[string]*heldHandle{}}
	eng.SetSpawner(sp.spawn)
	if err := eng.AddTrigger(rule); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.ObserveScoped(ctx)
	<-eng.Opened() // nothing was left: what a test writes from here it settles itself
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": stage}.Payload())
	return eng, store, sp
}

// claimAt writes the claim a rule's action at chain holds about demo.pr.1 as a verse before this
// one left it, taken at the commit occurrence, and answers it with the id of the task it guards.
func claimAt(t *testing.T, eng *Engine, store entity.Store, chain string, spawn bool, occurrence int64) (entity.Entity, string) {
	t.Helper()
	id := spawnID(eng.idOwner(), chainID(chain), prRef("1"))
	payload := entity.Obj{"flow": prRef("1").String(), "chain": chain, takenField: "2026-01-01T00:00:00Z"}
	if spawn {
		payload[mirror.ClaimOfField] = mirror.ClaimOfSpawn
	}
	if occurrence != 0 {
		payload["occurrence"] = occurrence
	}
	entitytest.Set(t, store, mirror.ClaimRef(id), payload.Payload())
	claim, ok, err := store.Get(mirror.ClaimRef(id))
	if err != nil || !ok {
		t.Fatalf("the claim at %s does not stand: %v", chain, err)
	}
	return claim, id
}

func subjectNow(t *testing.T, store entity.Store) map[string]any {
	t.Helper()
	ent, _, err := store.Get(prRef("1"))
	if err != nil {
		t.Fatal(err)
	}
	return entity.ToObj(ent.Payload)
}

func claimStands(t *testing.T, store entity.Store, id string) bool {
	t.Helper()
	_, ok, err := store.Get(mirror.ClaimRef(id))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// A CLAIM FOUND WITH NO TASK IS PAID FROM THE TASK'S RECORD. A hooked spawn's claim stands and
// nothing is running its task: a record that says `done` runs onDone; a record that says no end,
// or no record at all, is an orphaned failure and runs onFailed. The claim comes off either way.
func TestSettlingASpawnClaimRunsWhatItsTasksEndOwed(t *testing.T) {
	rule := Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Spawn:    &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review"},
			OnDone:   &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
			OnFailed: &Action{Patch: entity.Obj{"stage": "failed", "lost": ".[orphaned]"}.Payload()},
		},
	}
	for name, c := range map[string]struct {
		record entity.Obj
		stage  string
		ends   bool // the record is given an end
	}{
		"a record that says done":   {entity.Obj{"status": string(task.Done)}, "reviewed", false},
		"a record that says no end": {entity.Obj{"status": string(task.Running)}, "failed", true},
		"no record":                 {nil, "failed", false},
	} {
		t.Run(name, func(t *testing.T) {
			eng, store, _ := settleWorld(t, rule, "reviewing")
			claim, id := claimAt(t, eng, store, "review", true, 0)
			if c.record != nil {
				entitytest.Set(t, store, mirror.Ref(id), c.record.Payload())
			}
			if !eng.settleClaim(claim) {
				t.Fatal("Settle found nothing to pay")
			}
			got := subjectNow(t, store)
			if got["stage"] != c.stage {
				t.Fatalf("subject = %v, want stage %s", got, c.stage)
			}
			if c.stage == "failed" && got["lost"] != true {
				t.Fatalf("subject = %v, want the failure read as orphaned", got)
			}
			if claimStands(t, store, id) {
				t.Fatal("the claim still stands")
			}
			rec, ok, _ := store.Get(mirror.Ref(id))
			if c.record == nil && ok {
				t.Fatalf("a record was written for a task that had none: %v", entity.ToObj(rec.Payload))
			}
			if c.ends && (entity.Str(rec.Payload, "status") != string(task.Failed) || entity.ToObj(rec.Payload)[mirror.OrphanedField] != true) {
				t.Fatalf("record = %v, want it ended failed and orphaned", entity.ToObj(rec.Payload))
			}
		})
	}
}

// deploy is a sequence of a task, a write, and a task, with an off-ramp on the first and a hook
// for after the whole of it.
func deploy() Trigger {
	return Trigger{
		Name:      "deploy",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Seq: []Action{
				{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "build"},
					OnFailed: &Action{Patch: entity.Obj{"stage": "broke", "lost": ".[orphaned]"}.Payload()}},
				{Patch: entity.Obj{"built": "yes"}.Payload()},
				{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "ship"}},
			},
			OnDone: &Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
		},
	}
}

// run is a deploy run found at a start: its claim standing, `step` elements completed.
func run(t *testing.T, step int) (*Engine, entity.Store, *spawned, entity.Entity, string) {
	t.Helper()
	eng, store, sp := settleWorld(t, deploy(), "deploying")
	claim, id := claimAt(t, eng, store, "deploy", false, 0)
	eng.publishSeq("deploy", 0, prRef("1"), entity.Obj{seqStep: step, seqSteps: 3, seqShape: eng.shapeOf("deploy")})
	return eng, store, sp, claim, id
}

func charterFacts(t *testing.T, store entity.Store) map[string]any {
	t.Helper()
	ent, _, err := store.Get(prRef("1"))
	if err != nil {
		t.Fatal(err)
	}
	return entity.ToObj(entity.Get(entity.Get(entity.Get(ent.Payload, VerseRoot), CharterKey), "deploy"))
}

func elementID(eng *Engine, i int) string {
	return spawnID(eng.idOwner(), chainID(seqChain("deploy", i)), prRef("1"))
}

// A RUN CLAIM FOUND WITH NO TASK IS SETTLED BY ITS ELEMENT IN FLIGHT, the one at `step`.
func TestSettlingARunClaim(t *testing.T) {
	t.Run("its task's record says done: the run advances and the next element runs", func(t *testing.T) {
		eng, store, sp, claim, id := run(t, 0)
		entitytest.Set(t, store, mirror.Ref(elementID(eng, 0)), entity.Obj{"status": string(task.Done)}.Payload())
		eng.settleClaim(claim)
		if got := subjectNow(t, store); got["built"] != "yes" {
			t.Fatalf("subject = %v, want the second element's write", got)
		}
		if sp.get(elementID(eng, 2)) == nil {
			t.Fatal("the third element's task was not started")
		}
		if !claimStands(t, store, id) {
			t.Fatal("the run's claim came off while its third element runs")
		}
	})
	t.Run("its task's record says another end: the run stops and the off-ramp runs", func(t *testing.T) {
		eng, store, sp, claim, id := run(t, 0)
		entitytest.Set(t, store, mirror.Ref(elementID(eng, 0)), entity.Obj{"status": string(task.Failed), "err": "it could not"}.Payload())
		eng.settleClaim(claim)
		if got := subjectNow(t, store); got["stage"] != "broke" || got["lost"] != false {
			t.Fatalf("subject = %v, want the off-ramp's write, not orphaned", got)
		}
		if f := charterFacts(t, store); f[seqEnded] != string(task.Failed) {
			t.Fatalf("run facts = %v, want it ended failed", f)
		}
		if claimStands(t, store, id) || sp.get(elementID(eng, 2)) != nil {
			t.Fatal("the run's claim stands, or the run went on")
		}
	})
	t.Run("it spawns and no record says an end: the run stops as an orphaned failure", func(t *testing.T) {
		eng, store, _, claim, id := run(t, 0)
		eng.settleClaim(claim)
		if got := subjectNow(t, store); got["stage"] != "broke" || got["lost"] != true {
			t.Fatalf("subject = %v, want the off-ramp's write, orphaned", got)
		}
		if f := charterFacts(t, store); f[seqEnded] != string(task.Failed) {
			t.Fatalf("run facts = %v, want it ended failed", f)
		}
		if claimStands(t, store, id) {
			t.Fatal("the run's claim still stands")
		}
	})
	t.Run("it spawns nothing: the run stops in error, saying why, and the element does not run", func(t *testing.T) {
		eng, store, sp, claim, id := run(t, 1)
		eng.settleClaim(claim)
		if got := subjectNow(t, store); got["built"] != nil {
			t.Fatalf("subject = %v: the element ran again", got)
		}
		if f := charterFacts(t, store); f[seqEnded] != seqEndedError || f[seqReason] != errRestarted.Error() {
			t.Fatalf("run facts = %v, want it ended in error with the restart for its reason", f)
		}
		if claimStands(t, store, id) || sp.get(elementID(eng, 2)) != nil {
			t.Fatal("the run's claim stands, or the run went on")
		}
	})
	t.Run("every element completed and no done: the sequence's onDone is owed and runs", func(t *testing.T) {
		eng, store, _, claim, id := run(t, 3)
		entitytest.Set(t, store, mirror.Ref(elementID(eng, 2)), entity.Obj{"status": string(task.Done)}.Payload())
		eng.settleClaim(claim)
		if got := subjectNow(t, store); got["stage"] != "shipped" {
			t.Fatalf("subject = %v, want the sequence's onDone", got)
		}
		if f := charterFacts(t, store); f[seqDone] != true {
			t.Fatalf("run facts = %v, want the run recorded done", f)
		}
		if claimStands(t, store, id) {
			t.Fatal("the run's claim still stands")
		}
	})
	t.Run("the run is recorded done: its claim is released and its onDone does not run", func(t *testing.T) {
		// What a run leaves whose onDone ran, and what a firing leaves that claimed a run
		// already concluded and went no further.
		eng, store, _, claim, id := run(t, 3)
		eng.publishSeq("deploy", 0, prRef("1"), entity.Obj{seqDone: true})
		entitytest.Set(t, store, mirror.Ref(elementID(eng, 2)), entity.Obj{"status": string(task.Done)}.Payload())
		eng.settleClaim(claim)
		if got := subjectNow(t, store); got["stage"] != "deploying" {
			t.Fatalf("subject = %v: the onDone of a run recorded done ran", got)
		}
		if claimStands(t, store, id) {
			t.Fatal("the run's claim still stands")
		}
	})
}

// AN ELEMENT'S END RUN TWICE ADVANCES THE RUN ONCE. The second end finds `step` past its
// element and runs nothing: the element after it does not run again.
func TestAnElementsEndRunTwiceAdvancesOnce(t *testing.T) {
	eng, store, sp, _, _ := run(t, 0)
	entitytest.Set(t, store, mirror.Ref(elementID(eng, 0)), entity.Obj{"status": string(task.Done)}.Payload())
	eng.settleTask(elementID(eng, 0), prRef("1"), seqChain("deploy", 0), 0)
	if sp.get(elementID(eng, 2)) == nil {
		t.Fatal("the first end did not carry the run to its third element")
	}
	// What the second element wrote is written over, so that its running again would show.
	now := subjectNow(t, store)
	now["built"] = "once"
	entitytest.Set(t, store, prRef("1"), entity.Obj(now).Payload())
	eng.settleTask(elementID(eng, 0), prRef("1"), seqChain("deploy", 0), 0)
	if got := subjectNow(t, store); got["built"] != "once" {
		t.Fatalf("subject = %v: the second end ran the second element again", got)
	}
	if f := charterFacts(t, store); f[seqStep] != int64(2) {
		t.Fatalf("run facts = %v, want step 2", f)
	}
}

// A CLAIM THIS VERSE IS PAYING IS NOT A DEBT: a claim one of its firings took, at the address of
// one that was listed; the claim of a task the engine tracks; and the run claim of a sequence a
// task it tracks is an element of. Settle leaves each standing.
func TestSettleLeavesAClaimBeingPaid(t *testing.T) {
	live := func() task.Handle { return &heldHandle{ch: make(chan task.Event, 1)} }
	t.Run("taken since by a firing, at the address of the one listed", func(t *testing.T) {
		eng, store, _ := settleWorld(t, deploy(), "deploying")
		listed, id := claimAt(t, eng, store, "deploy", false, 0)
		eng.releaseRun("deploy", prRef("1"))
		if !eng.claimRun("deploy", prRef("1"), nil) {
			t.Fatal("the claim was not taken")
		}
		if eng.settleClaim(listed) || !claimStands(t, store, id) {
			t.Fatal("Settle paid a claim a firing took after the listing")
		}
	})
	t.Run("of a task this engine tracks", func(t *testing.T) {
		eng, store, _ := settleWorld(t, deploy(), "deploying")
		claim, id := claimAt(t, eng, store, seqChain("deploy", 0), true, 0)
		entitytest.Set(t, store, mirror.Ref(id), entity.Obj{"status": string(task.Running)}.Payload())
		eng.AdoptChain(prRef("1").String(), id, seqChain("deploy", 0), live())
		if eng.settleClaim(claim) || !claimStands(t, store, id) {
			t.Fatal("Settle paid the claim of a tracked task")
		}
		if got := subjectNow(t, store); got["stage"] != "deploying" {
			t.Fatalf("subject = %v: a hook ran for a task that has not ended", got)
		}
	})
	t.Run("the run of a sequence whose element this engine tracks", func(t *testing.T) {
		eng, store, _, claim, id := run(t, 0)
		entitytest.Set(t, store, mirror.Ref(elementID(eng, 0)), entity.Obj{"status": string(task.Running)}.Payload())
		eng.AdoptChain(prRef("1").String(), elementID(eng, 0), seqChain("deploy", 0), live())
		if eng.settleClaim(claim) || !claimStands(t, store, id) {
			t.Fatal("Settle paid the run claim of a sequence whose element is tracked")
		}
		if f := charterFacts(t, store); f[seqEnded] != nil {
			t.Fatalf("run facts = %v: a running sequence was ended", f)
		}
	})
	t.Run("but a tracked task of the hook does not hold the spawn's claim", func(t *testing.T) {
		eng, store, _ := settleWorld(t, deploy(), "deploying")
		claim, id := claimAt(t, eng, store, seqChain("deploy", 0), true, 0)
		hook := seqChain("deploy", 0) + ".onFailed"
		eng.AdoptChain(prRef("1").String(), spawnID(eng.idOwner(), chainID(hook), prRef("1")), hook, live())
		if !eng.settleClaim(claim) || claimStands(t, store, id) {
			t.Fatal("a task of the hook kept the claim of the spawn the hook is of")
		}
	})
}

// THE RECORD AT A CLAIM'S ID MAY BE THE RUN BEFORE'S. Verse ended after a firing took its claim
// and before its task started: the record standing at the id says how the previous run ended.
// It is not this claim's task's end, so the claim is settled as a task with no account, and the
// record is left as it stands.
func TestSettleDoesNotReadTheRunBeforesRecord(t *testing.T) {
	rule := Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Spawn:    &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review"},
			OnDone:   &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
			OnFailed: &Action{Patch: entity.Obj{"stage": "failed"}.Payload()},
		},
	}
	eng, store, _ := settleWorld(t, rule, "reviewing")
	claim, id := claimAt(t, eng, store, "review", true, 20)
	entitytest.Set(t, store, mirror.Ref(id), entity.Obj{"status": string(task.Done), "occurrence": int64(10)}.Payload())
	if !eng.settleClaim(claim) {
		t.Fatal("Settle found nothing to pay")
	}
	if got := subjectNow(t, store); got["stage"] != "failed" {
		t.Fatalf("subject = %v, want onFailed: the done record is the run before's", got)
	}
	rec, _, _ := store.Get(mirror.Ref(id))
	if entity.Str(rec.Payload, "status") != string(task.Done) {
		t.Fatalf("record = %v: the run before's record was written over", entity.ToObj(rec.Payload))
	}
}

// AN ELEMENT'S OWN CLAIM, SETTLED FIRST, LEAVES ITS RUN TO BE SETTLED. Verse ended after an
// element's end advanced the run and before that element's claim came off; the element the run
// advanced to has since failed with nobody to hear it. The first element's claim is paid and
// does nothing to the run, and the run's claim then stops it.
func TestAnElementsClaimSettledFirstDoesNotKeepItsRunsClaim(t *testing.T) {
	eng, store, _, runClaim, runID := run(t, 2)
	elClaim, elID := claimAt(t, eng, store, seqChain("deploy", 0), true, 0)
	entitytest.Set(t, store, mirror.Ref(elID), entity.Obj{"status": string(task.Done)}.Payload())
	entitytest.Set(t, store, mirror.Ref(elementID(eng, 2)), entity.Obj{"status": string(task.Failed)}.Payload())
	if !eng.settleClaim(elClaim) || claimStands(t, store, elID) {
		t.Fatal("the element's claim was not paid")
	}
	if !eng.settleClaim(runClaim) || claimStands(t, store, runID) {
		t.Fatal("the run's claim was not paid after its element's")
	}
	if f := charterFacts(t, store); f[seqEnded] != string(task.Failed) {
		t.Fatalf("run facts = %v, want the run ended failed", f)
	}
}

// A RUN RECORDED ENDED OWES NOTHING BUT ITS CLAIM. Its stop is on the subject; settling it
// releases the claim and runs no hook again.
func TestSettlingARunThatEndedReleasesItsClaim(t *testing.T) {
	eng, store, _, claim, id := run(t, 0)
	eng.publishSeq("deploy", 0, prRef("1"), entity.Obj{seqEnded: string(task.Cancelled)})
	if !eng.settleClaim(claim) || claimStands(t, store, id) {
		t.Fatal("the claim of an ended run was not released")
	}
	if got := subjectNow(t, store); got["stage"] != "deploying" {
		t.Fatalf("subject = %v: a hook ran for a run that had ended", got)
	}
	if f := charterFacts(t, store); f[seqEnded] != string(task.Cancelled) {
		t.Fatalf("run facts = %v: how the run ended was written over", f)
	}
}

// AN ENGINE TAKES NO TASK'S END AND ARMS NO RULE UNTIL IT IS OPEN. A task the last verse left is
// handed over with its record already saying `done`. While the host has more to hand over, the
// task stands live, its hook has not run, and the rule has fired about nothing. Once the tasks
// are in, the engine opens: the end is taken, and the rule arms.
func TestAnEngineIsShutUntilItsTasksAreIn(t *testing.T) {
	rule := Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review"},
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
	}
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	sp := &spawned{by: map[string]*heldHandle{}}
	eng.SetSpawner(sp.spawn)
	if err := eng.AddTrigger(rule); err != nil {
		t.Fatal(err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "reviewing"}.Payload())
	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "open"}.Payload())
	_, id := claimAt(t, eng, store, "review", true, 0)
	entitytest.Set(t, store, mirror.Ref(id), entity.Obj{"status": string(task.Done)}.Payload())

	eng.AwaitTasks()
	eng.AdoptChain(prRef("1").String(), id, "review", &heldHandle{ch: make(chan task.Event, 1)})
	eng.ObserveScoped(t.Context())

	time.Sleep(300 * time.Millisecond)
	if _, live := eng.Live(id); !live {
		t.Fatal("the task's end was taken while the engine was shut")
	}
	if got := subjectNow(t, store); got["stage"] != "reviewing" || !claimStands(t, store, id) {
		t.Fatalf("subject = %v: the hook ran, or the claim came off, while the engine was shut", got)
	}
	other := spawnID(eng.idOwner(), "review", prRef("2"))
	if sp.get(other) != nil {
		t.Fatal("the rule fired while the engine was shut")
	}

	eng.TasksIn()
	waitField(t, store, prRef("1"), "stage", "reviewed")
	sp.wait(t, other)
	if claimStands(t, store, id) {
		t.Fatal("the claim stands after its task's hook ran")
	}
}

// `done` IS WRITTEN AFTER THE SEQUENCE'S onDone HAS RUN. While the hook runs, the run's facts say
// every element completed and do not say `done`: that is the run whose onDone is owed.
func TestASequenceIsDoneOnceItsOnDoneHasRun(t *testing.T) {
	rule := Trigger{
		Name:      "tidy",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Seq:    []Action{{Patch: entity.Obj{"stage": "tidying"}.Payload()}},
			OnDone: &Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "announce"}},
		},
	}
	eng, store, _ := settleWorld(t, rule, "closed")
	seen := make(chan map[string]any, 1)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		ent, _, _ := store.Get(prRef("1"))
		seen <- entity.ToObj(entity.Get(entity.Get(entity.Get(ent.Payload, VerseRoot), CharterKey), "tidy"))
		return &heldHandle{ch: make(chan task.Event, 1)}
	})
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	select {
	case f := <-seen:
		if f[seqStep] != int64(1) || f[seqDone] != nil {
			t.Fatalf("run facts as onDone ran = %v, want step 1 and no done", f)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the sequence's onDone did not run")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		ent, _, _ := store.Get(prRef("1"))
		f := entity.ToObj(entity.Get(entity.Get(entity.Get(ent.Payload, VerseRoot), CharterKey), "tidy"))
		if f[seqDone] == true {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run facts = %v, want done once the onDone has run", f)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A RUN WAITING AT A PARKED ELEMENT IS IN FLIGHT. A settle advanced the run to an element a
// suspension holds, so no task of the run is live and none has a record: the run's claim is left
// for the task the lift starts.
func TestSettleLeavesARunWaitingAtAParkedElement(t *testing.T) {
	eng, store, _, claim, id := run(t, 2)
	eng.park(parkedLaunch{a: deploy().Action.Seq[2], chain: seqChain("deploy", 2), subj: prRef("1")})
	if eng.settleClaim(claim) || !claimStands(t, store, id) {
		t.Fatal("Settle paid the claim of a run waiting at a parked element")
	}
	if f := charterFacts(t, store); f[seqEnded] != nil {
		t.Fatalf("run facts = %v: the run was ended", f)
	}
}

// A SPAWN CLAIM WAITING ON A PARKED LAUNCH IS NOT A DEBT. No task is live at its id and none has
// a record, and a suspension holds its launch: the claim stands, and no hook runs.
func TestSettleLeavesAClaimWaitingOnAParkedLaunch(t *testing.T) {
	rule := Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Spawn:    &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review"},
			OnFailed: &Action{Patch: entity.Obj{"stage": "failed"}.Payload()},
		},
	}
	eng, store, _ := settleWorld(t, rule, "reviewing")
	claim, id := claimAt(t, eng, store, "review", true, 0)
	eng.park(parkedLaunch{a: rule.Action, chain: "review", subj: prRef("1")})
	if eng.settleClaim(claim) || !claimStands(t, store, id) {
		t.Fatal("Settle paid the claim of a launch a suspension holds")
	}
	if got := subjectNow(t, store); got["stage"] != "reviewing" {
		t.Fatalf("subject = %v: a hook ran for a task that has not started", got)
	}
}

// AN ELEMENT'S END ADVANCES A RUN WHOSE `step` IS BEHIND IT. The write of `step` that follows the
// element before did not land, so the facts say the run is at an earlier element than the one
// whose task ended: its end carries the run on all the same.
func TestAnElementsEndAdvancesARunWhoseStepIsBehind(t *testing.T) {
	eng, store, _, _, id := run(t, 0)
	entitytest.Set(t, store, mirror.Ref(elementID(eng, 2)), entity.Obj{"status": string(task.Done)}.Payload())
	eng.settleTask(elementID(eng, 2), prRef("1"), seqChain("deploy", 2), 0)
	if got := subjectNow(t, store); got["stage"] != "shipped" {
		t.Fatalf("subject = %v, want the sequence's onDone to have run", got)
	}
	if f := charterFacts(t, store); f[seqDone] != true || claimStands(t, store, id) {
		t.Fatalf("run facts = %v, claim stands = %v: the run did not finish", f, claimStands(t, store, id))
	}
}
