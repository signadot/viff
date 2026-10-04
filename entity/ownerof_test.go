package entity_test

import (
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// WHOSE A NODE IS, read at a firing: its own record's say, else — if it stands — the nearest owner
// above, else nobody yet. An owner writing inside their own place leaves no record, so what they
// make is theirs by inheritance; somebody ELSE creating there under a grant leaves a record naming
// them at the topmost node they made — which is how a place one person owns holds what another
// asked in it.
func TestOwnerOfReadsTheRecordThenTheNearestOwnerAboveAndNobodyForWhatDoesNotStand(t *testing.T) {
	inner := entitytest.OpenStore(t)
	store := entity.As(inner, entity.SystemDefault)
	x, carol := "user:x@example.com", "user:carol@example.com"
	team := entity.RefAt("team")
	if err := entity.Claim(store, team, x); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, team, []entity.Grant{
		{At: team.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {carol}}},
	}); err != nil {
		t.Fatal(err)
	}
	// x makes team.a.b inside x's own place: no record, x's by inheritance.
	xs := entity.RefAt("team", "a", "b")
	if _, _, err := entity.As(store, x).Write(entity.Write{At: xs.At(), Value: entity.Obj{"v": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	// carol makes team.c.d under the grant: a record naming her at team.c.
	carols := entity.RefAt("team", "c", "d")
	if _, _, err := entity.As(store, carol).Write(entity.Write{At: carols.At(), Value: entity.Obj{"v": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	// Something made beneath the store's view, so no record anywhere above it names an owner.
	raw := entity.RefAt("raw", "n")
	if _, _, err := inner.Write(entity.Write{At: raw.At(), Value: entity.Obj{"v": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		ref  entity.Ref
		want string
	}{
		{"its own record", team, x},
		{"made inside its owner's place: the owner above", entity.RefAt("team", "a"), x},
		{"deeper inside it: still the owner above", xs, x},
		{"made by somebody else under a grant: the record they left", entity.RefAt("team", "c"), carol},
		{"beneath what they made: theirs, not the place's owner's", carols, carol},
		{"nothing stands and no record names anybody: nobody yet", entity.RefAt("team", "nothing"), ""},
		{"standing, and no record anywhere above: the system, which owns the root", raw, entity.SystemDefault},
	} {
		got, err := entity.OwnerOf(store, c.ref)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: OwnerOf(%s) = %q, want %q", c.name, c.ref, got, c.want)
		}
	}
}

// OwnerOf answers only over an attributed store: a bare store keeps no records, and an answer
// from one would be a guess.
func TestOwnerOfRefusesAStoreThatKeepsNoRecords(t *testing.T) {
	if _, err := entity.OwnerOf(entitytest.OpenStore(t), entity.RefAt("a")); err == nil {
		t.Fatal("OwnerOf answered over an unattributed store")
	}
}

// WHOSE A NODE IS, read the moment it is made. A creation's record lands after the write, so a
// firing hearing the write can read the node before it: the place's owner's, where it is the
// creator's. Settled, the node's own record is waited for; a node its place's owner made, which
// leaves none, is theirs once the wait is over.
func TestSettledOwnerOfWaitsForTheRecordACreationLeaves(t *testing.T) {
	inner := entitytest.OpenStore(t)
	store := entity.As(inner, entity.SystemDefault)
	x, carol := "user:x@example.com", "user:carol@example.com"
	team := entity.RefAt("team")
	if err := entity.Claim(store, team, x); err != nil {
		t.Fatal(err)
	}
	// Carol's write, as a firing can hear it: standing, its record not yet written.
	made := entity.RefAt("team", "made")
	if _, _, err := inner.Write(entity.Write{At: made.At(), Value: entity.Obj{"v": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if got, _ := entity.OwnerOf(store, made); got != x {
		t.Fatalf("the fixture is wrong: before its record the node reads as %q's", got)
	}
	go func() {
		time.Sleep(60 * time.Millisecond)
		// The record the creation leaves, as the attributed write leaves it: after.
		_, _, _ = inner.Write(entity.Write{At: entity.PermsRef(made).At(),
			Value: entity.Obj{entity.OwnerField: carol}.Payload()})
	}()
	if got, err := entity.SettledOwnerOf(store, made, 2*time.Second); err != nil || got != carol {
		t.Fatalf("settled: %q (%v), want the creator's", got, err)
	}
	// x's own node leaves no record: x's, once the wait is over.
	own := entity.RefAt("team", "own")
	if _, _, err := entity.As(store, x).Write(entity.Write{At: own.At(), Value: entity.Obj{"v": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if got, err := entity.SettledOwnerOf(store, own, 50*time.Millisecond); err != nil || got != x {
		t.Fatalf("an owner's own node: %q (%v), want theirs", got, err)
	}
}
