package trigger

import (
	"reflect"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
)

// A Match round-trips without losing meaning: whatever the authored form, re-reading the
// marshalled output yields the same dimensions. A string address becomes the object form it
// parses to, so `trigger show` displays the system/kind/id the matcher will actually apply.
//
// Authored in JSON, rendered in tony — which is the shape of every real round trip now.
func TestMatchJSONRoundTrip(t *testing.T) {
	for _, in := range []string{
		`"demo:pr:1"`,
		`{"system":"demo","kind":"pr","value":{"stage":"open"}}`,
		`{"system":"demo","kind":"pr","id":"*"}`,
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
		// MEANING, not shape. A match naming only system/kind/id marshals to the address
		// string, and `id: "*"` is the same thing as an unset id — a wildcard — so
		// {"system":"demo","kind":"pr","id":"*"} comes back as demo:pr:* and re-parses with
		// the id dropped. The dimensions the matcher applies are what has to survive.
		if back.System() != m.System() || back.Kind() != m.Kind() {
			t.Fatalf("round-trip lost system/kind: %s → %s", in, out)
		}
		if wildcard(back.ID()) != wildcard(m.ID()) {
			t.Fatalf("round-trip changed the id dimension: %s → %s (%q vs %q)",
				in, out, m.ID(), back.ID())
		}
		if !sameFields(back.Predicate(), m.Predicate()) {
			t.Fatalf("round-trip lost fields: %s → %s", in, out)
		}
	}
}

// wildcard normalises the two spellings of "any id": unset and "*".
func wildcard(id string) string {
	if id == "*" {
		return ""
	}
	return id
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

// Selects() reports a set-valued pattern: a definite system AND kind (the engine must know the
// kind it spawns for). A system alone is not enough.
func TestSelects(t *testing.T) {
	if !(Match{"system": "demo", "kind": "pr"}).Selects() {
		t.Fatal("system+kind must be a set-valued selector")
	}
	// A system alone cannot select: the engine must know the kind it spawns for.
	if (Match{"system": "demo"}).Selects() {
		t.Fatal("system without kind must not be a set-valued selector")
	}
}

// The string sugar is an ADDRESS, in the same grammar `verse flow <addr>` uses. There is no
// deferred `$var` form: a `$` in an address is a typo now, and fails at install.
func TestStringFormIsAnAddress(t *testing.T) {
	cases := map[string]Match{
		`"demo:pr:1"`:        {"system": "demo", "kind": "pr", "id": "1"},
		`"demo:pr:*"`:        {"system": "demo", "kind": "pr"},
		`"demo:pr:hotfix-*"`: {"system": "demo", "kind": "pr", "id": "hotfix-*"},
		`"demo:*:*"`:         {"system": "demo"},
		`"demo:pr"`:          {"system": "demo", "kind": "pr"},
		`"*"`:                {},
		// An id may itself contain colons, so only the first two separators are structural.
		`"github:pr:org/repo#42"`: {"system": "github", "kind": "pr", "id": "org/repo#42"},
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			var m Match
			if err := codec.Unmarshal([]byte(in), &m, codec.JSON); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(map[string]any(m), map[string]any(want)) {
				t.Fatalf("%s parsed to %#v, want %#v", in, m, want)
			}
		})
	}
	// An empty or half-written address, or a leftover $var, is a typo, not a wildcard: it must
	// fail at install rather than silently matching nothing (or, for a subject, creating "*").
	for _, bad := range []string{`""`, `"demo::1"`, `":pr:1"`, `"$flow"`, `"demo:pr:$id"`} {
		var m Match
		if err := codec.Unmarshal([]byte(bad), &m, codec.JSON); err == nil {
			t.Errorf("accepted %s (parsed as %#v)", bad, m)
		}
	}
}

// REGRESSION: a string-form `when` used to become a {"ref": …}, which no matcher dimension
// reads — so `"when": "demo:pr:1"` was an unconstrained match that fired on EVERY event in
// the verse. It must now constrain exactly the address it names.
func TestStringFormWhenConstrains(t *testing.T) {
	var m Match
	if err := codec.Unmarshal([]byte(`"demo:pr:1"`), &m, codec.JSON); err != nil {
		t.Fatal(err)
	}
	named := entity.Event{Ref: entity.Ref{System: "demo", Kind: "pr", ID: "1"}, Op: entity.Set}
	other := entity.Event{Ref: entity.Ref{System: "totally", Kind: "unrelated", ID: "9"}, Op: entity.Set}
	if !m.MatchEvent(named) {
		t.Error(`"demo:pr:1" did not match demo:pr:1`)
	}
	if m.MatchEvent(other) {
		t.Error(`"demo:pr:1" matched an unrelated event — the string form is not constraining`)
	}
}

// Names() is what separates the one case a Subject may bring into existence from a selection
// over what already exists. A glob anywhere must fall to the selection side, so an action can
// never `set` an entity literally called "*".
func TestMatchNames(t *testing.T) {
	yes := map[string]entity.Ref{
		`{"system":"demo","kind":"pr","id":"1"}`: {System: "demo", Kind: "pr", ID: "1"},
		`"demo:pr:1"`:                            {System: "demo", Kind: "pr", ID: "1"},
	}
	for in, want := range yes {
		var m Match
		if err := codec.Unmarshal([]byte(in), &m, codec.JSON); err != nil {
			t.Fatal(err)
		}
		got, ok := m.Names()
		if !ok || got != want {
			t.Errorf("%s: Names() = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	no := []string{
		`"demo:pr:*"`,                                   // the kind
		`"demo:pr:hotfix-*"`,                            // a family
		`{"system":"demo","kind":"pr"}`,                 // no id
		`{"system":"demo","kind":"pr","id":"*"}`,        // explicit wildcard
		`{"system":"demo","kind":"pr","id":"hotfix-*"}`, // a glob is never a name
	}
	for _, in := range no {
		var m Match
		if err := codec.Unmarshal([]byte(in), &m, codec.JSON); err != nil {
			t.Fatal(err)
		}
		if got, ok := m.Names(); ok {
			t.Errorf("%s: Names() = %v, true; must not name a single entity", in, got)
		}
	}
}

// The id glob, which is what makes "demo:pr:*" mean the kind and "hotfix-*" a family in it.
func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"1", "1", true},
		{"1", "2", false},
		{"hotfix-*", "hotfix-42", true},
		{"hotfix-*", "hotfix-", true},
		{"hotfix-*", "hotfi", false},
		{"hotfix-*", "release-42", false},
		{"*-42", "hotfix-42", true},
		{"*-42", "hotfix-421", false},
		{"pr-*-fix", "pr-9-fix", true},
		{"pr-*-fix", "pr--fix", true},
		{"pr-*-fix", "pr-9-fixup", false},
		{"a*b*c", "azzbzzc", true},
		{"a*b*c", "azzc", false},
		// The backtracking case: the first "*" must give ground for the literal to land.
		{"*abc", "aabc", true},
		{"*a*a", "aXaXa", true},
		{"", "", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pat, c.s, got, c.want)
		}
	}
}

// A typo'd charter must fail loudly at parse rather than silently matching nothing.
func TestMatchRejectsBadShape(t *testing.T) {
	// A predicate of the wrong SHAPE is NOT here, and deliberately: `value` takes any tony
	// value, so `{"value":"stage=open"}` is a legal predicate on a string payload rather than
	// a typo verse can name. See TestMatchValueScalarIsTheWholePayload.
	cases := map[string]string{
		"unknown key":       `{"sytem":"demo"}`,
		"non-string system": `{"system":{"a":1}}`,
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

// FieldString reads one literal string back out of the predicate — what the quorum machinery
// asks of the facts match it built itself ("which choice does this count"). A predicate that
// is not a record has no key to read, and an operator has no single value behind it, so both
// answer "" rather than a guess.
func TestMatchFieldString(t *testing.T) {
	m := Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}
	if got := m.FieldString("stage"); got != "open" {
		t.Errorf("FieldString(stage) = %q, want open", got)
	}
	if got := m.FieldString("absent"); got != "" {
		t.Errorf("FieldString of an absent key = %q, want empty", got)
	}
	scalar := Match{"system": "demo", "value": "open"}
	if got := scalar.FieldString("stage"); got != "" {
		t.Errorf("FieldString of a scalar predicate = %q, want empty", got)
	}
}

// MatchEvent is the EDGE face: it ANDs every set dimension against one observed change, and an
// empty match matches everything (as an empty pattern list did).
func TestMatchEventDimensions(t *testing.T) {
	ev := entity.Event{
		Ref: entity.Ref{System: "demo", Kind: "pr", ID: "1"},
		Op:  entity.Set, To: entity.Obj{"stage": "open"}.Payload(),
	}
	yes := []Match{
		{},
		{"system": "demo"},
		{"system": "demo", "kind": "pr"},
		{"system": "demo", "kind": "pr", "id": "1"},
		{"system": "demo", "kind": "pr", "id": "*"},
		{"op": "set"},
		{"value": map[string]any{"stage": "open"}},
	}
	no := []Match{
		{"system": "other"},
		{"kind": "issue"},
		{"id": "2"},
		{"op": "delete"},
		{"value": map[string]any{"stage": "closed"}},
		{"system": "demo", "kind": "pr", "id": "2"}, // one wrong dimension fails the conjunction
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

// Select is the SET face. The `id` dimension is owned by Match (entity.Pattern has none), so
// it must be applied on top of the delegated system/kind/value selection — including the "*"
// wildcard that flow addressing will use.
func TestMatchSelect(t *testing.T) {
	store := entitytest.Store(t)
	entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "pr", ID: "1"}, entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "pr", ID: "2"}, entity.Obj{"stage": "reviewed"}.Payload())
	entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "pr", ID: "3"}, entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, entity.Ref{System: "other", Kind: "pr", ID: "1"}, entity.Obj{"stage": "open"}.Payload())

	count := func(m Match) int { return len(m.Select(store)) }

	if n := count(Match{"system": "demo", "kind": "pr"}); n != 3 {
		t.Errorf("whole kind: %d, want 3", n)
	}
	if n := count(Match{"system": "demo", "kind": "pr", "id": "*"}); n != 3 {
		t.Errorf("explicit wildcard id: %d, want 3", n)
	}
	if n := count(Match{"system": "demo", "kind": "pr", "id": "2"}); n != 1 {
		t.Errorf("one id: %d, want 1", n)
	}
	if n := count(Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}); n != 2 {
		t.Errorf("payload predicate: %d, want 2", n)
	}
	// system+kind pins the slice: a same-id entity in another system is not picked.
	refs := Match{"system": "demo", "kind": "pr", "id": "1"}.Select(store)
	if len(refs) != 1 || refs[0].System != "demo" {
		t.Errorf("id selection crossed systems: %v", refs)
	}
	// An invalid match selects nothing rather than everything — fail closed.
	if n := count(Match{"sytem": "demo"}); n != 0 {
		t.Errorf("invalid match selected %d entities; must fail closed", n)
	}
}

// The match-key namespace and the formula tag namespace must stay disjoint apart from `op`.
// Step 2 classifies a `when` object as a match or as a formula tag BY ITS KEYS, so an overlap
// would silently reclassify charters. `op` is the known collision (a match dimension and the
// verbose formula node's discriminator), resolved by testing match keys first; anything else
// overlapping is a bug in naming, and this test is what catches it.
func TestMatchKeysDisjointFromFormulaTags(t *testing.T) {
	tags := map[string]bool{}
	for _, op := range []FormulaOp{OpAtom, OpUntil, OpEventually, OpAlways, OpSequence} {
		tags[string(op)] = true
	}
	for _, k := range []RequirementKind{ReqQuorum, ReqFact, ReqDeadline, ReqJudgment, ReqAsk, ReqDefault} {
		tags[string(k)] = true
	}
	// The verbose formula node's own keys count as tags too — that is where `op` collides.
	for _, k := range []string{"op", "req", "args"} {
		tags[k] = true
	}

	var overlap []string
	for k := range matchKeys {
		if tags[k] {
			overlap = append(overlap, k)
		}
	}
	if len(overlap) != 1 || overlap[0] != "op" {
		t.Fatalf("match keys overlap formula tags at %v; want exactly [op].\n"+
			"Either rename the new tag/dimension, or extend the ordered classification in "+
			"Formula.UnmarshalJSON to disambiguate it (see docs/trigger-model.md).", overlap)
	}
}

// Requirement.Facts is now a Match, so a formula's leaf and a trigger's subject share one
// grammar. The COMPACT form must be unchanged by that retype — `verse docket` output and
// `goals add --done-when` inputs are user-visible.
//
// Each case is written twice: the tag-keyed spelling a JSON-shaped charter must use, and
// the tagged spelling verse renders. Both have to read as the same formula, and rendering
// has to give back the tagged one — which is the whole of "one grammar, two syntaxes" now
// that only one of them is also a rendering.
func TestRequirementFactsWireUnchanged(t *testing.T) {
	cases := []struct{ keyed, tagged string }{
		{`{"quorum":2}`, `!quorum 2`},
		{`{"fact":"ci:status"}`, `!fact "ci:status"`},
		{`{"quorum":{"choice":"reject","n":2}}`, `!quorum {choice: reject, n: 2}`},
		{`{"deadline":"4h"}`, `!deadline "4h"`},
		{`{"until":[{"quorum":2},{"deadline":"4h"}]}`, `!until [!quorum 2, !deadline "4h"]`},
	}
	for _, c := range cases {
		var fromKeyed, fromTagged Formula
		if err := codec.Unmarshal([]byte(c.keyed), &fromKeyed, codec.JSON); err != nil {
			t.Fatalf("unmarshal %s: %v", c.keyed, err)
		}
		if err := codec.Unmarshal([]byte(c.tagged), &fromTagged, codec.Tony); err != nil {
			t.Fatalf("unmarshal %s: %v", c.tagged, err)
		}

		out, err := codec.MarshalWire(&fromKeyed, codec.Tony)
		if err != nil {
			t.Fatalf("marshal %s: %v", c.keyed, err)
		}
		// Compared as DOCUMENTS, not bytes. What must not drift is the compact SHAPE —
		// `!quorum 2`, never the internal {op: …, req: …} parse node. Object key order is
		// not part of that contract and asserting it byte-for-byte would pin an encoder
		// detail as if it were the wire format.
		want, err := codec.ParseNode([]byte(c.tagged), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.ParseNode(out, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		if !sameDoc(want, got) {
			t.Errorf("compact form drifted: %s → %s, want %s", c.keyed, out, c.tagged)
		}
	}
}

// A fact selector parsed from "system:kind" must read back through the Match accessors, which
// is what Subtrees()/eval() now use to find what to watch and read.
func TestFactSelFromRefIsAMatch(t *testing.T) {
	m := factSelFromRef("ci:status")
	if m.System() != "ci" || m.Kind() != "status" {
		t.Fatalf("factSelFromRef(\"ci:status\") = %#v", m)
	}
	if m := factSelFromRef("ci"); m.System() != "ci" || m.Kind() != "" {
		t.Fatalf("factSelFromRef(\"ci\") = %#v", m)
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
