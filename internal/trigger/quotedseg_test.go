package trigger

import (
	"testing"

	"go.viff.dev/viff/pkg/entity"
)

// ONE SPELLING, ONE MEANING, wherever a path is written. `demo.pr."*"` names the node called `*`
// at a read (`viff data -at`), and it names the same node in a charter — where it used to read
// as the WILDCARD, so a rule about one node fired on every node beside it. Tony quoting carries a
// value's type rather than an escape, so the list form says that name with the match over it
// (`!or ["*"]`), and the string form now compiles to exactly that.
func TestAQuotedSegmentNamesItInEverySpelling(t *testing.T) {
	star := entity.RefAt("demo", "pr", "*")
	dots := entity.RefAt("demo", "pr", "..")
	one := entity.RefAt("demo", "pr", "1")
	for _, c := range []struct {
		addr            string
		star, dots, one bool
	}{
		{`demo.pr."*"`, true, false, false},  // the name that is an asterisk
		{`demo.pr.*`, true, true, true},      // the wildcard: any name, that one included
		{`demo.pr.".."`, false, true, false}, // the name that is two dots
		{`demo.pr.1`, false, false, true},
	} {
		m, err := ParseExpr(c.addr)
		if err != nil {
			t.Errorf("ParseExpr(%q): %v", c.addr, err)
			continue
		}
		p, err := m.pattern()
		if err != nil {
			t.Errorf("%q: %v", c.addr, err)
			continue
		}
		if got := [3]bool{p.MatchRef(star), p.MatchRef(dots), p.MatchRef(one)}; got != [3]bool{c.star, c.dots, c.one} {
			t.Errorf("%q matches (star, .., 1) = %v, want %v", c.addr, got, [3]bool{c.star, c.dots, c.one})
		}
	}
	// A descent is still a descent, and a quoted one is a name: the two cannot be told apart by
	// their characters alone, which is why the kind is kept rather than the text.
	if m, err := ParseExpr("demo..finding"); err != nil || len(m.Segs()) != 3 || m.Segs()[1] != ".." {
		t.Errorf("ParseExpr(demo..finding) = (%v, %v), want the descent as its own segment", m, err)
	}
}

// A READ AND A CHARTER AGREE about the same characters, which is the invariant the two grammars
// exist under: the address is one grammar, spelled twice.
func TestAReadAndACharterAgreeOnAQuotedStar(t *testing.T) {
	star := entity.RefAt("demo", "pr", "*")
	one := entity.RefAt("demo", "pr", "1")

	at, err := entity.ParseAt(`demo.pr."*"`)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseExpr(`demo.pr."*"`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.pattern()
	if err != nil {
		t.Fatal(err)
	}
	if !entity.Reaches(at, star) || !p.MatchRef(star) {
		t.Error(`demo.pr."*" does not name the node called * on one of the two faces`)
	}
	if entity.Reaches(at, one) || p.MatchRef(one) {
		t.Error(`demo.pr."*" reaches an ordinary id on one of the two faces`)
	}
}
