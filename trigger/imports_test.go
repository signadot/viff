package trigger

import "testing"

// The graph is over PACKAGES and its edges are dependencies — P → Q when what P reads meets what
// Q writes, which is Q's own address. So the loop the confinement could not see is refused, and
// the shapes that are not loops still install.
func TestAnImportCycleIsFound(t *testing.T) {
	st := func(s string) Subtree {
		out, err := ParseAt(s)
		if err != nil {
			t.Fatalf("ParseAt(%q): %v", s, err)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		pkgs map[string][]Subtree
		want bool
	}{
		{"the two-package loop the issue names",
			map[string][]Subtree{"a": {st("b")}, "b": {st("a")}}, true},
		{"one that goes the long way round",
			map[string][]Subtree{"a": {st("b")}, "b": {st("c")}, "c": {st("a")}}, true},
		{"a package importing itself is not an edge",
			map[string][]Subtree{"a": {st("a")}}, false},
		{"a chain is not a cycle",
			map[string][]Subtree{"drift": {st("git")}, "merge": {st("git"), st("drift")}}, false},
		{"two packages reading one source share no edge",
			map[string][]Subtree{"a": {st("git")}, "b": {st("git")}}, false},
		{"a kind narrows: demo.pr and demo.issue do not meet",
			map[string][]Subtree{"demo.pr": {st("demo.issue")}, "demo.issue": {st("demo.note")}}, false},
		{"but a whole system covers the kinds under it",
			map[string][]Subtree{"demo.pr": {st("demo")}, "demo.issue": {st("demo.pr")}}, true},
	} {
		got := Cycle(tc.pkgs)
		if (got != "") != tc.want {
			t.Errorf("%s: Cycle = %q, want cycle=%v", tc.name, got, tc.want)
		}
	}

	// And it SHOWS the loop, because "there is a cycle" would send somebody to draw it by hand.
	if got := Cycle(map[string][]Subtree{"a": {st("b")}, "b": {st("a")}}); got != "a → b → a" {
		t.Errorf("the cycle does not read as a path: %q", got)
	}
}
