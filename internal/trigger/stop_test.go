package trigger

import (
	"errors"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/task"
)

// reviewing is a rule that latches a pull request and starts a task about it, with a hook for
// each way the task can end.
func reviewing() Trigger {
	return Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:       entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn:       &launch.Spec{Agent: &runner.Agent{Name: "viff"}, Prompt: "review"},
			OnDone:      &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
			OnFailed:    &Action{Patch: entity.Obj{"stage": "broke"}.Payload()},
			OnCancelled: &Action{Patch: entity.Obj{"stage": "dropped"}.Payload()},
		},
	}
}

// A STOPPED ENGINE ANSWERS ITS TASKS, AND STARTS NOTHING. Its task is answered with the handle
// it is cancelled through, and is not cancelled or ended by the engine: its claim stands for the
// host. A subject its rule selects afterwards starts no task, and the task's end runs no hook.
func TestAStoppedEngineAnswersItsTasksAndStartsNothing(t *testing.T) {
	store := entitytest.Store(t)
	eng, _, sp, _ := deploying(t, store, reviewing(), func(sp *spawned) Spawner { return sp.spawn })
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	id := spawnID(eng.idOwner(), "review", prRef("1"))
	h := sp.wait(t, id)
	waitFacts(t, store, "the task to be tracked", func(map[string]any) bool { _, live := eng.Live(id); return live })

	got := eng.Stop("the charter was taken down")
	if len(got) != 1 || got[0].ID != id || got[0].Handle != task.Handle(h) {
		t.Fatalf("Stop answered %v, want the one task running", got)
	}
	if _, live := eng.Live(id); live {
		t.Fatal("a stopped engine still tracks its task")
	}
	if !claimStands(t, store, id) {
		t.Fatal("the engine released the claim of a task that has not ended")
	}

	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "open"}.Payload())
	h.finish()
	waitField(t, store, mirror.Ref(id), "status", string(task.Done))
	time.Sleep(300 * time.Millisecond)
	if sp.get(spawnID(eng.idOwner(), "review", prRef("2"))) != nil {
		t.Fatal("a stopped engine's rule fired")
	}
	if e, _, _ := store.Get(prRef("2")); entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("a stopped engine's rule wrote: %v", e.Payload)
	}
	if got := subjectNow(t, store); got["stage"] != "reviewing" {
		t.Fatalf("subject = %v: a hook ran in a stopped engine", got)
	}
}

// STOP WAITS FOR THE WORK IN PROGRESS. A firing is starting its task when the engine is stopped:
// the stop returns once the firing has, and the task it started is among those answered.
func TestStopWaitsForAFiringInProgress(t *testing.T) {
	store := entitytest.Store(t)
	starting, start := make(chan struct{}), make(chan struct{})
	eng, _, _, _ := deploying(t, store, reviewing(), func(sp *spawned) Spawner {
		return func(id string, spec launch.Spec) task.Handle {
			close(starting)
			<-start
			return sp.spawn(id, spec)
		}
	})
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	<-starting

	stopped := make(chan []Running, 1)
	go func() { stopped <- eng.Stop("the charter was taken down") }()
	select {
	case got := <-stopped:
		t.Fatalf("Stop returned %v while a firing was starting its task", got)
	case <-time.After(300 * time.Millisecond):
	}
	close(start)
	select {
	case got := <-stopped:
		if id := spawnID(eng.idOwner(), "review", prRef("1")); len(got) != 1 || got[0].ID != id {
			t.Fatalf("Stop answered %v, want the task the firing started", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return once the firing had")
	}
}

// A STOPPED ENGINE'S PARKED LAUNCHES ARE DROPPED, and their records with them: a lift starts
// nothing, and the next engine at the address finds no launch to park again.
func TestAStoppedEngineDropsItsParkedLaunches(t *testing.T) {
	eng, tbl, store, sp, build := parking(t, func(sp *spawned) Spawner { return sp.spawn })
	s := suspendPR1(t, tbl)
	build.finish()
	waitFacts(t, store, "its second element parked", func(map[string]any) bool { return eng.Parked() == 1 })

	if got := eng.Stop("the charter was taken down"); len(got) != 0 {
		t.Fatalf("Stop answered %v, want no task: the run waits at a parked element", got)
	}
	if n := eng.Parked(); n != 0 {
		t.Fatalf("%d launches parked in a stopped engine", n)
	}
	if recs, err := store.(entity.Scratch).ScratchRead(entity.ViffRef(mirror.System, parkedKind)); err != nil || len(entity.Fields(recs)) != 0 {
		t.Fatalf("a stopped engine's parked launch is still recorded: %v (err %v)", recs, err)
	}
	tbl.Lift(s.ID())
	time.Sleep(300 * time.Millisecond)
	for i := 1; i < 3; i++ {
		if sp.get(elementID(eng, i)) != nil {
			t.Fatalf("element %d started in a stopped engine", i)
		}
	}
}

// cancelled ends the task as a cancel does.
func (h *heldHandle) cancelled() {
	h.ch <- task.Event{Kind: task.EventStatus, Text: string(task.Cancelled)}
}

// A CLOSED ENGINE LAUNCHES NOTHING, AND TAKES THE ENDS OF ITS TASKS. Its rule fires about nothing
// and its charter takes no rule and loses none to a request. Its task, cancelled, ends as any task
// does: `onCancelled` runs, the claim comes off, and the engine is not busy.
func TestAClosedEngineLaunchesNothingAndTakesItsTasksEnds(t *testing.T) {
	store := entitytest.Store(t)
	eng, _, sp, _ := deploying(t, store, reviewing(), func(sp *spawned) Spawner { return sp.spawn })
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	id := spawnID(eng.idOwner(), "review", prRef("1"))
	h := sp.wait(t, id)
	waitFacts(t, store, "the task to be tracked", func(map[string]any) bool { _, live := eng.Live(id); return live })

	eng.Close()
	if got := eng.Running(); len(got) != 1 || got[0].ID != id || !eng.Busy() {
		t.Fatalf("a closed engine with a task running: Running = %v, Busy = %v", got, eng.Busy())
	}
	other := reviewing()
	other.Name = "again"
	if err := eng.AddTrigger(other); !errors.Is(err, ErrClosing) {
		t.Fatalf("adding a rule to a closed engine = %v, want ErrClosing", err)
	}
	if removed, err := eng.RemoveTriggerAs(t.Context(), "", "review"); removed || !errors.Is(err, ErrClosing) {
		t.Fatalf("removing the rule of a closed engine = %v, %v, want ErrClosing: its task's end runs its hook", removed, err)
	}
	if _, stands := eng.Rule("review"); !stands {
		t.Fatal("a closed engine lost the rule its task's end runs the hook of")
	}
	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "open"}.Payload())
	time.Sleep(300 * time.Millisecond)
	if sp.get(spawnID(eng.idOwner(), "review", prRef("2"))) != nil {
		t.Fatal("a closed engine's rule fired")
	}
	if e, _, _ := store.Get(prRef("2")); entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("a closed engine's rule wrote: %v", e.Payload)
	}

	h.cancelled()
	waitField(t, store, prRef("1"), "stage", "dropped")
	waitUntilGone(t, store, id)
	waitFacts(t, store, "the engine to be done", func(map[string]any) bool { return !eng.Busy() })

	eng.Reopen()
	sp.wait(t, spawnID(eng.idOwner(), "review", prRef("2"))) // the rule fires again
}

// A HOOK THAT WOULD LAUNCH IS REFUSED WHOLE IN A CLOSED ENGINE: its write with its spawn. The
// task's claim comes off all the same.
func TestAClosedEnginesHookThatWouldLaunchIsRefusedWhole(t *testing.T) {
	rule := reviewing()
	rule.Action.OnCancelled = &Action{
		Patch: entity.Obj{"stage": "again"}.Payload(),
		Spawn: &launch.Spec{Agent: &runner.Agent{Name: "viff"}, Prompt: "review again"},
	}
	store := entitytest.Store(t)
	eng, _, sp, _ := deploying(t, store, rule, func(sp *spawned) Spawner { return sp.spawn })
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	id := spawnID(eng.idOwner(), "review", prRef("1"))
	h := sp.wait(t, id)
	waitFacts(t, store, "the task to be tracked", func(map[string]any) bool { _, live := eng.Live(id); return live })

	eng.Close()
	h.cancelled()
	waitUntilGone(t, store, id)
	waitFacts(t, store, "the engine to be done", func(map[string]any) bool { return !eng.Busy() })
	if got := subjectNow(t, store); got["stage"] != "reviewing" {
		t.Fatalf("subject = %v: the write of a hook that would launch landed in a closed engine", got)
	}
	if sp.get(spawnID(eng.idOwner(), chainID("review.onCancelled"), prRef("1"))) != nil {
		t.Fatal("a hook launched a task in a closed engine")
	}
	// And nothing is said of it as a failure: the subject outlives the charter.
	subj, _, _ := store.Get(prRef("1"))
	if said := entity.Get(entity.Get(entity.Get(entity.Get(subj.Payload, ViffRoot), CharterKey), "review"), hookTrouble); said != nil {
		t.Fatalf("a hook refused by the closing was recorded on its subject as a failure: %v", said)
	}
	if rec, _, _ := store.Get(mirror.Ref(id)); entity.Get(rec.Payload, "hook") != nil {
		t.Fatalf("a hook refused by the closing was recorded on its task as a failure: %v", rec.Payload)
	}
}

// A SEQUENCE ENDS WHERE ITS ELEMENT ENDS IN A CLOSED ENGINE: cancelled, with nothing started and
// its claim off, though the element ended done.
func TestAClosedEngineEndsASequenceWhereItsElementEnds(t *testing.T) {
	eng, _, store, sp, build := parking(t, func(sp *spawned) Spawner { return sp.spawn })
	runID := spawnID(eng.idOwner(), chainID("deploy"), prRef("1"))
	eng.Close()
	build.finish()
	waitFacts(t, store, "the run to end cancelled", func(f map[string]any) bool { return f[seqEnded] == string(task.Cancelled) })
	waitUntilGone(t, store, runID)
	waitFacts(t, store, "the engine to be done", func(map[string]any) bool { return !eng.Busy() })
	if got := subjectNow(t, store); got["built"] != nil || sp.get(elementID(eng, 2)) != nil {
		t.Fatalf("the sequence went on in a closed engine: subject %v, third element started = %v", got, sp.get(elementID(eng, 2)) != nil)
	}
}

// CLOSING DROPS A PARKED LAUNCH AND ENDS ITS RUN, cancelled: the launch's record is removed, the
// run's claim comes off, and a lift starts nothing.
func TestClosingDropsAParkedLaunchAndEndsItsRun(t *testing.T) {
	eng, tbl, store, sp, build := parking(t, func(sp *spawned) Spawner { return sp.spawn })
	runID := spawnID(eng.idOwner(), chainID("deploy"), prRef("1"))
	s := suspendPR1(t, tbl)
	build.finish()
	waitFacts(t, store, "its third element parked", func(map[string]any) bool { return eng.Parked() == 1 })

	eng.Close()
	if n := eng.Parked(); n != 0 || eng.Busy() {
		t.Fatalf("a closed engine: %d launches parked, busy = %v", n, eng.Busy())
	}
	if f := charterFacts(t, store); f[seqEnded] != string(task.Cancelled) || claimStands(t, store, runID) {
		t.Fatalf("the run whose launch was dropped: facts %v, claim stands %v", f, claimStands(t, store, runID))
	}
	if recs, err := store.(entity.Scratch).ScratchRead(entity.ViffRef(mirror.System, parkedKind)); err != nil || len(entity.Fields(recs)) != 0 {
		t.Fatalf("the dropped launch is still recorded: %v (err %v)", recs, err)
	}
	tbl.Lift(s.ID())
	time.Sleep(300 * time.Millisecond)
	if sp.get(elementID(eng, 2)) != nil {
		t.Fatal("a lift started a launch in a closed engine")
	}
}

// CLOSING ENDS A FIRING THAT WAITS ON ITS GATE. Its question leaves the docket, and nothing of
// the rule's action runs.
func TestClosingEndsAFiringWaitingOnItsGate(t *testing.T) {
	rule := reviewing()
	rule.Gates = []Gate{{Name: "ok", Mode: "ask"}}
	store := entitytest.Store(t)
	eng, _, sp, _ := deploying(t, store, rule, func(sp *spawned) Spawner { return sp.spawn })
	reg := eng.resolver.(*task.Registry)
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitFacts(t, store, "the gate's question to be asked", func(map[string]any) bool { return len(reg.Pending()) == 1 })

	closed := make(chan struct{})
	go func() { eng.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return while a firing waited on its gate")
	}
	if n := len(reg.Pending()); n != 0 {
		t.Fatalf("%d questions of a closed engine stand on the docket", n)
	}
	if got := subjectNow(t, store); got["stage"] != "open" || sp.get(spawnID(eng.idOwner(), "review", prRef("1"))) != nil {
		t.Fatalf("the action of a firing that closing ended ran: subject %v", got)
	}
}

// AN ENGINE IS BUSY WHILE THE END OF A TASK IS BEING TAKEN. A task has ended and its hook is
// starting the task that follows: no task is tracked at that moment, and the engine is busy
// until the hook has returned.
func TestAnEngineIsBusyWhileAnEndIsBeingTaken(t *testing.T) {
	rule := reviewing()
	rule.Action.OnDone = &Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "viff"}, Prompt: "follow up"}}
	store := entitytest.Store(t)
	starting, start := make(chan struct{}), make(chan struct{})
	var eng *Engine
	eng, _, sp, _ := deploying(t, store, rule, func(sp *spawned) Spawner {
		return func(id string, spec launch.Spec) task.Handle {
			if spec.Prompt == "follow up" {
				close(starting)
				<-start
			}
			return sp.spawn(id, spec)
		}
	})
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	id := spawnID(eng.idOwner(), "review", prRef("1"))
	h := sp.wait(t, id)
	waitFacts(t, store, "the task to be tracked", func(map[string]any) bool { _, live := eng.Live(id); return live })
	h.finish()
	<-starting
	if got := eng.Running(); len(got) != 0 {
		t.Fatalf("Running = %v while the hook starts its task, want none tracked", got)
	}
	if !eng.Busy() {
		t.Fatal("the engine is not busy while a task's end is being taken")
	}
	close(start)
}
