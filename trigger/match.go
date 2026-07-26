package trigger

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"
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
// The swap has STARTED, at the one dimension where it is free: `id` and `op` may carry a
// tony match instead of a string — `id: !glob "hotfix-*"`, or `{"id": {"glob": "hotfix-*"}}`
// in a JSON-shaped charter — and are then evaluated by tony.Match. `system` and `kind`
// cannot, and that is structural rather than unfinished: those two become a docd WATCH PATH
// (Formula.Subtrees, engine.witnessPreconditions) and the store's List prefilter (Select), and
// an operator has no path. `id` is only ever a filter, which is exactly why it goes first.
//
// `fields` IS NOW A TONY MATCH TOO, evaluated at the payload ROOT. A payload is an ir.Node
// (entity.Payload), so tony.Match(payload, predicate) needs no conversion — the same collapse
// that already happened in engine.providerMatches, which used to marshal a map through gomap
// to call it. entity.Pattern carries the predicate as a node and evaluates nothing itself.
//
// What it bought:
//
//   - operators inside a payload predicate — `fields: {stage: !or [open, merged]}` and
//     `{stage: !glob "op*"}` match, where before they were REFUSED with a diagnostic saying a
//     payload predicate is equality-only;
//   - a scalar payload against a field predicate answers false without a special case;
//   - extra payload fields are still ignored, so it remains a subset match.
//
// Two semantics CHANGED with it, deliberately, and each is pinned by a test:
//
//  1. NUMBERS ARE TYPE-EXACT. `{n: 2.0}` no longer matches `{n: 2}`. The old tolerance
//     existed because a payload was a Go value whose type came from whichever decoder built
//     it (encoding/json's float64, a literal's int); payload-as-node removed that variance,
//     since every dialect goes through the tony parser and `2` is an integer however it was
//     written. Integer WIDTHS still agree — int/int32/int64/uint are one tony integer. What
//     is gone is int-vs-float, which is now a difference in the value rather than in who
//     decoded it (entity.TestMatchPayloadIntAndFloatAreDistinct).
//
//  2. ABSENT IS NOT NULL, and a null in a predicate is a PRESENCE test. `{stage: null}` now
//     means "stage is there, whatever its value" — a null pattern matches any value in tony,
//     so the only thing it constrains is that the field exists. It used to mean the
//     opposite: an absent key read as nil, so `{stage: null}` was how you spelled "has NO
//     stage". Three questions, three spellings, pinned in
//     entity.TestNullPresenceAbsenceIdioms:
//
//     fields: {stage: null}            stage is PRESENT, any value
//     fields: !not {stage: null}       stage is ABSENT
//     fields: {stage: !irtype null}    stage is present AND null
//
//     tony distinguishes absent from null — docd tombstones with null, so the difference
//     records whether a field was cleared or never written — and inheriting that beats
//     keeping a second comparison that flattens all three into one.
//
// THE PREDICATE HAS THREE SPELLINGS, one meaning. `payload` is the general form — a tony
// match against the payload whatever shape it is — and `fields`/`entity` are its
// object-shaped spellings, kept because a record is what almost every charter matches and
// because those names read better over one. Only one may be set: two would be two predicates
// on the same value with no stated way to combine them.
//
// The split is on purpose rather than a loosening of `fields`. Those two names promise
// fields, and requiring an object under them is what keeps `fields: "stage=open"` an
// install-time error instead of a predicate that quietly matches almost nothing. `payload`
// promises nothing, so it can carry what a payload may now actually be:
//
//	payload: 42                    the payload IS 42
//	payload: !or [1, 2]            an operator at the root
//	payload: !glob "hotfix-*"      a scalar predicate
//	payload: {stage: open}         the object case, spelled generally
//	fields:  {stage: open}         the same, spelled for a record
//
//	{"system": "demo", "kind": "pr", "fields": {"stage": "open"}}   a field predicate
//	{"system": "demo", "kind": "count", "payload": 42}              a root predicate
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
//
//tony:schemagen=verse-match,notag,codec=custom
type Match map[string]any

// The match dimensions. `entity` is accepted as an alias for `fields` (docs/trigger-model.md
// spells a Subject's payload predicate `entity:`).
const (
	mKeySystem  = "system"
	mKeyKind    = "kind"
	mKeyID      = "id"
	mKeyOp      = "op"
	mKeyFields  = "fields"
	mKeyEntity  = "entity"  // alias for fields, the spelling a Subject uses
	mKeyPayload = "payload" // the same predicate, unrestricted: any tony value, not only fields
)

// payloadKeys are the three spellings of the ONE payload predicate, in resolution order.
// `payload` is the general form — a tony match against the payload whatever shape it is —
// and `fields`/`entity` are the object-shaped spellings kept because that is what almost
// every charter writes and because their names read better over a record.
var payloadKeys = []string{mKeyPayload, mKeyFields, mKeyEntity}

// matchKeys is the closed set of dimensions a Match may carry. Its disjointness from the
// formula tag names is load-bearing and asserted in match_test.go: once `when` is a Formula
// (docs/trigger-model-plan.md step 2), an object is classified as a match or as a formula tag
// by its keys, so the two namespaces must not collide.
var matchKeys = map[string]bool{
	mKeySystem: true, mKeyKind: true, mKeyID: true,
	mKeyOp: true, mKeyFields: true, mKeyEntity: true, mKeyPayload: true,
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
		v, present := m[k]
		if !present {
			continue
		}
		if _, ok := v.(string); ok {
			continue
		}
		n, ok := v.(*ir.Node)
		if !ok {
			return fmt.Errorf("match: %q must be a string, got %T", k, v)
		}
		// An operator on system or kind has nowhere to run: those two name the subtree a
		// formula watches and the set the store lists, and a watch path cannot be a
		// predicate. Saying so beats letting it match nothing.
		if k == mKeySystem || k == mKeyKind {
			return fmt.Errorf("match: %q cannot take an operator (%s) — it names the subtree "+
				"to watch and the set to list, which must be a literal; write the whole "+
				"segment as \"*\" to match any, and put the operator on %q",
				k, tagText(n), mKeyID)
		}
		// Both halves of "fails at install rather than by matching nothing forever after":
		// every tag must NAME an operator (tony would skip an unknown one), and the
		// operator must ACCEPT a string operand — `!glob 5` is refused by the probe.
		if err := checkOps(n); err != nil {
			return fmt.Errorf("match: %q: %w", k, err)
		}
		if _, err := tony.Match(ir.FromString(""), n); err != nil {
			return fmt.Errorf("match: %q: %w", k, err)
		}
	}
	for _, k := range payloadKeys {
		if v, present := m[k]; present {
			n, ok := fieldsNode(v)
			if !ok {
				return fmt.Errorf("match: %q must be a payload predicate, got %T", k, v)
			}
			if k == mKeyPayload {
				continue // the general form: any tony value, which is the point of it
			}
			// Field-shaped, still: `fields` and `entity` name a predicate OVER FIELDS, and
			// their own names say so. A payload predicate may now carry operators, but it
			// may not stop being an object here — that is what keeps `fields: "stage=open"`
			// an install-time error rather than a predicate that quietly matches only an
			// entity whose whole payload is that string.
			//
			// A root-scalar predicate (`42` matching the payload 42) is a real thing the
			// matcher can now express; it wants a dimension whose name does not promise
			// fields. See the note on Match.
			if n.Type != ir.ObjectType {
				return fmt.Errorf("match: %q must be an object of field predicates, got %s "+
					"— write %q to match the payload ROOT (a scalar, a list, or an operator "+
					"over one)", k, n.Type, mKeyPayload)
			}
		}
	}
	// One predicate, one spelling. Two would be two predicates on the same value with no
	// stated way to combine them, and picking one silently is how a charter comes to mean
	// something its author did not write.
	var spelled []string
	for _, k := range payloadKeys {
		if _, present := m[k]; present {
			spelled = append(spelled, strconv.Quote(k))
		}
	}
	if len(spelled) > 1 {
		return fmt.Errorf("match: set only one payload predicate, got %s — %q is the general "+
			"form and %q/%q are its object-shaped spellings",
			strings.Join(spelled, " and "), mKeyPayload, mKeyFields, mKeyEntity)
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
//
// ID is "" for an id carrying an OPERATOR too, which reads correctly everywhere it is
// consulted: an operator is a selection over what exists, never a name, so `Names` must
// decline it and `Selects` must not depend on it. The one place "" would be wrong — the id
// filter itself — goes through matchDim, which looks at the node first.
func (m Match) System() string { return m.str(mKeySystem) }
func (m Match) Kind() string   { return m.str(mKeyKind) }
func (m Match) ID() string     { return m.str(mKeyID) }

// dim returns the tony match a dimension carries, or nil when it is a plain string or
// absent. It is the ONE place the two spellings of a dimension are told apart.
func (m Match) dim(key string) *ir.Node {
	n, _ := m[key].(*ir.Node)
	return n
}

// hasDim reports whether a dimension constrains anything, in either spelling.
func (m Match) hasDim(key string) bool { return m.str(key) != "" || m.dim(key) != nil }

// Fields is the payload predicate — a tony match evaluated at the payload root, nil when
// unset. It is a NODE, not a map of equalities: that is what carries an operator
// (`fields: {stage: !glob "review*"}`) through to tony.Match, and what lets a predicate be a
// scalar so a scalar payload can be matched.
func (m Match) Predicate() *ir.Node {
	for _, k := range payloadKeys {
		if n, ok := fieldsNode(m[k]); ok {
			return n
		}
	}
	return nil
}

// HasPredicate reports whether a payload predicate is set at all, in any spelling.
func (m Match) HasPredicate() bool { return m.Predicate() != nil }

// FieldString reads a LITERAL string out of the payload predicate — "which choice does this
// quorum count", for a facts match the engine built itself as {choice: approve}.
//
// It answers "" when the key is absent or its predicate is not a plain string, which is the
// honest answer once a predicate may carry operators: there is no single value behind
// `{choice: !glob "appr*"}` to recover, and inventing one would be worse than reporting none.
func (m Match) FieldString(key string) string {
	f := m.Predicate()
	if f == nil || f.Type != ir.ObjectType {
		return ""
	}
	v := ir.Get(f, key)
	if v == nil || v.Type != ir.StringType || v.Tag != "" {
		return ""
	}
	return v.String
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
	// An operator on any segment is a selection, whatever the other two say. This cannot
	// fire today (ID() is "" for an operator, so the check above already caught it) and is
	// kept because the consequence of getting it wrong is not a failed match but a CREATED
	// entity — an action's `set` on a named subject brings it into being.
	for _, k := range []string{mKeySystem, mKeyKind, mKeyID} {
		if m.dim(k) != nil {
			return entity.Ref{}, false
		}
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
	// `op` is applied twice when it is a plain string — once by the pattern, once here —
	// which is the same answer either way. The second call is what an operator-valued `op`
	// needs, since pattern() cannot carry one.
	return p.MatchEvent(ev) && m.matchID(ev.Ref.ID) && m.matchDim(mKeyOp, string(ev.Op))
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
	if m.hasDim(mKeyID) {
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
		System:    m.System(),
		Kind:      m.Kind(),
		Op:        entity.Op(m.str(mKeyOp)),
		Predicate: m.Predicate(),
	}, nil
}

// matchID applies the `id` dimension. See matchDim; kept as its own name because the id is
// the dimension callers reason about.
func (m Match) matchID(id string) bool { return m.matchDim(mKeyID, id) }

// matchDim applies one const dimension to one string, in whichever spelling it was written.
//
// A tony match wins when there is one, and it is the RICHER grammar: `!glob` is
// filepath.Match, so "*" does not cross "/" and "?"/"[…]" are metacharacters — which is the
// path-shaped globbing this type always said would arrive with the tony leaf.
//
// A plain string keeps the older, smaller grammar: for `id`, a "*"-glob where "*" crosses
// anything (globMatch); for everything else, equality. Those are not the same rule, and
// they are not meant to be — an existing charter's `id: "a*"` must keep matching "a/b",
// while `id: !glob "a*"` means the stricter thing the author asked for.
func (m Match) matchDim(key, s string) bool {
	if n := m.dim(key); n != nil {
		ok, err := tony.Match(ir.FromString(s), n)
		return err == nil && ok
	}
	want := m.str(key)
	if want == "" {
		return true
	}
	if key == mKeyID {
		return globMatch(want, s)
	}
	return want == s
}

// ── the tag chain ────────────────────────────────────────────────────────────
//
// A tony tag is a DOT-SEPARATED CHAIN, not one name: `!or.bracket` is two tags on one node,
// decomposed with ir.TagArgs (head, args, rest). Everything below walks the chain rather
// than comparing Tag as a string, because `n.Tag == "!glob"` is true of only one of the
// several ways a parser may legitimately spell the same document.

// layoutTags are attached by the PARSER to record how the source was written — whether a
// collection was bracketed, whether a string was a block literal. They are not something an
// author wrote, so nothing here may read them as an operator.
var layoutTags = map[string]bool{"!bracket": true, "!literal": true}

// semTags returns the tags an AUTHOR wrote, in chain order, each "!"-prefixed.
func semTags(tag string) []string {
	var out []string
	for tag != "" {
		head, _, rest := ir.TagArgs(tag)
		if !layoutTags[head] {
			out = append(out, head)
		}
		tag = rest
	}
	return out
}

// isLayoutTag reports whether a tag chain carries nothing an author wrote.
func isLayoutTag(tag string) bool { return len(semTags(tag)) == 0 }

// checkOps reports the first tag in a match that names no operator.
//
// It exists because tony will NOT report one: mergeop.SplitChild walks the chain looking for
// a registered tag and quietly skips the rest, so `!gloob "hotfix-*"` matches by plain
// equality and a charter typo becomes a trigger that never fires. Checking here is what
// turns that back into an install-time error.
func checkOps(n *ir.Node) error {
	if n == nil {
		return nil
	}
	for _, t := range semTags(n.Tag) {
		name, _, _ := ir.TagArgs(t)
		if mergeop.Lookup(strings.TrimPrefix(name, "!")) == nil {
			return fmt.Errorf("unknown operator %q (tony ignores a tag it does not know, so "+
				"this would match by plain equality instead of failing)", name)
		}
	}
	for _, v := range n.Values {
		if err := checkOps(v); err != nil {
			return err
		}
	}
	return nil
}

// tagText renders a node's operator for an error message — "!glob" — falling back to the
// node's type when it carries no tag an author wrote.
func tagText(n *ir.Node) string {
	if n == nil {
		return "null"
	}
	if ts := semTags(n.Tag); len(ts) > 0 {
		return strings.Join(ts, ".")
	}
	return n.Type.String()
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
// fieldsNode normalizes the two ways a payload predicate arrives into the one form that is
// evaluated: a NODE from a parsed charter (which preserves any operators it carries), or a Go
// map from a predicate the engine constructed itself (quorumFormula), converted here.
func fieldsNode(v any) (*ir.Node, bool) {
	switch f := v.(type) {
	case nil:
		return nil, false
	case *ir.Node:
		return f, f != nil
	case entity.Obj:
		return present(f.Payload())
	case map[string]any:
		return present(entity.Obj(f).Payload())
	}
	// Any other Go value: a predicate may be a scalar or a list now, so a caller building a
	// Match in Go (the engine's own quorumFormula, a test) can write `payload: 42` and mean
	// it. Conversion failure is reported rather than swallowed — a value tony cannot
	// represent is not a predicate that matches nothing, it is a mistake to name at install.
	n, err := gomap.ToTonyIR(v)
	if err != nil {
		return nil, false
	}
	return present(n)
}

// present reports a node as set only when it really is one. A NIL MAP held in an interface
// is not a nil interface, so `fields: (map[string]any)(nil)` reaches the conversion, which
// answers nil — and a nil node handed to the encoder as a value panics inside CloneTo. An
// absent predicate and an empty one are both "no predicate"; neither is a node.
func present(n *ir.Node) (*ir.Node, bool) { return n, n != nil }

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
