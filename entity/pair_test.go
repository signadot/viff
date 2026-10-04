package entity_test

import (
	"errors"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

const (
	asker     = "user:daniel@example.com"
	installer = "user:installer@example.com"
)

// pairPlaces is three places: one only the asker holds, one only the installer holds, and one
// the asker owns and holds out `read-write` and `control` to the installer on.
func pairPlaces(t *testing.T) entity.Store {
	t.Helper()
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	for place, owner := range map[string]string{"daniels": asker, "installers": installer, "shared": asker} {
		if err := entity.Claim(store, entity.RefAt(place), owner); err != nil {
			t.Fatal(err)
		}
	}
	shared := entity.RefAt("shared")
	if err := entity.SetGrants(store, shared, []entity.Grant{{At: shared.Segs(),
		To: map[entity.Level][]string{entity.ReadWrite: {installer}, entity.Control: {installer}}}}); err != nil {
		t.Fatal(err)
	}
	return store
}

// A PAIR MAY DO WHAT ITS ASKER MAY, and the rule's installer neither adds nor takes away
// (9ce3bva0): a consequence is its raiser's. Where only the asker holds the place, the pair is
// admitted; where only the installer holds it, a record's author would be borrowing the
// installer's reach, and is refused.
func TestAPairIsAdmittedAsItsAskerAlone(t *testing.T) {
	store := pairPlaces(t)
	pair := entity.Pair(asker, installer)
	for _, c := range []struct {
		place string
		want  bool
	}{
		{"daniels", true},     // the asker's, whatever the installer holds there
		{"installers", false}, // the asker holds nothing there
		{"shared", true},      // the asker's
	} {
		err := entity.Check(store, pair, entity.ReadWrite, entity.RefAt(c.place, "x"), "writing it")
		if got := err == nil; got != c.want {
			t.Errorf("%s: pair admitted = %v, want %v (%v)", c.place, got, c.want, err)
		}
		if err != nil && !errors.Is(err, entity.ErrForbidden) {
			t.Errorf("%s: refused with %v, want a refusal", c.place, err)
		}
	}
	// Control the same way: the asker's.
	if ok, err := entity.Holds(store, pair, entity.Control, entity.RefAt("daniels")); err != nil || !ok {
		t.Errorf("control at daniels, the asker's: %v %v", ok, err)
	}
	if ok, err := entity.Holds(store, pair, entity.Control, entity.RefAt("installers")); err != nil || ok {
		t.Errorf("control at installers, which the asker does not hold: %v %v", ok, err)
	}
}

// A PAIR ACTS AS ITS ASKER: what it creates is the asker's, and its writes carry the pair as
// their author, so the record says who asked and through what.
func TestWhatAPairCreatesIsTheAskers(t *testing.T) {
	store := pairPlaces(t)
	pair := entity.Pair(asker, installer)
	made := entity.RefAt("shared", "task", "1")
	if _, _, err := entity.As(store, pair).Write(entity.Write{At: made.At(), Value: entity.Obj{"v": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if owner, err := entity.OwnerOf(store, made); err != nil || owner != asker {
		t.Fatalf("what the pair made is %q's (%v), want the asker's", owner, err)
	}
	// And an id the asker already holds is the pair's own to write under, not somebody else's.
	if owner, other, err := entity.OwnedByOther(store, made, pair); err != nil || other {
		t.Fatalf("the asker's own id reads as %q's to the pair (%v)", owner, err)
	}
}

// THE SYSTEM HOLDS EVERYTHING, so a pair with it is the other party alone — and taken as that
// party whole. Taken as the system, it would skip the checks beneath the node a write names,
// and carry the other party past its own limits into everything the value reaches.
func TestAPairWithTheSystemIsTheOtherPartyAlone(t *testing.T) {
	store := pairPlaces(t)
	for _, c := range []struct {
		name   string
		pair   string
		holder string // the party the pair should reduce to
	}{
		{"the system asking through a rule", entity.Pair(entity.SystemDefault, installer), installer},
		{"a person asking through the system's rule", entity.Pair(asker, entity.SystemCharter), asker},
	} {
		for _, place := range []string{"daniels", "installers", "shared"} {
			ref := entity.RefAt(place, "x")
			want := entity.Check(store, c.holder, entity.ReadWrite, ref, "writing it") == nil
			got := entity.Check(store, c.pair, entity.ReadWrite, ref, "writing it") == nil
			if got != want {
				t.Errorf("%s at %s: admitted = %v, want %v, as %s alone", c.name, place, got, want, c.holder)
			}
		}
	}
	// And a write whose VALUE reaches a place the other party may not write is refused there —
	// the check beneath the named node, which a system actor skips. shared is Daniel's, held out
	// `read-write` to the installer; carol makes shared.c under a grant, and a grant stops at an
	// owner's record, so the installer's does not reach it.
	const carol = "user:carol@example.com"
	shared, carols := entity.RefAt("shared"), entity.RefAt("shared", "c")
	if err := entity.SetGrants(store, shared, []entity.Grant{
		{At: shared.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {installer}, entity.Control: {installer}}},
		{At: shared.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {carol}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := entity.As(store, carol).Write(entity.Write{At: carols.At(), Value: entity.Obj{"v": 0}.Payload()}); err != nil {
		t.Fatal(err)
	}
	into := entity.Write{At: shared.At(), Value: entity.Obj{"c": entity.Obj{"v": 1}}.Payload()}
	if _, _, err := entity.As(store, installer).Write(into); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("the fixture is wrong: the installer alone may write into carol's place (%v)", err)
	}
	if _, _, err := entity.As(store, entity.Pair(entity.SystemDefault, installer)).Write(into); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("a pair of the system and the installer wrote into carol's place = %v, want refused", err)
	}
}

// NO REQUEST PRESENTS A PAIR. Only the engine makes one, for a rule's task started for the
// person whose record fired it; as a name a caller sends, it parses as nothing.
func TestAPairIsNoPrincipalACallerCanName(t *testing.T) {
	pair := entity.Pair(asker, installer)
	if _, err := entity.ParsePrincipal(pair); err == nil {
		t.Fatalf("%s parsed as a principal", pair)
	}
	a, b, ok := entity.PairOf(pair)
	if !ok || a != asker || b != installer {
		t.Fatalf("PairOf(%s) = %q %q %v", pair, a, b, ok)
	}
	for _, notAPair := range []string{asker, "pair:", "pair:" + asker, "pair: " + installer} {
		if _, _, ok := entity.PairOf(notAPair); ok {
			t.Errorf("PairOf(%q) read a pair", notAPair)
		}
	}
}
