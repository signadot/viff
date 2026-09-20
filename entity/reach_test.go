package entity

import "testing"

// A PATH REACHES THE NODES AT ITS OWN LENGTH and no others: not an ancestor, which is a
// different node, and not a descendant, which a read of the path answers inside the node it
// reaches. This is what a change stream is filtered by, now that a change is reported at the
// depth its observer asked about.
func TestAPathReachesItsOwnLength(t *testing.T) {
	at, err := ParseAt(`demo.pr."1".stage`)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ref  Ref
		want bool
	}{
		{RefAt("demo", "pr", "1", "stage"), true},
		{RefAt("demo", "pr", "1"), false},               // above: a different node
		{RefAt("demo", "pr", "1", "stage", "x"), false}, // beneath: inside the node it reaches
		{RefAt("demo", "pr", "2"), false},
	} {
		if got := Reaches(at, c.ref); got != c.want {
			t.Errorf("Reaches(%s, %s) = %v, want %v", at, c.ref, got, c.want)
		}
	}
	// An access into an array is a place INSIDE the node the names reach, so the path reaches
	// that node.
	inArray, err := ParseAt(`demo.pr."1".votes(scott)`)
	if err != nil {
		t.Fatal(err)
	}
	if !Reaches(inArray, RefAt("demo", "pr", "1", "votes")) {
		t.Error("a keyed access did not reach the node holding the array")
	}
}

// The walk under every "is A under B": a pattern no longer than the path, admitting each of its
// segments; an ancestor strictly shorter, admitted as far as it goes; and two patterns that meet.
func TestReachAndOverlap(t *testing.T) {
	for _, c := range []struct {
		pattern, path []string
		under, above  bool
	}{
		{nil, nil, true, false},                                            // the root, at the root
		{nil, []string{"demo"}, true, false},                               // everything is under the root
		{[]string{"demo", "pr"}, nil, false, true},                         // the root is above everything
		{[]string{"demo", "pr"}, []string{"demo", "pr"}, true, false},      // at
		{[]string{"demo", "pr"}, []string{"demo", "pr", "1"}, true, false}, // under
		{[]string{"demo", "pr", "1"}, []string{"demo", "pr"}, false, true}, // above
		{[]string{"demo", "*"}, []string{"demo", "pr", "1"}, true, false},
		{[]string{"demo", "*", "1"}, []string{"demo", "pr"}, false, true},
		{[]string{"demo", "*", "1"}, []string{"other"}, false, false},
		{[]string{"demo", "pr"}, []string{"demo", "prs"}, false, false},
		{[]string{"verse", "task"}, []string{"task", "instance"}, false, false}, // the root is a segment
		{[]string{"demo", "pr", "a.b"}, []string{"demo", "pr", "a.b"}, true, false},
		{[]string{"demo", "pr", "*"}, []string{"demo", "pr", "*"}, true, false}, // a `*` admits the name `*`
	} {
		if got := Under(c.pattern, c.path, Wild); got != c.under {
			t.Errorf("Under(%q, %q) = %v, want %v", c.pattern, c.path, got, c.under)
		}
		if got := Above(c.pattern, c.path, Wild); got != c.above {
			t.Errorf("Above(%q, %q) = %v, want %v", c.pattern, c.path, got, c.above)
		}
	}
	// A literal pattern admits only its own names: `*` there is a name.
	if Under([]string{"demo", "*"}, []string{"demo", "pr"}, Literal) {
		t.Error("a literal `*` admitted the name pr")
	}
	wildDisjoint := func(a, b string) bool { return a != "*" && b != "*" && a != b }
	for _, c := range []struct {
		a, b []string
		want bool
	}{
		{[]string{"demo", "*", "x"}, []string{"*", "pr", "x"}, true}, // crossed wildcards meet at demo.pr.x
		{[]string{"demo", "pr"}, []string{"demo", "pr", "1"}, true},  // a prefix reaches what extends it
		{[]string{"demo", "pr"}, []string{"demo", "issue"}, false},
		{[]string{"demo", "*", "x"}, []string{"*", "pr", "y"}, false},
		{nil, []string{"anything"}, true},
	} {
		if got := Overlap(c.a, c.b, wildDisjoint, NoDescent); got != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// stepDescends is the `descends` of a path written as steps: `..` is the descent, as it is
// everywhere else a step is a string (StepIsDescend).
func stepDescends(s string) bool { return s == ".." }

// TWO PATTERNS MEET THROUGH A DESCENT, which the positional walk could not see: it compared `..`
// against the name at that position and answered "no common address", so two strata could both
// own a node and install anyway. The walk fails toward ACCEPTING, which is why this is the case
// worth having.
func TestOverlapReadsADescent(t *testing.T) {
	wildDisjoint := func(a, b string) bool { return a != "*" && b != "*" && a != b }
	for _, c := range []struct {
		a, b []string
		want bool
		why  string
	}{
		// The case from the residual: the descent takes `drift` and the two meet.
		{[]string{"verse-dev", "..", "finding"}, []string{"verse-dev", "drift", "finding"}, true,
			"the descent takes one segment"},
		// Zero segments: `..` includes the position itself, as kpath's Matches has it.
		{[]string{"a", "..", "x"}, []string{"a", "x"}, true, "a descent takes none"},
		{[]string{"a", "..", "x"}, []string{"a", "b", "c", "x"}, true, "and takes several"},
		// The root disagrees, so the descent never gets a say.
		{[]string{"a", ".."}, []string{"b", "c"}, false, "disjoint at the first segment"},
		// A shorter pattern is an ancestor of what the longer reaches, descent or not.
		{[]string{"a", "b"}, []string{"a", "..", "z"}, true, "a.b is above a.b.c.z"},
		// A DESCENT REACHES PAST WHAT LOOKS LIKE A DISAGREEING TAIL, because a shorter pattern
		// is an ancestor of what a longer one reaches and governs it. `a.b.z` names a node, and
		// `a..y` reaches `a.b.z.y` INSIDE it — so the two do meet, on the same argument that
		// makes `a.b` meet `a..z`. Worth a row because the opposite reads as obvious.
		{[]string{"a", "..", "y"}, []string{"a", "b", "z"}, true, "a.b.z is above a.b.z.y"},
		{[]string{"..", "y"}, []string{"..", "z"}, true, "a.z is above a.z.y"},
		// What a descent does not do is cross a disagreement it stands after.
		{[]string{"a", "..", "y"}, []string{"b", "..", "y"}, false, "the roots are disjoint"},
		// Two descents facing each other.
		{[]string{"a", "..", "y"}, []string{"..", "y"}, true, "both descend to the same tail"},
		// A descent meets a wildcard run.
		{[]string{"a", "..", "z"}, []string{"a", "*", "*", "z"}, true, "the descent takes both stars"},
	} {
		if got := Overlap(c.a, c.b, wildDisjoint, stepDescends); got != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v — %s", c.a, c.b, got, c.want, c.why)
		}
		// Overlap is symmetric, and a walk that steps two sequences at once is where that
		// stops being obvious.
		if got := Overlap(c.b, c.a, wildDisjoint, stepDescends); got != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v — not symmetric", c.b, c.a, got, c.want)
		}
	}
}

// reachesByHand is the obvious reading of a pattern, written out again so the walk under test is
// checked against something of a different shape rather than against itself.
func reachesByHand(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == ".." {
		for n := 0; n <= len(path); n++ {
			if reachesByHand(pat[1:], path[n:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 || (pat[0] != "*" && pat[0] != path[0]) {
		return false
	}
	return reachesByHand(pat[1:], path[1:])
}

func prefixOf(p, q []string) bool {
	if len(p) > len(q) {
		return false
	}
	for i := range p {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// OVERLAP AGAINST AN ORACLE. Two patterns overlap when some address each reaches stand in an
// ancestor relation — which is what the doc comment says in words, and what a walk over two
// sequences at once is easy to get subtly wrong. So: enumerate every address over a small
// alphabet, read both patterns by hand, and require the walk to agree on every pair.
//
// The bound is chosen so a witness is never missed: a pattern no longer than three reaches
// its shortest address at three, and the ancestor relation needs no more than the longer of the
// two, so addresses to five cover every pair below.
func TestOverlapAgreesWithReadingBothPatterns(t *testing.T) {
	const maxPath = 5
	alphabet := []string{"a", "b"}

	var paths [][]string
	var walk func(cur []string)
	walk = func(cur []string) {
		paths = append(paths, append([]string(nil), cur...))
		if len(cur) == maxPath {
			return
		}
		for _, s := range alphabet {
			walk(append(cur, s))
		}
	}
	walk(nil)

	// Every pattern up to length three over the names, a wildcard and a descent.
	var pats [][]string
	var build func(cur []string)
	build = func(cur []string) {
		pats = append(pats, append([]string(nil), cur...))
		if len(cur) == 3 {
			return
		}
		for _, s := range []string{"a", "b", "*", ".."} {
			build(append(cur, s))
		}
	}
	build(nil)

	// The addresses each pattern reaches, once, so the pair loop is over answers.
	reach := make([][]int, len(pats))
	for i, p := range pats {
		for j, path := range paths {
			if reachesByHand(p, path) {
				reach[i] = append(reach[i], j)
			}
		}
	}

	wildDisjoint := func(a, b string) bool { return a != "*" && b != "*" && a != b }
	bad := 0
	for i, a := range pats {
		for j, b := range pats {
			want := false
			for _, x := range reach[i] {
				for _, y := range reach[j] {
					if prefixOf(paths[x], paths[y]) || prefixOf(paths[y], paths[x]) {
						want = true
						break
					}
				}
				if want {
					break
				}
			}
			if got := Overlap(a, b, wildDisjoint, stepDescends); got != want {
				if bad++; bad < 10 {
					t.Errorf("Overlap(%q, %q) = %v, want %v", a, b, got, want)
				}
			}
		}
	}
	if bad >= 10 {
		t.Errorf("…and %d more disagreements", bad-9)
	}
}
