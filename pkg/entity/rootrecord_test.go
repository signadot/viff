package entity_test

import (
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// The root is a node, and its record decides what stands beneath it: the walk asks about every
// prefix of an address down to the empty path, which is where a deployment says who may take a
// place of their own. With no record there the check admits nobody but the system, so these
// tests write the record the way versed's seed does.

// seedRoot writes the root's record, which stands beside the mirror at `verse.perms.root`: the
// address it is about is the empty path, and a key that is no key would put its fields on the
// node every other record stands under.
func seedRoot(t *testing.T, store entity.Store, owner string, grants ...entity.Grant) {
	t.Helper()
	rec := entity.Obj{entity.OwnerField: owner}
	if len(grants) > 0 {
		rec[entity.GrantsField] = entity.GrantsPayload(nil, grants)
	}
	if _, _, err := store.Write(entity.Write{
		At: entity.PermsRef(entity.RefAt()).At(), Value: rec.Payload(), Principal: entity.SystemPerms,
	}); err != nil {
		t.Fatalf("seeding the root's record: %v", err)
	}
}

func grant(level entity.Level, to ...string) entity.Grant {
	return entity.Grant{Below: true, To: map[entity.Level][]string{level: to}}
}

func TestTheRootsRecordDecidesBeneathIt(t *testing.T) {
	inner := entitytest.Store(t)
	system := entity.As(inner, entity.SystemDefault)
	// alice owns the root and holds `read` out to everyone beneath it — the shape of a verse
	// whose top is administered rather than open.
	seedRoot(t, system, "user:alice@example.com", grant(entity.Read, entity.Everyone))

	pr := entity.RefAt("demo", "pr", "1")
	task := entity.VerseRef("task", "instance", "run-1")
	for _, at := range []entity.Ref{pr, task} {
		// Ownership descends all the way down, from the root as from anywhere else.
		if ok, err := entity.Holds(system, "user:alice@example.com", entity.OwnerLevel, at); err != nil || !ok {
			t.Fatalf("the root's owner holds owner at %s: %v %v", at, ok, err)
		}
	}
	// And the grant reaches a world-half node for somebody who owns nothing.
	if ok, err := entity.Holds(system, "user:bob@example.com", entity.Read, pr); err != nil || !ok {
		t.Fatalf("bob reads %s through the root's grant: %v %v", pr, ok, err)
	}
	if ok, _ := entity.Holds(system, "user:bob@example.com", entity.ReadWrite, pr); ok {
		t.Fatalf("bob writes %s, and the root holds out read alone", pr)
	}
}

// A STORE NOBODY SEEDED ADMITS NOBODY. Openness is a deployment's policy — the root record its
// seed writes — and not something the check supplies when it finds no record: a verse whose seed
// failed refuses rather than standing wide open, which is the safe way for that to be wrong.
// The system still holds everything, since it owns the root whichever part of it writes.
func TestWithNoRootRecordNobodyButTheSystemHolds(t *testing.T) {
	inner := entitytest.Store(t)
	system := entity.As(inner, entity.SystemDefault)
	bob := "user:bob@example.com"
	for _, at := range []entity.Ref{
		entity.RefAt("demo", "pr", "1"),
		entity.RefAt(),
		entity.VerseRef("task", "instance", "run-1"),
	} {
		if ok, err := entity.Holds(system, bob, entity.Read, at); err != nil || ok {
			t.Errorf("bob reads %s in an unseeded store: %v %v", at, ok, err)
		}
		if ok, err := entity.Holds(system, entity.SystemDefault, entity.OwnerLevel, at); err != nil || !ok {
			t.Errorf("the system does not own %s: %v %v", at, ok, err)
		}
	}
}

// The loaded mirror carries the root's record, which a listing of the mirror cannot see: it
// stands beside the mirror rather than in it, so the load asks for it apart. A tracked view that
// missed it would answer every check as though the root were unowned.
func TestALoadedMirrorCarriesTheRootsRecord(t *testing.T) {
	inner := entitytest.Store(t)
	system := entity.As(inner, entity.SystemDefault)
	seedRoot(t, system, "user:alice@example.com", grant(entity.Read, entity.Everyone))

	tracked := entity.As(inner, entity.SystemDefault)
	if err := entity.Track(t.Context(), tracked); err != nil {
		t.Fatal(err)
	}
	if !entity.PolicyOf(tracked).Tracked() {
		t.Fatal("not tracked")
	}
	pr := entity.RefAt("demo", "pr", "1")
	if ok, err := entity.Holds(tracked, "user:bob@example.com", entity.Read, pr); err != nil || !ok {
		t.Fatalf("bob reads %s through the root's grant on a tracked view: %v %v", pr, ok, err)
	}
	if ok, _ := entity.MayCreate(tracked, "user:bob@example.com", pr); ok {
		t.Fatalf("bob owns %s on a tracked view: the root is owned, so the open default is gone", pr)
	}
}

// A write to the root's record is read back by the next check, as a write to any other record
// is: the cache is keyed by the node a record is about, and the root's key is the empty path.
func TestWritingTheRootsRecordIsSeenByTheNextCheck(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	pr := entity.RefAt("demo", "pr", "1")
	bob := "user:bob@example.com"
	if ok, err := entity.MayCreate(system, bob, pr); err != nil || !ok {
		t.Fatalf("bob may create in an open verse: %v %v", ok, err)
	}
	// The operator closes the top, and the next check is asked as the record now stands.
	if err := entity.SetGrants(system, entity.RefAt(), nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.MayCreate(system, bob, pr); ok {
		t.Fatal("bob still holds owner after the root was closed: the check read a stale record")
	}
}

// THE ROOT'S DEFAULT IS READ, like any other record's. The walk that finds what a new node holds
// out stopped one short of the root, so `verse perms --root --child-grants '{}'` was accepted, echoed
// back, and did nothing: whatever anybody created was still readable by everyone.
func TestTheRootsDefaultIsWhatATopLevelCreationHoldsOut(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	if err := entity.SetChildGrants(store, entity.RefAt(), entity.Levels{}); err != nil {
		t.Fatal(err)
	}
	bob, carol := "user:bob@example.com", "user:carol@example.com"
	private := entity.RefAt("private", "note")
	if _, _, err := entity.As(store, bob).Write(entity.Write{
		At: private.At(), Value: entity.Obj{"n": 1}.Payload(), Principal: bob,
	}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(store, carol, entity.Read, private); ok {
		t.Error("carol reads what bob created under a root whose default holds out nothing")
	}
	if ok, err := entity.Holds(store, bob, entity.OwnerLevel, private); err != nil || !ok {
		t.Errorf("bob does not own what he created: %v %v", ok, err)
	}
}
