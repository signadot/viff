package main

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
)

// `--paths` WALKS, and the walk is what makes it impossible to be silently wrong.
//
// An address is a path of any length — a write names a place at whatever depth — and
// nothing in an answer says where an entity ends (docs/working/entity-tree.md: entity-hood is a
// lens). Listing "the level an address has" therefore answered `verse-dev.drift.range` for a
// place holding four-segment records: true, and nothing anybody wanted. Every address under what
// was asked for cannot miss them.
func TestPathsWalksEverythingUnderTheAddress(t *testing.T) {
	store := serve(t)
	write := func(segs []string, o entity.Obj) {
		t.Helper()
		if _, _, err := store.Write(entity.Write{
			At: entity.RefAt(segs...).At(), Value: o.Payload(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Four segments, which is the shape that used to disappear.
	write([]string{"demo", "drift", "range", "abc"}, entity.Obj{"to": "abc"})
	write([]string{"demo", "drift", "range", "def"}, entity.Obj{"to": "def"})

	out, err := runState(t, "demo", "drift", "range", "--paths")
	if err != nil {
		t.Fatalf("data --paths: %v", err)
	}
	// ONE LINE PER ADDRESS, in steps — so the comparison is per LINE, not per whitespace-
	// separated token: a step is a word, and an address is several of them.
	got := lines(out)
	for _, want := range []string{"demo drift range abc", "demo drift range def"} {
		if !has(got, want) {
			t.Errorf("--paths did not list %q; it answered %q", want, got)
		}
	}
	// …and it goes on into the payload, because that is what "every address" means: the reader
	// bounds it when they want less, rather than guessing a level and losing what is deeper.
	if !has(got, "demo drift range abc to") {
		t.Errorf("the walk stopped at the record rather than walking into it: %q", got)
	}
}

// --depth BOUNDS the walk. It is a ceiling on the number of segments, so it reads in the same
// units as the rest of the model counts in.
func TestDepthBoundsTheWalk(t *testing.T) {
	store := serve(t)
	if _, _, err := store.Write(entity.Write{
		At:    entity.RefAt("demo", "drift", "range", "abc").At(),
		Value: entity.Obj{"to": "abc"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	out, err := runState(t, "demo", "drift", "range", "--paths", "--depth", "4")
	if err != nil {
		t.Fatalf("data --paths --depth 4: %v", err)
	}
	if !has(lines(out), "demo drift range abc") {
		t.Errorf("--depth 4 lost the record: %q", out)
	}
	if has(lines(out), "demo drift range abc to") {
		t.Errorf("--depth 4 walked into the payload: %q", out)
	}
}

// lines splits a listing into its lines, dropping blanks — one line is one address.
func lines(out string) []string {
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			got = append(got, l)
		}
	}
	return got
}

func has(got []string, want string) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}
