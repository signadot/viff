package entity

import (
	"strings"
	"testing"
)

// `..` IS RECURSIVE DESCENT, and both address readers refuse it BY THAT NAME.
//
// The refusal is the point, and so is the name. kpath carries `..` as its own segment kind —
// one that "names a set rather than a step" — and an address here selects at ONE depth, which
// is the whole decision this package rests on: the question
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

// A PATTERN'S SEGMENTS KEEP THEIR KINDS. `*` and `..` are query segments and a quoted `"*"` or
// `".."` is the name those characters spell — which text alone cannot carry, since both arrive
// as the same two or one characters. The name comes back as the match over it, which is how the
// list form spells one (`path: [demo, pr, !or ["*"]]`).
func TestPatternNodesKeepEachSegmentsKind(t *testing.T) {
	for _, c := range []struct {
		addr string
		want []string // "*" wildcard, ".." descent, "=x" the name x, "~x" the match naming x
	}{
		{`demo.pr.1`, []string{"=demo", "=pr", "=1"}},
		{`demo.pr.*`, []string{"=demo", "=pr", "*"}},
		{`demo.pr."*"`, []string{"=demo", "=pr", "~*"}},
		{`demo..finding`, []string{"=demo", "..", "=finding"}},
		{`demo.".."`, []string{"=demo", "~.."}},
	} {
		segs, err := PatternNodes(c.addr)
		if err != nil {
			t.Errorf("PatternNodes(%q): %v", c.addr, err)
			continue
		}
		var got []string
		for _, seg := range segs {
			switch {
			case seg.Tag != "":
				got = append(got, "~"+seg.Values[0].String)
			case seg.String == "*":
				got = append(got, "*")
			case seg.String == "..":
				got = append(got, "..")
			default:
				got = append(got, "="+seg.String)
			}
		}
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("PatternNodes(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
	// An element of a node is not a segment: a pattern names nodes.
	if _, err := PatternNodes(`demo.pr.1.votes[0]`); err == nil {
		t.Error("an array access was read as a segment of a pattern")
	}
}
