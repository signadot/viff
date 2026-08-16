package trigger

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// AN ADDRESS AS DEEP AS THE TREE, which is what a charter installed at `verse-dev.drift` needs:
// its entities are `verse-dev.drift.head.<id>` and `verse-dev.drift.finding.<sym>`, four
// segments, and the three dimensions have nowhere to put the fourth.
//
// The `path:` spelling carries it whole. `{system, kind, id}` stays the reading of the first
// three, which is every address written before this.
func TestAMatchMayNameAPathOfAnyDepth(t *testing.T) {
	m, err := ParseSteps([]string{"verse-dev", "drift", "head", "abc"})
	if err != nil {
		t.Fatalf("four steps: %v", err)
	}
	if !m.Deep() {
		t.Fatal("four segments did not become a path")
	}
	if got := m.Segs(); len(got) != 4 || got[3] != "abc" {
		t.Fatalf("Segs = %v", got)
	}
	if !m.MatchRef(entity.RefAt("verse-dev", "drift", "head", "abc")) {
		t.Error("the address it names does not match")
	}
	for _, other := range []entity.Ref{
		entity.RefAt("verse-dev", "drift", "head", "xyz"),    // a different leaf
		entity.RefAt("verse-dev", "drift", "finding", "abc"), // a sibling kind
		entity.RefAt("verse-dev", "drift"),                   // shallower
	} {
		if m.MatchRef(other) {
			t.Errorf("%s matched a path that does not name it", other)
		}
	}

	// A wildcard is a POSITION, which is what a path needs and dimensions cannot say: an unset
	// `kind` means "and nothing below", where `*` means "any one segment, then the rest".
	star, err := ParseSteps([]string{"verse-dev", "drift", "*", "abc"})
	if err != nil {
		t.Fatalf("a wildcard step: %v", err)
	}
	for _, ref := range []entity.Ref{
		entity.RefAt("verse-dev", "drift", "head", "abc"),
		entity.RefAt("verse-dev", "drift", "finding", "abc"),
	} {
		if !star.MatchRef(ref) {
			t.Errorf("%s did not match the wildcard position", ref)
		}
	}
	if star.MatchRef(entity.RefAt("verse-dev", "drift", "head", "xyz")) {
		t.Error("the wildcard leaked past its own position")
	}

	// And the watch it opens is the path up to that wildcard — never the leaf, which would see
	// an entity's own changes and not its appearance.
	if got := subtreeOf(star).Path(); len(got) != 2 || got[0] != "verse-dev" || got[1] != "drift" {
		t.Errorf("the watch subtree is %v, want the path up to the wildcard", got)
	}
}

// The three-dimension spelling still reads as it always did, which is what every charter
// written so far holds.
func TestTheThreeDimensionsAreStillTheFirstThreeSegments(t *testing.T) {
	m, err := ParseSteps([]string{"demo", "pr", "1"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Deep() {
		t.Error("three segments became a path; they are dimensions")
	}
	if got := m.Segs(); len(got) != 3 || got[0] != "demo" || got[2] != "1" {
		t.Errorf("Segs = %v", got)
	}
	if !m.MatchRef(entity.RefAt("demo", "pr", "1")) || m.MatchRef(entity.RefAt("demo", "pr", "2")) {
		t.Error("the dimension reading changed")
	}
	// A system alone still means everything under it.
	sys, _ := ParseSteps([]string{"demo"})
	if !sys.MatchRef(entity.RefAt("demo", "pr", "1")) {
		t.Error("a system alone stopped covering what is under it")
	}
}

// THE WHOLE LOOP AT FOUR SEGMENTS, which is what a charter installed at `verse-dev.drift`
// needs: entities live at `verse-dev.drift.head.<id>`, a rule selects them by path, and the
// store answers at the depth the pattern asks for rather than at the three it always had.
func TestARuleSelectsEntitiesFourSegmentsDeep(t *testing.T) {
	store := entitytest.Store(t)
	deep := entity.RefAt("verse-dev", "drift", "head", "abc")
	entitytest.Set(t, store, deep, entity.Obj{"reviewed": "pending"}.Payload())
	entitytest.Set(t, store, entity.RefAt("verse-dev", "drift", "head", "xyz"),
		entity.Obj{"reviewed": "done"}.Payload())
	entitytest.Set(t, store, entity.RefAt("verse-dev", "drift", "finding", "abc"),
		entity.Obj{"status": "open"}.Payload())

	// Read back: a four-segment address is an ordinary read.
	if e, ok, err := store.Get(deep); err != nil || !ok || entity.Str(e.Payload, "reviewed") != "pending" {
		t.Fatalf("a four-segment entity did not read back: %v %v %v", e, ok, err)
	}

	// Select by path, at the pattern's own depth.
	m, err := ParseSteps([]string{"verse-dev", "drift", "head", "*"})
	if err != nil {
		t.Fatal(err)
	}
	m[mKeyValue] = ir.FromMap(map[string]*ir.Node{"reviewed": ir.FromString("pending")})
	pat, err := m.pattern()
	if err != nil {
		t.Fatal(err)
	}
	got := pat.Select(store)
	if len(got) != 1 || got[0] != deep {
		t.Fatalf("selected %v, want just %s — the predicate and the depth both apply", got, deep)
	}

	// The sibling kind is a different path, and is not swept in by a wildcard at the leaf.
	all, err := ParseSteps([]string{"verse-dev", "drift", "head", "*"})
	if err != nil {
		t.Fatal(err)
	}
	pat, _ = all.pattern()
	if len(pat.Select(store)) != 2 {
		t.Errorf("a leaf wildcard selected %d, want the two under `head`", len(pat.Select(store)))
	}
}
