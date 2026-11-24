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
		RefAt("demo", "pr", "signadot/viff#42"),
		// The three-segment id that holds colons — the case the colon form is arity-fixed for.
		ViffRef("task", "instance", "review@demo.pr.1"),
		RefAt("viff-dev", "drift", "head", "abc"),
		// THE ONE THAT BIT: four segments, and the id is itself an address.
		RefAt("viff-dev", "drift", "head", "head-moved@git.head.viff"),
		RefAt("viff-dev", "merge", "train", "train-cut@git.head.viff@4bb4464d"),
		ViffRef("motion", "channel", "x", "y"),
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

// A SEGMENT ENDS WHERE THE QUOTING SAYS, which is the whole of the grammar now that there is
// only one. An id may hold dots, and that is what a dotted string could not say before: it is
// said by quoting the segment.
func TestADottedIDIsStillAnID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`demo.pr."a.b.c.d"`, []string{"demo", "pr", "a.b.c.d"}},
		{`demo.pr."1"`, []string{"demo", "pr", "1"}},
		{`mirror.task.instance."x.y"`, []string{"mirror", "task", "instance", "x.y"}},
		{"viff-dev.drift.head.abc", []string{"viff-dev", "drift", "head", "abc"}},
		{`viff-dev.drift.head."head-moved@git.head.viff"`,
			[]string{"viff-dev", "drift", "head", "head-moved@git.head.viff"}},
		// UNQUOTED, the dots are the delimiter — which is the other half of the same rule.
		{"demo.pr.a.b.c.d", []string{"demo", "pr", "a", "b", "c", "d"}},
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

// THE COLON FORM IS NOT A SPELLING ANY MORE — it is a name that happens to hold colons, which is
// what that string means in the one grammar left. So `demo.pr.1` is ONE segment, not three.
//
// This is the clobber stated as a test rather than left to be discovered: a record written
// before the change reads as an address one segment deep, and is re-seeded rather than
// translated. Nothing tries to guess which spelling a string is, because guessing is exactly
// what the two grammars cost — a discriminator, stated in more than one place, that read
// `viff-dev.drift.head."head-moved@git.head.viff"` as three segments with a mangled id and
// answered "nothing stands there".
func TestTheColonFormIsJustAName(t *testing.T) {
	const old = "demo" + ":" + "pr" + ":" + "1" // spelled in pieces: nothing may rewrite it here
	got, err := ParseAddr(old)
	if err != nil {
		t.Fatalf("%s: %v", old, err)
	}
	if segs := got.Segs(); len(segs) != 1 || segs[0] != old {
		t.Errorf("%s → %q, want the one segment it names", old, segs)
	}
	// And it round-trips as that: a colon needs no quoting, since nothing reads one.
	if s := got.String(); s != old {
		t.Errorf("rendered as %q", s)
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
		{[]string{"a b", "pr", "1"}, `"a b".pr."1"`},     // a space in the system
		{[]string{"demo", "p r", "1"}, `demo."p r"."1"`}, // …and in the kind
		{[]string{"demo", "pr", "x y"}, `demo.pr."x y"`},
		{[]string{"a.b", "pr", "1"}, `"a.b".pr."1"`}, // a dot in the system: the quoting says so
		{[]string{"a:b", "pr", "1"}, `a:b.pr."1"`},   // a COLON: ordinary, since nothing reads one
		{[]string{"demo", "pr", "a.b"}, `demo.pr."a.b"`},
	} {
		p, err := TargetOf(c.segs)
		if err != nil {
			t.Errorf("TargetOf(%q): %v", c.segs, err)
			continue
		}
		if got := p.Ref.String(); got != c.ref {
			t.Errorf("TargetOf(%q) → %q, want %q", c.segs, got, c.ref)
		}
		if p.Access != "" {
			t.Errorf("TargetOf(%q) read an access %q out of names", c.segs, p.Access)
		}
	}
}

// …and a longer address is simply a longer path: there is no split in it.
func TestPathOfKeepsEverySegment(t *testing.T) {
	p, err := TargetOf([]string{"demo", "pr", "a.b", "labels", "urgent"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `demo.pr."a.b".labels.urgent`; p.Ref.String() != want {
		t.Errorf("path = %q, want %q", p.Ref.String(), want)
	}
	if p.Access != "" {
		t.Errorf("access = %q, want none", p.Access)
	}
}
