package trigger

import (
	"context"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

// A firing reads the places its rule names and NOT the whole viff — the difference between a
// read whose cost is flat and one that grows with everything anybody else has written.
func TestAFiringReadsOnlyThePlacesItsRuleNames(t *testing.T) {
	store := &countingStore{Store: entitytest.Store(t)}
	eng := New(store, task.AutoResolver{})

	head := entity.RefAt("git", "viff", "head")
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
      covering: !get-path {at: "git.viff.head", path: sha}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, cursor, entity.Obj{"stage": "idle", "poke": 1}.Payload())
	waitField(t, store, cursor, "covering", "bbb222") // the action's read was answered

	if whole := store.wholeViff(); whole != 0 {
		t.Fatalf("%d of the firing's snapshots read the whole viff — a firing pays the log "+
			"replay for every entity its rule cannot see, on a store that only grows", whole)
	}
	// And it held what the ACTION reads, which is the half `reads()` does not cover: the
	// condition never names `git.viff.head`, only the patch does.
	if !store.narrowedTo("git", "viff") {
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
	entitytest.Set(t, store, entity.RefAt("git", "viff", "head"), entity.Obj{"sha": "bbb222"}.Payload())

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: two-reads
  condition:
    subject: {path: "drive.item.*", value: {stage: open}}
  action:
    patch:
      stage: seen
      note: .[subject.value.note]
      covering: !get-path {at: "git.viff.head", path: sha}
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
// whole viff is what it falls back to: the expensive answer, in the one case where the cheap one
// would be wrong.
func TestARuleThatCannotNameWhatItReadsReadsTheWholeViff(t *testing.T) {
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
      covering: !get-path {at: "git.viff.head", path: sha}
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

// A HOOK PAID BEFORE THE ENGINE OPENS READS WHAT ITS RULE NAMES, as one paid after does. What
// the last viff left is settled at start, ahead of anything armed: a claim whose task's record
// says done, and its onDone reads a place the rule names.
func TestAHookPaidBeforeTheEngineOpensReadsWhatItsRuleNames(t *testing.T) {
	store := &countingStore{Store: entitytest.Store(t)}
	eng := New(store, task.AutoResolver{})
	sp := &spawned{by: map[string]*heldHandle{}}
	eng.SetSpawner(sp.spawn)
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: review
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    spawn: {agent: {name: viff}, prompt: review}
    onDone:
      patch: {stage: reviewed, covering: !get-path {at: "git.viff.head", path: sha}}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, entity.RefAt("git", "viff", "head"), entity.Obj{"sha": "bbb222"}.Payload())
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "reviewing"}.Payload())
	_, id := claimAt(t, eng, store, "review", true, 0)
	entitytest.Set(t, store, mirror.Ref(id), entity.Obj{"status": string(task.Done)}.Payload())
	before := store.wholeViff()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	<-eng.Opened()
	if got := subjectNow(t, store); got["covering"] != "bbb222" {
		t.Fatalf("subject = %v, want the hook's read written", got)
	}
	if whole := store.wholeViff() - before; whole != 0 {
		t.Fatalf("%d of the snapshots taken at start read the whole viff", whole)
	}
}

// A HOOK READS WHAT THE RULE THAT SPAWNED ITS TASK READS. A rule replaced while its task runs by
// one that reads other places does not narrow the old rule's hook away from its own reads:
// the task's onDone reads git.viff.head whatever the rule now holding the name reads.
func TestAHookReadsWhatTheRuleThatSpawnedItReads(t *testing.T) {
	for name, pending := range map[string]bool{"the replacement armed": false, "the replacement pending": true} {
		t.Run(name, func(t *testing.T) {
			if got := reviewReplacedWhileRunning(t, pending); got["covering"] != "bbb222" || got["stage"] != "reviewed" {
				t.Fatalf("subject = %v, want the hook's patch: reviewed, covering bbb222", got)
			}
		})
	}
}

// reviewReplacedWhileRunning runs review's task to its end with the rule replaced meanwhile, and
// answers the subject as the task's onDone leaves it.
func reviewReplacedWhileRunning(t *testing.T, pending bool) map[string]any {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	sp := &spawned{by: map[string]*heldHandle{}, store: store}
	eng.SetSpawner(sp.spawn)
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: review
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    spawn: {agent: {name: viff}, prompt: review}
    onDone:
      patch: {stage: reviewed, covering: !get-path {at: "git.viff.head", path: sha}}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, entity.RefAt("git", "viff", "head"), entity.Obj{"sha": "bbb222"}.Payload())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	<-eng.Opened()
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	id := spawnID(eng.idOwner(), chainID("review"), prRef("1"))
	h := sp.wait(t, id)

	// The rule is replaced by one of another shape, which reads nothing under git: armed, or
	// held pending (a replacement no runner takes yet).
	repl := mustParseCharter(t, `
- name: review
  condition:
    subject: {path: "demo.pr.*", value: {stage: other}}
  action:
    patch: {stage: other2}
`)
	eng.trigMu.Lock()
	eng.unplaceLocked("review")
	if pending {
		eng.hold(repl, cause{causeRunner, "waits for a runner"})
	} else {
		eng.triggers = append(eng.triggers, repl)
	}
	eng.trigMu.Unlock()

	h.finish()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := subjectNow(t, store); got["stage"] == "reviewed" {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return subjectNow(t, store)
}

// A SEQUENCE GOES ON BY THE RULE AS IT STANDS, and reads by it. An edit to the carrier's onDone
// that leaves the elements alone, made while an element's task runs, is the run's onDone from
// then on, and it reads the places the edited rule names: not only the firing's.
func TestASequenceEditedWhileItsElementRunsReadsByTheRuleAsItStands(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	sp := &spawned{by: map[string]*heldHandle{}, store: store}
	eng.SetSpawner(sp.spawn)
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: review
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    seq:
    - spawn: {agent: {name: viff}, prompt: review}
    onDone:
      patch: {stage: finished}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, entity.RefAt("git", "viff", "head"), entity.Obj{"sha": "bbb222"}.Payload())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	<-eng.Opened()
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	id := spawnID(eng.idOwner(), chainID(seqChain("review", 0)), prRef("1"))
	h := sp.wait(t, id)

	if err := eng.InstallTrigger(mustParseCharter(t, `
- name: review
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    seq:
    - spawn: {agent: {name: viff}, prompt: review}
    onDone:
      patch: {stage: finished, covering: !get-path {at: "git.viff.head", path: sha}}
`)); err != nil {
		t.Fatalf("InstallTrigger: %v", err)
	}
	h.finish()
	deadline := time.Now().Add(3 * time.Second)
	var got map[string]any
	for time.Now().Before(deadline) {
		got = subjectNow(t, store)
		if got["stage"] == "finished" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got["covering"] != "bbb222" || got["stage"] != "finished" {
		t.Fatalf("subject = %v, want the new onDone's patch: finished, covering bbb222", got)
	}
}
