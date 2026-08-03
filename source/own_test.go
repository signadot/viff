package source_test

import (
	"testing"

	"github.com/signadot/verse/entity"

	"github.com/signadot/verse/source"
)

// The case the whole rule exists for: two git repos, on two hosts, in one verse. If this needed
// --allow-overlap then the flag would be the ordinary path and would mark nothing — which is
// why "distinct system or distinct kind" was not enough on its own.
func TestTwoReposAreProvablyDisjoint(t *testing.T) {
	verse := []string{"git:*:verse/*", "git:head:verse"}
	kernel := []string{"git:*:kernel/*", "git:head:kernel"}

	if ok, err := source.Disjoint(verse, kernel); err != nil || !ok {
		t.Fatalf("two repos should be provably disjoint: ok=%v err=%v", ok, err)
	}

	// The near miss, and the reason a repo claims two globs rather than the one that looks
	// tidier. `verse*` would claim `verse-extras/main`, and these two would refuse to install
	// alongside each other for an overlap that is not real.
	extras := []string{"git:*:verse-extras/*", "git:head:verse-extras"}
	if ok, err := source.Disjoint(verse, extras); err != nil || !ok {
		t.Fatalf("verse and verse-extras should be disjoint: ok=%v err=%v", ok, err)
	}
	if ok, _ := source.Disjoint([]string{"git:*:verse*"}, extras); ok {
		t.Fatal("`verse*` really does claim verse-extras/*; it must not read as disjoint")
	}
}

// The proof rule itself, pair by pair. A claim is disjoint when ANY segment is, and a `*`
// segment is disjoint from nothing.
func TestClaimDisjoint(t *testing.T) {
	for _, tc := range []struct {
		name, a, b string
		want       bool
	}{
		{"different systems", "git:ref:x", "linear:issue:x", true},
		{"different literal kinds", "git:ref:x", "git:head:x", true},
		{"same everything", "git:ref:x", "git:ref:x", false},
		{"a wildcard kind overlaps a literal one", "git:*:x", "git:head:x", false},
		{"a wildcard system overlaps a literal one", "*:ref:x", "git:ref:x", false},
		{"different literal ids", "git:ref:a", "git:ref:b", true},
		{"disjoint prefixes", "git:ref:a/*", "git:ref:b/*", true},
		{"a prefix that extends another is not disjoint", "git:ref:a/*", "git:ref:a/b/*", false},
		{"a literal under a prefix is not disjoint", "git:ref:a/b", "git:ref:a/*", false},
		{"a literal outside a prefix is disjoint", "git:ref:b", "git:ref:a/*", true},
		// The claim that swallows its system. It is the one the note calls out: it claims the
		// lot, so nothing in git can be proved apart from it.
		{"everything in a system", "git:*:*", "git:ref:anything", false},
		{"everything, against another system", "git:*:*", "linear:issue:x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := source.ParseClaim(tc.a)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.a, err)
			}
			b, err := source.ParseClaim(tc.b)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.b, err)
			}
			if got := a.Disjoint(b); got != tc.want {
				t.Fatalf("%s disjoint %s = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// Symmetry, over the whole table: an install checks one way and means both, so an
			// asymmetric answer would make the refusal depend on installation order.
			if got := b.Disjoint(a); got != tc.want {
				t.Fatalf("%s disjoint %s = %v, want %v (asymmetric)", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// Ownership is a union, so one overlapping pair is an overlap however disjoint the rest are.
func TestDisjointIsEveryPair(t *testing.T) {
	a := []string{"git:*:verse/*", "git:head:verse"}
	b := []string{"git:*:kernel/*", "git:head:verse"} // the second entry collides
	if ok, err := source.Disjoint(a, b); err != nil || ok {
		t.Fatalf("one overlapping pair is an overlap: ok=%v err=%v", ok, err)
	}
	// And a registration claiming nothing is disjoint from everything, which is the honest
	// reading of an empty list rather than a special case.
	if ok, err := source.Disjoint(nil, a); err != nil || !ok {
		t.Fatalf("an empty claim should be disjoint: ok=%v err=%v", ok, err)
	}
}

// A malformed claim is a parse ERROR, never a quiet approximation. A claim meaning something
// narrower than it looks is a single-writer guarantee resting on a typo.
func TestParseClaimRefusals(t *testing.T) {
	for _, s := range []string{
		"git:ref",        // two segments
		"git::x",         // empty kind
		"git:ref:",       // empty id
		"gi*t:ref:x",     // a glob in the system
		"git:re*:x",      // a glob in the kind
		"git:ref:a*b",    // a `*` that is not trailing
		"git:ref:*/main", // ditto, at the front
		"git:ref:a/*/b",  // ditto, in the middle
	} {
		if c, err := source.ParseClaim(s); err == nil {
			t.Fatalf("%q should be refused, parsed as %s", s, c)
		}
	}
	// And what a well-formed one round-trips to, since the refusal messages quote it back.
	for _, s := range []string{"git:*:verse/*", "git:head:verse", "*:*:*"} {
		c, err := source.ParseClaim(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		if c.String() != s {
			t.Fatalf("%q round-tripped to %q", s, c.String())
		}
	}
	// An id may contain colons — the same three-way split entity.ParseRef makes.
	c, err := source.ParseClaim("git:ref:a:b/*")
	if err != nil || c.ID != "a:b/" || !c.Prefix {
		t.Fatalf("an id may contain colons: %+v %v", c, err)
	}
}

// The claim a source declares has to be the claim its reflection actually writes under, or the
// install proves something about a slice nobody touches. This is the one place the two halves
// meet, so it is asserted rather than assumed.
func TestGitOwnsCoversWhatItReflects(t *testing.T) {
	g := source.NewGit()
	g.AddRepo("A", t.TempDir())
	for _, ref := range []string{"git:ref:A/main", "git:head:A", "git:commit:A/abc123"} {
		if !source.Claims(g.Owns(), mustRef(t, ref)) {
			t.Fatalf("%s is reflected but not claimed by Owns() = %v", ref, g.Owns())
		}
	}
	// And it does not claim a neighbour's — which is what bounds a --forget removal to the
	// slice this registration was proved to own.
	for _, ref := range []string{"git:ref:B/main", "git:head:B"} {
		if source.Claims(g.Owns(), mustRef(t, ref)) {
			t.Fatalf("%s belongs to another repo but is claimed by %v", ref, g.Owns())
		}
	}
}

func mustRef(t *testing.T, s string) entity.Ref {
	t.Helper()
	ref, err := entity.ParseRef(s)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", s, err)
	}
	return ref
}
