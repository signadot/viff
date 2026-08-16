package entity

import "testing"

// A payload is ANY tony value. It used to be map[string]any, which forced every entity to
// be a little record — a counter, a flag, a state name, a list were all inexpressible for
// no reason anyone had chosen. These are the cases that were impossible before.
func TestScalarAndListPayloadsRoundTrip(t *testing.T) {
	s := openStore(t)
	cases := []struct {
		name string
		ref  Ref
		val  any
		want string // how Str/Strs/ToObj should read it back, per kind
	}{
		{"int", RefAt("demo", "count", "1"), 42, ""},
		{"bool", RefAt("demo", "flag", "1"), true, ""},
		{"string", RefAt("demo", "name", "1"), "open", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set(t, s, c.ref, Val(c.val))
			got, ok, err := s.Get(c.ref)
			if err != nil || !ok {
				t.Fatalf("get: ok=%v err=%v", ok, err)
			}
			if IsObj(got.Payload) {
				t.Fatalf("payload became an object: %+v", got.Payload)
			}
			// It round-trips as the value it was, not as a record wrapping it.
			if !Equal(got.Payload, Val(c.val)) {
				t.Errorf("payload did not survive the store: got %+v", got.Payload)
			}
		})
	}

	list := RefAt("demo", "list", "1")
	set(t, s, list, Val([]any{"a", "b"}))
	got, _, _ := s.Get(list)
	if IsObj(got.Payload) {
		t.Fatalf("list payload became an object: %+v", got.Payload)
	}
}

// Field-shaped questions asked of a payload that has no fields answer empty rather than
// erroring or panicking — the rule the selector in pattern.go depends on.
func TestFieldReadsOfANonObjectPayloadAreEmpty(t *testing.T) {
	for _, p := range []Payload{nil, Val(42), Val("open"), Val([]any{"a"})} {
		if got := Get(p, "stage"); got != nil {
			t.Errorf("Get(%v, stage) = %v, want nil", p, got)
		}
		if got := Str(p, "stage"); got != "" {
			t.Errorf("Str(%v, stage) = %q, want empty", p, got)
		}
		if got := Strs(p, "stage"); got != nil {
			t.Errorf("Strs(%v, stage) = %v, want nil", p, got)
		}
		if got := ToObj(p); got != nil {
			t.Errorf("ToObj(%v) = %v, want nil", p, got)
		}
	}
}

// A field predicate against a payload with no fields does not match. It is not "false
// because the value differs" — the question is unanswerable, and a selector that cannot
// answer must not select. An EMPTY predicate still matches everything, including scalars.
func TestFieldPredicateDoesNotMatchANonObjectPayload(t *testing.T) {
	p := Pattern{System: "demo", Kind: "count", Predicate: Obj{"stage": "open"}.Payload()}
	if p.matchPayload(Val(42)) {
		t.Error("a field predicate matched a scalar payload")
	}
	if p.matchPayload(Val([]any{"a"})) {
		t.Error("a field predicate matched a list payload")
	}
	if p.matchPayload(nil) {
		t.Error("a field predicate matched a nil payload")
	}

	empty := Pattern{System: "demo", Kind: "count"}
	if !empty.matchPayload(Val(42)) {
		t.Error("an empty predicate must still match a scalar payload")
	}
}

// Strs exists because a payload no longer remembers Go types: a []string written comes back
// as a list of tony strings. The map-backed payload preserved the Go slice, but only in
// memory — the docd store always normalized. Both stores now agree.
func TestStrsReadsAListWrittenAsGoStrings(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "motion", "1")
	set(t, s, ref, Obj{"witness": []string{"a", "b"}}.Payload())
	got, _, _ := s.Get(ref)
	w := Strs(got.Payload, "witness")
	if len(w) != 2 || w[0] != "a" || w[1] != "b" {
		t.Fatalf("Strs = %v, want [a b]", w)
	}
}
