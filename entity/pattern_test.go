package entity

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/parse"
)

// A predicate and the payload it is matched against may be written by different hands in
// different Go types, and an INTEGER means the same number however it was spelled — the Go
// width is an artifact of the writer, not of the value. All of these normalize to one tony
// integer, so they must agree.
//
// This used to be a 6x6 cross-product including the float types, because a payload was a Go
// value and its type depended on who decoded it (encoding/json's float64, a literal's int).
// A payload is a tony node now, parsed by the same parser whatever dialect it arrived in, so
// that variance is gone — and with it the reason to compare across int and float. See
// TestMatchPayloadIntAndFloatAreDistinct for what replaced it.
func TestMatchPayloadIntegerWidthsAgree(t *testing.T) {
	ints := []struct {
		name string
		v    any
	}{
		{"int", int(2)},
		{"int32", int32(2)},
		{"int64", int64(2)},
		{"uint", uint(2)},
	}
	floats := []struct {
		name string
		v    any
	}{
		{"float32", float32(2.5)},
		{"float64", float64(2.5)},
	}
	for _, want := range ints {
		for _, got := range ints {
			t.Run(want.name+"/"+got.name, func(t *testing.T) {
				p := Pattern{Predicate: Obj{"n": want.v}.Payload()}
				if !p.matchPayload(Obj{"n": got.v}.Payload()) {
					t.Fatalf("want %v (%T) did not match payload %v (%T)",
						want.v, want.v, got.v, got.v)
				}
			})
		}
	}
	for _, want := range floats {
		for _, got := range floats {
			t.Run(want.name+"/"+got.name, func(t *testing.T) {
				p := Pattern{Predicate: Obj{"n": want.v}.Payload()}
				if !p.matchPayload(Obj{"n": got.v}.Payload()) {
					t.Fatalf("want %v (%T) did not match payload %v (%T)",
						want.v, want.v, got.v, got.v)
				}
			})
		}
	}
}

// 2 and 2.0 are DIFFERENT VALUES, which is tony's position and now verse's. A payload
// predicate is a tony match, so it takes tony's semantics rather than keeping a second,
// nearly-identical numeric comparison of its own — having two was how they would drift.
//
// The old behaviour matched them, deliberately, because a number's Go type was an accident
// of its decoder. It no longer is: `2` parses as an integer from tony, JSON or YAML alike,
// so a float can only appear where someone wrote a fractional number or handed a float to
// entity.Val. Treating those as the integer would be guessing at intent.
func TestMatchPayloadIntAndFloatAreDistinct(t *testing.T) {
	p := Pattern{Predicate: Obj{"n": 2}.Payload()}
	if p.matchPayload(Obj{"n": 2.0}.Payload()) {
		t.Error("integer predicate 2 matched a float payload 2.0")
	}
	q := Pattern{Predicate: Obj{"n": 2.0}.Payload()}
	if q.matchPayload(Obj{"n": 2}.Payload()) {
		t.Error("float predicate 2.0 matched an integer payload 2")
	}
}

func TestMatchPayloadNumericInequality(t *testing.T) {
	p := Pattern{Predicate: Obj{"n": int64(2)}.Payload()}
	for _, got := range []any{int(3), float64(2.5), int64(-2), "2", true, nil} {
		if p.matchPayload(Obj{"n": got}.Payload()) {
			t.Errorf("want 2 wrongly matched payload %v (%T)", got, got)
		}
	}
}

// Large integers must not round-trip through float64: 2^53+1 and 2^53+2 are the same
// float64, so comparing them as floats would report a match.
func TestMatchPayloadLargeIntPrecision(t *testing.T) {
	const big = int64(1) << 53
	p := Pattern{Predicate: Obj{"n": big + 1}.Payload()}
	if p.matchPayload(Obj{"n": big + 2}.Payload()) {
		t.Error("2^53+1 wrongly matched 2^53+2")
	}
	if !p.matchPayload(Obj{"n": big + 1}.Payload()) {
		t.Error("2^53+1 did not match itself")
	}
}

// A field predicate may name a nested object or list. `==` on two interfaces holding
// maps panics at runtime, so composites must not reach it.
func TestMatchPayloadComposites(t *testing.T) {
	p := Pattern{Predicate: Obj{"labels": map[string]any{"team": "core"}}.Payload()}
	if !p.matchPayload(Obj{"labels": map[string]any{"team": "core"}}.Payload()) {
		t.Error("equal nested objects did not match")
	}
	if p.matchPayload(Obj{"labels": map[string]any{"team": "infra"}}.Payload()) {
		t.Error("differing nested objects matched")
	}

	q := Pattern{Predicate: Obj{"tags": []any{"a", "b"}}.Payload()}
	if !q.matchPayload(Obj{"tags": []any{"a", "b"}}.Payload()) {
		t.Error("equal lists did not match")
	}
	if q.matchPayload(Obj{"tags": []any{"a"}}.Payload()) {
		t.Error("differing lists matched")
	}
}

// The pre-existing behaviour: scalars compare by value, a missing key never matches,
// and a nil payload satisfies only an empty predicate.
func TestMatchPayloadScalarsAndAbsence(t *testing.T) {
	p := Pattern{Predicate: Obj{"stage": "open"}.Payload()}
	if !p.matchPayload(Obj{"stage": "open", "extra": 1}.Payload()) {
		t.Error("string equality failed")
	}
	if p.matchPayload(Obj{"stage": "closed"}.Payload()) {
		t.Error("differing strings matched")
	}
	if p.matchPayload(Obj{"other": "open"}.Payload()) {
		t.Error("absent key matched")
	}
	// A `null` value in a predicate is a PRESENCE test: the key must be there, and its value
	// may be anything. It is not "the value is null" — a null pattern matches any value in
	// tony, so the only thing it constrains is that the field exists.
	//
	// This reverses the old reading, where an absent key read as nil and `{x: null}` was
	// therefore a way to spell "has NO x" — the exact opposite of what it means now. tony
	// distinguishes absent from null (a removal takes the key, so a cleared field and a field
	// holding null are different shapes, not one), and a payload predicate is a tony match now,
	// so it inherits that rather than flattening it.
	//
	// The three questions and their spellings are TestNullPresenceAbsenceIdioms below.
	present := Pattern{Predicate: Obj{"x": nil}.Payload()}
	if present.matchPayload(Obj{"y": 1}.Payload()) {
		t.Error("a null predicate matched an entity that LACKS the field")
	}
	if !present.matchPayload(Obj{"x": nil}.Payload()) {
		t.Error("a null predicate did not match a field present and null")
	}
	if !present.matchPayload(Obj{"x": "anything"}.Payload()) {
		t.Error("a null predicate is a presence test — it must match any value")
	}
	if p.matchPayload(nil) {
		t.Error("nil payload matched a non-empty predicate")
	}
	if !(Pattern{}).matchPayload(nil) {
		t.Error("nil payload did not match an empty predicate")
	}
}

// At points the predicate at a place INSIDE the payload, so a predicate can be written about
// a subtree without restating the shape above it. Rooted at `labels`, `{urgent: true}` is the
// same question as `{labels: {urgent: true}}` asked at the root — and the equivalence is the
// point: `verse watch --if-at labels --if '{urgent: true}'` must mean what a charter means.
func TestMatchPayloadRootedAtAPath(t *testing.T) {
	payload := Obj{"stage": "open", "labels": Obj{"urgent": true, "team": "core"}}.Payload()

	rooted := Pattern{At: []string{"labels"}, Predicate: Obj{"urgent": true}.Payload()}
	nested := Pattern{Predicate: Obj{"labels": Obj{"urgent": true}}.Payload()}
	if !rooted.matchPayload(payload) {
		t.Error("a predicate rooted at labels did not match the subtree it names")
	}
	if rooted.matchPayload(payload) != nested.matchPayload(payload) {
		t.Error("rooting a predicate and nesting it disagree — they are the same question")
	}
	if (Pattern{At: []string{"labels"}, Predicate: Obj{"urgent": false}.Payload()}).matchPayload(payload) {
		t.Error("a rooted predicate matched a subtree that contradicts it")
	}

	// The subtree need not be an object: rooted at a scalar, the predicate is a scalar match,
	// which is the case an object-shaped predicate could not express at all.
	if !(Pattern{At: []string{"stage"}, Predicate: Val("open")}).matchPayload(payload) {
		t.Error("a scalar predicate did not match the scalar it was rooted at")
	}

	// Deeper than one segment, and rooted at the root by an empty path.
	if !(Pattern{At: []string{"labels", "team"}, Predicate: Val("core")}).matchPayload(payload) {
		t.Error("a two-segment path did not reach labels.team")
	}
	if !(Pattern{At: nil, Predicate: Obj{"stage": "open"}.Payload()}).matchPayload(payload) {
		t.Error("an empty path must leave the predicate at the payload root")
	}
}

// A path that leads nowhere is a NON-MATCH, not an error and not a wildcard — for the reason
// an absent payload is one: a predicate that could not be evaluated has not been satisfied.
// Both ways of leading nowhere are here, since a scalar in the way is the one a caller does
// not think of.
func TestMatchPayloadRootedAtAPathThatIsNotThere(t *testing.T) {
	p := Pattern{At: []string{"labels", "urgent"}, Predicate: Val(true)}
	for _, payload := range []Payload{
		Obj{"stage": "open"}.Payload(),               // no labels at all
		Obj{"labels": Obj{"team": "core"}}.Payload(), // labels, but no urgent
		Obj{"labels": "urgent"}.Payload(),            // a scalar where the path descends
		Val(42),                                      // a payload with no fields to speak of
		nil,                                          // a delete
	} {
		if p.matchPayload(payload) {
			t.Errorf("labels.urgent matched a payload that has no such place: %v", payload)
		}
	}
	// And a path is never a filter of its own: with no predicate to root, everything matches,
	// including a payload the path does not reach.
	if !(Pattern{At: []string{"nowhere"}}).matchPayload(Obj{"stage": "open"}.Payload()) {
		t.Error("a path without a predicate filtered something out")
	}
}

// Presence, absence, and null-valued are three different questions, and each has its own
// spelling. Getting them confused is easy — the first two look like the same syntax negated,
// and the third looks like the first — so they are pinned together, in one place, with the
// distinction stated rather than implied.
//
//	{x: null}            x is PRESENT, whatever its value
//	!not {x: null}       x is ABSENT
//	{x: !irtype null}    x is present AND its value is null
func TestNullPresenceAbsenceIdioms(t *testing.T) {
	pred := func(src string) Pattern {
		n, err := parse.Parse([]byte(src), parse.ParseTony())
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		return Pattern{Predicate: n}
	}
	var (
		hasX     = Obj{"x": "open"}.Payload() // present, non-null
		nullX    = Obj{"x": nil}.Payload()    // present, null
		withoutX = Obj{"y": 1}.Payload()      // absent
	)
	cases := []struct {
		name                          string
		src                           string
		wantHas, wantNull, wantAbsent bool
	}{
		{"presence", `{x: null}`, true, true, false},
		{"absence", `!not {x: null}`, false, false, true},
		{"null-valued", `{x: !irtype null}`, false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := pred(c.src)
			if got := p.matchPayload(hasX); got != c.wantHas {
				t.Errorf("%s vs present-non-null = %v, want %v", c.src, got, c.wantHas)
			}
			if got := p.matchPayload(nullX); got != c.wantNull {
				t.Errorf("%s vs present-null = %v, want %v", c.src, got, c.wantNull)
			}
			if got := p.matchPayload(withoutX); got != c.wantAbsent {
				t.Errorf("%s vs absent = %v, want %v", c.src, got, c.wantAbsent)
			}
		})
	}
}

// A Pattern goes on the wire — it is what an external source declares as the slice its
// answer is complete for (docs/sources.md), and what a source registration carries
// as `owns`. So every dimension has to survive a round trip, the payload predicate included:
// a cover that arrived with its predicate dropped would claim a whole kind rather than the
// one repo it meant.
func TestPatternSurvivesTheWire(t *testing.T) {
	in := Pattern{
		System:    "git",
		Kind:      "ref",
		Predicate: Obj{"repo": "verse"}.Payload(),
		At:        []string{"labels"},
	}
	node, err := gomap.ToTonyIR(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out Pattern
	if err := gomap.FromTonyIR(node, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.System != in.System || out.Kind != in.Kind {
		t.Fatalf("ref dimensions lost: %+v", out)
	}
	if len(out.At) != 1 || out.At[0] != "labels" {
		t.Fatalf("At lost: %+v", out.At)
	}
	if !Equal(out.Predicate, in.Predicate) {
		t.Fatalf("the payload predicate did not survive: %v", ToObj(out.Predicate))
	}

	// `op` rides along as "" rather than being omitted, and that is upstream's doing rather
	// than this type's: codegen honours `omitzero` for every field here EXCEPT Op, because Op
	// implements MarshalText and the TextMarshaler branch does not consult the option. It is
	// noise in the document and nothing more — it decodes back to the empty Op it was, so the
	// round trip above is unaffected — and it disappears on its own when go-tony is fixed.
	// Pinned so that the day it changes, this says why it was ever there.
	if _, ok := Fields(node)["op"]; !ok {
		t.Log("op is now omitted when empty — the upstream omitzero defect is fixed")
	}
}
