package entity

import "testing"

// MatchesAt keeps its ABOVE half, and this pins it: a replay or watch reporting at a fixed arity
// delivers `demo.pr.1` for a read of `demo.pr.1.stage`, and dropping that before changes are
// reported at the question's depth would drop the events. Step 5 of the paths-and-nodes plan
// flips the second case, on purpose.
func TestMatchesAtAdmitsAnAncestorUntilChangesAreExact(t *testing.T) {
	at, err := ParseAt(`demo.pr."1".stage`)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ref  Ref
		want bool
	}{
		{RefAt("demo", "pr", "1", "stage"), true},
		{RefAt("demo", "pr", "1"), true}, // above: the fixed-arity event
		{RefAt("demo", "pr", "2"), false},
		{RefAt("demo", "pr", "1", "stage", "x"), true},
	} {
		if got := MatchesAt(c.ref, at); got != c.want {
			t.Errorf("MatchesAt(%s, %s) = %v, want %v", c.ref, at, got, c.want)
		}
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
		if got := Overlap(c.a, c.b, wildDisjoint); got != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
