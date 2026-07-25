package entity

import "testing"

// A field predicate and the payload it is matched against are decoded by different
// paths, and those paths disagree about the Go type of a number: encoding/json makes
// every number a float64, tony makes an integer an int64, and a Go caller writing a
// literal gets an untyped constant that defaults to int. All three mean the same
// number, so the match must not care which one it got.
func TestMatchPayloadNumericTypes(t *testing.T) {
	nums := []struct {
		name string
		v    any
	}{
		{"int", int(2)},
		{"int32", int32(2)},
		{"int64", int64(2)},
		{"uint", uint(2)},
		{"float32", float32(2)},
		{"float64", float64(2)},
	}
	for _, want := range nums {
		for _, got := range nums {
			t.Run(want.name+"/"+got.name, func(t *testing.T) {
				p := Pattern{Fields: map[string]any{"n": want.v}}
				if !p.matchPayload(Payload{"n": got.v}) {
					t.Fatalf("want %v (%T) did not match payload %v (%T)",
						want.v, want.v, got.v, got.v)
				}
			})
		}
	}
}

func TestMatchPayloadNumericInequality(t *testing.T) {
	p := Pattern{Fields: map[string]any{"n": int64(2)}}
	for _, got := range []any{int(3), float64(2.5), int64(-2), "2", true, nil} {
		if p.matchPayload(Payload{"n": got}) {
			t.Errorf("want 2 wrongly matched payload %v (%T)", got, got)
		}
	}
}

// Large integers must not round-trip through float64: 2^53+1 and 2^53+2 are the same
// float64, so comparing them as floats would report a match.
func TestMatchPayloadLargeIntPrecision(t *testing.T) {
	const big = int64(1) << 53
	p := Pattern{Fields: map[string]any{"n": big + 1}}
	if p.matchPayload(Payload{"n": big + 2}) {
		t.Error("2^53+1 wrongly matched 2^53+2")
	}
	if !p.matchPayload(Payload{"n": big + 1}) {
		t.Error("2^53+1 did not match itself")
	}
}

// A field predicate may name a nested object or list. `==` on two interfaces holding
// maps panics at runtime, so composites must not reach it.
func TestMatchPayloadComposites(t *testing.T) {
	p := Pattern{Fields: map[string]any{"labels": map[string]any{"team": "core"}}}
	if !p.matchPayload(Payload{"labels": map[string]any{"team": "core"}}) {
		t.Error("equal nested objects did not match")
	}
	if p.matchPayload(Payload{"labels": map[string]any{"team": "infra"}}) {
		t.Error("differing nested objects matched")
	}

	q := Pattern{Fields: map[string]any{"tags": []any{"a", "b"}}}
	if !q.matchPayload(Payload{"tags": []any{"a", "b"}}) {
		t.Error("equal lists did not match")
	}
	if q.matchPayload(Payload{"tags": []any{"a"}}) {
		t.Error("differing lists matched")
	}
}

// The pre-existing behaviour: scalars compare by value, a missing key never matches,
// and a nil payload satisfies only an empty predicate.
func TestMatchPayloadScalarsAndAbsence(t *testing.T) {
	p := Pattern{Fields: map[string]any{"stage": "open"}}
	if !p.matchPayload(Payload{"stage": "open", "extra": 1}) {
		t.Error("string equality failed")
	}
	if p.matchPayload(Payload{"stage": "closed"}) {
		t.Error("differing strings matched")
	}
	if p.matchPayload(Payload{"other": "open"}) {
		t.Error("absent key matched")
	}
	// An absent key reads as nil, so a null predicate is satisfied by its absence.
	// Pinning the long-standing behaviour, not endorsing it — see matchPayload.
	if !(Pattern{Fields: map[string]any{"x": nil}}).matchPayload(Payload{"y": 1}) {
		t.Error("a null predicate did not match an entity lacking the field")
	}
	if p.matchPayload(nil) {
		t.Error("nil payload matched a non-empty predicate")
	}
	if !(Pattern{}).matchPayload(nil) {
		t.Error("nil payload did not match an empty predicate")
	}
}
