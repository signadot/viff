package processd_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/signadot/verse/process"
	"github.com/signadot/verse/process/processd"
	"github.com/signadot/verse/process/processtest"
)

// The local runtime joins the process.Instance conformance suite, so the two producers are
// held to the same properties rather than each being spot-checked on its own terms.
func TestConformance(t *testing.T) {
	p := &localProducer{specs: map[string]processtest.Spec{}}
	p.d = processd.New(p.build)
	processtest.Run(t, p)
}

// localProducer drives processd with a driver it builds itself.
//
// It cannot reuse verse's ScriptedBuilder: that lives in package versed, which imports
// process, so a suite under process/ reaching for it would close an import cycle. The
// driver below is the smaller thing the properties actually need — something that ends the
// run either with an answer or with an error.
type localProducer struct {
	d *processd.Daemon
	n int

	// specs carries each spawn's intent to the Builder. processd begins executing the
	// instant it is handed a process, so the outcome has to be decided at build time —
	// there is no "now fail it" for a process that is already running.
	mu    sync.Mutex
	specs map[string]processtest.Spec
}

func (p *localProducer) Name() string { return "processd" }

// A local process fails through its driver, and the loop reports the driver's error — so a
// caller-chosen failure reason is available. A completion's answer comes from the optional
// Resulter capability, likewise. Gates are the gap: they are raised BY the driver mid-run,
// and there is no outside-in way to open one on a hosted process the way an executor does
// through a broker.
func (p *localProducer) Caps() processtest.Caps {
	return processtest.Caps{FailureReason: true, CompletionAnswer: true}
}

func (p *localProducer) build(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
	p.mu.Lock()
	spec := p.specs[l.ID]
	p.mu.Unlock()
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, reg)
	return process.NewProcess(l.ID, sup, &conformanceDriver{spec: spec}, noopExec{})
}

func (p *localProducer) Start(t *testing.T, spec processtest.Spec) string {
	t.Helper()
	p.n++
	id := "local" + strconv.Itoa(p.n)
	p.mu.Lock()
	p.specs[id] = spec
	p.mu.Unlock()
	p.d.Spawn(process.Launch{ID: id, Task: "a task", Driver: spec.Driver}, nil)
	return id
}

func (p *localProducer) RaiseGate(t *testing.T, id, gate, reason string) string {
	t.Helper()
	t.Fatal("processd cannot raise a gate from outside its driver; Caps().ExecutorGates is false")
	return ""
}

func (p *localProducer) Snapshot(t *testing.T, id string) (process.Instance, bool) {
	t.Helper()
	return p.d.SnapshotOf(id)
}

// conformanceDriver ends its run immediately: with an error when the spec asks for a
// failure, otherwise normally, reporting the spec's text as its result.
type conformanceDriver struct{ spec processtest.Spec }

func (d *conformanceDriver) Next(context.Context, process.StepResult) (process.ToolUse, bool, error) {
	if d.spec.Outcome == process.Failed {
		return process.ToolUse{}, false, errors.New(d.spec.Text)
	}
	return process.ToolUse{}, false, nil
}

// Result is the optional Resulter capability — a completed run's final answer.
func (d *conformanceDriver) Result() string {
	if d.spec.Outcome == process.Failed {
		return "" // a failure's account is an Err; it is not this process's answer
	}
	return d.spec.Text
}

// noopExec never runs: conformanceDriver dispatches no actions.
type noopExec struct{}

func (noopExec) Execute(context.Context, process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	return process.StepResult{}, nil, nil
}
