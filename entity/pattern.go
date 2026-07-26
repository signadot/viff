package entity

import (
	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
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
	Op Op
	// Predicate is the payload predicate: a tony match evaluated at the PAYLOAD ROOT, nil
	// when unset. It is a match rather than a map of equalities, which is what lets it carry
	// operators (`{stage: !glob "op*"}`) and match a payload that is not an object at all
	// (`42` against `42`). tony.Match owns the semantics; this type only carries it.
	//
	// It is not called Fields because it is not field-shaped: `fields` is one SPELLING of it
	// in the charter grammar, and naming the carrier after the sugar is how the general case
	// stays invisible.
	Predicate *ir.Node
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

// matchPayload evaluates the payload predicate against the payload, as a tony match at its
// root. No predicate matches everything, including a payload that is absent.
//
// Everything else is tony.Match's semantics, deliberately, since a payload is a tony value
// and a predicate is a tony match — having a second, nearly-identical comparison here is how
// the two drift. Notably: extra payload fields are ignored (it stays a subset match), a
// scalar payload cannot satisfy a field-shaped predicate, and a scalar predicate CAN match a
// scalar payload, which a map of field equalities could not express at all.
//
// An unevaluable predicate (a malformed operator) is a non-match rather than an error: a
// selector that cannot answer must not select. The charter that carries it failed validation
// at install; this is the belt to that suspenders.
func (p Pattern) matchPayload(payload Payload) bool {
	if p.Predicate == nil {
		return true
	}
	if payload == nil {
		return false // nothing to match against — a deleted entity, or an absent one
	}
	ok, err := tony.Match(payload, p.Predicate)
	return err == nil && ok
}
