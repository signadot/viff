package trigger

import (
	"context"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// A firing reads the places its rule names and NOT the whole verse — the difference between a
// read whose cost is flat and one that grows with everything anybody else has written.
func TestAFiringReadsOnlyThePlacesItsRuleNames(t *testing.T) {
	store := &countingStore{Store: entitytest.Store(t)}
	eng := New(store, task.AutoResolver{})

	head := entity.RefAt("git", "verse", "head")
	cursor := entity.RefAt("drive", "cursor", "at")
	entitytest.Set(t, store, head, entity.Obj{"sha": "bbb222"}.Payload())
	entitytest.Set(t, store, cursor, entity.Obj{"stage": "idle"}.Payload())
	// A slice nobody's rule names, to prove the snapshot is not merely small.
	entitytest.Set(t, store, entity.RefAt("noise", "thing", "1"), entity.Obj{"n": 1}.Payload())

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: claim
  condition:
    subject: {path: "drive.cursor.at", value: {stage: idle}}
  action:
    patch:
      stage: claimed
      covering: !get-path {at: "git.verse.head", path: sha}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, cursor, entity.Obj{"stage": "idle", "poke": 1}.Payload())
	waitField(t, store, cursor, "covering", "bbb222") // the action's read was answered

	if whole := store.wholeVerse(); whole != 0 {
		t.Fatalf("%d of the firing's snapshots read the whole verse — a firing pays the log "+
			"replay for every entity its rule cannot see, on a store that only grows", whole)
	}
	// And it held what the ACTION reads, which is the half `reads()` does not cover: the
	// condition never names `git.verse.head`, only the patch does.
	if !store.narrowedTo("git", "verse") {
		t.Fatal("no snapshot was narrowed to the place the action reads — the read would have " +
			"been answered with nothing")
	}
	if !store.narrowedTo("drive", "cursor") {
		t.Fatal("no snapshot held the subject's own place")
	}
}

// ONE SNAPSHOT PER FIRING, however many references read it. `subject.value` used to take its
// own, so a fan-out of twenty members was twenty reads of one world.
func TestAFiringTakesOneSnapshotForEveryReferenceInIt(t *testing.T) {
	store := &countingStore{Store: entitytest.Store(t)}
	eng := New(store, task.AutoResolver{})

	subj := entity.RefAt("drive", "item", "a")
	entitytest.Set(t, store, subj, entity.Obj{"stage": "open", "note": "hello"}.Payload())
	entitytest.Set(t, store, entity.RefAt("git", "verse", "head"), entity.Obj{"sha": "bbb222"}.Payload())

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: two-reads
  condition:
    subject: {path: "drive.item.*", value: {stage: open}}
  action:
    patch:
      stage: seen
      note: .[subject.value.note]
      covering: !get-path {at: "git.verse.head", path: sha}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, subj, entity.Obj{"stage": "open", "note": "hello", "poke": 1}.Payload())
	waitField(t, store, subj, "stage", "seen")

	// The evaluate reads (at C, and at C-1 for an edge) are the engine's own and are already
	// narrowed; what this counts is that the FIRING did not add one per reference.
	if got := store.asOf(); got > 3 {
		t.Fatalf("%d snapshots for one firing with two references — they are supposed to share "+
			"one, and each one is a log replay", got)
	}
}

// A read whose address cannot be worked out at install narrows to NOTHING, and a snapshot
// narrowed to nothing would answer "nothing" for a read that should have found something. So the
// whole verse is what it falls back to: the expensive answer, in the one case where the cheap one
// would be wrong.
func TestARuleThatCannotNameWhatItReadsReadsTheWholeVerse(t *testing.T) {
	trig := mustParseCharter(t, `
- name: templated
  condition:
    subject: {path: "drive.item.*", value: {stage: open}}
  action:
    patch:
      seen: !get-path {at: [.[subject.value.where]], path: sha}
`)
	if within := firingWithin(trig); within != nil {
		t.Fatalf("firingWithin = %v, want nil — the read names no place, so nothing may be "+
			"narrowed away", within)
	}

	// And the ordinary rule, whose reads are all placeable, names them.
	placed := mustParseCharter(t, `
- name: placed
  condition:
    subject: {path: "drive.cursor.at"}
  action:
    patch:
      covering: !get-path {at: "git.verse.head", path: sha}
`)
	within := firingWithin(placed)
	if len(within) == 0 {
		t.Fatal("firingWithin named nothing for a rule whose reads are all literal")
	}
	for _, p := range within {
		if len(p) == 0 {
			t.Fatalf("firingWithin = %v, which holds an empty place — that narrows to nothing", within)
		}
	}
}
