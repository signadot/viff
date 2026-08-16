package trigger

import (
	"testing"

	"github.com/signadot/verse/entity"
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
