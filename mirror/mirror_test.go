package mirror

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/process"
)

// steps drives a fixed number of trivial actions, then completes.
type steps struct{ n, i int }

func (s *steps) Next(context.Context, process.StepResult) (process.ToolUse, bool, error) {
	if s.i >= s.n {
		return process.ToolUse{}, false, nil
	}
	s.i++
	return process.ToolUse{Name: "act"}, true, nil
}

type noopExec struct{}

func (noopExec) Execute(context.Context, process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	return process.StepResult{Output: "ok"}, nil, nil
}

// sayer narrates (EvSay), runs one action, completes, and reports a final result —
// exercising the output projection. emit is wired to the process's read plane.
type sayer struct {
	emit   func(process.Frame)
	result string
	step   int
}

func (s *sayer) Next(_ context.Context, _ process.StepResult) (process.ToolUse, bool, error) {
	s.step++
	if s.step == 1 {
		if s.emit != nil {
			s.emit(process.Frame{Kind: process.EvSay, Text: "working on it"})
		}
		return process.ToolUse{Name: "act"}, true, nil // an action → EvExec → lastOutput
	}
	return process.ToolUse{}, false, nil // complete
}

func (s *sayer) Result() string { return s.result }

// A process's output is durable in the verse: its narration, last tool output, and
// full final answer land on the process:instance entity, readable after the run.
func TestReflectProjectsOutput(t *testing.T) {
	store := entity.NewMemStore()
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	d := &sayer{result: "here is the summary you asked for"}
	p := process.NewProcess("p2", sup, d, noopExec{})
	d.emit = p.Emit // narration onto the read plane, as harness.Build wires it
	Reflect(store, p.ID, p, "", "local")
	go func() { p.Run(context.Background()) }()

	ref := Ref("p2")
	deadline := time.After(2 * time.Second)
	for {
		if e, ok, _ := store.Get(ref); ok && e.Payload["status"] == "completed" {
			if e.Payload["say"] != "working on it" {
				t.Fatalf("narration not projected: %+v", e.Payload)
			}
			if e.Payload["lastOutput"] != "ok" {
				t.Fatalf("tool output not projected: %+v", e.Payload)
			}
			if e.Payload["result"] != d.result {
				t.Fatalf("final result not projected: %+v", e.Payload)
			}
			return
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(ref)
			t.Fatalf("timed out; entity = %+v", e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// dialer wraps a handle with a worker-facing dial URL, as the broker's handle does.
type dialer struct {
	process.ProcHandle
	url string
}

func (d dialer) DialURL() string { return d.url }

// A dial-in policy publishes where a worker connects: Reflect projects a handle's DialURL
// onto the entity as `dialURL`, so a worker can be told where to dial in (mint→publish→dial)
// rather than being pre-configured. A handle without one (local, remote-processd) sets none.
func TestReflectPublishesDialURL(t *testing.T) {
	store := entity.NewMemStore()
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	p := process.NewProcess("d1", sup, &steps{n: 1}, noopExec{})
	const url = "http://verse.test:9090/broker/abc123"
	Reflect(store, p.ID, dialer{p, url}, "", "broker") // initial Put is synchronous

	got, ok, _ := store.Get(Ref("d1"))
	if !ok || got.Payload["dialURL"] != url {
		t.Fatalf("dialURL not published: %+v", got.Payload)
	}

	// a plain handle (no DialURL) publishes no dialURL key.
	p2 := process.NewProcess("d2", sup, &steps{n: 1}, noopExec{})
	Reflect(store, p2.ID, p2, "", "local")
	if g2, _, _ := store.Get(Ref("d2")); g2.Payload["dialURL"] != nil {
		t.Fatalf("local handle should publish no dialURL: %+v", g2.Payload)
	}
}

// A running process shows up in the verse as a process:instance:<id> entity that
// progresses to a terminal status — so another observer sees its lifecycle purely
// by watching entities.
func TestReflectProjectsProcessState(t *testing.T) {
	store := entity.NewMemStore()
	ch, unsub := store.Observe()
	defer unsub()

	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	p := process.NewProcess("p1", sup, &steps{n: 2}, noopExec{})
	Reflect(store, p.ID, p, "", "local")

	go func() { p.Run(context.Background()) }()

	ref := Ref("p1")
	sawRunning, sawCompleted := false, false
	deadline := time.After(2 * time.Second)
	for !sawCompleted {
		select {
		case ev := <-ch:
			if ev.Ref != ref {
				continue
			}
			if ev.Op == entity.Set && ev.To["status"] == "running" {
				sawRunning = true
			}
			if ev.To != nil && ev.To["status"] == "completed" {
				sawCompleted = true
			}
		case <-deadline:
			t.Fatalf("timed out; sawRunning=%v sawCompleted=%v", sawRunning, sawCompleted)
		}
	}
	if !sawRunning {
		t.Fatal("never saw the process appear as running in the verse")
	}

	// the terminal entity remains, queryable, marked completed
	got, ok, _ := store.Get(ref)
	if !ok || got.Payload["status"] != "completed" {
		t.Fatalf("terminal entity wrong: %+v ok=%v", got, ok)
	}
}
