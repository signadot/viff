package trigger

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// AN ADDRESS AS DEEP AS THE TREE, which is what a charter installed at `verse-dev.drift` needs:
// its entities are `verse-dev.drift.head.<id>` and `verse-dev.drift.finding.<sym>`, four
// segments, and the three dimensions have nowhere to put the fourth.
//
// The `path:` spelling carries it whole. `{system, kind, id}` stays the reading of the first
// three, which is every address written before this.
func TestAMatchMayNameAPathOfAnyDepth(t *testing.T) {
	m, err := ParseSteps([]string{"verse-dev", "drift", "head", "abc"})
	if err != nil {
		t.Fatalf("four steps: %v", err)
	}
	if !m.Deep() {
		t.Fatal("four segments did not become a path")
	}
	if got := m.Segs(); len(got) != 4 || got[3] != "abc" {
		t.Fatalf("Segs = %v", got)
	}
	if !m.MatchRef(entity.RefAt("verse-dev", "drift", "head", "abc")) {
		t.Error("the address it names does not match")
	}
	for _, other := range []entity.Ref{
		entity.RefAt("verse-dev", "drift", "head", "xyz"),    // a different leaf
		entity.RefAt("verse-dev", "drift", "finding", "abc"), // a sibling kind
		entity.RefAt("verse-dev", "drift"),                   // shallower
	} {
		if m.MatchRef(other) {
			t.Errorf("%s matched a path that does not name it", other)
		}
	}

	// A wildcard is a POSITION, which is what a path needs and dimensions cannot say: an unset
	// `kind` means "and nothing below", where `*` means "any one segment, then the rest".
	star, err := ParseSteps([]string{"verse-dev", "drift", "*", "abc"})
	if err != nil {
		t.Fatalf("a wildcard step: %v", err)
	}
	for _, ref := range []entity.Ref{
		entity.RefAt("verse-dev", "drift", "head", "abc"),
		entity.RefAt("verse-dev", "drift", "finding", "abc"),
	} {
		if !star.MatchRef(ref) {
			t.Errorf("%s did not match the wildcard position", ref)
		}
	}
	if star.MatchRef(entity.RefAt("verse-dev", "drift", "head", "xyz")) {
		t.Error("the wildcard leaked past its own position")
	}

	// And the watch it opens is the path up to that wildcard — never the leaf, which would see
	// an entity's own changes and not its appearance.
	if got := subtreeOf(star).Path(); len(got) != 2 || got[0] != "verse-dev" || got[1] != "drift" {
		t.Errorf("the watch subtree is %v, want the path up to the wildcard", got)
	}
}

// The three-dimension spelling still reads as it always did, which is what every charter
// written so far holds.
func TestTheThreeDimensionsAreStillTheFirstThreeSegments(t *testing.T) {
	m, err := ParseSteps([]string{"demo", "pr", "1"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Deep() {
		t.Error("three segments became a path; they are dimensions")
	}
	if got := m.Segs(); len(got) != 3 || got[0] != "demo" || got[2] != "1" {
		t.Errorf("Segs = %v", got)
	}
	if !m.MatchRef(entity.RefAt("demo", "pr", "1")) || m.MatchRef(entity.RefAt("demo", "pr", "2")) {
		t.Error("the dimension reading changed")
	}
	// A system alone still means everything under it.
	sys, _ := ParseSteps([]string{"demo"})
	if !sys.MatchRef(entity.RefAt("demo", "pr", "1")) {
		t.Error("a system alone stopped covering what is under it")
	}
}

// THE WHOLE LOOP AT FOUR SEGMENTS, which is what a charter installed at `verse-dev.drift`
// needs: entities live at `verse-dev.drift.head.<id>`, a rule selects them by path, and the
// store answers at the depth the pattern asks for rather than at the three it always had.
func TestARuleSelectsEntitiesFourSegmentsDeep(t *testing.T) {
	store := entitytest.Store(t)
	deep := entity.RefAt("verse-dev", "drift", "head", "abc")
	entitytest.Set(t, store, deep, entity.Obj{"reviewed": "pending"}.Payload())
	entitytest.Set(t, store, entity.RefAt("verse-dev", "drift", "head", "xyz"),
		entity.Obj{"reviewed": "done"}.Payload())
	entitytest.Set(t, store, entity.RefAt("verse-dev", "drift", "finding", "abc"),
		entity.Obj{"status": "open"}.Payload())

	// Read back: a four-segment address is an ordinary read.
	if e, ok, err := store.Get(deep); err != nil || !ok || entity.Str(e.Payload, "reviewed") != "pending" {
		t.Fatalf("a four-segment entity did not read back: %v %v %v", e, ok, err)
	}

	// Select by path, at the pattern's own depth.
	m, err := ParseSteps([]string{"verse-dev", "drift", "head", "*"})
	if err != nil {
		t.Fatal(err)
	}
	m[mKeyValue] = ir.FromMap(map[string]*ir.Node{"reviewed": ir.FromString("pending")})
	pat, err := m.pattern()
	if err != nil {
		t.Fatal(err)
	}
	got := pat.Select(store)
	if len(got) != 1 || got[0] != deep {
		t.Fatalf("selected %v, want just %s — the predicate and the depth both apply", got, deep)
	}

	// The sibling kind is a different path, and is not swept in by a wildcard at the leaf.
	all, err := ParseSteps([]string{"verse-dev", "drift", "head", "*"})
	if err != nil {
		t.Fatal(err)
	}
	pat, _ = all.pattern()
	if len(pat.Select(store)) != 2 {
		t.Errorf("a leaf wildcard selected %d, want the two under `head`", len(pat.Select(store)))
	}
}

// A STEP MAY BE AN OPERATOR, at any position — `!verse-path [demo pr !glob "hot-*"]` — which is
// what the three-dimension spelling already allows per dimension (`{kind: !glob "k-*"}`).
//
// It used to be dropped on the way in. The failure was quiet in the worst way: a `!glob` at the
// ID position behaved, because an id containing `*` globs anyway, and the same operator one
// level along matched a segment literally named `head-*`. A pattern that reads as a glob and
// selects nothing is worse than one that is refused.
func TestAStepMayCarryAnOperator(t *testing.T) {
	store := entitytest.Store(t)
	for _, ref := range []entity.Ref{
		entity.RefAt("verse-dev", "drift", "head-main", "abc"),
		entity.RefAt("verse-dev", "drift", "head-next", "abc"),
		entity.RefAt("verse-dev", "drift", "finding", "abc"),
	} {
		entitytest.Set(t, store, ref, entity.Obj{"seen": true}.Payload())
	}

	node, err := codec.ParseNode([]byte(`{subject: !verse-path [verse-dev, drift, !glob "head-*", "*"]}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var c Condition
	if err := c.FromTonyIR(node); err != nil {
		t.Fatalf("an operator in a step was refused: %v", err)
	}

	if !c.Subject.MatchRef(entity.RefAt("verse-dev", "drift", "head-main", "abc")) {
		t.Error("the glob did not match what it names")
	}
	if c.Subject.MatchRef(entity.RefAt("verse-dev", "drift", "finding", "abc")) {
		t.Error("the glob matched past itself")
	}

	pat, err := c.Subject.pattern()
	if err != nil {
		t.Fatal(err)
	}
	if got := pat.Select(store); len(got) != 2 {
		t.Errorf("selected %v, want the two under a head-* level", got)
	}

	// ANY OPERATOR, not just a string-shaped one: `!or [hot, cold]` is a list, and a step that
	// had to have a text reading could not hold it at all.
	node, err = codec.ParseNode([]byte(`{subject: !verse-path [demo, pr, !or [hot, cold]]}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var either Condition
	if err := either.FromTonyIR(node); err != nil {
		t.Fatalf("a list-shaped operator in a step was refused: %v", err)
	}
	for _, id := range []string{"hot", "cold"} {
		if !either.Subject.MatchRef(entity.RefAt("demo", "pr", id)) {
			t.Errorf("%q is one of the two named", id)
		}
	}
	if either.Subject.MatchRef(entity.RefAt("demo", "pr", "warm")) {
		t.Error("the or matched what it does not name")
	}

	// And the watch it opens stops ABOVE the operator: a level that is constrained but not
	// named cannot narrow a subtree, so the watch is the literal head and the pattern does the
	// rest — which is what a `*` position does too.
	if got := subtreeOf(c.Subject).Path(); len(got) != 2 || got[1] != "drift" {
		t.Errorf("the watch subtree is %v, want the literal head of the path", got)
	}
}

// EACH ELEMENT OF A PATH IS A MATCH, in tony's own grammar, asked of that one segment — so the
// operators a payload predicate takes work at an address position too, with nothing here
// enumerating them.
//
// Two conveniences sit above that and only two: `*` is any segment, and `!raw` is the escape
// that says a segment is a NAME. Without the second, an id that happens to be `*` — or to be
// named after an operator — could not be addressed at all, which is a hole exactly where
// somebody's id is.
func TestEveryPathSegmentIsAMatch(t *testing.T) {
	for _, tc := range []struct {
		seg  string
		id   string
		want bool
	}{
		{`!not archived`, "live", true},
		{`!not archived`, "archived", false},
		{`!or [hot, cold]`, "cold", true},
		{`!or [hot, cold]`, "warm", false},
		{`!and [!glob "h*", !not hold]`, "hot", true},
		{`!and [!glob "h*", !not hold]`, "hold", false},
		{`1`, "1", true},     // an untagged scalar is the name it spells
		{`"*"`, "any", true}, // the wildcard, as everywhere else in an address
		{`!raw "*"`, "*", true},
		{`!raw "*"`, "any", false}, // …and escaped, it is the id that IS an asterisk
	} {
		node, err := codec.ParseNode([]byte(`{subject: !verse-path [demo, pr, `+tc.seg+`]}`), codec.Tony)
		if err != nil {
			t.Fatalf("%s: %v", tc.seg, err)
		}
		var c Condition
		if err := c.FromTonyIR(node); err != nil {
			t.Errorf("%s was refused: %v", tc.seg, err)
			continue
		}
		if got := c.Subject.MatchRef(entity.RefAt("demo", "pr", tc.id)); got != tc.want {
			t.Errorf("[demo pr %s] vs id %q = %v, want %v", tc.seg, tc.id, got, tc.want)
		}
	}
}
