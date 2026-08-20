package entity

import "testing"

// AN ADDRESS THAT CANNOT BE READ BACK IS NOT AN ADDRESS, and this is the pair that makes the
// deep spelling one: whatever String writes, ParseAddr reads.
//
// It was broken in the way that costs the most to find. `applyHook` recovered a completed task's
// subject with ParseRef, which reads exactly three segments and takes everything past the second
// colon as the id — so a subject four segments deep whose id held colons came back as a DIFFERENT
// ref, with no error, and the charter's own confinement refused the patch. On staging that meant
// a comment review did its work and could not record that it had, and a merge train landed a
// batch and could not say so.
func TestAnAddressReadsBackAtAnyDepth(t *testing.T) {
	for _, ref := range []Ref{
		RefAt("demo", "pr", "1"),
		RefAt("demo", "pr", "signadot/verse#42"),
		// The three-segment id that holds colons — the case the colon form is arity-fixed for.
		VerseRef("task", "instance", "review@demo:pr:1"),
		RefAt("verse-dev", "drift", "head", "abc"),
		// THE ONE THAT BIT: four segments, and the id is itself an address.
		RefAt("verse-dev", "drift", "head", "head-moved@git:head:verse"),
		RefAt("verse-dev", "merge", "train", "train-cut@git:head:verse@4bb4464d"),
		VerseRef("motion", "channel", "x", "y"),
		RefAt("vote", "abc"),
		RefAt("a", "b", "c", "d", "e"),
	} {
		back, err := ParseAddr(ref.String())
		if err != nil {
			t.Errorf("%s: %v", ref, err)
			continue
		}
		if back != ref {
			t.Errorf("%s read back as %v (segments %q, wanted %q)", ref.String(), back, back.Segs(), ref.Segs())
		}
	}
}

// THE DISCRIMINATOR IS WHERE THE COLONS ARE, and the two shapes it has to tell apart are these.
// An id may hold dots, which is what makes a dotted string ambiguous at all.
func TestADottedIDIsStillAnID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"demo:pr:a.b.c.d", []string{"demo", "pr", "a.b.c.d"}},
		{"demo:pr:1", []string{"demo", "pr", "1"}},
		{"verse:task:instance:x.y", []string{"verse", "task", "instance", "x.y"}},
		{"verse-dev.drift.head.abc", []string{"verse-dev", "drift", "head", "abc"}},
		{"verse-dev.drift.head.head-moved@git:head:verse",
			[]string{"verse-dev", "drift", "head", "head-moved@git:head:verse"}},
	} {
		got, err := ParseAddr(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if len(got.Segs()) != len(tc.want) {
			t.Errorf("%s → %q, wanted %q", tc.in, got.Segs(), tc.want)
			continue
		}
		for i, w := range tc.want {
			if got.Segs()[i] != w {
				t.Errorf("%s → %q, wanted %q", tc.in, got.Segs(), tc.want)
				break
			}
		}
	}
}

// ParseRef IS UNCHANGED, deliberately: a charter and a command line write the colon form, and a
// dotted string THERE is an address followed by a payload path (`demo.pr.1.stage`, which
// trigger.splitAddress reads as demo:pr:1 plus `stage`). Reading it as a four-segment entity
// would swallow the field silently, which is the same class of bug as the one above.
func TestParseRefStillReadsThreeSegments(t *testing.T) {
	if _, err := ParseRef("verse-dev.drift.head.abc"); err == nil {
		t.Error("ParseRef accepted a dotted path: splitAddress depends on it refusing")
	}
	ref, err := ParseRef("demo:pr:a.b.c.d")
	if err != nil || ref.ID() != "a.b.c.d" {
		t.Errorf("ParseRef(%q) = %v, %v", "demo:pr:a.b.c.d", ref.Segs(), err)
	}
}

// A DELIMITER IS PART OF A NAME, wherever the segment sits — because there is no delimiter.
//
// An address arrives as its SEGMENTS (PathOf), so nothing scans for a `.`, a `:` or a space to
// decide where one ends. That scan is what this file used to be about: `"a b".pr.1` — a system
// holding a space — was split AT the space into `"a` and `b".pr.1` and refused as naming two
// segments of three, and only the first segment could ever reach the bug, since anywhere later
// there is a `.` before the space.
//
// The scan is gone. Every case below is now the same case: the segments are what was passed.
func TestPathOfTakesASegmentWhole(t *testing.T) {
	for _, c := range []struct {
		segs []string
		ref  string
	}{
		{[]string{"a b", "pr", "1"}, "a b:pr:1"},     // a space in the system
		{[]string{"demo", "p r", "1"}, "demo:p r:1"}, // …and in the kind
		{[]string{"demo", "pr", "x y"}, "demo:pr:x y"},
		{[]string{"a.b", "pr", "1"}, "a.b:pr:1"}, // a dot in the system
		{[]string{"a:b", "pr", "1"}, "a:b:pr:1"}, // a COLON in the system
		{[]string{"demo", "pr", "a.b"}, "demo:pr:a.b"},
	} {
		p, err := PathOf(c.segs)
		if err != nil {
			t.Errorf("PathOf(%q): %v", c.segs, err)
			continue
		}
		if got := p.Ref.String(); got != c.ref {
			t.Errorf("PathOf(%q) → %q, want %q", c.segs, got, c.ref)
		}
		if len(p.Field) != 0 {
			t.Errorf("PathOf(%q) named a field path %v", c.segs, p.Field)
		}
	}
}

// …and the field path is simply the segments past the entity.
func TestPathOfSplitsTheFieldOffPositionally(t *testing.T) {
	p, err := PathOf([]string{"demo", "pr", "a.b", "labels", "urgent"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Ref.String() != "demo:pr:a.b" {
		t.Errorf("ref = %q", p.Ref.String())
	}
	if len(p.Field) != 2 || p.Field[0] != "labels" || p.Field[1] != "urgent" {
		t.Errorf("field = %v, want [labels urgent]", p.Field)
	}
}
