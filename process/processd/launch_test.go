package processd

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/process"
)

// execDriver proposes one shell action and then completes, so what the loop does with that
// action is the only thing under test.
type execDriver struct{ done bool }

func (d *execDriver) Next(context.Context, process.StepResult) (process.ToolUse, bool, error) {
	if d.done {
		return process.ToolUse{}, false, nil
	}
	d.done = true
	return process.ToolUse{Name: "run_bash", Input: map[string]any{"command": "id"}}, true, nil
}

// ranExec records whether the action reached an executor at all.
type ranExec struct{ ran bool }

func (e *ranExec) Execute(_ context.Context, tu process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	e.ran = true
	return process.StepResult{Use: tu, Output: "ok"}, nil, nil
}

// The daemon installs a spawn's authority axes on every process it hosts, whatever driver
// built it. This is deliberately NOT the Builder's job: a Builder that forgot would hand
// back an unbounded process and nothing would say so, which is the shape of the original
// defect. Here the builder is maximally permissive — an auto vector, no scope — and the
// envelope still has to bite.
func TestSpawnInstallsTheGrant(t *testing.T) {
	ex := &ranExec{}
	d := New(func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		sup.SetDeriver(process.BasicDeriver{})
		return process.NewProcess(l.ID, sup, &execDriver{}, ex)
	})

	g, _, err := process.NamedGrant("read-only")
	if err != nil {
		t.Fatal(err)
	}
	p := d.Spawn(process.Launch{ID: "p1", Task: "run id", Grant: &g}, nil)
	waitTerminal(t, d, p.ID)

	if ex.ran {
		t.Error("the exec action ran; a read-only grant installed at spawn must refuse it")
	}
}

// The counterpart: a spawn that declares no grant is unaffected. This is the regression
// that would break every existing charter — the zero Grant permits nothing, so treating
// "unstated" as "empty" anywhere along this path would refuse every action of every
// process in the system.
func TestSpawnWithoutAGrantStaysUnbounded(t *testing.T) {
	ex := &ranExec{}
	d := New(func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		sup.SetDeriver(process.BasicDeriver{})
		return process.NewProcess(l.ID, sup, &execDriver{}, ex)
	})

	p := d.Spawn(process.Launch{ID: "p1", Task: "run id"}, nil)
	waitTerminal(t, d, p.ID)

	if !ex.ran {
		t.Error("a spawn that declared no grant must run exactly as before")
	}
}

// denyResolver refuses every question it is asked. It is what makes the vector test below
// non-vacuous: AutoResolver promotes a blocking gate to proceed, so a process whose vector
// was ignored would still reach a terminal status and still execute, and the assertion
// would hold for the wrong reason.
type denyResolver struct{}

func (denyResolver) Resolve(_ context.Context, q process.GateQuestion, _ process.ToolUse) (process.Resolution, error) {
	return process.Resolution{Proceed: false, By: "test", Question: q}, nil
}

// A named vector reaches the process too — the soft axis had the same non-consumer as the
// hard one. The builder's own vector blocks every action into a resolver that refuses, so
// the action executes only if the spawn's `vector: auto` actually replaced it.
func TestSpawnInstallsTheVector(t *testing.T) {
	ex := &ranExec{}
	build := func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Ask}, denyResolver{})
		sup.SetDeriver(process.BasicDeriver{})
		return process.NewProcess(l.ID, sup, &execDriver{}, ex)
	}

	// First, without a vector on the spawn: the builder's ask-and-deny stands.
	d := New(build)
	p := d.Spawn(process.Launch{ID: "p1", Task: "run id"}, nil)
	waitTerminal(t, d, p.ID)
	if ex.ran {
		t.Fatal("the builder's vector should have blocked this action; the test proves nothing if it did not")
	}

	// Then with one: `vector: auto` replaces it, and the same action proceeds.
	vec, err := process.NamedVector("auto")
	if err != nil {
		t.Fatal(err)
	}
	ex.ran = false
	d2 := New(build)
	p2 := d2.Spawn(process.Launch{ID: "p2", Task: "run id", Vector: vec}, nil)
	waitTerminal(t, d2, p2.ID)
	if !ex.ran {
		t.Error("the spawn's auto vector did not replace the builder's blocking one")
	}
}

func waitTerminal(t *testing.T, d *Daemon, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap, ok := d.SnapshotOf(id); ok && process.TerminalStatus(string(snap.Status)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process %s did not reach a terminal status", id)
}
