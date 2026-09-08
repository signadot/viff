package entity

import (
	"sort"
	"testing"
)

// Select reads the two halves a Ref can be rooted at — the world's and verse's own — rather
// than the whole root. The halves are exhaustive (Ref.root is "" or VerseRoot), so the union
// has to be exactly what a root read filtered to the same system would give. This pins that,
// because getting it wrong is the silent kind: a narrowing that misses a half finds nothing
// there and reports an empty selection, which reads as "no entity matched" rather than as a
// failure.
func TestSelectSeesBothHalves(t *testing.T) {
	s := openStore(t)
	// The same system and kind on each side of the root.
	world := RefAt("task", "instance", "w1")
	own := RefAt(VerseRoot, "task", "instance", "v1")
	set(t, s, world, Obj{"stage": "open"}.Payload())
	set(t, s, own, Obj{"stage": "open"}.Payload())
	// A neighbour in another system, which must NOT be selected.
	set(t, s, RefAt("other", "instance", "x"), Obj{"stage": "open"}.Payload())

	got := Pattern{System: "task", Kind: "instance"}.Select(s)
	have := make([]string, 0, len(got))
	for _, r := range got {
		have = append(have, r.String())
	}
	sort.Strings(have)

	if len(have) != 2 || have[0] != own.String() && have[1] != own.String() {
		t.Fatalf("Select = %v, want both halves (%s and %s)", have, world, own)
	}
	var sawWorld, sawOwn bool
	for _, h := range have {
		sawWorld = sawWorld || h == world.String()
		sawOwn = sawOwn || h == own.String()
	}
	if !sawWorld {
		t.Errorf("Select = %v, missing the world's half %s", have, world)
	}
	if !sawOwn {
		t.Errorf("Select = %v, missing verse's own half %s — the reserved root is a segment of "+
			"its own, and a narrowing that forgets it reports an empty selection", have, own)
	}
}

// The same, with the kind left open: a pattern naming only a system still reaches both halves.
func TestSelectSystemOnlySeesBothHalves(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("task", "instance", "w1"), Obj{"a": "1"}.Payload())
	set(t, s, RefAt(VerseRoot, "task", "run", "v1"), Obj{"a": "1"}.Payload())
	set(t, s, RefAt("other", "instance", "x"), Obj{"a": "1"}.Payload())

	got := Pattern{System: "task"}.Select(s)
	if len(got) != 2 {
		t.Fatalf("Select(system only) = %v, want the two task entities across both halves", got)
	}
}
