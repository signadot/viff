package trigger

import (
	"context"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/task"
)

// A COMPLETION HOOK ABOUT A DEEP SUBJECT LANDS — which is the whole of what a hook is for, and
// what was quietly not happening.
//
// The engine recovers a finished task's subject from the text it recorded, and a subject four
// segments deep whose id is itself an address (`verse-dev.drift.head.head-moved@git.head.verse`
// — every derived entity's name has this shape) came back through ParseRef as a DIFFERENT ref,
// cut short with the rest read as one name. No error. The hook then
// patched that ref, the package's confinement saw an address outside itself, and refused.
//
// On staging that is exactly what it looked like: a comment review ran for four minutes, fixed
// what it found, and could not write `reviewed: done` on the entity it was about — so the rule
// stayed selected and would review again. The merge train landed a batch and could not write
// `decided: landed`. Both engines were behaving; the address was lost in the round trip.
//
// The confinement is set here BECAUSE that is what turns the lost subject into a refusal rather
// than a silent write to the wrong place — which is the more frightening half of the same bug.
func TestACompletionHookPatchesADeepSubject(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.Confine(SubtreeAt("verse-dev", "drift"))
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		return startHookTask(store, id, spec)
	})
	if err := eng.AddStratum(Stratum{Name: "tip", Level: 10,
		Match: []Match{mustPathMatch(t, `{path: "verse-dev.drift.head"}`)}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: review
  stratum: tip
  condition:
    subject:
      path: "verse-dev.drift.head.*"
      value: {reviewed: gathered}
  action:
    patch: {reviewed: running}
    spawn: {agent: {name: verse}, prompt: "review $[subject.ref]"}
    onDone:
      patch: {reviewed: done}
`)); err != nil {
		t.Fatalf("install: %v", err)
	}

	// The id is one an engine writes: `<rule>@<subject>`, so it holds the colons that made the
	// round trip lossy. A hand-picked short id would pass with the bug still in place.
	ref := entity.RefAt("verse-dev", "drift", "head", "head-moved@git.head.verse")
	entitytest.Set(t, store, ref, entity.Obj{"reviewed": "gathered"}.Payload())
	waitField(t, store, ref, "reviewed", "done")
}
