package runner_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
	"github.com/signadot/verse/process/processd"
	"github.com/signadot/verse/runner"
)

// a driver that takes `steps` no-op actions then completes.
type doneDriver struct{ steps int }

func (d *doneDriver) Next(context.Context, process.StepResult) (process.ToolUse, bool, error) {
	if d.steps <= 0 {
		return process.ToolUse{}, false, nil // complete
	}
	d.steps--
	return process.ToolUse{Name: "noop"}, true, nil
}

type okExec struct{}

func (okExec) Execute(_ context.Context, tu process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	return process.StepResult{Use: tu}, nil, nil
}

// The engine drives a REMOTE processd entirely over HTTP: httpRunner.Spawn issues the
// spawn, remoteHandle.Subscribe streams the SSE read plane (cursored), and the process
// runs to a terminal status — no in-process handle. This is the Phase-3 proof of the
// base-URL decoupling.
func TestHTTPRunnerDrivesRemoteProcessd(t *testing.T) {
	build := func(id, task, _ string, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(id, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	h := pol.Spawn("t1", "do a thing", "scripted", nil)

	ch, unsub := h.Subscribe()
	defer unsub()

	var sawCursor, sawExec, completed bool
	timeout := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break loop
			}
			if ev.Cursor > 0 {
				sawCursor = true
			}
			if ev.Kind == process.EvExec {
				sawExec = true
			}
			if ev.Kind == process.EvStatus && ev.Text == "done" {
				completed = true
				break loop
			}
		case <-timeout:
			t.Fatal("timed out waiting for the remote process to complete")
		}
	}
	if !completed {
		t.Fatal("never saw a completed status over the wire")
	}
	if !sawCursor {
		t.Fatal("events carried no cursor")
	}
	if !sawExec {
		t.Fatal("never saw the action execute remotely")
	}
}

// A remote runner enumerates its processes so versed can union them into `process list`/
// `get`: ListProcesses/ProcessSnapshot are a thin client over the remote's GET /processes.
func TestHTTPRunnerListsRemoteProcesses(t *testing.T) {
	build := func(id, task, _ string, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(id, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	pol.Spawn("r1", "remote task", "scripted", nil)

	lister, ok := pol.(interface {
		ListProcesses() []process.Instance
		ProcessSnapshot(string) (process.Instance, bool)
	})
	if !ok {
		t.Fatal("httpRunner should implement the lister interface")
	}

	var found bool
	for _, m := range lister.ListProcesses() {
		if m.ID == "r1" {
			found = true
		}
	}
	if !found {
		t.Fatal("remote process r1 not in the runner's ListProcesses")
	}

	snap, ok := lister.ProcessSnapshot("r1")
	if !ok || snap.Task != "remote task" {
		t.Fatalf("ProcessSnapshot(r1) = %+v, %v; want the remote task", snap, ok)
	}
	if _, ok := lister.ProcessSnapshot("nope"); ok {
		t.Fatal("ProcessSnapshot of an unknown id should be false")
	}
}

// Phase 4 resume: a process:instance left stuck "running" (verse crashed mid-run),
// tagged with a remote runner, is re-attached on restart — the replay from the remote
// (which kept running and finished) converges the entity to its terminal status. This is
// what versed.reconcileProcesses does: resume, not orphan, when the work outlived verse.
func TestResumeReAttachesToRemoteProcess(t *testing.T) {
	build := func(id, task, _ string, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(id, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	pol.Spawn("t1", "task", "scripted", nil) // runs on the remote to completion
	time.Sleep(300 * time.Millisecond)       // let it finish (retained in the ring)

	// "verse restart": a fresh store with a STALE running entity tagged with the runner.
	store := entitytest.Store(t)
	entitytest.Set(t, store, mirror.Ref("t1"), entity.Obj{"status": "running", "runner": srv.URL}.Payload())

	// resume: re-attach and re-mirror (the core of reconcileProcesses).
	mirror.Reflect(store, "t1", pol.Attach("t1"), mirror.Meta{Runner: srv.URL})

	deadline := time.After(5 * time.Second)
	for {
		e, _, _ := store.Get(mirror.Ref("t1"))
		if s, _ := entity.ToObj(e.Payload)["status"].(string); s == string(process.Done) {
			return // converged: resume recovered the terminal from the remote
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(mirror.Ref("t1"))
			t.Fatalf("entity never converged to completed; status=%v", entity.ToObj(e.Payload)["status"])
		case <-time.After(20 * time.Millisecond):
		}
	}
}
