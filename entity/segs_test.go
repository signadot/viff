package entity

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// A ref's segments and the ref are the same address said two ways, and the round trip has to
// be exact for the case that motivates having both: an id CONTAINING A COLON. Walking a
// document hands back segments, and rebuilding the string for ParseRef to take apart again is
// how `merge.proposal.x@git.ref.y` comes back as a different entity than the one that was
// found — the id would be re-split at its own colons.
func TestSegsRoundTripsAnIDHoldingColons(t *testing.T) {
	for _, ref := range []Ref{
		RefAt("demo", "pr", "1"),
		RefAt("merge", "proposal", "appeared@git.ref.verse/auto/x"),
		RefAt("github", "pr", "org/repo#42"),
		RefAt("demo", "pr", "a.b"),
	} {
		segs := ref.Segs()
		got, ok := RefOfAny(segs)
		if !ok {
			t.Fatalf("RefOfAny(%q) refused the segments of a real ref", segs)
		}
		if got != ref {
			t.Errorf("round trip: %s → %q → %s", ref, segs, got)
		}
		// And the string spelling is unchanged by any of this, which is what keeps a charter
		// and a command line reading the same address as before.
		if again, err := ParseAddr(ref.String()); err != nil || again != ref {
			t.Errorf("ParseAddr(%q) = (%s, %v), want the ref back", ref.String(), again, err)
		}
	}
}

// RefOfAny answers whether segments name an address AT ALL: a document with an empty key must
// come back as "not an address" rather than as a ref built out of whatever was there. Any
// number of segments is an address; none is not.
func TestRefOfAnyRefusesWhatIsNotAnAddress(t *testing.T) {
	for _, segs := range [][]string{
		{},
		{"demo", "", "1"},
		{"", "pr", "1"},
	} {
		if got, ok := RefOfAny(segs); ok {
			t.Errorf("RefOfAny(%q) = (%s, true), want a refusal", segs, got)
		}
	}
	for _, segs := range [][]string{{"demo"}, {"demo", "pr"}, {"demo", "pr", "1", "extra"}} {
		if _, ok := RefOfAny(segs); !ok {
			t.Errorf("RefOfAny(%q) refused an address of %d segments", segs, len(segs))
		}
	}
}

// An address has the segments it was given, whatever their number. This used to assert that
// every address has exactly Depth of them, guarding a constant that said three — a claim
// RefOfAny above already refuses to enforce, since it accepts one, two and four. What is
// actually true, and what a walk depends on, is that the count survives the round trip: a ref
// spelled as a string and read back names the same segments it was built from.
func TestAnAddressKeepsTheSegmentsItWasGiven(t *testing.T) {
	for _, want := range [][]string{
		{"demo"},
		{"demo", "pr"},
		{"demo", "pr", "1"},
		{"demo", "pr", "1", "labels"},
	} {
		ref := RefAt(want...)
		if got := ref.Segs(); !slices.Equal(got, want) {
			t.Errorf("RefAt(%q).Segs() = %q", want, got)
		}
		segs, err := PathSegs(ref.String())
		if err != nil || !slices.Equal(segs, want) {
			t.Errorf("PathSegs(%q) = %q, %v; want %q", ref.String(), segs, err, want)
		}
	}
	// And a write's target keeps every segment it was given: there is no split in an address.
	p, err := TargetOf([]string{"demo", "pr", "1", "labels", "urgent"})
	if err != nil {
		t.Fatalf("TargetOf: %v", err)
	}
	if p.Ref != RefAt("demo", "pr", "1", "labels", "urgent") {
		t.Fatalf("TargetOf = %+v, want the whole path", p)
	}
}

// A path fixes some segments and the answer carries the rest; Reached interleaves them. The
// wildcard case is the one that needs saying out loud — the fixed and carried segments are not
// a prefix and a suffix, they interleave — and it is what `verse data demo '*' 1` rests on.
func TestReachedInterleavesFixedAndCarriedSegments(t *testing.T) {
	root := mustParseNode(t, `{demo: {pr: {1: {stage: open}, 2: {stage: merged}}}}`)
	for _, tc := range []struct {
		at   string
		want []string
	}{
		{"*.*.*", []string{`demo.pr."1"`, `demo.pr."2"`}},
		{"demo.*.*", []string{`demo.pr."1"`, `demo.pr."2"`}},
		{"demo.pr.*", []string{`demo.pr."1"`, `demo.pr."2"`}},
		{"demo.pr.1", []string{`demo.pr."1"`}},
		{"demo.*.1", []string{`demo.pr."1"`}},
		{"*.pr.2.stage", []string{`demo.pr."2".stage`}},
	} {
		at, err := ParseAt(tc.at)
		if err != nil {
			t.Fatalf("ParseAt(%q): %v", tc.at, err)
		}
		out, _, err := Reach(root, at, nil)
		if err != nil {
			t.Fatalf("reach %q: %v", tc.at, err)
		}
		var got []string
		for _, e := range (Doc{Node: out}).Reached(at) {
			got = append(got, e.Ref.String())
		}
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("at %q: %v, want %v", tc.at, got, tc.want)
		}
	}
}

// AN ADDRESS ARRIVES AS ITS SEGMENTS, and each one is whole.
//
// The shell separates the words, so `verse data demo pr a.b` hands over three strings and the
// third simply contains a dot. Nothing here scans for a delimiter, which is what makes every
// row below one row: a dot, a colon or a space inside a segment is a character in a name.
//
// It used to take a single token too, split by whichever of `:` `.` ` ` came first. That is
// ambiguous and could not be made otherwise — a word `w` reads either as the one segment `w` or
// as `split(w, delim)`, and choosing either makes the other unsayable — so the token spelling is
// gone from the command line and a ref is taken where it is MARKED (`--ref demo.pr.1`).
func TestAnAddressArrivesAsItsSegments(t *testing.T) {
	for _, c := range []struct {
		segs []string
		want Ref
	}{
		{[]string{"demo", "pr", "1"}, RefAt("demo", "pr", "1")},
		{[]string{"demo", "pr", "a.b"}, RefAt("demo", "pr", "a.b")},                 // a dot INSIDE a step
		{[]string{"git", "ref", "x@git.ref.y"}, RefAt("git", "ref", "x@git.ref.y")}, // and a colon
		{[]string{"a.b.c", "pr", "1"}, RefAt("a.b.c", "pr", "1")},                   // …in the SYSTEM, which the token form could not say
		{[]string{"demo", "pr", "a b"}, RefAt("demo", "pr", "a b")},                 // and a space
		{[]string{"verse", "task", "instance", "run-a1"}, RefAt("verse", "task", "instance", "run-a1")},
	} {
		p, err := TargetOf(c.segs)
		if err != nil {
			t.Errorf("TargetOf(%q): %v", c.segs, err)
			continue
		}
		if p.Ref != c.want || p.Access != "" {
			t.Errorf("TargetOf(%q) = %s%s, want %s", c.segs, p.Ref, p.Access, c.want)
		}
		// And the READ of the same segments reaches the same place: the wire takes a kinded
		// path, and JoinSegs quotes each segment as kpath needs — which is lossless, since the
		// server parses it with ParseAt and never re-scans it for delimiters.
		at, err := ParseAt(JoinSegs(c.segs))
		if err != nil || at.String() != c.want.At().KPath() {
			t.Errorf("read of %q = (%v, %v), want %q", c.segs, at, err, c.want.At().KPath())
		}
	}

	// A longer address is a longer path, whole.
	p, err := TargetOf([]string{"demo", "pr", "1", "labels", "urgent"})
	if err != nil || p.Ref != RefAt("demo", "pr", "1", "labels", "urgent") {
		t.Errorf("TargetOf(demo pr 1 labels urgent) = (%v, %v)", p, err)
	}
}

// A LISTING ANSWERS IN THE STEPS THE COMMAND LINE TAKES, so what it prints hands straight back.
//
// A step is a whole word, which is what makes a dot inside one need no quoting — the property
// the steps form exists for. The shell's own quoting is applied only where a segment would
// otherwise be split or expanded, and it is SINGLE quotes: literal, so nothing inside is
// globbed or substituted, and `xargs` understands them where it reads the kinded path's `\"` as
// an unterminated quote.
func TestJoinStepsQuotesOnlyWhatAShellWouldTouch(t *testing.T) {
	for _, c := range []struct {
		segs []string
		want string
	}{
		{[]string{"demo", "pr", "1"}, "demo pr 1"},
		// The whole point: a dot is ordinary inside a step.
		{[]string{"demo", "pr", "a.b"}, "demo pr a.b"},
		{[]string{"verse", "task", "instance", "review@demo.pr.1"}, "verse task instance review@demo.pr.1"},
		// The separator IS the space, so a segment holding one carries quotes — the case that
		// made the bare steps form ambiguous and moved this listing to kinded paths for a while.
		{[]string{"demo", "pr", "a b.txt"}, "demo pr 'a b.txt'"},
		{[]string{"a b", "pr", "1"}, "'a b' pr 1"},
		// An id that IS an asterisk: the quoted clause, since a bare `*` step selects.
		{[]string{"git", "ref", "*"}, `git ref '"*"'`},
		// An empty segment would otherwise vanish from the line entirely.
		{[]string{"demo", "pr", ""}, "demo pr ''"},
		// Double quotes are the fallback, for a segment holding a single one.
		{[]string{"demo", "pr", "it's"}, `demo pr "it's"`},
		// A name the step clauses would read as something else carries the quoted clause too.
		{[]string{"demo", "pr", "1", "[0]"}, `demo pr 1 '"[0]"'`},
		{[]string{"demo", "pr", "1", "(walter)"}, `demo pr 1 '"(walter)"'`},
		{[]string{"demo", "pr", "1", `"x"`}, `demo pr 1 '""x""'`},
		{[]string{"demo", "pr", "1", "'x'"}, `demo pr 1 "''x''"`},
	} {
		if got := JoinSteps(c.segs); got != c.want {
			t.Errorf("JoinSteps(%q) = %q, want %q", c.segs, got, c.want)
		}
	}
}

// And what it prints is what a read takes, for every one of those — which is the property the
// listing exists to have.
//
// THE SHELL STRIPS ITS QUOTING before verse sees the words, and the step clauses read what is
// left, so the round trip is only proved by running both: a real shell (`eval set --`) and
// `xargs -L1` split the printed line, and KPathOf reads the words the way `verse data` does.
func TestWhatAListingPrintsIsWhatAReadTakes(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to split the line with")
	}
	for _, segs := range [][]string{
		{"demo", "pr", "1"},
		{"demo", "pr", "a.b"},
		{"demo", "pr", "a b.txt"},
		{"git", "ref", "*"},
		{"dotted.sys", "pr", "1"},
		{"verse", "task", "instance", "review@demo.pr.1"},
		{"demo", "pr", "1", "[0]"},
		{"demo", "pr", "1", "{0}"},
		{"demo", "pr", "1", "(walter)"},
		{"demo", "pr", "1", `"x"`},
		{"demo", "pr", "1", "'x'"},
		{"demo", "pr", "1", `"`},
	} {
		line := JoinSteps(segs)
		want := JoinSegs(segs)
		for _, split := range []struct{ name, script string }{
			{"sh", `eval "set -- $1"; printf '%s\0' "$@"`},
			{"xargs", `printf '%s\n' "$1" | xargs -L1 sh -c 'printf "%s\0" "$@"' _`},
		} {
			out, err := exec.Command(sh, "-c", split.script, "_", line).Output()
			if err != nil {
				t.Errorf("%s could not split %s: %v", split.name, line, err)
				continue
			}
			words := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			got, err := KPathOf(words)
			if err != nil || got != want {
				t.Errorf("%q prints %s, which %s reads back as %q (%v), want %q",
					segs, line, split.name, got, err, want)
			}
		}
	}
}

// AN ELEMENT'S LINE READS BACK AS THE ELEMENT, which is what makes a listing that reaches into
// an array worth printing: the access is its own word, so the step clauses read it as an access
// rather than as a field of that name. The inverse case is above — a segment that IS the name
// `[0]` prints wrapped in the quoted clause and reads back as the name.
func TestAnElementsListingLineReadsBackAsTheElement(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to split the line with")
	}
	for _, at := range []Path{
		RefAt("demo", "pr", "1", "votes").Into("[0]"),
		RefAt("demo", "pr", "1", "votes").Into("[0].choice"),
		RefAt("demo", "pr", "1", "votes").Into("{7}"),
		RefAt("demo", "pr", "1", "votes").Into("(walter)"),
		RefAt("demo", "pr", "1", "votes").Into("(walter).choice"),
		RefAt("demo", "pr", "a b.txt", "votes").Into("[0].by"),
		RefAt("demo", "pr", "1").Into("[0]"), // an array under the entity itself
	} {
		line := JoinStepsAt(at)
		want := at.KPath()
		for _, split := range []struct{ name, script string }{
			{"sh", `eval "set -- $1"; printf '%s\0' "$@"`},
			{"xargs", `printf '%s\n' "$1" | xargs -L1 sh -c 'printf "%s\0" "$@"' _`},
		} {
			out, err := exec.Command(sh, "-c", split.script, "_", line).Output()
			if err != nil {
				t.Errorf("%s could not split %s: %v", split.name, line, err)
				continue
			}
			words := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			got, err := KPathOf(words)
			if err != nil || got != want {
				t.Errorf("%s prints %s, which %s reads back as %q (%v), want %q",
					want, line, split.name, got, err, want)
			}
		}
	}
}
