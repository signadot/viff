package entity_test

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

func at(t *testing.T, s string) *kpath.KPath {
	t.Helper()
	p, err := entity.ParseAt(s)
	if err != nil {
		t.Fatalf("ParseAt(%q): %v", s, err)
	}
	return p
}

// AtPrefix says how narrowly a read at a path may be asked, which is the difference between
// reading a subtree and reading the verse and discarding it.
func TestAtPrefix(t *testing.T) {
	for _, c := range []struct {
		at     string
		prefix []string
		depth  int
		ok     bool
	}{
		{"demo.pr", []string{"demo", "pr"}, 3, true},
		{"demo.pr.1", []string{"demo", "pr", "1"}, 3, true},
		// Deeper than an entity: the entity is still demo:pr:1, and Nest trims the rest.
		{"demo.pr.1.labels.urgent", []string{"demo", "pr", "1"}, 3, true},
		// A wildcard ends the prefix — nothing below it is a literal to read under.
		{"demo.pr.*.stage", []string{"demo", "pr"}, 3, true},
		// Verse's own half is one deeper, and the root counts as a segment.
		{"verse.gate.question", []string{"verse", "gate", "question"}, 4, true},
		// Naming nothing spans both halves, whose entities sit at different depths: no single
		// ListAt answers that, so the caller must fall back to the root read.
		{"", nil, 0, false},
	} {
		prefix, depth, ok := entity.AtPrefix(at(t, c.at))
		if ok != c.ok || depth != c.depth || len(prefix) != len(c.prefix) {
			t.Errorf("AtPrefix(%q) = %v, %d, %v; want %v, %d, %v",
				c.at, prefix, depth, ok, c.prefix, c.depth, c.ok)
			continue
		}
		for i := range prefix {
			if prefix[i] != c.prefix[i] {
				t.Errorf("AtPrefix(%q) = %v; want %v", c.at, prefix, c.prefix)
				break
			}
		}
	}
}

// A READ AT AN EMPTY PATH IS AN ANSWER, NOT AN OUTAGE.
//
// logd distinguishes them — not_found says the path is empty, a failure says the store could
// not be read — and verse folds the second into a 503. Recording the first as a read error
// made `verse data <a system nothing has written>` report the verse as unavailable, which
// only became reachable once reads went to their path instead of to the root.
func TestReadAtAnAbsentPathIsNotAnOutage(t *testing.T) {
	s := entitytest.Store(t)
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
