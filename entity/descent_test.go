package entity

import (
	"strings"
	"testing"
)

// `..` IS RECURSIVE DESCENT, and both address readers refuse it BY THAT NAME.
//
// The refusal is the point, and so is the name. kpath carries `..` as its own segment kind —
// one that "names a set rather than a step" — and an address here selects at ONE depth, which
// is the whole decision this package rests on (docs/working/entity-tree.md): the question
// carries the depth, and a question spanning depths is a different question.
//
// BY THAT NAME, because the two ways of refusing it by accident are both worse. Flattened to a
// single `*` it selects one level where the author asked for every level; caught by a lexical
// guard it reads as an empty segment, which sends the author looking for a typo they did not
// make. Either way they are sent somewhere other than the thing they wrote.
func TestDescentIsRefusedByName(t *testing.T) {
	for _, addr := range []string{
		"lineage.trace..pr",
		"lineage.trace..",
		"..pr",
		"a..b.c",
	} {
		for _, r := range []struct {
			name string
			read func(string) ([]string, error)
		}{
			{"ExprSegs", ExprSegs},
			{"PathSegs", PathSegs},
		} {
			got, err := r.read(addr)
			if err == nil {
				t.Errorf("%s(%q) = %q, want a refusal", r.name, addr, got)
				continue
			}
			if !strings.Contains(err.Error(), "recursive descent") {
				t.Errorf("%s(%q) refused with %q — it should say WHICH thing it refused, so "+
					"nobody goes looking for a typo they did not make", r.name, addr, err)
			}
		}
	}
}

// A QUOTED `..` is a name, not a descent — the same escape that makes `demo.pr."a.b"` an id
// holding a dot. Nothing in verse writes an entity called `..`, but the grammar has one way to
// spell a segment literally and it holds here too, which is why this is pinned rather than
// refused alongside the case above.
func TestQuotedDotsAreAnOrdinaryName(t *testing.T) {
	segs, err := ExprSegs(`lineage.trace."..".pr`)
	if err != nil {
		t.Fatalf("ExprSegs of a quoted `..`: %v", err)
	}
	want := []string{"lineage", "trace", "..", "pr"}
	if strings.Join(segs, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("ExprSegs = %q, want %q", segs, want)
	}
}

// `**` is TWO OPEN POSITIONS, not a descent. It reads as two segments and selects two levels,
// which is an ordinary address of its own length — and is exactly the confusion that made the
// case above worth a test: the two spellings look alike and mean different things, and only one
// of them is a question this grammar can ask.
func TestDoubleStarIsTwoOpenPositions(t *testing.T) {
	segs, err := ExprSegs("lineage.trace.**")
	if err != nil {
		t.Fatalf("ExprSegs(`lineage.trace.**`): %v", err)
	}
	want := []string{"lineage", "trace", "*", "*"}
	if strings.Join(segs, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("ExprSegs = %q, want %q", segs, want)
	}
}
