package entity

import (
	"sort"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// patternAt is a pattern reaching these path segments, each one a literal or `*`.
func patternAt(segs ...string) Pattern {
	path := make([]*ir.Node, len(segs))
	for i, s := range segs {
		path[i] = ir.FromString(s)
	}
	return Pattern{Path: path}
}

// selected is a selection as its sorted renderings, for a message and a comparison.
func selected(refs []Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.String())
	}
	sort.Strings(out)
	return out
}

// Viff's own half is a place in the tree like any other: a pattern whose path begins with
// `viff` selects there, and a pattern whose path does not begin with it does not reach it. The
// same segments on either side of the reserved root are different addresses, so a selection
// over one must not answer with the other — reading viff's own records as the world's is how a
// rule would act on a task record somebody's fixture happens to share segments with.
func TestSelectReachesTheHalfItsPathNames(t *testing.T) {
	s := openStore(t)
	world := RefAt("task", "instance", "w1")
	own := RefAt(ViffRoot, "task", "instance", "v1")
	set(t, s, world, Obj{"stage": "open"}.Payload())
	set(t, s, own, Obj{"stage": "open"}.Payload())
	// A neighbour in another place, which neither selection may reach.
	set(t, s, RefAt("other", "instance", "x"), Obj{"stage": "open"}.Payload())

	if got := selected(patternAt("task", "instance", "*").Select(s)); len(got) != 1 || got[0] != world.String() {
		t.Errorf("task.instance.* selected %v, want only the world's %s", got, world)
	}
	if got := selected(patternAt(ViffRoot, "task", "instance", "*").Select(s)); len(got) != 1 || got[0] != own.String() {
		t.Errorf("mirror.task.instance.* selected %v, want only viff's own %s", got, own)
	}
}

// A wildcard stands for one segment at its level and nothing more: `task.*.*` selects the nodes
// three deep under `task`, whatever the middle segment, and stops at `task` — it does not reach
// viff's own `task` records, which are four deep and under another first segment.
func TestSelectWildcardsStayInTheirPlace(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("task", "instance", "w1"), Obj{"a": "1"}.Payload())
	set(t, s, RefAt("task", "run", "w2"), Obj{"a": "1"}.Payload())
	set(t, s, RefAt(ViffRoot, "task", "run", "v1"), Obj{"a": "1"}.Payload())
	set(t, s, RefAt("other", "instance", "x"), Obj{"a": "1"}.Payload())

	got := selected(patternAt("task", "*", "*").Select(s))
	if len(got) != 2 || got[0] != RefAt("task", "instance", "w1").String() ||
		got[1] != RefAt("task", "run", "w2").String() {
		t.Fatalf("task.*.* selected %v, want the two world task entities and nothing else", got)
	}
}

// A pattern with no path selects nothing: a match that lost its address must not quietly become a
// sweep of the whole tree.
func TestSelectWithNoPathSelectsNothing(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("task", "instance", "w1"), Obj{"a": "1"}.Payload())
	if got := (Pattern{}).Select(s); len(got) != 0 {
		t.Fatalf("a pattern with no path selected %v", got)
	}
}
