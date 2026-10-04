package trigger

import (
	"reflect"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
)

// A Match round-trips without losing meaning: whatever the authored form, re-reading the
// marshalled output yields the same path and predicate. An address with no predicate renders as
// the kinded path it is, so `trigger show` displays the path the matcher will actually apply.
//
// Authored in JSON, rendered in tony — which is the shape of every real round trip now.
func TestMatchJSONRoundTrip(t *testing.T) {
	for _, in := range []string{
		`"demo.pr.1"`,
		`{"path":"demo.pr.*","value":{"stage":"open"}}`,
		`{"path":"demo.pr.*"}`,
		`{"path":"verse.task.instance.*"}`,
		`{"path":"verse.task.instance.*","value":{"status":"done"}}`,
		`{}`,
	} {
		var m Match
		if err := codec.Unmarshal([]byte(in), &m, codec.JSON); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		out, err := codec.Marshal(m, codec.Tony)
		if err != nil {
			t.Fatalf("marshal %s: %v", in, err)
		}
		var back Match
		if err := codec.Unmarshal(out, &back, codec.Tony); err != nil {
			t.Fatalf("re-unmarshal %s → %s: %v", in, out, err)
		}
		if !reflect.DeepEqual(back.Segs(), m.Segs()) {
			t.Fatalf("round-trip changed the path: %s → %s (%q vs %q)", in, out, m.Segs(), back.Segs())
		}
		if !sameFields(back.Predicate(), m.Predicate()) {
			t.Fatalf("round-trip lost fields: %s → %s", in, out)
		}
	}
}

// sameFields compares two payload predicates. They are nodes, so they are compared as
// nodes: marshalling them to text would go through codec.Marshal, which panics on a typed
// nil *ir.Node — and "no predicate" is exactly the case a round-trip test must handle.
func sameFields(a, b *ir.Node) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.DeepEqual(b)
}

// mm parses an address into a Match — the test spelling of the string sugar.
func mm(s string) Match {
	m, err := ParseExpr(s)
	if err != nil {
		panic(err)
	}
	return m
}

// pathOf is a path of literal segments (`*` any one), as a Match's `path` holds it — for a Go
// literal that also carries a `value`.
func pathOf(segs ...string) []*ir.Node {
	out := make([]*ir.Node, 0, len(segs))
	for _, s := range segs {
		out = append(out, ir.FromString(s))
	}
	return out
}

// isPath reports whether a match's path is the one an address string spells, operators read as
// `*` (Segs).
func isPath(m Match, addr string) bool { return reflect.DeepEqual(m.Segs(), mm(addr).Segs()) }

// Selects() reports a set anchored at a place: a path whose first segment is a literal. A path
// opening on `*` is a sweep from the root rather than a selection.
func TestSelects(t *testing.T) {
	for _, in := range []string{"demo.pr.*", "demo.*.*", "demo.pr.1"} {
		if !mm(in).Selects() {
			t.Errorf("%s must be a set-valued selector", in)
		}
	}
	if mm("*.pr.*").Selects() {
		t.Fatal("a path opening on `*` must not be a set-valued selector")
	}
}

// The string sugar is an ADDRESS, in the same grammar `verse flow <addr>` uses: a kinded path,
// a whole `*` segment any one. There is no deferred `$var` form: a `$` in an address is a typo
// now, and fails at install.
func TestStringFormIsAnAddress(t *testing.T) {
	cases := map[string][]string{
		`"demo.pr.1"`: {"demo", "pr", "1"},
		`"demo.pr.*"`: {"demo", "pr", "*"},
		`"demo.*.*"`:  {"demo", "*", "*"},
		`"demo.pr"`:   {"demo", "pr"}, // a short path names the node at it
		`"*"`:         {"*"},
		// A `*` INSIDE a segment is part of the name: a glob is `!glob` in `path:`.
		`"demo.pr.hotfix-*"`: {"demo", "pr", "hotfix-*"},
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			var m Match
			if err := codec.Unmarshal([]byte(in), &m, codec.JSON); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := m.Segs(); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s parsed to %q, want %q", in, got, want)
			}
			for i, n := range m.SegNodes() {
				if n.Tag != "" {
					t.Fatalf("%s: segment %d carries tag %q; a string address has no operators", in, i, n.Tag)
				}
			}
		})
	}
	// An empty or half-written address, or a leftover $var, is a typo, not a wildcard: it must
	// fail at install rather than silently matching nothing (or, for a subject, creating "*").
	for _, bad := range []string{`""`, `".pr.1"`, `"$flow"`, `"demo:pr:$id"`} {
		var m Match
		if err := codec.Unmarshal([]byte(bad), &m, codec.JSON); err == nil {
			t.Errorf("accepted %s (parsed as %#v)", bad, m)
		}
	}
}

// REGRESSION: a string-form `when` used to become a {"ref": …}, which no matcher dimension
// reads — so `"when": "demo.pr.1"` was an unconstrained match that fired on EVERY event in
// the verse. It must now constrain exactly the address it names.
func TestStringFormWhenConstrains(t *testing.T) {
	var m Match
	if err := codec.Unmarshal([]byte(`"demo.pr.1"`), &m, codec.JSON); err != nil {
		t.Fatal(err)
	}
	named := entity.Event{Ref: entity.RefAt("demo", "pr", "1"), Op: entity.Set}
	other := entity.Event{Ref: entity.RefAt("totally", "unrelated", "9"), Op: entity.Set}
	if !m.MatchEvent(named) {
		t.Error(`"demo.pr.1" did not match demo.pr.1`)
	}
	if m.MatchEvent(other) {
		t.Error(`"demo.pr.1" matched an unrelated event — the string form is not constraining`)
	}
}

// Names() is what separates the one case a Subject may bring into existence from a selection
// over what already exists. A `*` or a `..` in any segment must fall to the selection side, so an
// action can never `set` a node literally called "*".
func TestMatchNames(t *testing.T) {
	yes := map[string]entity.Ref{
		`{path: 'demo.pr."1"'}`: entity.RefAt("demo", "pr", "1"),
		`"demo.pr.1"`:           entity.RefAt("demo", "pr", "1"),
		// A literal path names one node at whatever length it has.
		`demo.pr`:                           entity.RefAt("demo", "pr"),
		`{path: "verse.task.instance.abc"}`: entity.RefAt("verse", "task", "instance", "abc"),
	}
	for in, want := range yes {
		var m Match
		if err := codec.Unmarshal([]byte(in), &m, codec.Tony); err != nil {
			t.Fatal(err)
		}
		got, ok := m.Names()
		if !ok || got != want {
			t.Errorf("%s: Names() = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	no := []string{
		`"demo.pr.*"`,          // the prs
		`{path: "demo.pr.*"}`,  // the same, as a path
		`{path: 'demo.*."1"'}`, // a wildcard above the leaf
		// …and neither is a match that says what the node must LOOK like. Naming is by
		// identity alone, because that is the case whose answer does not depend on the store.
		`{path: 'demo.pr."1"', value: {stage: open}}`,
	}
	for _, in := range no {
		var m Match
		if err := codec.Unmarshal([]byte(in), &m, codec.Tony); err != nil {
			t.Fatal(err)
		}
		if got, ok := m.Names(); ok {
			t.Errorf("%s: Names() = %v, true; must not name a single node", in, got)
		}
	}
}

// A typo'd charter must fail loudly at parse rather than silently matching nothing.
func TestMatchRejectsBadShape(t *testing.T) {
	// A predicate of the wrong SHAPE is NOT here, and deliberately: `value` takes any tony
	// value, so `{"value":"stage=open"}` is a legal predicate on a string payload rather than
	// a typo verse can name. See TestMatchValueScalarIsTheWholePayload.
	cases := map[string]string{
		"unknown key":      `{"sytem":"demo"}`,
		"a path as a list": `{"path":["demo","pr","*"]}`,
		"an empty path":    `{"path":""}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			var m Match
			if err := codec.Unmarshal([]byte(in), &m, codec.JSON); err == nil {
				t.Fatalf("accepted %s (parsed as %#v) — a bad match must not parse", in, m)
			}
		})
	}
}

// AN ADDRESS IS A PATH, written as the kinded path string or as `path:` — the two mean one thing.
func TestAnAddressIsAPath(t *testing.T) {
	want := []string{"verse", "ask", "*"}
	for _, src := range []string{
		`"verse.ask.*"`,
		`{path: "verse.ask.*"}`,
	} {
		var m Match
		if err := codec.Unmarshal([]byte(src), &m, codec.Tony); err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if got := m.Segs(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s → %q, want %q", src, got, want)
		}
	}
}

// Every positional spelling of an address is REFUSED, not compiled to a path: the dimension keys,
// `at:`, `!verse-path`, and a bare list. The refusal names the key so a charter carrying one says
// what to write instead.
func TestPositionalAddressSpellingsAreRefused(t *testing.T) {
	for _, src := range []string{
		`{system: demo, kind: pr}`,
		`{system: demo}`,
		`{kind: pr}`,
		`{id: "1"}`,
		`{root: verse, path: "task.instance.*"}`,
		`{at: demo.pr.1}`,
		`{at: !verse-path [demo pr 1]}`,
		`!verse-path [demo pr 1]`,
		`[demo, pr, 1]`,
	} {
		var m Match
		err := codec.Unmarshal([]byte(src), &m, codec.Tony)
		if err == nil {
			t.Errorf("%s installed as %v, want a refusal", src, m)
			continue
		}
		if !strings.Contains(err.Error(), "path") {
			t.Errorf("%s: refusal %q does not say an address is a path", src, err)
		}
	}
	// A Go literal carrying a dimension key fails Validate the same way.
	if err := (Match{"system": "demo", "kind": "pr"}).Validate(); err == nil {
		t.Error("Validate accepted a match carrying system/kind")
	}
}

// FieldString reads one literal string back out of the predicate — what the quorum machinery
// asks of the facts match it built itself ("which choice does this count"). A predicate that
// is not a record has no key to read, and an operator has no single value behind it, so both
// answer "" rather than a guess.
func TestMatchFieldString(t *testing.T) {
	m := Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}
	if got := m.FieldString("stage"); got != "open" {
		t.Errorf("FieldString(stage) = %q, want open", got)
	}
	if got := m.FieldString("absent"); got != "" {
		t.Errorf("FieldString of an absent key = %q, want empty", got)
	}
	scalar := Match{"path": pathOf("demo", "*", "*"), "value": "open"}
	if got := scalar.FieldString("stage"); got != "" {
		t.Errorf("FieldString of a scalar predicate = %q, want empty", got)
	}
}

// MatchEvent is the EDGE face: every segment of the path and the predicate must agree with one
// observed change, and an empty match matches everything (as an empty pattern list did).
func TestMatchEventPath(t *testing.T) {
	ev := entity.Event{
		Ref: entity.RefAt("demo", "pr", "1"),
		Op:  entity.Set, To: entity.Obj{"stage": "open"}.Payload(),
	}
	yes := []Match{
		{},
		mm("demo.*.*"),
		mm("demo.pr.*"),
		mm("demo.pr.1"),
		mm("*.pr.*"),
		{"value": map[string]any{"stage": "open"}},
	}
	no := []Match{
		mm("other.*.*"),
		mm("*.issue.*"),
		mm("*.*.2"),
		{"value": map[string]any{"stage": "closed"}},
		mm("demo.pr.2"),   // one wrong segment fails the whole path
		mm("demo.pr.1.x"), // a path deeper than the change does not reach it
	}
	for _, m := range yes {
		if !m.MatchEvent(ev) {
			t.Errorf("%v should match %s", m, ev.Ref)
		}
	}
	for _, m := range no {
		if m.MatchEvent(ev) {
			t.Errorf("%v should NOT match %s", m, ev.Ref)
		}
	}
}

// Select is the SET face: the nodes the path reaches whose payload satisfies the predicate.
func TestMatchSelect(t *testing.T) {
	store := entitytest.Store(t)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "2"), entity.Obj{"stage": "reviewed"}.Payload())
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "3"), entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, entity.RefAt("other", "pr", "1"), entity.Obj{"stage": "open"}.Payload())

	count := func(m Match) int { return len(m.Select(store)) }

	if n := count(mm("demo.pr.*")); n != 3 {
		t.Errorf("the prs: %d, want 3", n)
	}
	if n := count(mm("demo.pr.2")); n != 1 {
		t.Errorf("one node: %d, want 1", n)
	}
	if n := count(Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}); n != 2 {
		t.Errorf("payload predicate: %d, want 2", n)
	}
	// The path pins the slice: a same-named node under another first segment is not picked.
	refs := Match{"path": pathOf("demo", "pr", "1"), "value": map[string]any{"stage": "open"}}.Select(store)
	if len(refs) != 1 || refs[0] != entity.RefAt("demo", "pr", "1") {
		t.Errorf("selection crossed first segments: %v", refs)
	}
	// An invalid match selects nothing rather than everything — fail closed.
	if n := count(Match{"sytem": "demo"}); n != 0 {
		t.Errorf("invalid match selected %d entities; must fail closed", n)
	}
}

// The match-key namespace and the retired formula tags must stay disjoint. A single-key
// object whose key is a retired tag is refused by name (Formula.FromTonyIR), so a match
// key that shared a name with one would be refused as a formula instead of read.
func TestMatchKeysDisjointFromFormulaTags(t *testing.T) {
	var overlap []string
	for k := range matchKeys {
		if retiredFormulaTags[k] {
			overlap = append(overlap, k)
		}
	}
	if len(overlap) != 0 {
		t.Fatalf("match keys overlap the retired formula tags at %v; want none — a key "+
			"by that name would be refused as a formula", overlap)
	}
}

// sameDoc compares two documents ignoring object key order, which no wire format
// guarantees and no reader depends on.
func sameDoc(a, b *ir.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Type != b.Type || a.Tag != b.Tag {
		return false
	}
	switch a.Type {
	case ir.ObjectType:
		if len(a.Fields) != len(b.Fields) {
			return false
		}
		for i, k := range a.Fields {
			other := ir.Get(b, k.String)
			if other == nil || !sameDoc(a.Values[i], other) {
				return false
			}
		}
		return true
	case ir.ArrayType:
		if len(a.Values) != len(b.Values) {
			return false
		}
		for i := range a.Values {
			if !sameDoc(a.Values[i], b.Values[i]) {
				return false
			}
		}
		return true
	default:
		return a.DeepEqual(b)
	}
}
