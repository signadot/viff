package runnerd_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/tasktest"
)

// The local runtime joins the task.Instance conformance suite, so the two producers are
// held to the same properties rather than each being spot-checked on its own terms.
func TestConformance(t *testing.T) {
	p := &localProducer{specs: map[string]tasktest.Spec{}}
	p.d = runnerd.NewInProcessHandler(p.build)
	tasktest.Run(t, p)
}

// localProducer drives runnerd with a driver it builds itself.
//
// It cannot reuse verse's ScriptedBuilder: that lives in package versed, which imports
// task, so a suite under task/ reaching for it would close an import cycle. The
// driver below is the smaller thing the properties actually need — something that ends the
// run either with an answer or with an error.
type localProducer struct {
	d *runnerd.InProcessHandler
	n int

	// specs carries each spawn's intent to the Builder. runnerd begins executing the
	// instant it is handed a task, so the outcome has to be decided at build time —
	// there is no "now fail it" for a task that is already running.
	mu    sync.Mutex
	specs map[string]tasktest.Spec
}

func (p *localProducer) Name() string { return "runnerd" }

// A local task fails through its driver, and the loop reports the driver's error — so a
// caller-chosen failure reason is available. A completion's answer comes from the optional
// Resulter capability, likewise. Gates are the gap: they are raised BY the driver mid-run,
// and there is no outside-in way to open one on a hosted task.
func (p *localProducer) Caps() tasktest.Caps {
	return tasktest.Caps{FailureReason: true, CompletionAnswer: true}
}

func (p *localProducer) build(l task.Launch, reg *task.Registry) *task.Task {
	p.mu.Lock()
	spec := p.specs[l.ID]
	p.mu.Unlock()
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, reg)
	return task.NewTask(l.ID, sup, &conformanceDriver{spec: spec}, noopExec{})
}

func (p *localProducer) Start(t *testing.T, spec tasktest.Spec) string {
	t.Helper()
	p.n++
	id := "local" + strconv.Itoa(p.n)
	p.mu.Lock()
	p.specs[id] = spec
	p.mu.Unlock()
	p.d.Spawn(task.Launch{ID: id, Prompt: "a task", Driver: spec.Driver})
	return id
}

func (p *localProducer) RaiseGate(t *testing.T, id, gate, reason string) string {
	t.Helper()
	t.Fatal("runnerd cannot raise a gate from outside its driver; Caps().ExecutorGates is false")
	return ""
}

func (p *localProducer) Snapshot(t *testing.T, id string) (task.Instance, bool) {
	t.Helper()
	return p.d.SnapshotOf(id)
}

// conformanceDriver ends its run immediately: with an error when the spec asks for a
// failure, otherwise normally, reporting the spec's text as its result.
type conformanceDriver struct{ spec tasktest.Spec }

func (d *conformanceDriver) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	if d.spec.Outcome == task.Failed {
		return task.ToolUse{}, false, errors.New(d.spec.Text)
	}
	return task.ToolUse{}, false, nil
}

// Result is the optional Resulter capability — a completed run's final answer.
func (d *conformanceDriver) Result() string {
	if d.spec.Outcome == task.Failed {
		return "" // a failure's account is an Err; it is not this task's answer
	}
	return d.spec.Text
}

// noopExec never runs: conformanceDriver dispatches no actions.
type noopExec struct{}

func (noopExec) Execute(context.Context, task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{}, nil, nil
}
