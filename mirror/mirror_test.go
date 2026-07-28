package mirror

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
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
	store := entitytest.Store(t)
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	d := &sayer{result: "here is the summary you asked for"}
	p := process.NewProcess("p2", sup, d, noopExec{})
	d.emit = p.Emit // narration onto the read plane, as harness.Build wires it
	Reflect(store, p.ID, p, Meta{Runner: "local"})
	go func() { p.Run(context.Background()) }()

	ref := Ref("p2")
	deadline := time.After(2 * time.Second)
	for {
		if e, ok, _ := store.Get(ref); ok && entity.ToObj(e.Payload)["status"] == "done" {
			if entity.ToObj(e.Payload)["say"] != "working on it" {
				t.Fatalf("narration not projected: %+v", e.Payload)
			}
			if entity.ToObj(e.Payload)["lastOutput"] != "ok" {
				t.Fatalf("tool output not projected: %+v", e.Payload)
			}
			if entity.ToObj(e.Payload)["result"] != d.result {
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
	process.Handle
	url string
}

func (d dialer) DialURL() string { return d.url }

// A dial-in runner publishes where a worker connects: Reflect projects a handle's DialURL
// onto the entity as `dialURL`, so a worker can be told where to dial in (mint→publish→dial)
// rather than being pre-configured. A handle without one (local, remote-processd) sets none.
func TestReflectPublishesDialURL(t *testing.T) {
	store := entitytest.Store(t)
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	p := process.NewProcess("d1", sup, &steps{n: 1}, noopExec{})
	const url = "http://verse.test:9090/broker/abc123"
	Reflect(store, p.ID, dialer{p, url}, Meta{Runner: "broker"}) // initial Put is synchronous

	got, ok, _ := store.Get(Ref("d1"))
	if !ok || entity.ToObj(got.Payload)["dialURL"] != url {
		t.Fatalf("dialURL not published: %+v", got.Payload)
	}

	// a plain handle (no DialURL) publishes no dialURL key.
	p2 := process.NewProcess("d2", sup, &steps{n: 1}, noopExec{})
	Reflect(store, p2.ID, p2, Meta{Runner: "local"})
	if g2, _, _ := store.Get(Ref("d2")); entity.ToObj(g2.Payload)["dialURL"] != nil {
		t.Fatalf("local handle should publish no dialURL: %+v", g2.Payload)
	}
}

// A running process shows up in the verse as a process:instance:<id> entity that
// progresses to a terminal status — so another observer sees its lifecycle purely
// by watching entities.
func TestReflectProjectsProcessState(t *testing.T) {
	store := entitytest.Store(t)
	ch, unsub := store.Observe()
	defer unsub()

	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	p := process.NewProcess("p1", sup, &steps{n: 2}, noopExec{})
	Reflect(store, p.ID, p, Meta{Runner: "local"})

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
			if ev.Op == entity.Set && entity.ToObj(ev.To)["status"] == "running" {
				sawRunning = true
			}
			if ev.To != nil && entity.ToObj(ev.To)["status"] == "done" {
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
	if !ok || entity.ToObj(got.Payload)["status"] != "done" {
		t.Fatalf("terminal entity wrong: %+v ok=%v", got, ok)
	}
}

// Provenance is published with the process, not derived from its id by a reader: `chain`
// is the kpath of the action that spawned it and `trigger` is that chain's root, so
// "every process of the review trigger" is a payload predicate — which is all a `fields`
// match can express — rather than a glob over ids.
func TestReflectPublishesChainAndTrigger(t *testing.T) {
	store := entitytest.Store(t)
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	p := process.NewProcess("p-chain", sup, &sayer{}, noopExec{})
	Reflect(store, p.ID, p, Meta{
		Subject: "demo:pr:1",
		Runner:  "local",
		Chain:   "review.onDone",
	})
	e, ok, _ := store.Get(Ref("p-chain")) // the initial Put is synchronous
	if !ok {
		t.Fatal("process entity not mirrored")
	}
	for k, want := range map[string]string{
		"chain":   "review.onDone",
		"trigger": "review",
		"flow":    "demo:pr:1",
	} {
		if got := entity.Str(e.Payload, k); got != want {
			t.Errorf("payload[%q] = %q; want %q (payload %+v)", k, got, want, e.Payload)
		}
	}
}

// A process with no trigger behind it (`verse process run`) carries no chain, and must not
// be given an empty one — an empty `trigger` field would match a predicate looking for it.
func TestReflectOmitsChainWhenAbsent(t *testing.T) {
	store := entitytest.Store(t)
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	p := process.NewProcess("p-bare", sup, &sayer{}, noopExec{})
	Reflect(store, p.ID, p, Meta{Runner: "local"})
	e, ok, _ := store.Get(Ref("p-bare"))
	if !ok {
		t.Fatal("process entity not mirrored")
	}
	if _, present := entity.ToObj(e.Payload)["chain"]; present {
		t.Errorf("chain published for a process with none: %+v", e.Payload)
	}
	if _, present := entity.ToObj(e.Payload)["trigger"]; present {
		t.Errorf("trigger published for a process with none: %+v", e.Payload)
	}
}

// fakeHandle is a process.Handle whose read plane is a channel the test drives, so a
// transition can be observed at the instant it happens rather than raced against.
type fakeHandle struct{ ch chan process.Frame }

func (f *fakeHandle) Subscribe() (<-chan process.Frame, func()) { return f.ch, func() {} }
func (f *fakeHandle) Pause()                                    {}
func (f *fakeHandle) Resume()                                   {}
func (f *fakeHandle) Cancel(string)                             {}
func (f *fakeHandle) Steer(string, string)                      {}
func (f *fakeHandle) SetVector(*process.Vector, string)         {}
func (f *fakeHandle) Result() string                            { return "" }
func (f *fakeHandle) Err() string                               { return "" }

// A gate that has been answered must LEAVE the entity, not linger on it. Reflect writes the
// state it knows, and a write merges — so clearing a field means saying so, and the field
// that says "this process is blocked on a human" is the one nobody may get wrong: a stale
// `pending` reads as a process waiting forever on a gate that was already answered.
func TestReflectClearsAnAnsweredGate(t *testing.T) {
	store := entitytest.Store(t)
	h := &fakeHandle{ch: make(chan process.Frame)}
	Reflect(store, "g1", h, Meta{Runner: "local"})

	fields := func() map[string]any {
		t.Helper()
		e, _, _ := store.Get(Ref("g1"))
		return entity.ToObj(e.Payload)
	}
	await := func(why string, ok func(map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			if f := fields(); ok(f) {
				return f
			}
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for %s; entity = %+v", why, fields())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}

	h.ch <- process.Frame{Kind: process.EvPending, ID: "gate1", Gate: "write"}
	await("the gate to appear", func(f map[string]any) bool { return f["pending"] == "gate1" })

	h.ch <- process.Frame{Kind: process.EvDecision, ID: "gate1", Tool: "act"}
	f := await("the gate to clear", func(f map[string]any) bool { return f["pending"] == nil })
	if f["pendingGate"] != nil {
		t.Fatalf("the gate name outlived the gate: %+v", f)
	}
	if f["lastAction"] != "act" {
		t.Fatalf("clearing the gate lost the rest of the state: %+v", f)
	}
}

// The envelope a process runs under is on its entity: which doer was picked, and what the
// launch may do. It is published for EVERY spawn because the alternative surface does not
// exist for most of them — `select: auto` opens no motion, so an auto-picked doer appeared on
// no docket, and a driver name no longer identifies a doer now that two can share one.
func TestReflectPublishesTheEnvelope(t *testing.T) {
	store := entitytest.Store(t)
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	p := process.NewProcess("e1", sup, &steps{n: 1}, noopExec{})
	Reflect(store, p.ID, p, Meta{ // the initial Put is synchronous
		Runner: "local", Doer: "provider:treat:fixer", Grant: "no-delegate",
		Tools: []string{"read_file", "run_bash"},
	})

	got, _, _ := store.Get(Ref("e1"))
	obj := entity.ToObj(got.Payload)
	if obj["doer"] != "provider:treat:fixer" {
		t.Errorf("doer not published: %+v", got.Payload)
	}
	if obj["grant"] != "no-delegate" {
		t.Errorf("grant not published: %+v", got.Payload)
	}
	if tools := entity.Strs(got.Payload, "tools"); len(tools) != 2 || tools[1] != "run_bash" {
		t.Errorf("tools not published: %+v", got.Payload)
	}
}

// A spawn that said nothing about its envelope publishes no envelope keys — an absent grant
// is not the same claim as an empty one, and a reader must be able to tell "unbounded, by
// omission" from "bounded to nothing".
func TestReflectOmitsAnUndeclaredEnvelope(t *testing.T) {
	store := entitytest.Store(t)
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	p := process.NewProcess("e2", sup, &steps{n: 1}, noopExec{})
	Reflect(store, p.ID, p, Meta{Runner: "local"})

	got, _, _ := store.Get(Ref("e2"))
	obj := entity.ToObj(got.Payload)
	for _, k := range []string{"doer", "grant", "tools"} {
		if _, present := obj[k]; present {
			t.Errorf("%q published for a spawn that declared none: %+v", k, got.Payload)
		}
	}
}
