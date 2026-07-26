package entity

import (
	"math"
	"reflect"
)

// Pattern is a conjunctive selector over the entity-verse: it picks by System,
// Kind, an optional change Op, and exact key==value equality over a payload's
// Fields. Empty dimensions are wildcards. One Pattern serves both faces of
// "select entities" — matching an observed change (MatchEvent, the trigger
// "when") and listing the stored entities that satisfy it (Select, a formula's
// fact-read). The match grammar is deliberately tiny (system/kind/op/field
// equality); a richer tony match is the eventual destination.
type Pattern struct {
	System string
	Kind   string
	// Op filters on the kind of change: "set" (created-or-updated) or "delete".
	// It applies to MatchEvent only; Select (stored entities have no op) ignores
	// it. Create and update are not distinguished (see Op) — express "only the
	// first time" as a state latch, not an edge.
	Op     Op
	Fields map[string]any
}

// MatchEvent reports whether an observed change satisfies every non-empty
// dimension: system/kind/op, plus exact equality for each Fields entry against
// the changed payload.
func (p Pattern) MatchEvent(ev Event) bool {
	if p.System != "" && p.System != ev.Ref.System {
		return false
	}
	if p.Kind != "" && p.Kind != ev.Ref.Kind {
		return false
	}
	if p.Op != "" && p.Op != ev.Op {
		return false
	}
	return p.matchPayload(ev.To)
}

// Select returns the refs of every stored entity the pattern picks: entities of
// (System, Kind) whose payload satisfies every Fields entry. Requires a Lister
// store (MemStore and the logd store both are); a non-listing store yields none.
func (p Pattern) Select(store Store) []Ref {
	lister, ok := store.(Lister)
	if !ok {
		return nil
	}
	ents := lister.List(func(ref Ref) bool {
		return ref.System == p.System && (p.Kind == "" || ref.Kind == p.Kind)
	})
	out := make([]Ref, 0, len(ents))
	for _, e := range ents {
		if p.matchPayload(e.Payload) {
			out = append(out, e.Ref)
		}
	}
	return out
}

// matchPayload reports whether payload satisfies every Fields entry by exact
// equality. A nil payload matches only an empty Fields.
func (p Pattern) matchPayload(payload Payload) bool {
	for k, want := range p.Fields {
		// An absent key reads as nil, so a `null` predicate is satisfied by an entity
		// that simply lacks the field. That is the long-standing behaviour and it is
		// preserved here deliberately — tony does distinguish absent from null (docd
		// tombstones with null), so whether a field predicate should too is a real
		// question, but it is a semantic change and does not belong in this fix.
		if payload == nil || !valueEqual(payload[k], want) {
			return false
		}
	}
	return true
}

// valueEqual reports whether two payload values are equal for matching purposes.
// It is deliberately not `==`, for two reasons.
//
// NUMBERS ARRIVE WITH DIFFERENT GO TYPES depending on who decoded them. tony decodes
// an integer to int64 and a fractional number to float64; encoding/json decodes every
// number to float64; a Go caller writing a literal gets an untyped constant that
// defaults to int. So a charter's `fields: {n: 2}` and a payload's `2` can be int64,
// float64 or int and still be the same number — which is the only thing a field
// predicate means by "2". Compare numerically, across types.
//
// `==` ON TWO INTERFACES HOLDING MAPS OR SLICES PANICS at run time. A field predicate
// may legitimately name a nested object or a list, so composites must never reach the
// operator; they go to reflect.DeepEqual instead.
func valueEqual(got, want any) bool {
	if eq, both := numEqual(got, want); both {
		return eq
	}
	return reflect.DeepEqual(got, want)
}

// numEqual compares two values as numbers. both reports whether they ARE both numbers;
// when it is false the eq result is meaningless and the caller falls back to a
// structural comparison. Two integers are compared as integers, not as float64 — past
// 2^53 the float conversion is lossy, and reporting 2^53+1 equal to 2^53+2 is exactly
// the kind of silent wrong answer this function exists to avoid.
func numEqual(got, want any) (eq, both bool) {
	gi, gInt := asInt64(got)
	wi, wInt := asInt64(want)
	if gInt && wInt {
		return gi == wi, true
	}
	gf, gNum := asFloat64(got)
	wf, wNum := asFloat64(want)
	if gNum && wNum {
		return gf == wf, true
	}
	return false, false
}

// asInt64 reports v as an integer. A uint64 above math.MaxInt64 is not representable,
// so it is reported as a non-integer and falls through to the float comparison.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		if uint64(n) > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

// asFloat64 reports v as a floating-point number, including the integer types (so an
// int64 and a float64 holding the same value compare equal).
func asFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case uint64:
		return float64(n), true
	}
	if i, ok := asInt64(v); ok {
		return float64(i), true
	}
	return 0, false
}
