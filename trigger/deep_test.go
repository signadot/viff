package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
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

// A RULE WITH OPERATORS IN ITS ADDRESS HAS TO BE STORABLE, which is a different question from
// whether it parses: a charter record is a WRITE, a write is a tony patch, and a patch carrying
// `!glob` is executed rather than stored — the failure that has cost staging a wipe three times
// (entity/raw.go). The charter is persisted under `!raw` for exactly this reason; a path segment
// is one more place operators now appear, so this is the check that the escape covers it.
func TestARuleWithOperatorsInItsPathIsDurable(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: deep-glob
  condition:
    subject: !verse-path [verse-dev, drift, !glob "head-*", "*"]
  action:
    patch: {seen: true}
`)); err != nil {
		t.Fatalf("install: %v", err)
	}

	// It is in the store, and reading it back does not fail — which is what an unescaped
	// operator would have broken, for this entity and every later read of it.
	ent, ok, err := store.Get(CharterRef("deep-glob"))
	if err != nil || !ok {
		t.Fatalf("the rule was not written down: ok=%v err=%v", ok, err)
	}
	stored, err := codec.NodeBytes(ent.Payload, codec.Tony)
	if err != nil {
		t.Fatalf("rendering the stored rule: %v", err)
	}
	// The escape is CONSUMED by the read — `!raw` is an operation, and applying it is what
	// leaves the data behind — so what comes back is the document, glob and all.
	if !strings.Contains(string(stored), "!glob") {
		t.Errorf("the stored rule lost its operator:\n%s", stored)
	}

	// AND THE ESCAPE IS WHAT MADE THAT POSSIBLE: the same document written unescaped is refused,
	// because a write is a patch and `!glob` is not a patch operation. This is the check that
	// says the charter's `entity.Raw` is load-bearing for a path segment too, rather than
	// happening to work.
	spec := entity.Get(ent.Payload, "spec")
	if spec == nil {
		t.Fatal("the record has no spec")
	}
	if _, _, err := store.Write(entity.Write{
		At:    entity.RefAt("demo", "unescaped", "rule").At(),
		Value: spec,
	}); err == nil {
		t.Error("an unescaped charter document was accepted; the store would be unreadable after it")
	}

	// And it comes back as the rule it was: a fresh engine loading the durable charter selects
	// what the glob names and nothing else.
	second := New(store, task.AutoResolver{})
	if err := second.LoadCharter(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	back, ok := second.Trigger("deep-glob")
	if !ok {
		t.Fatal("the rule did not come back")
	}
	if !back.Condition.Subject.MatchRef(entity.RefAt("verse-dev", "drift", "head-main", "abc")) {
		t.Error("the restored rule lost its glob")
	}
	if back.Condition.Subject.MatchRef(entity.RefAt("verse-dev", "drift", "finding", "abc")) {
		t.Error("the restored rule matches past its glob")
	}
}

// THE ID THAT IS AN ASTERISK is nameable without an escape of verse's own, which is what keeps
// `!raw` where it belongs — it is the STORE's escape, added when a charter is written down and
// consumed when it is read back, and a charter never writes one.
//
// A bare `*` is the wildcard; inside an operator it is an ordinary character. So the grammar
// already says both things, and this pins that it survives being stored.
func TestTheIDThatIsAnAsteriskIsNameable(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: literal-star
  condition:
    subject: !verse-path [demo, pr, !glob "[*]"]
  action:
    patch: {seen: true}
`)); err != nil {
		t.Fatalf("install: %v", err)
	}

	second := New(store, task.AutoResolver{})
	if err := second.LoadCharter(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	back, ok := second.Trigger("literal-star")
	if !ok {
		t.Fatal("the rule did not come back")
	}
	if !back.Condition.Subject.MatchRef(entity.RefAt("demo", "pr", "*")) {
		t.Error("the restored rule no longer names the id that IS an asterisk")
	}
	if back.Condition.Subject.MatchRef(entity.RefAt("demo", "pr", "anything")) {
		t.Error("it reads as a wildcard now")
	}
}

// THE CASE THAT STARTED THIS. A charter installed at `verse-dev.drift` writes only inside that
// address — so its tip fact and its ledger of findings cannot be two SYSTEMS, and under the old
// two-level derive they could not be two kinds either: the address ended at `<system>.<kind>`
// and the confinement was the whole of it.
//
// With an address as deep as the tree they are two levels UNDER it, each with a stratum of its
// own, and the ordering that makes a derive finite still holds.
func TestAPackageKeepsTwoLevelsUnderOneAddress(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.Confine(SubtreeAt("verse-dev", "drift"))
	eng.SetImports([]Subtree{SubtreeAt("git")})

	for _, st := range []Stratum{
		{Name: "reflected", Level: 0, Match: []Match{{"system": "git"}}},
		{Name: "tip", Level: 10, Match: []Match{mustPathMatch(t, `{path: [verse-dev, drift, head]}`)}},
		{Name: "ledger", Level: 20, Match: []Match{mustPathMatch(t, `{path: [verse-dev, drift, finding]}`)}},
	} {
		if err := eng.AddStratum(st); err != nil {
			t.Fatalf("stratum %q: %v", st.Name, err)
		}
	}

	// The tip: reads the source it imports, writes one level under its own address.
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: head-moved
  stratum: reflected
  condition:
    subject: git:head:verse
  action:
    derive:
    - path: [verse-dev, drift, head]
      value: {sha: $subject.value.sha}
`)); err != nil {
		t.Fatalf("the tip rule did not install: %v", err)
	}

	// The ledger: one level higher, under the same address — which is what two kinds under one
	// package needs, and what a two-level derive could not express.
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: gather
  stratum: tip
  condition:
    subject: !verse-path [verse-dev, drift, head, "*"]
  action:
    derive:
    - path: [verse-dev, drift, finding]
      value: {noted: true}
`)); err != nil {
		t.Fatalf("the ledger rule did not install: %v", err)
	}

	// And the confinement still bites: the same charter may not write outside its address.
	err := eng.AddTrigger(mustParseCharter(t, `
- name: reach
  stratum: reflected
  condition:
    subject: git:head:verse
  action:
    derive:
    - path: [somebody-else, notes]
      value: {tampered: true}
`))
	if err == nil {
		t.Fatal("a derive outside the package's address installed")
	}
	// The level check happens to speak first — a derive with nowhere to land is refused before
	// anyone asks whose address it is — and either refusal is the right one. What matters is
	// that it NAMES THE ADDRESS, which a path rendering as a Go map did not.
	if !strings.Contains(err.Error(), "somebody-else") {
		t.Errorf("the refusal does not name the address it refused: %v", err)
	}
	if strings.Contains(err.Error(), "0x") {
		t.Errorf("the refusal renders pointers instead of an address: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())

	// The tip lands four segments deep, and the ledger lands beside it — the cascade crossing a
	// level inside one package.
	waitDeep(t, store, []string{"verse-dev", "drift", "head"}, 4)
	waitDeep(t, store, []string{"verse-dev", "drift", "finding"}, 4)
}

// waitDeep waits until something stands at a depth under an address.
func waitDeep(t *testing.T, store entity.Store, at []string, depth int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if len(store.ListAt(at, depth, nil)) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing stands under %v at depth %d", at, depth)
}

// mustPathMatch parses one match written in tony, for a test that wants a path pattern.
func mustPathMatch(t *testing.T, src string) Match {
	t.Helper()
	node, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}
	var m Match
	if err := m.FromTonyIR(node); err != nil {
		t.Fatalf("match %s: %v", src, err)
	}
	return m
}
