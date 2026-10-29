package mirror

import (
	"context"
	"fmt"
	"github.com/signadot/verse/systems"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/runner"
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

// sayer narrates (EventSay), runs one action, completes, and reports a final result —
// exercising the output projection. emit is wired to the task's read plane.
type sayer struct {
	emit   func(task.Event)
	result string
	step   int
}

func (s *sayer) Next(_ context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	s.step++
	if s.step == 1 {
		if s.emit != nil {
			s.emit(task.Event{Kind: task.EventSay, Text: "working on it"})
		}
		return task.ToolUse{Name: "act"}, true, nil // an action → EventExec → lastOutput
	}
	return task.ToolUse{}, false, nil // complete
}

func (s *sayer) Result() string { return s.result }

// A task's output is durable in the verse: its narration, last tool output, and
// full final answer land on the task:instance entity, readable after the run.
func TestReflectProjectsOutput(t *testing.T) {
	store := entitytest.Store(t)
	sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
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

	sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
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
	sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
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
	sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
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
type fakeHandle struct{ ch chan task.Event }

func (f *fakeHandle) Subscribe() (<-chan task.Event, func()) { return f.ch, func() {} }
func (f *fakeHandle) SubscribeSince(int64) (<-chan task.Event, func()) {
	return f.ch, func() {}
}
func (f *fakeHandle) Pause(string) error                      { return nil }
func (f *fakeHandle) Resume(string) error                     { return nil }
func (f *fakeHandle) Cancel(string, string) error             { return nil }
func (f *fakeHandle) Steer(string, string, string) error      { return nil }
func (f *fakeHandle) SetAutonomy(task.Autonomy, string) error { return nil }
func (f *fakeHandle) Result() string                          { return "" }
func (f *fakeHandle) Err() string                             { return "" }
func (*fakeHandle) Refused() map[string]int                   { return nil }
func (*fakeHandle) Agent() (string, map[string]string)        { return "", nil }

// A gate that has been answered must LEAVE the entity, not linger on it. Reflect writes the
// state it knows, and a write merges — so clearing a field means saying so, and the field
// that says "this task is blocked on a human" is the one nobody may get wrong: a stale
// `pending` reads as a task waiting forever on a gate that was already answered.
func TestReflectClearsAnAnsweredGate(t *testing.T) {
	store := entitytest.Store(t)
	h := &fakeHandle{ch: make(chan task.Event)}
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

	h.ch <- task.Event{Kind: task.EventPending, ID: "gate1", Gate: "write"}
	await("the gate to appear", func(f map[string]any) bool {
		return f["pending"] == QuestionRef("gate1").String()
	})

	h.ch <- task.Event{Kind: task.EventDecision, ID: "gate1", Tool: "act", Proceed: true}
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

// The envelope a task runs under is on its entity: what the launch may do, and what it works
// in. It is published for EVERY spawn, since the record is the one place a reader of a task
// on another runner can read it.
func TestReflectPublishesTheEnvelope(t *testing.T) {
	store := entitytest.Store(t)
	sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
	p := task.NewTask("e1", sup, &steps{n: 1}, noopExec{})
	Reflect(store, p.ID, p, Meta{ // the initial Put is synchronous
		Runner: "local", Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}},
		Git: []runner.Repo{{Repo: "acme/app", From: "main"}, {Repo: "acme/docs"}},
	})

	got, _, _ := store.Get(Ref("e1"))
	obj := entity.ToObj(got.Payload)
	if p := AutonomyOf(got.Payload); len(p) != 1 || p[0].Then.Mode != task.ThenAllow {
		t.Errorf("the policy not published: %+v", got.Payload)
	}
	// What the run worked on, in the spawn's own words: a reader of the record sees which
	// repositories, and where each started, without the charter that spawned it.
	repos := fmt.Sprint(obj["git"])
	for _, want := range []string{"acme/app", "main", "acme/docs"} {
		if !strings.Contains(repos, want) {
			t.Errorf("git not published (%q missing): %s", want, repos)
		}
	}
}

// A spawn that said nothing about its envelope publishes no envelope keys — an absent grant
// is not the same claim as an empty one, and a reader must be able to tell "unbounded, by
// omission" from "bounded to nothing".
func TestReflectOmitsAnUndeclaredEnvelope(t *testing.T) {
	store := entitytest.Store(t)
	sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
	p := task.NewTask("e2", sup, &steps{n: 1}, noopExec{})
	Reflect(store, p.ID, p, Meta{Runner: "local"})

	got, _, _ := store.Get(Ref("e2"))
	obj := entity.ToObj(got.Payload)
	for _, k := range []string{"selected", "doer", "repos"} {
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
	sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
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

// refusingHandle is a fake whose runner reports connections its proxy refused.
type refusingHandle struct {
	fakeHandle
	refused map[string]int
}

func (r *refusingHandle) Refused() map[string]int { return r.refused }

// WHAT A TASK'S PROXY REFUSED IS ON ITS RECORD when it ends, by destination and how often: the
// runner's account, which no frame carries.
func TestARefusedConnectionIsOnTheRecord(t *testing.T) {
	store := entitytest.Store(t)
	h := &refusingHandle{fakeHandle: fakeHandle{ch: make(chan task.Event, 1)}, refused: map[string]int{"evil.example:443": 3}}
	done := make(chan struct{})
	go func() { Reflect(store, "r1", h, Meta{Runner: "local"}); close(done) }()
	h.ch <- task.Event{Kind: task.EventStatus, Text: string(task.Done)}
	deadline := time.After(5 * time.Second)
	for {
		e, _, _ := store.Get(Ref("r1"))
		if r, ok := entity.ToObj(e.Payload)["refused"].(map[string]any); ok {
			if fmt.Sprint(r["evil.example:443"]) != "3" {
				t.Fatalf("refused on the record: %v", r)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("no refused on the record: %+v", entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// agentHandle is a fake whose runner reports which agent runs and with what.
type agentHandle struct {
	fakeHandle
	name     string
	settings map[string]string
}

func (a *agentHandle) Agent() (string, map[string]string) { return a.name, a.settings }

// recordOf is the record of id once ok holds of it, or a failure after a while.
func recordOf(t *testing.T, store entity.Store, id string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		e, found, _ := store.Get(Ref(id))
		if found {
			if f := entity.ToObj(e.Payload); ok(f) {
				return f
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s: never as wanted: %+v", id, entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// WHO RAN A TASK IS ON ITS RECORD: the spawn's agent from the first write, its name and settings
// and never its annotations, so a task that never started says what was asked; the runner's
// resolved agent and the settings it ran with once it runs, so a running or finished task says
// what ran. A script's record says it is one, and has no agent.
func TestTheRecordSaysWhoRanTheTask(t *testing.T) {
	store := entitytest.Store(t)
	h := &agentHandle{fakeHandle: fakeHandle{ch: make(chan task.Event, 2)}, name: "verse", settings: map[string]string{"model": "claude-sonnet-5"}}
	asked := &runner.Agent{Settings: map[string]string{"effort": "high"}, Annotations: map[string]string{"verse.portkey/virtual-key": "vk"}}
	go Reflect(store, "a1", h, Meta{Runner: "local", Agent: asked})
	first := recordOf(t, store, "a1", func(f map[string]any) bool { return f["agent"] != nil })
	agent, _ := first["agent"].(map[string]any)
	if agent["effort"] != "high" || agent["name"] != nil || agent["annotations"] != nil {
		t.Fatalf("at first write the record says %v, want the spawn's settings, no name, no annotations", agent)
	}
	if first["script"] != nil {
		t.Errorf("an agent's record says script: %v", first["script"])
	}
	h.ch <- task.Event{Kind: task.EventStatus, Text: string(task.Running)}
	recordOf(t, store, "a1", func(f map[string]any) bool {
		a, _ := f["agent"].(map[string]any)
		return a["name"] == "verse" && a["model"] == "claude-sonnet-5" && a["effort"] == nil
	})

	script := &agentHandle{fakeHandle: fakeHandle{ch: make(chan task.Event, 2)}}
	go Reflect(store, "s1", script, Meta{Runner: "local", Script: true})
	script.ch <- task.Event{Kind: task.EventStatus, Text: string(task.Running)}
	rec := recordOf(t, store, "s1", func(f map[string]any) bool { return f["script"] == true && f["status"] == string(task.Running) })
	if rec["agent"] != nil {
		t.Errorf("a script's record has an agent: %v", rec["agent"])
	}
	if a, script := AgentOf(entity.Obj(rec).Payload()); a != nil || !script {
		t.Errorf("AgentOf a script's record: %v %v", a, script)
	}
}

// A RESUMED TASK IS NOT PAUSED: `paused` on the record is whether the latest status said so.
// Task.Resume says `running`, as a status, so the flag goes with it.
func TestAResumedTaskIsNotPaused(t *testing.T) {
	store := entitytest.Store(t)
	h := &fakeHandle{ch: make(chan task.Event, 3)}
	go Reflect(store, "p1", h, Meta{Runner: "local", Script: true})
	h.ch <- task.Event{Kind: task.EventStatus, Text: string(task.Running)}
	h.ch <- task.Event{Kind: task.EventStatus, Text: string(task.Paused), By: "user:ana@example.com"}
	recordOf(t, store, "p1", func(f map[string]any) bool { return f["paused"] == true })
	h.ch <- task.Event{Kind: task.EventStatus, Text: string(task.Running), By: "user:ana@example.com"}
	rec := recordOf(t, store, "p1", func(f map[string]any) bool { return f["paused"] != true })
	if rec["status"] != string(task.Running) {
		t.Fatalf("a resumed task's status = %v, want running", rec["status"])
	}
}
