package entity

// Pattern is a conjunctive selector over the entity-verse: it picks by System,
// Kind, an optional change Op, and exact key==value equality over a payload's
// Fields. Empty dimensions are wildcards. One Pattern serves both faces of
// "select entities" — matching an observed change (MatchEvent, the trigger
// "when") and listing the stored entities that satisfy it (Select, a formula's
// fact-read). The match grammar is deliberately tiny (system/kind/op/field
// equality); a richer tony match is the eventual destination.
type Pattern struct {
	System string `json:"system,omitempty"`
	Kind   string `json:"kind,omitempty"`
	// Op filters on the kind of change: "set" (created-or-updated) or "delete".
	// It applies to MatchEvent only; Select (stored entities have no op) ignores
	// it. Create and update are not distinguished (see Op) — express "only the
	// first time" as a state latch, not an edge.
	Op     Op             `json:"op,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
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
		if payload == nil || payload[k] != want {
			return false
		}
	}
	return true
}
