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
// segments. A path carries it whole.
func TestAMatchMayNameAPathOfAnyDepth(t *testing.T) {
	m, err := ParseSteps([]string{"verse-dev", "drift", "head", "abc"})
	if err != nil {
		t.Fatalf("four steps: %v", err)
	}
	if got := m.Segs(); len(got) != 4 || got[3] != "abc" {
		t.Fatalf("Segs = %v", got)
	}
	if !m.MatchRef(entity.RefAt("verse-dev", "drift", "head", "abc")) {
		t.Error("the address it names does not match")
	}
	for _, other := range []entity.Ref{
		entity.RefAt("verse-dev", "drift", "head", "xyz"),    // a different leaf
		entity.RefAt("verse-dev", "drift", "finding", "abc"), // a sibling place
		entity.RefAt("verse-dev", "drift"),                   // shallower
	} {
		if m.MatchRef(other) {
			t.Errorf("%s matched a path that does not name it", other)
		}
	}

	// A wildcard is a POSITION: `*` means "any one segment, then the rest".
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

	// A shorter path covers what is under it: MatchRef is "at or under".
	prefix, _ := ParseSteps([]string{"verse-dev"})
	if !prefix.MatchRef(entity.RefAt("verse-dev", "drift", "head", "abc")) {
		t.Error("a one-segment path stopped covering what is under it")
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

	// The sibling place is a different path, and is not swept in by a wildcard at the leaf.
	all, err := ParseSteps([]string{"verse-dev", "drift", "head", "*"})
	if err != nil {
		t.Fatal(err)
	}
	pat, _ = all.pattern()
	if len(pat.Select(store)) != 2 {
		t.Errorf("a leaf wildcard selected %d, want the two under `head`", len(pat.Select(store)))
	}
}

// THE ID THAT IS AN ASTERISK is nameable without an escape of verse's own, which is what keeps
// `!raw` where it belongs — it is the STORE's escape, added when a charter is written down and
// consumed when it is read back, and a charter never writes one.
//
// A bare `*` is the wildcard and a quoted `"*"` is the name, which is the address grammar's own
// escape — the same characters `verse data -at 'demo.pr."*"'` reads. This pins that it survives
// being stored and read back.
func TestTheIDThatIsAnAsteriskIsNameable(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: literal-star
  condition:
    subject: 'demo.pr."*"' 
  action:
    patch: {seen: true}
`)); err != nil {
		t.Fatalf("install: %v", err)
	}

	second := reopened(t, store)
	back, ok := second.Rule("literal-star")
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
// address — so its tip fact and its ledger of findings cannot be two top-level places.
//
// With an address as deep as the tree they are two levels UNDER it, each with a stratum of its
// own, and the ordering that makes a derive finite still holds.
func TestAPackageKeepsTwoLevelsUnderOneAddress(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.Confine(SubtreeAt("verse-dev", "drift"))
	eng.SetReads([]Subtree{SubtreeAt("git")})

	for _, st := range []Stratum{
		{Name: "reflected", Level: 0, Match: []Match{mm("git")}},
		{Name: "tip", Level: 10, Match: []Match{mustPathMatch(t, `{path: "verse-dev.drift.head"}`)}},
		{Name: "ledger", Level: 20, Match: []Match{mustPathMatch(t, `{path: "verse-dev.drift.finding"}`)}},
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
    subject: git.head.verse
  action:
    derive:
    - path: "verse-dev.drift.head"
      value: {sha: .[subject.value.sha]}
`)); err != nil {
		t.Fatalf("the tip rule did not install: %v", err)
	}

	// The ledger: one level higher, under the same address — two places under one package.
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: gather
  stratum: tip
  condition:
    subject: {path: "verse-dev.drift.head.*"}
  action:
    derive:
    - path: "verse-dev.drift.finding"
      value: {noted: true}
`)); err != nil {
		t.Fatalf("the ledger rule did not install: %v", err)
	}

	// And the confinement still bites: the same charter may not write outside its address.
	err := eng.AddTrigger(mustParseCharter(t, `
- name: reach
  stratum: reflected
  condition:
    subject: git.head.verse
  action:
    derive:
    - path: "somebody-else.notes"
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

// A PATH READ AT DEPTH: `!get-path` and `!list-paths` name their address as `at:`, so a rule can
// read the entity a package keeps four segments down.
//
// The address is `at:` here and `path:` in a condition, and that is not an accident of taste: in
// a READ, `path:` is already the FIELD path inside the payload. Two things cannot both be
// `path`, and the field path is the one that was there first.
func TestAPathReadNamesItsAddressAtDepth(t *testing.T) {
	store := entitytest.Store(t)
	entitytest.Set(t, store, entity.RefAt("verse-dev", "drift", "head", "abc"),
		entity.Obj{"sha": "deadbeef", "reviewed": "done"}.Payload())
	entitytest.Set(t, store, entity.RefAt("verse-dev", "drift", "finding", "f1"),
		entity.Obj{"status": "open", "says": "a comment drifted"}.Payload())
	entitytest.Set(t, store, entity.RefAt("verse-dev", "drift", "finding", "f2"),
		entity.Obj{"status": "closed", "says": "fixed"}.Payload())

	snap, err := store.AsOf(store.Rev())
	if err != nil {
		t.Fatal(err)
	}

	// One node, four segments down, reaching a field of it.
	got, err := readOne(t, `!get-path {at: "verse-dev.drift.head.abc", path: sha}`, snap)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.String != "deadbeef" {
		t.Errorf("get answered %v, want the sha", got)
	}

	// Many, with a wildcard at the leaf and a payload predicate narrowing them.
	list, err := readOne(t, `!list-paths {at: "verse-dev.drift.finding.*", value: {status: open}, path: says}`, snap)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Values) != 1 || list.Values[0].String != "a comment drifted" {
		t.Errorf("list answered %v, want the one open finding", list)
	}

	// And a get whose leaf globs is refused, because a value slot takes one thing.
	if _, err := readOne(t, `!get-path {at: "verse-dev.drift.finding."f*"", path: says}`, snap); err == nil {
		t.Error("a get with a globbing leaf was accepted")
	}
}

// readOne parses one path-read expression and answers it against a snapshot.
func readOne(t *testing.T, src string, snap entity.Snapshot) (*ir.Node, error) {
	t.Helper()
	node, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}
	return resolveReadAt(node, nil, snap)
}

// A LIVE CHANGE four segments down has to wake a rule that selects there. The watch is opened at
// the literal head of the pattern (`verse-dev.drift.head`), and the store reports changes under
// it at the depth an address has always had — so what arrives is a change to the THREE-segment
// node, and the rule's subject names a four-segment one.
//
// This is the check that the two still meet. It writes the entity AFTER the engine is running,
// so nothing here is the level-triggered replay: it is the delta path or nothing.
func TestALiveChangeAtDepthWakesADeepRule(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: notice
  condition:
    subject: {path: "verse-dev.drift.head.*"}
    value: {reviewed: pending}
  action:
    patch: {reviewed: seen}
`)); err != nil {
		t.Fatalf("install: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("verse-dev", "drift", "head", "abc"),
		entity.Obj{"reviewed": "pending"}.Payload())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e, ok, _ := store.Get(entity.RefAt("verse-dev", "drift", "head", "abc"))
		if ok && entity.Str(e.Payload, "reviewed") == "seen" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a live change four segments down did not wake the rule that selects there")
}

// ONE PATTERN ASKED OF ANOTHER, segment by segment: a name contains and meets only itself, a `*`
// contains anything at its position, and two patterns meet where every position can. That is what
// keeps a stratum of one path and a stratum of another separable.
func TestAPathContainsOnlyWhatItMatches(t *testing.T) {
	name := mustPathMatch(t, `{path: "demo.abc"}`)
	for _, c := range []struct {
		inner    string
		contains bool
		overlaps bool
	}{
		{`{path: "demo.abc.x"}`, true, true},
		{`{path: "demo.b"}`, false, false},
		{`{path: "demo.*"}`, false, true},
		{`{path: "other.abc"}`, false, false},
	} {
		inner := mustPathMatch(t, c.inner)
		if got := matchContains(name, inner); got != c.contains {
			t.Errorf("demo.abc contains %s = %v, want %v", c.inner, got, c.contains)
		}
		if got := matchOverlap(name, inner); got != c.overlaps {
			t.Errorf("demo.abc overlaps %s = %v, want %v", c.inner, got, c.overlaps)
		}
	}
	// And the ordinary cases are what they were: `*` contains anything, a prefix contains what
	// extends it, and crossed wildcards meet.
	if !matchContains(mustPathMatch(t, `{path: "demo.*"}`), mustPathMatch(t, `{path: "demo.b.c"}`)) {
		t.Error("[demo, *] does not contain [demo, b, c]")
	}
	if !matchOverlap(mustPathMatch(t, `{path: "demo.*.x"}`), mustPathMatch(t, `{path: "*.pr.x"}`)) {
		t.Error("[demo, *, x] and [*, pr, x] do not meet at demo.pr.x")
	}
}
