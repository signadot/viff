package trigger

import (
	"encoding/json"
	"testing"

	"github.com/signadot/verse/entity"
)

// A Match round-trips as authored: the object form stays an object, and the string sugar
// ("$flow", "demo:pr:1") comes back as a string rather than leaking the {"ref": …} form into
// `trigger show`.
func TestMatchJSONRoundTrip(t *testing.T) {
	for _, in := range []string{
		`"$flow"`,
		`"demo:pr:1"`,
		`{"system":"demo","kind":"pr","fields":{"stage":"open"}}`,
		`{"system":"demo","kind":"pr","id":"*"}`,
		`{}`,
	} {
		var m Match
		if err := json.Unmarshal([]byte(in), &m); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal %s: %v", in, err)
		}
		var back Match
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("re-unmarshal %s → %s: %v", in, out, err)
		}
		if len(back) != len(m) {
			t.Fatalf("round-trip changed shape: %s → %s", in, out)
		}
		for k, v := range m {
			if fmtAny(back[k]) != fmtAny(v) {
				t.Fatalf("round-trip lost %q: %s → %s", k, in, out)
			}
		}
	}
}

func fmtAny(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The string form is the degenerate single-entity selector, and it is NOT a set to select
// from: a bound ref names one entity, so Selects() must be false (a set-valued Subject has to
// name a definite system+kind).
func TestMatchRefForm(t *testing.T) {
	var m Match
	if err := json.Unmarshal([]byte(`"$flow"`), &m); err != nil {
		t.Fatal(err)
	}
	ref, ok := m.Ref()
	if !ok || ref != "$flow" {
		t.Fatalf("Ref() = %q, %v; want \"$flow\", true", ref, ok)
	}
	if m.Selects() {
		t.Fatal("a bound ref must not be treated as a set-valued selector")
	}
	if !(Match{"system": "demo", "kind": "pr"}).Selects() {
		t.Fatal("system+kind must be a set-valued selector")
	}
	// A system alone cannot select: the engine must know the kind it spawns for.
	if (Match{"system": "demo"}).Selects() {
		t.Fatal("system without kind must not be a set-valued selector")
	}
}

// A typo'd charter must fail loudly at parse rather than silently matching nothing.
func TestMatchRejectsBadShape(t *testing.T) {
	cases := map[string]string{
		"unknown key":       `{"sytem":"demo"}`,
		"non-string system": `{"system":{"a":1}}`,
		"non-object fields": `{"system":"demo","fields":"stage=open"}`,
		"fields and alias":  `{"system":"demo","fields":{"a":"b"},"entity":{"c":"d"}}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			var m Match
			if err := json.Unmarshal([]byte(in), &m); err == nil {
				t.Fatalf("accepted %s (parsed as %#v) — a bad match must not parse", in, m)
			}
		})
	}
}

// `entity` is accepted as an alias for `fields` (docs/trigger-model.md spells a Subject's
// payload predicate that way), and both spellings must behave identically.
func TestMatchEntityAliasesFields(t *testing.T) {
	ev := entity.Event{
		Ref: entity.Ref{System: "demo", Kind: "pr", ID: "1"},
		Op:  entity.Set, To: entity.Payload{"stage": "open"},
	}
	for _, key := range []string{"fields", "entity"} {
		m := Match{"system": "demo", "kind": "pr", key: map[string]any{"stage": "open"}}
		if !m.MatchEvent(ev) {
			t.Fatalf("%q spelling did not match", key)
		}
		if got := m.Fields()["stage"]; got != "open" {
			t.Fatalf("%q spelling: Fields() = %#v", key, m.Fields())
		}
	}
}

// MatchEvent is the EDGE face: it ANDs every set dimension against one observed change, and an
// empty match matches everything (as an empty pattern list did).
func TestMatchEventDimensions(t *testing.T) {
	ev := entity.Event{
		Ref: entity.Ref{System: "demo", Kind: "pr", ID: "1"},
		Op:  entity.Set, To: entity.Payload{"stage": "open"},
	}
	yes := []Match{
		{},
		{"system": "demo"},
		{"system": "demo", "kind": "pr"},
		{"system": "demo", "kind": "pr", "id": "1"},
		{"system": "demo", "kind": "pr", "id": "*"},
		{"op": "set"},
		{"fields": map[string]any{"stage": "open"}},
	}
	no := []Match{
		{"system": "other"},
		{"kind": "issue"},
		{"id": "2"},
		{"op": "delete"},
		{"fields": map[string]any{"stage": "closed"}},
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
// it must be applied on top of the delegated system/kind/fields selection — including the "*"
// wildcard that flow addressing will use.
func TestMatchSelect(t *testing.T) {
	store := entity.NewMemStore()
	store.Put(entity.Ref{System: "demo", Kind: "pr", ID: "1"}, entity.Payload{"stage": "open"})
	store.Put(entity.Ref{System: "demo", Kind: "pr", ID: "2"}, entity.Payload{"stage": "reviewed"})
	store.Put(entity.Ref{System: "demo", Kind: "pr", ID: "3"}, entity.Payload{"stage": "open"})
	store.Put(entity.Ref{System: "other", Kind: "pr", ID: "1"}, entity.Payload{"stage": "open"})

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
	if n := count(Match{"system": "demo", "kind": "pr", "fields": map[string]any{"stage": "open"}}); n != 2 {
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
			"Formula.UnmarshalJSON to disambiguate it (see docs/trigger-model-plan.md step 2).", overlap)
	}
}

// Requirement.Facts is now a Match, so a formula's leaf and a trigger's subject share one
// grammar. The compact wire form must be unchanged by that retype — `verse decisions` output
// and `goals add --done-when` inputs are user-visible.
func TestRequirementFactsWireUnchanged(t *testing.T) {
	cases := []string{
		`{"quorum":2}`,
		`{"fact":"ci:status"}`,
		`{"quorum":{"choice":"reject","n":2}}`,
		`{"deadline":"4h"}`,
		`{"until":[{"quorum":2},{"deadline":"4h"}]}`,
	}
	for _, in := range cases {
		var f Formula
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		out, err := json.Marshal(&f)
		if err != nil {
			t.Fatalf("marshal %s: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("compact form drifted: %s → %s", in, out)
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
