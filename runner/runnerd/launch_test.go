package runnerd

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/task"
)

// execDriver proposes one shell action and then completes, so what the loop does with that
// action is the only thing under test.
type execDriver struct{ done bool }

func (d *execDriver) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	if d.done {
		return task.ToolUse{}, false, nil
	}
	d.done = true
	return task.ToolUse{Name: "run_bash", Input: map[string]any{"command": "id"}}, true, nil
}

// ranExec records whether the action reached an executor at all.
type ranExec struct{ ran bool }

func (e *ranExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	e.ran = true
	return task.StepResult{Use: tu, Output: "ok"}, nil, nil
}

// The daemon installs a spawn's authority axes on every task it hosts, whatever driver
// built it. This is deliberately NOT the Builder's job: a Builder that forgot would hand
// back an unbounded task and nothing would say so, which is the shape of the original
// defect. Here the builder is maximally permissive — an auto vector, no scope — and the
// envelope still has to bite.
func TestSpawnInstallsTheGrant(t *testing.T) {
	ex := &ranExec{}
	d := New(func(l task.Launch, reg *task.Registry) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		sup.SetDeriver(task.BasicDeriver{})
		return task.NewTask(l.ID, sup, &execDriver{}, ex)
	})

	g, _, err := task.NamedGrant("read-only")
	if err != nil {
		t.Fatal(err)
	}
	p := d.Spawn(task.Launch{ID: "p1", Prompt: "run id", Grant: &g})
	waitTerminal(t, d, p.ID)

	if ex.ran {
		t.Error("the exec action ran; a read-only grant installed at spawn must refuse it")
	}
}

// The counterpart: a spawn that declares no grant is unaffected. This is the regression
// that would break every existing charter — the zero Grant permits nothing, so treating
// "unstated" as "empty" anywhere along this path would refuse every action of every
// task in the system.
func TestSpawnWithoutAGrantStaysUnbounded(t *testing.T) {
	ex := &ranExec{}
	d := New(func(l task.Launch, reg *task.Registry) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		sup.SetDeriver(task.BasicDeriver{})
		return task.NewTask(l.ID, sup, &execDriver{}, ex)
	})

	p := d.Spawn(task.Launch{ID: "p1", Prompt: "run id"})
	waitTerminal(t, d, p.ID)

	if !ex.ran {
		t.Error("a spawn that declared no grant must run exactly as before")
	}
}

// denyResolver refuses every question it is asked. It is what makes the vector test below
// non-vacuous: AutoResolver promotes a blocking gate to proceed, so a task whose vector
// was ignored would still reach a terminal status and still execute, and the assertion
// would hold for the wrong reason.
type denyResolver struct{}

func (denyResolver) Resolve(_ context.Context, q task.GateQuestion, _ task.ToolUse) (task.Resolution, error) {
	return task.Resolution{Proceed: false, By: "test", Question: q}, nil
}

// A named vector reaches the task too — the soft axis had the same non-consumer as the
// hard one. The builder's own vector blocks every action into a resolver that refuses, so
// the action executes only if the spawn's `vector: auto` actually replaced it.
func TestSpawnInstallsTheVector(t *testing.T) {
	ex := &ranExec{}
	build := func(l task.Launch, reg *task.Registry) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Ask}, denyResolver{})
		sup.SetDeriver(task.BasicDeriver{})
		return task.NewTask(l.ID, sup, &execDriver{}, ex)
	}

	// First, without a vector on the spawn: the builder's ask-and-deny stands.
	d := New(build)
	p := d.Spawn(task.Launch{ID: "p1", Prompt: "run id"})
	waitTerminal(t, d, p.ID)
	if ex.ran {
		t.Fatal("the builder's vector should have blocked this action; the test proves nothing if it did not")
	}

	// Then with one: `vector: auto` replaces it, and the same action proceeds.
	vec, err := task.NamedVector("auto")
	if err != nil {
		t.Fatal(err)
	}
	ex.ran = false
	d2 := New(build)
	p2 := d2.Spawn(task.Launch{ID: "p2", Prompt: "run id", Vector: vec})
	waitTerminal(t, d2, p2.ID)
	if !ex.ran {
		t.Error("the spawn's auto vector did not replace the builder's blocking one")
	}
}

func waitTerminal(t *testing.T, d *Daemon, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap, ok := d.SnapshotOf(id); ok && task.TerminalStatus(string(snap.Status)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach a terminal status", id)
}
