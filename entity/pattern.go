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
//
//tony:schemagen=verse-pattern,notag
type Pattern struct {
	System string `tony:"field=system"`
	Kind   string `tony:"field=kind,omitzero"`
	// Op filters on the kind of change: "set" (created-or-updated) or "delete".
	// It applies to MatchEvent only; Select (stored entities have no op) ignores
	// it. Create and update are not distinguished (see Op) — express "only the
	// first time" as a state latch, not an edge.
	Op Op `tony:"field=op,omitzero"`
	// Predicate is the payload predicate: a tony match evaluated at the PAYLOAD ROOT, nil
	// when unset. It is a match rather than a map of equalities, which is what lets it carry
	// operators (`{stage: !glob "op*"}`) and match a payload that is not an object at all
	// (`42` against `42`). tony.Match owns the semantics; this type only carries it.
	//
	// It is not called Fields because it is not field-shaped: `fields` is one SPELLING of it
	// in the charter grammar, and naming the carrier after the sugar is how the general case
	// stays invisible.
	Predicate *ir.Node `tony:"field=value,omitzero"`
	// At roots the predicate INSIDE the payload, at this field path — empty (the usual case)
	// evaluates it at the payload root. It is the same address a deep write takes
	// (Ref.At/Path.Field, api.PutEntity.Field), read rather than written, so "the place a
	// field lives" has one spelling whichever direction you are going.
	//
	// It is sugar, not reach: `At: ["labels"], Predicate: {urgent: true}` says what
	// `Predicate: {labels: {urgent: true}}` says. What it buys is the ability to point the
	// predicate at a place WITHOUT restating the shape above it — which matters most where
	// the predicate is typed rather than written down (`verse watch --if-at labels --if
	// '{urgent: true}'`), and where the subtree is not an object at all, so the predicate for
	// it is a bare scalar match (`At: ["stage"], Predicate: !glob "op*"`).
	//
	// A path that does not lead anywhere — an absent field, or a scalar in the way — is a
	// NON-MATCH, for the reason an absent payload is: a predicate that cannot be evaluated
	// has not been satisfied.
	At []string `tony:"field=at,omitzero"`
}

// MatchEvent reports whether an observed CHANGE satisfies every non-empty dimension:
// system/kind/op, plus the payload predicate against the changed payload.
//
// A delete carries no payload, so it never satisfies a predicate: a filtered stream is one of
// states, not of removals.
func (p Pattern) MatchEvent(ev Event) bool {
	if !p.MatchRef(ev.Ref) {
		return false
	}
	if p.Op != "" && p.Op != ev.Op {
		return false
	}
	return p.matchPayload(ev.To)
}

// MatchEntity reports whether a STORED entity satisfies the pattern — the same question
// MatchEvent asks of a change, minus Op, which a thing that merely stands there cannot
// answer.
//
// It is the whole question, ref dimensions included, even where a caller has already narrowed
// by ref: MatchRef is a PREFILTER the store can apply cheaply (Store.List takes exactly that
// callback), not a shortcut that lets the route decide half the predicate itself.
func (p Pattern) MatchEntity(e Entity) bool {
	return p.MatchRef(e.Ref) && p.matchPayload(e.Payload)
}

// MatchRef applies the dimensions an ADDRESS can answer — system and kind — with an empty
// dimension a wildcard. It is what a store's List prefilter takes.
//
// Select does NOT use it, and the difference is deliberate rather than an oversight: there,
// an empty System selects nothing, because a trigger's match always names one and a match
// that lost its system must not quietly become a whole-verse sweep. Here an empty dimension
// is an operator declining to filter, which is the opposite intent.
func (p Pattern) MatchRef(ref Ref) bool {
	return (p.System == "" || p.System == ref.System) &&
		(p.Kind == "" || p.Kind == ref.Kind)
}

// Select returns the refs of every stored entity the pattern picks: entities of
// (System, Kind) whose payload satisfies every Fields entry.
//
// An entity marked VANISHING is skipped unless the predicate asks about the marker. A rule
// acts on what it selects — patches it, spawns about it, raises a gate on it — and doing any
// of that to an entity on its way out is work about something that will not be there. Naming
// the marker is the opt-in, which is what lets a reaper select exactly the entities leaving;
// see VanishingField.
//
// A NAMED subject is not affected, because it does not come through here: naming an entity is
// addressing it by identity, not selecting from what exists (see trigger.Match.Select). If you
// name a retiring entity, you meant that one.
func (p Pattern) Select(r Reader) []Ref {
	ents := r.List(func(ref Ref) bool {
		return ref.System == p.System && (p.Kind == "" || ref.Kind == p.Kind)
	})
	skipVanishing := !namesVanishing(p.Predicate)
	out := make([]Ref, 0, len(ents))
	for _, e := range ents {
		if skipVanishing && Vanishing(e.Payload) {
			continue
		}
		if p.matchPayload(e.Payload) {
			out = append(out, e.Ref)
		}
	}
	return out
}

// matchPayload evaluates the payload predicate against the payload, as a tony match at At
// (the payload root, unless one was named). No predicate matches everything, including a
// payload that is absent — At is only asked about once there is a predicate to root, so a
// path is never a filter of its own.
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
	at := descend(payload, p.At)
	if at == nil {
		return false // nothing to match against — a deleted entity, an absent one, or no such field
	}
	ok, err := tony.Match(at, p.Predicate)
	return err == nil && ok
}

// descend walks a payload to a field path, nil if it does not lead anywhere. Get answers nil
// for a scalar as well as for an absent field, so a path THROUGH a non-object stops here
// rather than needing a case of its own.
func descend(payload Payload, field []string) Payload {
	for _, f := range field {
		if payload = Get(payload, f); payload == nil {
			return nil
		}
	}
	return payload
}
