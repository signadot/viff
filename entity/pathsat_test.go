package entity

import (
	"reflect"
	"sort"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// THE STORE'S LISTING AND THE DOCUMENT'S ANSWER THE SAME ADDRESSES, which is the whole claim of
// moving the walk: `--paths` stops shipping a subtree to print its names, and prints the same
// names. Doc.Paths is the oracle -- it is what the listing has always been -- so the test is a
// comparison rather than a table nobody can check against the old behaviour.
func TestPathsAtAnswersWhatTheDocumentWalkDoes(t *testing.T) {
	s := openStore(t)
	votes := ir.FromSlice([]*ir.Node{
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("scott"), "choice": ir.FromString("yes")}),
		ir.FromMap(map[string]*ir.Node{"voter": ir.FromString("ana"), "choice": ir.FromString("no")}),
	}).WithTag("!key(voter)")
	for _, w := range []Write{
		{At: RefAt("demo", "pr", "1").At(), Value: Obj{"stage": "open", "labels": Obj{"urgent": true}}.Payload()},
		{At: RefAt("demo", "pr", "2").At(), Value: Obj{"stage": "closed"}.Payload()},
		{At: RefAt("demo", "pr", "a.b").At(), Value: Obj{"stage": "open"}.Payload()},
		{At: RefAt("demo", "note").At(), Value: Obj{"text": "hi"}.Payload()},
		// An array, written as data so the `!key` annotation stands (doc.go: it is a merge
		// operator, and an ordinary write executes it).
		{At: RefAt("merge", "proposal", "X").At(), Value: AsData(ir.FromMap(map[string]*ir.Node{
			"votes": votes,
			"tags":  ir.FromSlice([]*ir.Node{ir.FromString("a"), ir.FromString("b")}),
		}))},
	} {
		if _, _, err := s.Write(w); err != nil {
			t.Fatalf("seed %s: %v", w.At, err)
		}
	}

	for _, tc := range []struct {
		at    []string
		depth int
	}{
		{[]string{"demo"}, 0},
		{[]string{"demo", "pr"}, 0},
		{[]string{"demo", "pr", "1"}, 0},
		{[]string{"merge", "proposal", "X"}, 0},
		{[]string{"merge"}, 0},
		{[]string{"demo"}, 2},
		{[]string{"demo"}, 3},
		{[]string{"merge"}, 4},
		{[]string{"nosuch"}, 0},
	} {
		got, err := s.PathsAt(tc.at, tc.depth)
		if err != nil {
			t.Fatalf("PathsAt(%v, %d): %v", tc.at, tc.depth, err)
		}
		node, ok, err := s.ReadAt(tc.at)
		if err != nil {
			t.Fatalf("ReadAt(%v): %v", tc.at, err)
		}
		var want []Path
		if ok {
			want = PathsUnder(node, tc.at, "", tc.depth)
		}
		if !reflect.DeepEqual(kpaths(got), kpaths(want)) {
			t.Errorf("PathsAt(%v, %d) =\n%v\nthe document walk answers\n%v",
				tc.at, tc.depth, kpaths(got), kpaths(want))
		}
	}
}

// AND EVERY ADDRESS IT ANSWERS IS ONE THE STORE READS, which is what a listing is for: a line
// goes back in as an address.
func TestPathsAtAnswersAddressesThatRead(t *testing.T) {
	s := openStore(t)
	if _, _, err := s.Write(Write{
		At:    RefAt("demo", "pr", "1").At(),
		Value: Obj{"stage": "open", "labels": Obj{"urgent": true}}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PathsAt([]string{"demo"}, 0)
	if err != nil {
		t.Fatalf("PathsAt: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the listing answered nothing")
	}
	for _, p := range got {
		if p.Access != "" {
			continue // an element is read through Reach, which ReadAt does not do
		}
		if _, ok, err := s.ReadAt(p.Ref.Segs()); err != nil || !ok {
			t.Errorf("%s was listed and reads as nothing (ok=%v, err=%v)", p.KPath(), ok, err)
		}
	}
}

func kpaths(ps []Path) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.KPath())
	}
	sort.Strings(out)
	return out
}
