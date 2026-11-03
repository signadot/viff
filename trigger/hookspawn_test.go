package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// IN A COMPLETION HOOK'S SPAWN, `subject.value` IS THE SUBJECT, as it is in the hook's patch:
// the subject at the commit its rule fired at, not the finished task's record. The task's own
// facts are top-level names, as `status`.
func TestAHookSpawnReadsTheSubjectAtTheFiring(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		specs <- spec
		return startHookTask(store, id, spec)
	})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: step
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: developing}
    spawn:
      script: "true"
      autonomy: [{then: allow}]
    onDone:
      spawn:
        script: "echo $ROUND"
        autonomy: [{then: allow}]
        environment:
          vars: {ROUND: $[subject.value.round], STAGE: $[subject.value.stage], STATUS: $[status], WHOLE: $[subject.value]}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open", "round": 0}.Payload())

	var hook launch.Spec
	for hook.Chain != "step.onDone" {
		select {
		case hook = <-specs:
		case <-time.After(5 * time.Second):
			t.Fatal("the hook never spawned: it was refused, or never fired. Bound from the task's record, `subject.value.round` is a path the binding does not hold")
		}
	}
	vars := hook.Environment.Vars
	if vars["ROUND"] != "0" {
		t.Errorf("ROUND = %q, want 0, the subject's round", vars["ROUND"])
	}
	// At the commit the rule fired at, before its own patch moved the subject to `developing`.
	if vars["STAGE"] != "open" {
		t.Errorf("STAGE = %q, want open: the subject as the condition selected it", vars["STAGE"])
	}
	if vars["STATUS"] != "done" {
		t.Errorf("STATUS = %q, want done: the task's own facts are flat, and read from its record", vars["STATUS"])
	}
	if w := vars["WHOLE"]; !strings.Contains(w, "round: 0") || strings.Contains(w, "chain") {
		t.Errorf("WHOLE = %q, want the subject's payload and not the task's record", w)
	}
}

// A HOOK'S SPAWN FOR A TASK WITH NO FIRING COMMIT REFUSES `subject.value` BY NAME, as the hook's
// patch fails by name: an adopted task whose record says no `occurrence` has no revision to read
// the subject at, and the task's own record is not the subject.
func TestAHookSpawnWithNoFiringCommitRefusesSubjectValueByName(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		specs <- spec
		if spec.Refusal != "" {
			return task.FailedHandle(spec.Refusal)
		}
		return startHookTask(store, id, spec)
	})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: review
  condition:
    subject: {path: "demo.pr.*", value: {stage: never}}
  action:
    spawn:
      script: "true"
      autonomy: [{then: allow}]
    onDone:
      patch: {stage: reviewed}
      spawn:
        script: "echo $STAGE"
        autonomy: [{then: allow}]
        environment:
          vars: {STAGE: $[subject.value.stage]}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	// Adopted with no record at all, so nothing says which firing it belongs to.
	sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
	p := task.NewTask("review@demo.pr.1", sup, immediate{}, noExec{})
	eng.AdoptChain(prRef("1").String(), p.ID, "review", p)
	go p.Run(context.Background())

	select {
	case hook := <-specs:
		if hook.Chain != "review.onDone" || !strings.Contains(hook.Refusal, "subject.value: ") {
			t.Fatalf("hook spawn %q refused with %q, want it refused naming subject.value", hook.Chain, hook.Refusal)
		}
		// And saying why: the subject holds `stage`; there is no commit to read it at.
		if strings.Contains(hook.Refusal, `no "value"`) || !strings.Contains(hook.Refusal, "no causing commit") {
			t.Errorf("refusal = %q, want the reason the subject could not be read, not that it has no value", hook.Refusal)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hook never spawned")
	}
}
