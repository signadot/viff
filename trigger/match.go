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
//	{"system": "demo", "kind": "pr", "id": "hotfix-*"}              an id glob
//	"demo:pr:*"                                                     the same, as an address
//	{}                                                              everything
//
// A Subject is a plain Match too: on a firing it is matched against the entity the firing is
// about — including a reflected process's own subject (its `flow`), so `subject: "demo:pr:*"`
// on a completion trigger advances the PR that process was about. There is no deferred `$var`
// form: the pattern IS the reference (see Trigger.resolveSubject).
type Match map[string]any

// The match dimensions. `entity` is accepted as an alias for `fields` (docs/trigger-model.md
// spells a Subject's payload predicate `entity:`).
const (
	mKeySystem = "system"
	mKeyKind   = "kind"
	mKeyID     = "id"
	mKeyOp     = "op"
	mKeyFields = "fields"
	mKeyEntity = "entity" // alias for fields
)

// matchKeys is the closed set of dimensions a Match may carry. Its disjointness from the
// formula tag names is load-bearing and asserted in match_test.go: once `when` is a Formula
// (docs/trigger-model-plan.md step 2), an object is classified as a match or as a formula tag
// by its keys, so the two namespaces must not collide.
var matchKeys = map[string]bool{
	mKeySystem: true, mKeyKind: true, mKeyID: true,
	mKeyOp: true, mKeyFields: true, mKeyEntity: true,
}

// UnmarshalJSON accepts the object form, or a STRING as sugar for an address: "demo:pr:1"
// parses to the system/kind/id dimensions and "demo:pr:*" to the whole kind — the same grammar
// the CLI's `flow <addr>` uses (ParseExpr). There is no deferred `$var` form.
func (m *Match) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		parsed, err := ParseExpr(s)
		if err != nil {
			return err
		}
		*m = parsed
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("match: want an object or a \"system:kind:id\"/\"$var\" string: %w", err)
	}
	*m = Match(raw)
	return m.Validate()
}

// MarshalJSON emits the object form (`trigger show`).
func (m Match) MarshalJSON() ([]byte, error) {
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
	for _, k := range []string{mKeySystem, mKeyKind, mKeyID, mKeyOp} {
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
	// Only `id` globs. A "*" inside system or kind would silently match nothing (they are
	// compared exactly), and silently matching nothing is the failure this Validate exists to
	// prevent. Write the whole segment as "*" — an unset dimension — to mean "any".
	for _, k := range []string{mKeySystem, mKeyKind} {
		if v := m.str(k); v != "*" && strings.ContainsRune(v, '*') {
			return fmt.Errorf("match: %q cannot glob (%q); only %q does — write the whole "+
				"segment as \"*\" to match any", k, v, mKeyID)
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

// Names reports whether the match designates exactly ONE entity by identity: a definite
// system, kind, and glob-free id. This is the case a Subject may *name* rather than select —
// an action's `set` on it creates the entity if it does not exist yet, which is how a trigger
// publishes a fact nothing has written before. A glob in any segment makes it a selection
// over what exists instead, so it can only ever act on entities that are really there.
func (m Match) Names() (entity.Ref, bool) {
	r := entity.Ref{System: m.System(), Kind: m.Kind(), ID: m.ID()}
	if r.System == "" || r.Kind == "" || r.ID == "" {
		return entity.Ref{}, false
	}
	if strings.ContainsRune(r.System+r.Kind+r.ID, '*') {
		return entity.Ref{}, false
	}
	return r, true
}

// IsEmpty reports whether the match constrains nothing (matches everything).
func (m Match) IsEmpty() bool { return len(m) == 0 }

// Selects reports whether the match names a SET to select from the store — a definite
// system+kind, as opposed to one entity by ref. A set-valued Subject must name both, since
// the engine has to know what kind it is selecting and spawning for.
func (m Match) Selects() bool {
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
//
// The result is sorted by ref, so a fan-out acts on its members in a stable order (which
// matters when a budget truncates it) and a formula's witness is a stable audit trail.
func (m Match) Select(store entity.Store) []entity.Ref {
	p, err := m.pattern()
	if err != nil {
		return nil
	}
	refs := p.Select(store)
	if m.ID() != "" {
		kept := refs[:0]
		for _, r := range refs {
			if m.matchID(r.ID) {
				kept = append(kept, r)
			}
		}
		refs = kept
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs
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

// matchID applies the `id` dimension: unset is a wildcard, otherwise a glob — so "*" alone is
// the whole kind and "hotfix-*" a family within it. The id is ONE address segment, so there is
// no separator to be significant here; path-shaped globs ("demo:pr:1/**", where "/" bounds
// what "*" may cross) are a reference question and arrive with the tony leaf.
func (m Match) matchID(id string) bool {
	want := m.ID()
	if want == "" {
		return true
	}
	return globMatch(want, id)
}

// globMatch reports whether s satisfies a "*"-glob, where "*" matches any run of characters
// (including none) and everything else is literal. It is deliberately the smallest grammar
// that makes "demo:pr:*" mean the kind. Linear: one backtrack point, so a pathological
// pattern cannot make matching blow up. Byte-wise, which is UTF-8-safe because the only
// metacharacter is ASCII.
func globMatch(pat, s string) bool {
	i, j := 0, 0        // cursors into s and pat
	star, mark := -1, 0 // the last "*" in pat, and how much of s it had consumed there
	for i < len(s) {
		switch {
		case j < len(pat) && pat[j] == s[i]:
			i, j = i+1, j+1
		case j < len(pat) && pat[j] == '*':
			star, mark, j = j, i, j+1
		case star >= 0: // mismatch — give the last "*" one more character and retry
			mark++
			i, j = mark, star+1
		default:
			return false
		}
	}
	for j < len(pat) && pat[j] == '*' { // trailing stars may match nothing
		j++
	}
	return j == len(pat)
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

// ParseExpr parses an entity-address expression into a Match — the ONE string surface for
// addressing entities by identity, shared by the CLI (`flow pause <addr>`) and the charter
// wire (a string-form Match). `*` is a wildcard per segment, and an id may also glob:
//
//	demo:pr:1         one entity
//	demo:pr:*         the whole kind
//	demo:pr:hotfix-*  a family within it (an id glob — see matchID)
//	demo:*:*          the whole system
//	demo:pr           trailing segments omitted are wildcards
//	*                 everything
//
// This is what makes "relational flows" fall out of the model rather than being a second
// primitive: steering an entity-set is the same selector, at a wider setting. (A subtree
// form — "demo:pr:1/**" — waits for the reference grammar of the tony leaf.)
func ParseExpr(s string) (Match, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf(`empty address: want "system:kind:id", with * as a wildcard`)
	}
	if strings.ContainsRune(s, '$') {
		return nil, fmt.Errorf("address %q has a $var; there is no deferred subject form — a "+
			"subject is a pattern matched against the entity a firing is about, e.g. \"demo:pr:*\" "+
			"on a completion trigger names the PR its process was about (see docs/trigger-model.md)", s)
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
