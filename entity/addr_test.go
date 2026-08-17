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
