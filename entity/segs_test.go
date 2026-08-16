package entity

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir/kpath"
)

// A ref's segments and the ref are the same address said two ways, and the round trip has to
// be exact for the case that motivates having both: an id CONTAINING A COLON. Walking a
// document hands back segments, and rebuilding the string for ParseRef to take apart again is
// how `merge:proposal:x@git:ref:y` comes back as a different entity than the one that was
// found — the id would be re-split at its own colons.
func TestSegsRoundTripsAnIDHoldingColons(t *testing.T) {
	for _, ref := range []Ref{
		RefAt("demo", "pr", "1"),
		RefAt("merge", "proposal", "appeared@git:ref:verse/auto/x"),
		RefAt("github", "pr", "org/repo#42"),
		RefAt("demo", "pr", "a.b"),
	} {
		segs := ref.Segs()
		if len(segs) != Depth {
			t.Fatalf("%s has %d segments, want %d", ref, len(segs), Depth)
		}
		got, ok := RefOf(segs)
		if !ok {
			t.Fatalf("RefOf(%q) refused the segments of a real ref", segs)
		}
		if got != ref {
			t.Errorf("round trip: %s → %q → %s", ref, segs, got)
		}
		// And the string spelling is unchanged by any of this, which is what keeps a charter
		// and a command line reading the same address as before.
		if again, err := ParseRef(ref.String()); err != nil || again != ref {
			t.Errorf("ParseRef(%q) = (%s, %v), want the ref back", ref.String(), again, err)
		}
	}
}

// RefOf answers whether segments name an entity AT ALL — the question ParseRef answers about a
// string. A walk that reached the wrong depth, or a document with an empty key, must come back
// as "not an address" rather than as a ref built out of whatever was there.
func TestRefOfRefusesWhatIsNotAnAddress(t *testing.T) {
	for _, segs := range [][]string{
		{},
		{"demo"},
		{"demo", "pr"},
		{"demo", "pr", "1", "extra"},
		{"demo", "", "1"},
		{"", "pr", "1"},
	} {
		if got, ok := RefOf(segs); ok {
			t.Errorf("RefOf(%q) = (%s, true), want a refusal", segs, got)
		}
	}
}

// The arity is stated ONCE. This is the guard on that claim: it fails the moment somebody
// writes a literal 3 into a walk instead of asking, because the walk and the constant would
// then disagree the day the constant changes.
func TestDepthIsWhatEveryAddressHas(t *testing.T) {
	ref := RefAt("demo", "pr", "1")
	if got := len(ref.Segs()); got != Depth {
		t.Fatalf("a ref has %d segments and Depth says %d", got, Depth)
	}
	if got := len(strings.Split(ref.String(), ":")); got != Depth {
		t.Fatalf("the string spelling has %d segments and Depth says %d", got, Depth)
	}
	// ParsePath splits an address from the field path at exactly that depth.
	p, err := ParsePath("demo.pr.1.labels.urgent")
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	if p.Ref != ref || len(p.Field) != 2 {
		t.Fatalf("ParsePath = %+v, want the entity at Depth with the rest as fields", p)
	}
}

// A path fixes some segments and the document carries the rest; Entities interleaves them. The
// wildcard case is the one that needs saying out loud — the fixed and carried segments are not
// a prefix and a suffix, they interleave — and it is what `verse state 'demo.pr.*.stage'`
// rests on.
func TestEntitiesInterleavesFixedAndCarriedSegments(t *testing.T) {
	doc := Doc{Node: mustParseNode(t, `{demo: {pr: {1: {stage: open}, 2: {stage: merged}}}}`)}
	for _, tc := range []struct {
		at   string
		want []string
	}{
		{"", []string{"demo:pr:1", "demo:pr:2"}},
		{"demo", []string{"demo:pr:1", "demo:pr:2"}},
		{"demo.pr", []string{"demo:pr:1", "demo:pr:2"}},
		{"demo.pr.1", []string{"demo:pr:1"}},
		{"demo.*.1", []string{"demo:pr:1"}},
	} {
		at, err := ParseAt(tc.at)
		if err != nil {
			t.Fatalf("ParseAt(%q): %v", tc.at, err)
		}
		var got []string
		for _, e := range doc.narrowed(t, at).Entities(at) {
			got = append(got, e.Ref.String())
		}
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("at %q: %v, want %v", tc.at, got, tc.want)
		}
	}
}

// narrowed is what a read does before Entities sees the document: the server narrows to the
// path, then reads the addresses back out of what is left.
func (d Doc) narrowed(t *testing.T, at *kpath.KPath) Doc {
	t.Helper()
	n, err := narrow(d.Node, at)
	if err != nil {
		t.Fatalf("narrow: %v", err)
	}
	return Doc{Node: n}
}
