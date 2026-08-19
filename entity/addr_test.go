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

// A DELIMITER INSIDE QUOTES IS PART OF A NAME, wherever the segment sits.
//
// Which spelling an address is written in is decided by whichever delimiter comes first — a
// space means steps, a `:` means a ref, a `.` means a kinded path — and that test has to skip
// what is quoted. It did not, so `"a b".pr.1` (a system holding a space) was split at the space
// into `"a` and `b".pr.1`, and refused as naming two segments of the three. Only the FIRST
// segment could reach it: anywhere later there is a `.` before the space, which is the
// comparison that happened to be right.
//
// kpath.Parse and entity.PathSegs both handled it, which is what made it drift rather than a
// decision — the same address parsed one way through the charter's reader and another through
// this one.
func TestParsePathQuotedDelimiters(t *testing.T) {
	for _, c := range []struct{ in, ref string }{
		{`"a b".pr.1`, "a b:pr:1"},     // a space in the system — the case that was broken
		{`demo."p r".1`, "demo:p r:1"}, // …and in the kind, which always worked
		{`demo.pr."x y"`, "demo:pr:x y"},
		{`"a.b".pr.1`, "a.b:pr:1"}, // a dot in the system, quoted
		{`"a:b".pr.1`, "a:b:pr:1"}, // a COLON in the system: still a kinded path, not a ref
		{`demo.pr."a.b"`, "demo:pr:a.b"},
		{`demo:pr:a.b`, "demo:pr:a.b"}, // the ref spelling, where dots belong to the id
		{`demo pr a.b`, "demo:pr:a.b"}, // and steps, where they do too
	} {
		p, err := ParsePath(c.in)
		if err != nil {
			t.Errorf("ParsePath(%s): %v", c.in, err)
			continue
		}
		if got := p.Ref.String(); got != c.ref {
			t.Errorf("ParsePath(%s) → %q, want %q", c.in, got, c.ref)
		}
		if len(p.Field) != 0 {
			t.Errorf("ParsePath(%s) named a field path %v", c.in, p.Field)
		}
	}
}

// …and a field path still follows a quoted segment, which is the whole reason quoting exists.
func TestParsePathQuotedSegmentThenField(t *testing.T) {
	p, err := ParsePath(`demo.pr."a.b".labels.urgent`)
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
