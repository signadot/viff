package trigger

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/signadot/verse/entity"
)

// Match is a POINT-IN-TIME selector over the entity-verse: "which entities satisfy this
// right now". It is the atom of the trigger model and the leaf of every Formula, and it is
// deliberately the ONE type that knows the match grammar — so growing that grammar (globs,
// references, !and/!or, nested payload matches) is a change here and nowhere else.
//
// There is no time in a Match. A subject is *named* at the instant a firing happens, never
// accumulated, so `until`/`deadline`/`quorum` would be a category error here; those live in
// Formula, the temporal layer composed OVER matches. See docs/trigger-model.md.
//
// Today it is map-backed and interpreted through the existing typed matcher
// (entity.Pattern); tomorrow it becomes a real tony match. Callers only ever go through the
// methods below, so that swap does not reach them.
//
//	{"system": "demo", "kind": "pr", "fields": {"stage": "open"}}   an entity predicate
//	{"system": "demo", "kind": "pr", "id": "1"}                     one entity
//	{"system": "demo", "kind": "pr", "id": "*"}                     the whole kind
//	"$flow"                                                         a bound ref (sugar)
//	{}                                                              everything
type Match map[string]any

// The match dimensions. `entity` is accepted as an alias for `fields` (docs/trigger-model.md
// spells a Subject's payload predicate `entity:`); `ref` carries the string sugar below.
const (
	mKeySystem = "system"
	mKeyKind   = "kind"
	mKeyID     = "id"
	mKeyOp     = "op"
	mKeyFields = "fields"
	mKeyEntity = "entity" // alias for fields
	mKeyRef    = "ref"    // the single-entity/bound-ref form
)

// matchKeys is the closed set of dimensions a Match may carry. Its disjointness from the
// formula tag names is load-bearing and asserted in match_test.go: once `when` is a Formula
// (docs/trigger-model-plan.md step 2), an object is classified as a match or as a formula tag
// by its keys, so the two namespaces must not collide.
var matchKeys = map[string]bool{
	mKeySystem: true, mKeyKind: true, mKeyID: true,
	mKeyOp: true, mKeyFields: true, mKeyEntity: true, mKeyRef: true,
}

// UnmarshalJSON accepts the object form, or a STRING as sugar for the single-entity form:
// "$flow" and "demo:pr:1" both parse to {"ref": …}. The string may be a $var (expanded
// against a firing's bindings) or a literal ref.
func (m *Match) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*m = refMatch(s)
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("match: want an object or a \"system:kind:id\"/\"$var\" string: %w", err)
	}
	*m = Match(raw)
	return m.Validate()
}

// MarshalJSON emits the string form for a bare ref and the object form otherwise, so a
// charter round-trips the way it was authored (`trigger show`).
func (m Match) MarshalJSON() ([]byte, error) {
	if ref, ok := m.Ref(); ok && len(m) == 1 {
		return json.Marshal(ref)
	}
	return json.Marshal(map[string]any(m))
}

// Validate reports whether the match carries only known dimensions of the right shape. It is
// what makes a typo'd charter fail at install rather than silently match nothing.
func (m Match) Validate() error {
	var unknown []string
	for k := range m {
		if !matchKeys[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		known := make([]string, 0, len(matchKeys))
		for k := range matchKeys {
			known = append(known, k)
		}
		sort.Strings(known)
		return fmt.Errorf("match: unknown key(s) %s (want any of %s)",
			strings.Join(unknown, ", "), strings.Join(known, ", "))
	}
	for _, k := range []string{mKeySystem, mKeyKind, mKeyID, mKeyOp, mKeyRef} {
		if v, present := m[k]; present {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("match: %q must be a string, got %T", k, v)
			}
		}
	}
	for _, k := range []string{mKeyFields, mKeyEntity} {
		if v, present := m[k]; present {
			if _, ok := asFields(v); !ok {
				return fmt.Errorf("match: %q must be an object, got %T", k, v)
			}
		}
	}
	if _, both := m[mKeyFields]; both {
		if _, alias := m[mKeyEntity]; alias {
			return fmt.Errorf("match: set either %q or its alias %q, not both", mKeyFields, mKeyEntity)
		}
	}
	return nil
}

// System / Kind / ID are the match's const dimensions ("" = unset/wildcard).
func (m Match) System() string { return m.str(mKeySystem) }
func (m Match) Kind() string   { return m.str(mKeyKind) }
func (m Match) ID() string     { return m.str(mKeyID) }

// Fields is the payload predicate (key==value equality), nil when unset.
func (m Match) Fields() map[string]any {
	if f, ok := asFields(m[mKeyFields]); ok {
		return f
	}
	f, _ := asFields(m[mKeyEntity])
	return f
}

// Ref returns the single-entity/bound-ref form ("$flow", "demo:pr:1") if present. It is the
// degenerate single-entity selector; the string form is sugar for it.
func (m Match) Ref() (string, bool) {
	s := m.str(mKeyRef)
	return s, s != ""
}

// IsEmpty reports whether the match constrains nothing (matches everything).
func (m Match) IsEmpty() bool { return len(m) == 0 }

// Selects reports whether the match names a SET to select from the store — a definite
// system+kind, as opposed to one entity by ref. A set-valued Subject must name both, since
// the engine has to know what kind it is selecting and spawning for.
func (m Match) Selects() bool {
	if _, isRef := m.Ref(); isRef {
		return false
	}
	return m.System() != "" && m.Kind() != ""
}

// MatchEvent reports whether an observed change satisfies the match — the EDGE face, used to
// dispatch a `when` against one change event. It delegates the system/kind/op/fields
// dimensions to the existing typed matcher and ANDs the `id` dimension, which Match owns (so
// entity.Pattern stays as it is). An invalid match matches nothing.
func (m Match) MatchEvent(ev entity.Event) bool {
	p, err := m.pattern()
	if err != nil {
		return false
	}
	return p.MatchEvent(ev) && m.matchID(ev.Ref.ID)
}

// Select returns the refs of every stored entity the match picks — the SET face, used to
// resolve a set-valued Subject and to read a formula's facts. An invalid match picks none.
func (m Match) Select(store entity.Store) []entity.Ref {
	p, err := m.pattern()
	if err != nil {
		return nil
	}
	refs := p.Select(store)
	if m.ID() == "" {
		return refs
	}
	out := refs[:0]
	for _, r := range refs {
		if m.matchID(r.ID) {
			out = append(out, r)
		}
	}
	return out
}

// pattern interprets the match as today's typed matcher. This is THE seam: when Match grows
// a real tony match, this is what is replaced, and MatchEvent/Select stay as they are.
func (m Match) pattern() (entity.Pattern, error) {
	if err := m.Validate(); err != nil {
		return entity.Pattern{}, err
	}
	return entity.Pattern{
		System: m.System(),
		Kind:   m.Kind(),
		Op:     entity.Op(m.str(mKeyOp)),
		Fields: m.Fields(),
	}, nil
}

// matchID applies the `id` dimension: unset or "*" is a wildcard, otherwise exact equality.
// Richer globs ("demo:pr:1/**") arrive with the tony leaf.
func (m Match) matchID(id string) bool {
	want := m.ID()
	return want == "" || want == "*" || want == id
}

func (m Match) str(key string) string {
	s, _ := m[key].(string)
	return s
}

// asFields normalises a payload predicate, which may arrive as a Go literal or as decoded
// JSON (and, from a Go caller, as an entity.Payload).
func asFields(v any) (map[string]any, bool) {
	switch f := v.(type) {
	case nil:
		return nil, false
	case map[string]any:
		return f, true
	case entity.Payload:
		return map[string]any(f), true
	}
	return nil, false
}

// refMatch builds the degenerate single-entity match for a ref or $var — the Go-side
// constructor for what the string sugar parses to.
func refMatch(ref string) Match { return Match{mKeyRef: ref} }

// MatchRef reports whether a bare REF satisfies the match's const dimensions
// (system/kind/id). It is the face used to address things by identity when there is no
// payload or change to look at — steering live processes by their subject, where the subject
// is a ref string. `fields` is not consulted here: a caller wanting a payload predicate must
// read the entity and use MatchEvent.
func (m Match) MatchRef(ref entity.Ref) bool {
	if s := m.System(); s != "" && s != ref.System {
		return false
	}
	if k := m.Kind(); k != "" && k != ref.Kind {
		return false
	}
	return m.matchID(ref.ID)
}

// ParseExpr parses an entity-address expression into a Match — the CLI/wire surface for
// addressing a set of entities by identity, where `*` is a wildcard per segment:
//
//	demo:pr:1    one entity
//	demo:pr:*    the whole kind
//	demo:*:*     the whole system
//	demo:pr      trailing segments omitted are wildcards
//	*            everything
//
// This is what makes "relational flows" fall out of the model rather than being a second
// primitive: steering an entity-set is the same selector, at a wider setting. (A subtree
// form — "demo:pr:1/**" — waits for the reference grammar of the tony leaf.)
func ParseExpr(s string) (Match, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf(`empty address: want "system:kind:id", with * as a wildcard`)
	}
	parts := strings.SplitN(s, ":", 3) // an id may itself contain colons
	m := Match{}
	for i, key := range []string{mKeySystem, mKeyKind, mKeyID} {
		if i >= len(parts) {
			break
		}
		seg := strings.TrimSpace(parts[i])
		if seg == "" {
			return nil, fmt.Errorf("empty segment %d in address %q", i+1, s)
		}
		if seg == "*" {
			continue // a wildcard is simply an unset dimension
		}
		m[key] = seg
	}
	return m, nil
}
