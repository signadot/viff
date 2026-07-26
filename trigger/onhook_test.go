package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// failing is a driver whose first decision errors — the process reaches terminal "failed".
type failing struct{}

func (failing) Next(context.Context, process.StepResult) (process.ToolUse, bool, error) {
	return process.ToolUse{}, false, fmt.Errorf("boom")
}

// startHookProc builds and runs a process for a spec: a task mentioning "fail" errors
// (terminal failed), everything else completes (terminal done). It mirrors the process into
// the store under spec.Subject, exactly as a real Spawner would.
func startHookProc(store entity.Store, id string, spec launch.Spec) *process.Process {
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	var p *process.Process
	if strings.Contains(spec.Task, "fail") {
		p = process.NewProcess(id, sup, failing{}, noExec{})
	} else {
		p = process.NewProcess(id, sup, immediate{}, noExec{})
	}
	mirror.Reflect(store, id, p, mirror.Meta{Subject: spec.Subject, Policy: "local", Chain: spec.Chain, Task: spec.Task})
	go p.Run(context.Background())
	return p
}

func prRef(id string) entity.Ref { return entity.Ref{System: "demo", Kind: "pr", ID: id} }

// prWhen matches a demo:pr at a specific stage — a field predicate so advancing the stage does
// not re-fire the trigger.
func prWhen(stage string) *Formula {
	return MatchWhen(Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": stage}})
}

// waitField polls until store[ref].Payload[key] == want, or fails after a deadline.
func waitField(t *testing.T, store entity.Store, ref entity.Ref, key string, want any) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := store.Get(ref); ok && entity.Str(e.Payload, key) == want {
			return
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(ref)
			t.Fatalf("%s.%s never became %v; payload=%+v", ref, key, want, e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A spawn's onDone fires when its process completes — advancing the subject in ONE trigger,
// with no separate completion trigger. Runs under ObserveScoped (versed's mode) with the
// immediate (fast) driver, so it also exercises the race-free store-driven reapLoop path.
func TestOnDoneFiresUnderScoped(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		return startHookProc(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{When: prWhen("open")},
		Action: Action{
			Set:    map[string]any{"stage": "reviewing"},
			Spawn:  &launch.Spec{Task: "review $subject"},
			OnDone: &Action{Set: map[string]any{"stage": "reviewed"}},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	store.Put(prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "reviewed")
}

// onDone is recursive: each hook is a full Action, so review → merge → approve reads as one
// nested spine. Each link's spawn gets a distinct, deterministic id (chain descends by status).
func TestOnDoneChains(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	var mu sync.Mutex
	var ids []string
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		mu.Lock()
		ids = append(ids, id)
		mu.Unlock()
		return startHookProc(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{When: prWhen("open")},
		Action: Action{
			Set:   map[string]any{"stage": "reviewing"},
			Spawn: &launch.Spec{Task: "review $subject"},
			OnDone: &Action{
				Set:   map[string]any{"stage": "reviewed"},
				Spawn: &launch.Spec{Task: "merge $subject"},
				OnDone: &Action{
					Set:   map[string]any{"stage": "merged"},
					Spawn: &launch.Spec{Task: "approve $subject"},
					OnDone: &Action{
						Set: map[string]any{"stage": "approved"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	store.Put(prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "approved")

	// The chain produced three distinct spawn ids, keyed by chain position — and the
	// position is a KPATH into the trigger's action tree, so each id names the action that
	// spawned it: the trigger itself, its onDone, and that hook's own onDone.
	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{
		"review@demo:pr:1":               false,
		"review.onDone@demo:pr:1":        false,
		"review.onDone.onDone@demo:pr:1": false,
	}
	for _, id := range ids {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("expected spawn id %q in the chain; got ids=%v", id, ids)
		}
	}
}

// onFailed fires (and onDone does not) when the process errors.
func TestOnFailed(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		return startHookProc(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{When: prWhen("open")},
		Action: Action{
			Set:      map[string]any{"stage": "reviewing"},
			Spawn:    &launch.Spec{Task: "review and fail $subject"},
			OnDone:   &Action{Set: map[string]any{"stage": "reviewed"}},
			OnFailed: &Action{Set: map[string]any{"stage": "rejected"}},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	store.Put(prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "rejected")
	if e, _, _ := store.Get(prRef("1")); entity.ToObj(e.Payload)["stage"] == "reviewed" {
		t.Fatal("onDone fired on a failed process")
	}
}

// A completion hook rides the exact process (its flowProc), not the subject: two processes about
// the SAME subject each fire their own hook independently.
func TestOnHookPerProcess(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		return startHookProc(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	store.Put(prRef("1"), entity.Obj{"stage": "open"}.Payload())
	subj := prRef("1").String()
	eng.spawnHooked("pa", launch.Spec{Subject: subj, Task: "a"},
		&Action{OnDone: &Action{Set: map[string]any{"a": "1"}}}, "pa")
	eng.spawnHooked("pb", launch.Spec{Subject: subj, Task: "b"},
		&Action{OnDone: &Action{Set: map[string]any{"b": "1"}}}, "pb")

	waitField(t, store, prRef("1"), "a", "1")
	waitField(t, store, prRef("1"), "b", "1")
}

// AddTrigger rejects a completion hook with no spawn to complete, at any nesting depth.
func TestOnHookValidation(t *testing.T) {
	eng := New(entity.NewMemStore(), process.AutoResolver{})
	cond := Condition{When: MatchWhen(Match{"system": "demo", "kind": "pr"})}

	if err := eng.AddTrigger(Trigger{Name: "a", Condition: cond,
		Action: Action{OnDone: &Action{Set: map[string]any{"x": "1"}}}}); err == nil {
		t.Fatal("expected error: onDone without a spawn")
	}
	if err := eng.AddTrigger(Trigger{Name: "b", Condition: cond,
		Action: Action{
			Spawn:  &launch.Spec{Task: "t"},
			OnDone: &Action{OnFailed: &Action{Set: map[string]any{"x": "1"}}}, // nested hook, no nested spawn
		}}); err == nil {
		t.Fatal("expected error: nested onFailed without a spawn")
	}
	if err := eng.AddTrigger(Trigger{Name: "c", Condition: cond,
		Action: Action{
			Spawn:  &launch.Spec{Task: "t"},
			OnDone: &Action{Set: map[string]any{"x": "1"}},
		}}); err != nil {
		t.Fatalf("valid onDone rejected: %v", err)
	}
}

// The onX hooks round-trip through the tony codec under their canonical field names.
func TestOnHookTonyRoundTrip(t *testing.T) {
	a := Action{
		Spawn:    &launch.Spec{Task: "review"},
		OnDone:   &Action{Set: map[string]any{"stage": "reviewed"}},
		OnFailed: &Action{Set: map[string]any{"stage": "rejected"}},
	}
	b, err := a.ToTony()
	if err != nil {
		t.Fatalf("ToTony: %v", err)
	}
	if !strings.Contains(string(b), "onDone") || !strings.Contains(string(b), "onFailed") {
		t.Fatalf("encoded form missing hook keys:\n%s", b)
	}
	var got Action
	if err := got.FromTony(b); err != nil {
		t.Fatalf("FromTony: %v", err)
	}
	if got.OnDone == nil || got.OnDone.Set["stage"] != "reviewed" {
		t.Fatalf("onDone did not round-trip: %+v", got.OnDone)
	}
	if got.OnFailed == nil || got.OnFailed.Set["stage"] != "rejected" {
		t.Fatalf("onFailed did not round-trip: %+v", got.OnFailed)
	}
}

// Adopt (the resume path) carries no hooks: a completion after adoption fires nothing. Guards
// the documented resume limitation so a future rehydration change is noticed here.
func TestAdoptDropsHooks(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	store.Put(prRef("1"), entity.Obj{"stage": "open"}.Payload())
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
	p := process.NewProcess("adopted", sup, immediate{}, noExec{})
	mirror.Reflect(store, "adopted", p, mirror.Meta{Subject: prRef("1").String(), Policy: "local"})
	eng.Adopt(prRef("1").String(), "adopted", p)
	go p.Run(ctx)

	// wait for the process to reach terminal; the subject must be untouched (no hook ran).
	waitField(t, store, mirror.Ref("adopted"), "status", "done")
	time.Sleep(50 * time.Millisecond) // let any (erroneous) hook application land
	if e, _, _ := store.Get(prRef("1")); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("adopted process's completion mutated the subject: %+v", e.Payload)
	}
}
