package trigger

import (
	"context"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// A LAUNCH THE ENGINE REFUSES ENDS IN AN onFailed THAT READS AT THE FIRING, as any task's hook
// does: `was: $subject.value.stage` is the subject as the condition selected it.
//
// It was not. A refused task fails before anything has mirrored its record, so the hook's
// occasion took its commit from a record with no `occurrence`, read at revision 0, and failed
// saying the task was one nothing in the charter started. The subject stayed latched at
// `developing` — the outcome the refusal-as-a-failed-task exists to avoid — and the task id
// held only the hook's failure.
func TestARefusedLaunchsOnFailedReadsTheSubjectAtTheFiring(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	// Mirrors nothing for a refused launch, so the hook can only read at the firing from what
	// the engine itself holds.
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		return startHookTask(store, id, spec)
	})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: develop
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: developing}
    spawn:
      agent: {name: verse}
      autonomy: [{then: allow}]
      prompt: "deep=[$subject.value.never]"
    onFailed:
      patch: {stage: failed, why: $err, was: $subject.value.stage}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	waitField(t, store, prRef("1"), "stage", "failed")
	e, _, _ := store.Get(prRef("1"))
	if was := entity.Str(e.Payload, "was"); was != "open" {
		t.Errorf("was = %q, want open: the subject at the commit the rule fired at", was)
	}
	if why := entity.Str(e.Payload, "why"); !strings.Contains(why, "$subject.value.never") {
		t.Errorf("why = %q, want the refusal naming the reference", why)
	}
	id := spawnID(eng.idOwner(), chainID("develop"), prRef("1"))
	if rec, ok, _ := store.Get(mirror.Ref(id)); ok && entity.Get(rec.Payload, "hook") != nil {
		t.Errorf("the task's record holds a hook failure: %v", entity.ToObj(rec.Payload))
	}
}
