package entity

import (
	"testing"

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
	// distinguishes absent from null (docd tombstones with null, so the difference records
	// whether a field was cleared or never written), and a payload predicate is a tony match
	// now, so it inherits that rather than flattening it.
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
