package trigger

import (
	"errors"
	"testing"

	"github.com/signadot/verse/internal/trigger/charter"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// THE REFUSAL AT THE WRITE, which is the one every charter-authored write passes: a person's
// rule writes nothing under `verse`, whatever the address it arrived at, and the system's own
// does. Alice is given the right to create where her rule would write, so that what refuses it
// is this and not the perms check beneath.
func TestAPersonsRuleIsRefusedUnderVerseAtTheWrite(t *testing.T) {
	const alice = "user:alice@example.com"
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	for _, row := range []entity.Ref{entity.VerseRef(charter.System, charter.RuleKind), entity.VerseRef("schedule")} {
		if err := entity.SetGrants(store, row, []entity.Grant{{
			At: row.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {entity.Everyone}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	eng := New(store, task.NewRegistry())
	if err := eng.InstallTriggerAs(t.Context(), alice, reviewRule("hers", entity.Obj{"stage": "reviewed"})); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddTrigger(reviewRule("the-systems", entity.Obj{"stage": "seen"})); err != nil {
		t.Fatal(err)
	}
	squat := entity.VerseRef("schedule", "date")
	if ok, err := entity.MayCreate(store, entity.Via(eng.RuleRef("hers")), squat); err != nil || !ok {
		t.Fatalf("the fixture is wrong: alice's rule may not create %s anyway (%v %v)", squat, ok, err)
	}

	err := eng.patchEntity("hers", 0, squat, squat, entity.Obj{"date": "never"}.Payload())
	if !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("a person's rule writing %s: %v, want refused", squat, err)
	}
	if _, stands, _ := store.Get(squat); stands {
		t.Fatalf("%s stands", squat)
	}
	// A hook's write is the rule's write: the chain is the rule's name and what descends from it.
	if err := eng.patchEntity("hers.onDone", 0, squat, squat, entity.Obj{"date": "never"}.Payload()); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("a person's rule's hook writing %s: %v, want refused", squat, err)
	}
	world := entity.RefAt("demo", "pr", "1")
	if err := eng.patchEntity("hers", 0, world, world, entity.Obj{"stage": "reviewed"}.Payload()); err != nil {
		t.Fatalf("a person's rule writing the world's half: %v", err)
	}
	if err := eng.patchEntity("the-systems", 0, squat, squat, entity.Obj{"date": "today"}.Payload()); err != nil {
		t.Fatalf("the system's rule writing %s: %v", squat, err)
	}
}
