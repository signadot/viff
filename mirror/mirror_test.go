package mirror

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// steps drives a fixed number of trivial actions, then completes.
type steps struct{ n, i int }

func (s *steps) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	if s.i >= s.n {
		return task.ToolUse{}, false, nil
	}
	s.i++
	return task.ToolUse{Name: "act"}, true, nil
}

type noopExec struct{}

func (noopExec) Execute(context.Context, task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Output: "ok"}, nil, nil
}

// sayer narrates (EvSay), runs one action, completes, and reports a final result —
// exercising the output projection. emit is wired to the task's read plane.
type sayer struct {
	emit   func(task.Frame)
	result string
	step   int
}

func (s *sayer) Next(_ context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	s.step++
	if s.step == 1 {
		if s.emit != nil {
			s.emit(task.Frame{Kind: task.EvSay, Text: "working on it"})
		}
		return task.ToolUse{Name: "act"}, true, nil // an action → EvExec → lastOutput
	}
	return task.ToolUse{}, false, nil // complete
}

func (s *sayer) Result() string { return s.result }

// A task's output is durable in the verse: its narration, last tool output, and
// full final answer land on the task:instance entity, readable after the run.
func TestReflectProjectsOutput(t *testing.T) {
	store := entitytest.Store(t)
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	d := &sayer{result: "here is the summary you asked for"}
	p := task.NewTask("p2", sup, d, noopExec{})
	d.emit = p.Emit // narration onto the read plane, as agent.Build wires it
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

// A running task shows up in the verse as a task:instance:<id> entity that
// progresses to a terminal status — so another observer sees its lifecycle purely
// by watching entities.
func TestReflectProjectsTaskState(t *testing.T) {
	store := entitytest.Store(t)
	ch, unsub := store.Observe(entity.PrefixPattern(Ref("").Segs()[:3], 4))
	defer unsub()

	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask("p1", sup, &steps{n: 2}, noopExec{})
	Reflect(store, p.ID, p, Meta{Runner: "local"})

	go func() { p.Run(context.Background()) }()

	ref := Ref("p1")
	sawRunning, sawCompleted := false, false
	deadline := time.After(2 * time.Second)
	for !sawCompleted {
		select {
		case c := <-ch:
			for _, ev := range c.Changes {
				if ev.Ref != ref {
					continue
				}
				if ev.Op == entity.Set && entity.ToObj(ev.To)["status"] == "running" {
					sawRunning = true
				}
				if ev.To != nil && entity.ToObj(ev.To)["status"] == "done" {
					sawCompleted = true
				}
			}
		case <-deadline:
			t.Fatalf("timed out; sawRunning=%v sawCompleted=%v", sawRunning, sawCompleted)
		}
	}
	if !sawRunning {
		t.Fatal("never saw the task appear as running in the verse")
	}

	// the terminal entity remains, queryable, marked completed
	got, ok, _ := store.Get(ref)
	if !ok || entity.ToObj(got.Payload)["status"] != "done" {
		t.Fatalf("terminal entity wrong: %+v ok=%v", got, ok)
	}
}

// Provenance is published with the task, not derived from its id by a reader: `chain`
// is the kpath of the action that spawned it and `trigger` is that chain's root, so
// "every task of the review trigger" is a payload predicate — which is all a `fields`
// match can express — rather than a glob over ids.
func TestReflectPublishesChainAndTrigger(t *testing.T) {
	store := entitytest.Store(t)
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask("p-chain", sup, &sayer{}, noopExec{})
	Reflect(store, p.ID, p, Meta{
		Subject: `demo.pr."1"`,
		Runner:  "local",
		Chain:   "review.onDone",
	})
	e, ok, _ := store.Get(Ref("p-chain")) // the initial Put is synchronous
	if !ok {
		t.Fatal("task entity not mirrored")
	}
	for k, want := range map[string]string{
		"chain":   "review.onDone",
		"trigger": "review",
		"flow":    `demo.pr."1"`,
	} {
		if got := entity.Str(e.Payload, k); got != want {
			t.Errorf("payload[%q] = %q; want %q (payload %+v)", k, got, want, e.Payload)
		}
	}
}

// A task with no trigger behind it (`verse task run`) carries no chain, and must not
// be given an empty one — an empty `trigger` field would match a predicate looking for it.
func TestReflectOmitsChainWhenAbsent(t *testing.T) {
	store := entitytest.Store(t)
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask("p-bare", sup, &sayer{}, noopExec{})
	Reflect(store, p.ID, p, Meta{Runner: "local"})
	e, ok, _ := store.Get(Ref("p-bare"))
	if !ok {
		t.Fatal("task entity not mirrored")
	}
	if _, present := entity.ToObj(e.Payload)["chain"]; present {
		t.Errorf("chain published for a task with none: %+v", e.Payload)
	}
	if _, present := entity.ToObj(e.Payload)["trigger"]; present {
		t.Errorf("trigger published for a task with none: %+v", e.Payload)
	}
}

// fakeHandle is a task.Handle whose read plane is a channel the test drives, so a
// transition can be observed at the instant it happens rather than raced against.
type fakeHandle struct{ ch chan task.Frame }

func (f *fakeHandle) Subscribe() (<-chan task.Frame, func()) { return f.ch, func() {} }
func (f *fakeHandle) SubscribeSince(int64) (<-chan task.Frame, func()) {
	return f.ch, func() {}
}
func (f *fakeHandle) Pause() error                         { return nil }
func (f *fakeHandle) Resume() error                        { return nil }
func (f *fakeHandle) Cancel(string) error                  { return nil }
func (f *fakeHandle) Steer(string, string, string) error   { return nil }
func (f *fakeHandle) SetVector(*task.Vector, string) error { return nil }
func (f *fakeHandle) Result() string                       { return "" }
func (f *fakeHandle) Err() string                          { return "" }

// A gate that has been answered must LEAVE the entity, not linger on it. Reflect writes the
// state it knows, and a write merges — so clearing a field means saying so, and the field
// that says "this task is blocked on a human" is the one nobody may get wrong: a stale
// `pending` reads as a task waiting forever on a gate that was already answered.
func TestReflectClearsAnAnsweredGate(t *testing.T) {
	store := entitytest.Store(t)
	h := &fakeHandle{ch: make(chan task.Frame)}
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

	h.ch <- task.Frame{Kind: task.EvPending, ID: "gate1", Gate: "write"}
	await("the gate to appear", func(f map[string]any) bool {
		return f["pending"] == QuestionRef("gate1").String()
	})

	h.ch <- task.Frame{Kind: task.EvDecision, ID: "gate1", Tool: "act", Proceed: true}
	f := await("the gate to clear", func(f map[string]any) bool { return f["pending"] == nil })
	if f["lastAction"] != "act" {
		t.Fatalf("clearing the gate lost the rest of the state: %+v", f)
	}

	// Clearing the ref is only half of it. The question is its own entity, and one that
	// stayed `pending` after being answered would keep every docket and every predicate
	// reading it wrong — the ref is gone, so nothing else would ever say otherwise.
	q, _, _ := store.Get(QuestionRef("gate1"))
	got := entity.ToObj(q.Payload)
	if got["status"] != QuestionAnswered || got["verdict"] != "proceed" {
		t.Fatalf("the question did not settle: %+v", got)
	}
	if got["gate"] != "write" {
		t.Fatalf("settling overwrote what was asked: %+v", got)
	}
}

// The envelope a task runs under is on its entity: which doer was picked, and what the
// launch may do. It is published for EVERY spawn because the alternative surface does not
// exist for most of them — `select: auto` opens no motion, so an auto-picked doer appeared on
// no docket, and a driver name no longer identifies a doer now that two can share one.
func TestReflectPublishesTheEnvelope(t *testing.T) {
	store := entitytest.Store(t)
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask("e1", sup, &steps{n: 1}, noopExec{})
	Reflect(store, p.ID, p, Meta{ // the initial Put is synchronous
		Runner: "local", Doer: "provider.treat.fixer", Grant: "no-delegate",
		Tools: []string{"read_file", "run_bash"},
	})

	got, _, _ := store.Get(Ref("e1"))
	obj := entity.ToObj(got.Payload)
	if obj["doer"] != "provider.treat.fixer" {
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
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask("e2", sup, &steps{n: 1}, noopExec{})
	Reflect(store, p.ID, p, Meta{Runner: "local"})

	got, _, _ := store.Get(Ref("e2"))
	obj := entity.ToObj(got.Payload)
	for _, k := range []string{"doer", "grant", "tools"} {
		if _, present := obj[k]; present {
			t.Errorf("%q published for a spawn that declared none: %+v", k, got.Payload)
		}
	}
}

// A task record carries WHEN, at both ends and nowhere in between.
//
// `verse ps` could say what a task is and where it ran and not when it happened, which is the
// question asked most often about one that is over. Two instants, stamped once each — `started`
// when the record appears, `ended` when it reaches a terminal status — because stamping every
// update would make the entity change whenever a task narrates, waking every trigger watching
// this slice for a fact nobody wrote.
func TestReflectStampsWhenATaskStartedAndEnded(t *testing.T) {
	store := entitytest.Store(t)
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask("when1", sup, &steps{n: 1}, noopExec{})
	Reflect(store, p.ID, p, Meta{Runner: "local"})

	ref := Ref("when1")
	var started string
	deadline := time.After(2 * time.Second)
	for started == "" {
		select {
		case <-deadline:
			t.Fatal("no `started` on the record")
		default:
		}
		if e, ok, _ := store.Get(ref); ok {
			started = entity.Str(e.Payload, "started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := time.Parse(time.RFC3339, started); err != nil {
		t.Fatalf("started = %q, not an RFC3339 instant: %v", started, err)
	}
	if e, _, _ := store.Get(ref); entity.Str(e.Payload, "ended") != "" {
		t.Errorf("a task that has not finished is already ended")
	}

	go func() { p.Run(context.Background()) }()

	var ended string
	deadline = time.After(3 * time.Second)
	for ended == "" {
		select {
		case <-deadline:
			t.Fatal("no `ended` on a terminal record")
		default:
		}
		if e, ok, _ := store.Get(ref); ok {
			ended = entity.Str(e.Payload, "ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := time.Parse(time.RFC3339, ended); err != nil {
		t.Fatalf("ended = %q, not an RFC3339 instant: %v", ended, err)
	}
	// Stamped ONCE: a start that moves is a start nobody can reason about.
	if e, _, _ := store.Get(ref); entity.Str(e.Payload, "started") != started {
		t.Errorf("started moved to %q", entity.Str(e.Payload, "started"))
	}
}
