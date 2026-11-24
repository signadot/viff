package trigger

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/systems"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/task"
)

// HoldTask holds a run at its next boundary, and lifting it releases the run. It is per-task
// (not the engine loop) and honored at the boundary. Here the run is held before it starts, so it
// must hold at its first boundary and only complete once released.
func TestHoldTaskHoldsThenReleases(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	var tk *task.Task
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		tk = task.NewTask(id, sup, immediate{}, noExec{})
		mirror.Reflect(store, id, tk, mirror.Meta{Subject: spec.Subject, Runner: "local"})
		return tk // the test starts Run after arranging the pause
	})

	subj := `demo.pr."1"`
	eng.Spawn("work-1", launch.Spec{Agent: &runner.Agent{Name: "viff"}, Subject: subj})
	if tk == nil {
		t.Fatal("spawner was not invoked")
	}

	if _, ok := eng.HoldTask("work-1", true); !ok {
		t.Fatal("HoldTask did not find the live run")
	}
	go tk.Run(context.Background())

	// Held at the first boundary: an immediate task completes in microseconds
	// unpaused, so still-running well past that proves the hold.
	ref := mirror.Ref("work-1")
	time.Sleep(150 * time.Millisecond)
	if e, _, _ := store.Get(ref); entity.ToObj(e.Payload)["status"] == "done" {
		t.Fatal("the run completed while held — HoldTask did not hold")
	}

	if _, ok := eng.HoldTask("work-1", false); !ok {
		t.Fatal("HoldTask did not find the live run to release")
	}
	deadline := time.After(2 * time.Second)
	for {
		if e, ok, _ := store.Get(ref); ok && entity.ToObj(e.Payload)["status"] == "done" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("flow did not complete after resume")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A flow is reaped when its task reaches a terminal status: the engine observes
// the reflected task:instance completion and drops the handle, so Flows() shows
// only in-flight work (no stale subjects).
func TestFlowReapedOnCompletion(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		p := task.NewTask(id, sup, immediate{}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{Subject: spec.Subject, Runner: "local"})
		go p.Run(context.Background())
		return p
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx) // the engine must observe the store to reap

	subj := `demo.pr."1"`
	eng.Spawn("work-1", launch.Spec{Agent: &runner.Agent{Name: "viff"}, Subject: subj})

	deadline := time.After(2 * time.Second)
	for slices.Contains(eng.Flows(), subj) {
		select {
		case <-deadline:
			t.Fatalf("flow %q not reaped after completion; flows=%v", subj, eng.Flows())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// reap must not race a concurrent control op: reap runs on the loop goroutine while
// SelectFlow snapshots the live registry and HoldTask changes an entry. Under -race this trips if
// either touches the map unguarded. (Behavioral guard; -race is the assertion.)
func TestReapVsFlowControlNoRace(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	const subj = `demo.pr."1"`
	sel := mustSelect(t, "*", subj)
	mkTask := func(id string) *task.Task {
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		return task.NewTask(id, sup, immediate{}, noExec{})
	}
	terminal := entity.Event{
		Ref: mirror.Ref("b"), Op: entity.Delete,
		To: entity.Obj{"status": string(task.Done), "flow": subj}.Payload(),
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // repopulate then reap a member
		defer wg.Done()
		for range 3000 {
			eng.liveMu.Lock()
			for _, id := range []string{"a", "b", "c"} {
				eng.live[id] = flowTask{id: id, subject: subj, h: mkTask(id)}
			}
			eng.liveMu.Unlock()
			eng.reap(terminal)
		}
	}()
	go func() { // address the flow concurrently
		defer wg.Done()
		for range 3000 {
			for _, lt := range eng.SelectFlow(sel) {
				eng.HoldTask(lt.ID, true)
			}
		}
	}()
	wg.Wait()
}

// mustSelect is a selection of a charter and a subject for a test.
func mustSelect(t *testing.T, charter, subject string) Suspension {
	t.Helper()
	address, rule := ParseCharterFlag(charter)
	s, err := NewSuspension(address, rule, subject, false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// mustExpr parses a flow address for a test.
func mustExpr(t *testing.T, s string) Match {
	t.Helper()
	m, err := ParseExpr(s)
	if err != nil {
		t.Fatalf("ParseExpr(%q): %v", s, err)
	}
	return m
}

// do() must not deadlock when the loop has already exited (ctx cancelled): the
// closure can never be drained, so the caller falls back to the stopped signal.
func TestDoNoDeadlockAfterCancel(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	eng.Start(ctx)
	cancel()
	time.Sleep(50 * time.Millisecond) // let the loop observe cancellation and exit

	done := make(chan struct{})
	go func() { eng.Pause(); close(done) }() // Pause → do(); must return, not hang
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("do() deadlocked after the engine's ctx was cancelled")
	}
}
