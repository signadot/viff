package entity

import (
	"testing"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
)

// The precondition is asked at a path, and the value there is what the pattern is matched
// against — with an ABSENT path read as null (logd's tx.evaluateMatches). So a branch pushed
// down to a deeper path has to answer, at that path, what the nested one answered at the root
// for every state of the path above it. These pin that per branch kind, because the collapse
// is invisible: a pushed pattern that reads absence wrong does not error, it refuses writes.

func matches(t *testing.T, doc, pat *ir.Node) bool {
	t.Helper()
	ok, err := tony.Match(doc, pat)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	return ok
}

// descendablePath's table, asked at the parent's OWN path — the collapse that makes a write
// cost its slice instead of the store. The five columns are descendablePath's own.
func TestDescendableCollapsesAtTheParentsOwnPath(t *testing.T) {
	pat := descendablePath(nil)
	for _, c := range []struct {
		what string
		at   *ir.Node
		want bool
	}{
		{"an object: descend into it", ir.FromMap(map[string]*ir.Node{"a": ir.FromString("x")}), true},
		{"a null: nothing readable to lose", ir.Null(), true},
		{"absent, which reads as null", ir.Null(), true},
		{"an array: a descent would replace it", ir.FromSlice([]*ir.Node{ir.FromString("x")}), false},
		{"a scalar: a descent would replace it", ir.FromString("x"), false},
	} {
		if got := matches(t, c.at, pat); got != c.want {
			t.Errorf("%s: matched=%v, want %v", c.what, got, c.want)
		}
	}
}

// The same table through the nested spelling, so the two forms are held against each other
// rather than each against my reading of them.
func TestDescendableAgreesNestedAndCollapsed(t *testing.T) {
	nested := descendablePath([]string{"pr"})
	for _, c := range []struct {
		what   string
		parent *ir.Node
		want   bool
	}{
		{"an object", ir.FromMap(map[string]*ir.Node{"1": ir.FromString("x")}), true},
		{"a null", ir.Null(), true},
		{"an array", ir.FromSlice([]*ir.Node{ir.FromString("x")}), false},
		{"a scalar", ir.FromString("x"), false},
	} {
		doc := ir.FromMap(map[string]*ir.Node{"pr": c.parent})
		if got := matches(t, doc, nested); got != c.want {
			t.Errorf("nested, %s: matched=%v, want %v", c.what, got, c.want)
		}
	}
	// The absent column has no nested doc to build: the key is simply not there.
	if !matches(t, ir.FromMap(map[string]*ir.Node{}), nested) {
		t.Error("nested, an absent parent: refused, want admitted")
	}
}

// absentPath stops ONE SEGMENT SHORT of its own place, and this is why: from the key's own
// path a key holding null and a key that is not there are both null, so an absence check
// asked there would pass over an entity holding null and clobber it.
func TestAbsenceIsAskedFromTheObjectThatHoldsTheKey(t *testing.T) {
	pat := absentPath([]string{"1"})
	holder := func(v *ir.Node) *ir.Node { return ir.FromMap(map[string]*ir.Node{"1": v}) }
	if !matches(t, ir.FromMap(map[string]*ir.Node{}), pat) {
		t.Error("no key at all: refused, want admitted")
	}
	if !matches(t, ir.Null(), pat) {
		t.Error("an absent holder, which reads as null: refused, want admitted")
	}
	if matches(t, holder(ir.FromMap(map[string]*ir.Node{"stage": ir.FromString("open")})), pat) {
		t.Error("an entity stands there: admitted, want refused")
	}
	if matches(t, holder(ir.Null()), pat) {
		t.Error("an entity holding NULL stands there: admitted, want refused — a null payload is a value")
	}
}

// The floor per branch kind, and the prefix that falls out of them: a plain entity write
// carries one guard about its parent and is asked at the parent, not at the root.
func TestTheCommonFloorIsTheDeepestEveryBranchAllows(t *testing.T) {
	parent := RefAt("demo", "pr", "").At()
	entity := RefAt("demo", "pr", "1").At()

	if got := commonFloor([]pathPattern{{at: parent, object: true}}); len(got) != 2 ||
		got[0] != "demo" || got[1] != "pr" {
		t.Errorf("a descends guard alone: prefix %v, want [demo pr] — asked at the slice", got)
	}
	// An absence about the entity stops one short of it, and pins the prefix there.
	got := commonFloor([]pathPattern{{at: parent, object: true}, {at: entity, absent: true}})
	if len(got) != 2 || got[1] != "pr" {
		t.Errorf("a descends guard and an absence: prefix %v, want [demo pr]", got)
	}
	// Two branches in different kinds fall back to what they share.
	got = commonFloor([]pathPattern{
		{at: RefAt("demo", "pr", "1").At(), absent: true},
		{at: RefAt("demo", "task", "9").At(), absent: true},
	})
	if len(got) != 1 || got[0] != "demo" {
		t.Errorf("two kinds: prefix %v, want [demo]", got)
	}
	// And nothing shared means the root, as before.
	got = commonFloor([]pathPattern{
		{at: RefAt("demo", "pr", "1").At(), absent: true},
		{at: RefAt("other", "task", "9").At(), absent: true},
	})
	if len(got) != 0 {
		t.Errorf("two systems: prefix %v, want none", got)
	}
}
