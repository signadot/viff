package entity_test

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

func at(t *testing.T, s string) *kpath.KPath {
	t.Helper()
	p, err := entity.ParseAt(s)
	if err != nil {
		t.Fatalf("ParseAt(%q): %v", s, err)
	}
	return p
}

// LiteralPrefix says where a read at a path is asked of the store, which is the difference
// between reading a subtree and reading the viff instance and discarding it.
func TestLiteralPrefix(t *testing.T) {
	for _, c := range []struct {
		at     string
		prefix string
		rest   string
	}{
		{"demo.pr", "demo.pr", ""},
		{"demo.pr.1.labels.urgent", `demo.pr."1".labels.urgent`, ""},
		// A wildcard ends the prefix: nothing below it is a literal to read under.
		{"demo.pr.*.stage", "demo.pr", "*.stage"},
		{"demo.tags[0]", "demo.tags", "[0]"},
		{"*.pr", "", "*.pr"},
		{"", "", ""},
	} {
		prefix, rest := entity.LiteralPrefix(at(t, c.at))
		restStr := ""
		if rest != nil {
			restStr = rest.String()
		}
		if got := entity.JoinSegs(prefix); got != c.prefix || restStr != c.rest {
			t.Errorf("LiteralPrefix(%q) = %q, %q; want %q, %q", c.at, got, restStr, c.prefix, c.rest)
		}
	}
}

// A READ AT AN EMPTY PATH IS AN ANSWER, NOT AN OUTAGE.
//
// logd distinguishes them — not_found says the path is empty, a failure says the store could
// not be read — and viff folds the second into a 503. Recording the first as a read error
// made `viff data <a system nothing has written>` report the viff instance as unavailable, which
// only became reachable once reads went to their path instead of to the root.
func TestReadAtAnAbsentPathIsNotAnOutage(t *testing.T) {
	s := entitytest.OpenStore(t)
	entitytest.Set(t, s, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())

	if got := s.ListAt([]string{"other"}, 3, nil); len(got) != 0 {
		t.Errorf("ListAt at an absent path returned %d entities", len(got))
	}
	if err := s.ReadError(); err != nil {
		t.Errorf("an absent path left the store looking unreadable: %v", err)
	}
	// …and the store is still usable, which is the half a sticky error would break.
	if got := s.ListAt([]string{"demo", "pr"}, 3, nil); len(got) != 1 {
		t.Errorf("the read after an absent one gave %d entities; want 1", len(got))
	}
}
