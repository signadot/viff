package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// failing is a driver whose first decision errors — the task reaches terminal "failed".
type failing struct{}

func (failing) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	return task.ToolUse{}, false, fmt.Errorf("boom")
}

// startHookTask builds and runs a task for a spec: a task mentioning "fail" errors
// (terminal failed), everything else completes (terminal done). It mirrors the task into
// the store under spec.Subject, exactly as a real Spawner would.
func startHookTask(store entity.Store, id string, spec launch.Spec) *task.Task {
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	var p *task.Task
	if strings.Contains(spec.Prompt, "fail") {
		p = task.NewTask(id, sup, failing{}, noExec{})
	} else {
		p = task.NewTask(id, sup, immediate{}, noExec{})
	}
	// Occurrence rides along exactly as the production spawner sends it (versed's spawnerFor):
	// it is the commit that caused the firing, and a completion hook reads at it. A harness
	// that dropped it would let the record fall back to the revision its own write landed at,
	// which is a different number and would make the hook tests pass for the wrong reason.
	mirror.Reflect(store, id, p, mirror.Meta{
		Subject: spec.Subject, Runner: "local", Chain: spec.Chain,
		Prompt: spec.Prompt, Occurrence: spec.Occurrence,
	})
	go p.Run(context.Background())
	return p
}

func prRef(id string) entity.Ref { return entity.RefAt("demo", "pr", id) }

// prSubject is a demo:pr at a specific stage — the subject of most of these triggers, and
// with no `when` beside it, their cause too. A field predicate rather than a bare kind, so
// advancing the stage does not re-fire the trigger.
func prSubject(stage string) Match {
	return Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": stage}}
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

// A spawn's onDone fires when its task completes — advancing the subject in ONE trigger,
// with no separate completion trigger. Runs under ObserveScoped (versed's mode) with the
// immediate (fast) driver, so it also exercises the race-free store-driven reapLoop path.
func TestOnDoneFiresUnderScoped(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		return startHookTask(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:  entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn:  &launch.Spec{Prompt: "review $subject"},
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "reviewed")
}

// onDone is recursive: each hook is a full Action, so review → merge → approve reads as one
// nested spine. Each link's spawn gets a distinct, deterministic id (chain descends by status).
func TestOnDoneChains(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	var mu sync.Mutex
	var ids []string
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		mu.Lock()
		ids = append(ids, id)
		mu.Unlock()
		return startHookTask(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Prompt: "review $subject"},
			OnDone: &Action{
				Patch: entity.Obj{"stage": "reviewed"}.Payload(),
				Spawn: &launch.Spec{Prompt: "merge $subject"},
				OnDone: &Action{
					Patch: entity.Obj{"stage": "merged"}.Payload(),
					Spawn: &launch.Spec{Prompt: "approve $subject"},
					OnDone: &Action{
						Patch: entity.Obj{"stage": "approved"}.Payload(),
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "approved")

	// The chain produced three distinct spawn ids, keyed by chain position — and the
	// position is a KPATH into the trigger's action tree, so each id names the action that
	// spawned it: the trigger itself, its onDone, and that hook's own onDone.
	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{
		"review@demo.pr.1":               false,
		"review.onDone@demo.pr.1":        false,
		"review.onDone.onDone@demo.pr.1": false,
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

// onFailed fires (and onDone does not) when the task errors.
func TestOnFailed(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		return startHookTask(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:    entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn:    &launch.Spec{Prompt: "review and fail $subject"},
			OnDone:   &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
			OnFailed: &Action{Patch: entity.Obj{"stage": "rejected"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "rejected")
	if e, _, _ := store.Get(prRef("1")); entity.ToObj(e.Payload)["stage"] == "reviewed" {
		t.Fatal("onDone fired on a failed task")
	}
}

// A completion hook rides the exact task (its flowTask), not the subject: two tasks about
// the SAME subject each fire their own hook independently.
func TestOnHookPerTask(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		return startHookTask(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	subj := prRef("1").String()
	eng.spawnHooked("pa", launch.Spec{Subject: subj, Prompt: "a"},
		&Action{OnDone: &Action{Patch: entity.Obj{"a": "1"}.Payload()}}, "pa")
	eng.spawnHooked("pb", launch.Spec{Subject: subj, Prompt: "b"},
		&Action{OnDone: &Action{Patch: entity.Obj{"b": "1"}.Payload()}}, "pb")

	waitField(t, store, prRef("1"), "a", "1")
	waitField(t, store, prRef("1"), "b", "1")
}

// AddTrigger rejects a completion hook with no spawn to complete, at any nesting depth.
func TestOnHookValidation(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	// A subject the action can advance past: these cases are about onX validation, and a
	// charter that would re-spawn forever is a poor example to make them from.
	cond := Condition{Subject: Match{"system": "demo", "kind": "pr",
		"value": map[string]any{"stage": "open"}}}

	if err := eng.AddTrigger(Trigger{
		Name: "a", Condition: cond,
		Action: Action{OnDone: &Action{Patch: entity.Obj{"x": "1"}.Payload()}},
	}); err == nil {
		t.Fatal("expected error: onDone without a spawn")
	}
	if err := eng.AddTrigger(Trigger{
		Name: "b", Condition: cond,
		Action: Action{
			Spawn:  &launch.Spec{Prompt: "t"},
			OnDone: &Action{OnFailed: &Action{Patch: entity.Obj{"x": "1"}.Payload()}}, // nested hook, no nested spawn
		},
	}); err == nil {
		t.Fatal("expected error: nested onFailed without a spawn")
	}
	if err := eng.AddTrigger(Trigger{
		Name: "c", Condition: cond,
		Action: Action{
			Spawn:  &launch.Spec{Prompt: "t"},
			OnDone: &Action{Patch: entity.Obj{"stage": "done"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("valid onDone rejected: %v", err)
	}
}

// The onX hooks round-trip through the tony codec under their canonical field names.
func TestOnHookTonyRoundTrip(t *testing.T) {
	a := Action{
		Spawn:    &launch.Spec{Prompt: "review"},
		OnDone:   &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		OnFailed: &Action{Patch: entity.Obj{"stage": "rejected"}.Payload()},
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
	if got.OnDone == nil || entity.Str(got.OnDone.Patch, "stage") != "reviewed" {
		t.Fatalf("onDone did not round-trip: %+v", got.OnDone)
	}
	if got.OnFailed == nil || entity.Str(got.OnFailed.Patch, "stage") != "rejected" {
		t.Fatalf("onFailed did not round-trip: %+v", got.OnFailed)
	}
}

// Adopt with NO chain still carries no hooks, and correctly so: there is no address to look
// an action up by. It is the entry point for a task that answers to no charter (`task
// run`), and the one for a charter-spawned task is AdoptChain, below.
func TestAdoptDropsHooks(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask("adopted", sup, immediate{}, noExec{})
	mirror.Reflect(store, "adopted", p, mirror.Meta{Subject: prRef("1").String(), Runner: "local"})
	eng.Adopt(prRef("1").String(), "adopted", p)
	go p.Run(ctx)

	// wait for the task to reach terminal; the subject must be untouched (no hook ran).
	waitField(t, store, mirror.Ref("adopted"), "status", "done")
	time.Sleep(50 * time.Millisecond) // let any (erroneous) hook application land
	if e, _, _ := store.Get(prRef("1")); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("adopted task's completion mutated the subject: %+v", e.Payload)
	}
}

// Resume rehydrates a task's hooks from its CHAIN. This is what a chain being a lookup key
// into the charter buys: before it, a completion after a restart silently ended a chain,
// because Adopt tracked the task with no hooks at all.
func TestAdoptChainRehydratesHooks(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("never")}, // installed for its shape, not to fire
		Action: Action{
			Spawn:  &launch.Spec{Prompt: "review"},
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	// A task that outlived a restart: mirrored and adopted BEFORE it runs, because the
	// point is that adoption re-attaches the hooks a completion will look for. (Built here
	// rather than with startHookTask, which starts the task itself — adopting a task
	// that may already have finished would test nothing.)
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask("review@demo.pr.1", sup, immediate{}, noExec{})
	mirror.Reflect(store, p.ID, p, mirror.Meta{Subject: prRef("1").String(), Runner: "local", Chain: "review"})
	eng.AdoptChain(prRef("1").String(), p.ID, "review", p)
	go p.Run(context.Background())

	waitField(t, store, prRef("1"), "stage", "reviewed")
}

// Rehydration fails softly, and the failures are ordinary: a trigger removed while its
// task ran, or a chain the charter no longer has. The task is still adopted — steerable
// and reaped — there is simply no action left to run.
func TestAdoptChainSurvivesAMissingCharter(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask("gone@demo.pr.1", sup, immediate{}, noExec{})
	eng.AdoptChain(prRef("1").String(), p.ID, "gone.onDone", p)
	if got := eng.SubjectOf("gone@demo.pr.1"); got != prRef("1").String() {
		t.Errorf("task not adopted when its chain did not resolve: subject = %q", got)
	}
}

// A hook is scoped to its own spawn, which is the correlation the `flow` redirect used to
// stand in for — and it is exact where that was a guess: the engine spawned the task, so it
// knows which subject the task is about without reading anything out of a payload.
//
// TestOnHookPerTask is the other half of this (two tasks, one subject, each hook fires
// its own). Here it is two subjects: a completion must advance the one its task was about
// and leave the other alone. A `when` over task:instance cannot say that any more — it
// sweeps — so this is where "this task, this subject" now lives.
func TestOnHookDoesNotCrossSubjects(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		return startHookTask(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "open"}.Payload())

	eng.spawnHooked("pa", launch.Spec{Subject: prRef("1").String(), Prompt: "a"},
		&Action{OnDone: &Action{Patch: entity.Obj{"from": "a"}.Payload()}}, "pa")
	eng.spawnHooked("pb", launch.Spec{Subject: prRef("2").String(), Prompt: "b"},
		&Action{OnDone: &Action{Patch: entity.Obj{"from": "b"}.Payload()}}, "pb")

	waitField(t, store, prRef("1"), "from", "a")
	waitField(t, store, prRef("2"), "from", "b")
	// Decisive: each landed only its own. A hook that fired about the wrong subject would
	// have overwritten one of these with the other's value.
	if got := entityField(t, store, prRef("1"), "from"); got != "a" {
		t.Fatalf("pr:1 from = %q, want a — a hook crossed subjects", got)
	}
	if got := entityField(t, store, prRef("2"), "from"); got != "b" {
		t.Fatalf("pr:2 from = %q, want b — a hook crossed subjects", got)
	}
}

func entityField(t *testing.T, store entity.Store, ref entity.Ref, key string) string {
	t.Helper()
	e, _, _ := store.Get(ref)
	return entity.Str(e.Payload, key)
}

// A COMPLETION HOOK READS AT THE COMMIT THAT CAUSED ITS FIRING.
//
// It used to read at no commit at all: applyHook built an occasion with none, so
// `$subject.value`, `$subject.commit`, `!get-path` and `!list-paths` were unresolvable in
// every hook, and a rule that wanted to derive a release note from what shipped could not be
// written down (git-issue hjxd56jy). Worse, it failed HALFWAY — a hook's `patch` lands before
// its `derive`, so the subject latched to "finished" over a derivation that never happened.
//
// The commit offered is the firing's, taken from the task's own record. This test is the
// decisive half of what that means: the action advances `stage` to `reviewing` BEFORE the task
// starts, so the store says `reviewing` by the time the hook runs — and the hook still reads
// `open`, the value at the commit the rule was evaluated against. One rule, one moment.
func TestHookReadsTheSubjectAtTheCausingCommit(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		return startHookTask(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: review
  condition:
    subject: {path: [demo, pr, *], value: {stage: open}}
  action:
    patch: {stage: reviewing}
    spawn: {prompt: "review $subject"}
    onDone:
      patch:
        stage: reviewed
        was:  $subject.value.stage
        area: $subject.value.area
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open", "area": "auth"}.Payload())
	waitField(t, store, prRef("1"), "stage", "reviewed")

	ent, _, _ := store.Get(prRef("1"))
	if got := entity.Str(ent.Payload, "was"); got != "open" {
		t.Errorf("the hook read stage = %q; want \"open\" — the value at the causing commit, "+
			"not whatever the action had already written over it", got)
	}
	if got := entity.Str(ent.Payload, "area"); got != "auth" {
		t.Errorf("the hook read area = %q; want \"auth\" — a field the firing never touched", got)
	}
}

// The same commit reaches a SEQUENCE's carrier hook, which arrives by another road: a finished
// element resumes its sequence (resumeSeq) rather than firing a hook, and the carrier's own
// `onDone` runs at the end of that path. It was the shape that produced the original report —
// two rollout steps and a release note derived from what shipped.
func TestSeqCarrierHookReadsTheCausingCommit(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		return startHookTask(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: rollout
  condition:
    subject: {path: [demo, pr, *], value: {stage: open}}
  action:
    patch: {stage: deploying}
    seq:
    - spawn: {prompt: "canary $subject"}
    - spawn: {prompt: "rollout $subject"}
    onDone:
      patch:
        stage:   deployed
        shipped: $subject.value.version
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "open", "version": "1.4.2"}.Payload())
	waitField(t, store, prRef("2"), "stage", "deployed")

	ent, _, _ := store.Get(prRef("2"))
	if got := entity.Str(ent.Payload, "shipped"); got != "1.4.2" {
		t.Errorf("the carrier's onDone read version = %q; want \"1.4.2\" — a sequence's hook "+
			"reads at the same commit its rule fired at", got)
	}
}
