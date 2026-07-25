package trigger

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// PauseFlow holds a subject's processes at their next boundary; ResumeFlow releases
// them. It is per-flow (not the engine loop) and honored at the boundary — never a
// store write. Here the flow is paused before its process runs, so it must hold at
// its first boundary and only complete once resumed.
func TestPauseFlowHoldsThenResumes(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})

	var proc *process.Process
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		proc = process.NewProcess(id, sup, immediate{}, noExec{})
		mirror.Reflect(store, id, proc, spec.Subject, "local")
		return proc // the test starts Run after arranging the pause
	})

	subj := "demo:pr:1"
	eng.Spawn("work-1", launch.Spec{Subject: subj})
	if proc == nil {
		t.Fatal("spawner was not invoked")
	}

	if n := eng.PauseFlow(mustExpr(t, subj)); n != 1 {
		t.Fatalf("PauseFlow signalled %d, want 1", n)
	}
	go proc.Run(context.Background())

	// Held at the first boundary: an immediate process completes in microseconds
	// unpaused, so still-running well past that proves the hold.
	ref := mirror.Ref("work-1")
	time.Sleep(150 * time.Millisecond)
	if e, _ := store.Get(ref); e.Payload["status"] == "completed" {
		t.Fatal("flow completed while paused — PauseFlow did not hold")
	}

	if n := eng.ResumeFlow(mustExpr(t, subj)); n != 1 {
		t.Fatalf("ResumeFlow signalled %d, want 1", n)
	}
	deadline := time.After(2 * time.Second)
	for {
		if e, ok := store.Get(ref); ok && e.Payload["status"] == "completed" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("flow did not complete after resume")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A flow is reaped when its process reaches a terminal status: the engine observes
// the reflected process:instance completion and drops the handle, so Flows() shows
// only in-flight work (no stale subjects).
func TestFlowReapedOnCompletion(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		p := process.NewProcess(id, sup, immediate{}, noExec{})
		mirror.Reflect(store, id, p, spec.Subject, "local")
		go p.Run(context.Background())
		return p
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx) // the engine must observe the store to reap

	subj := "demo:pr:1"
	eng.Spawn("work-1", launch.Spec{Subject: subj})

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
// matchFlow snapshots the live registry. Under -race this trips if either touches the map
// unguarded. (Behavioral guard; -race is the assertion.)
func TestReapVsCancelFlowNoRace(t *testing.T) {
	eng := New(entity.NewMemStore(), process.AutoResolver{})
	const subj = "demo:pr:1"
	addr := mustExpr(t, subj)
	mkproc := func(id string) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(id, sup, immediate{}, noExec{})
	}
	terminal := entity.Event{
		Ref: mirror.Ref("b"), Op: entity.Delete,
		To: entity.Payload{"status": string(process.Completed), "flow": subj},
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // repopulate then reap a member
		defer wg.Done()
		for range 3000 {
			eng.liveMu.Lock()
			for _, id := range []string{"a", "b", "c"} {
				eng.live[id] = flowProc{id: id, subject: subj, h: mkproc(id)}
			}
			eng.liveMu.Unlock()
			eng.reap(terminal)
		}
	}()
	go func() { // address the flow concurrently
		defer wg.Done()
		for range 3000 {
			eng.CancelFlow(addr)
		}
	}()
	wg.Wait()
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
	eng := New(entity.NewMemStore(), process.AutoResolver{})
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

// On restart the durable store may hold process:instance entities a crashed run left
// running; ReconcileOrphans retires them (they have no live handle) without touching
// already-terminal ones.
func TestReconcileOrphans(t *testing.T) {
	store := entity.NewMemStore()
	store.Put(mirror.Ref("old-running"), entity.Payload{"status": string(process.Running), "flow": "demo:pr:1"})
	store.Put(mirror.Ref("old-done"), entity.Payload{"status": string(process.Completed), "flow": "demo:pr:2"})

	eng := New(store, process.AutoResolver{})
	if n := eng.ReconcileOrphans(); n != 1 {
		t.Fatalf("reconciled %d, want 1", n)
	}
	if e, _ := store.Get(mirror.Ref("old-running")); e.Payload["status"] != string(process.Orphaned) {
		t.Fatalf("running process not orphaned: %+v", e.Payload)
	}
	if e, _ := store.Get(mirror.Ref("old-done")); e.Payload["status"] != string(process.Completed) {
		t.Fatalf("completed process wrongly touched: %+v", e.Payload)
	}
}
