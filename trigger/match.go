package trigger

import (
	"fmt"
	"sort"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
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
// `value` IS A TONY MATCH TOO, evaluated at the payload ROOT. A payload is an ir.Node
// (entity.Payload), so tony.Match(payload, predicate) needs no conversion — the same collapse
// that already happened in engine.providerMatches, which used to marshal a map through gomap
// to call it. entity.Pattern carries the predicate as a node and evaluates nothing itself.
//
// What it bought:
//
//   - operators inside a payload predicate — `value: {stage: !or [open, merged]}` and
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
//     value: {stage: null}            stage is PRESENT, any value
//     value: !not {stage: null}       stage is ABSENT
//     value: {stage: !irtype null}    stage is present AND null
//
//     tony distinguishes absent from null — docd tombstones with null, so the difference
//     records whether a field was cleared or never written — and inheriting that beats
//     keeping a second comparison that flattens all three into one.
//
// THE PAYLOAD PREDICATE IS ONE DIMENSION, `value`: a tony match against the payload whatever
// shape it is. It replaced three spellings of itself — `payload` for the general form,
// `fields`/`entity` for object-shaped ones that also ASSERTED an object, so that
// `fields: "stage=open"` failed at install rather than quietly matching nothing.
//
// One name because the assertion was not worth a second dimension. It caught a predicate of
// the wrong SHAPE and never the typo people actually make — `{stge: open}`, a misspelled key,
// which matches nothing just as silently and which no shape check can see. And with only
// `value`, no static check can replace it: `value: open` is what you write against an entity
// whose payload is a bare state name, so a scalar predicate cannot be presumed a mistake.
//
// What the one dimension can carry is everything a payload may be (entity.Payload is any tony
// value: a count, a flag, a state name, a list, a record):
//
//	value: 42                    the payload IS 42
//	value: !or [1, 2]            an operator at the root
//	value: !glob "hotfix-*"      a scalar predicate
//	value: {stage: open}         a record — a SUBSET constraint, as a tony match over an
//	                             object always is: named fields agree, others are ignored
//
//	{"system": "demo", "kind": "pr", "value": {"stage": "open"}}   a field predicate
//	{"system": "demo", "kind": "count", "value": 42}               a root predicate
//	{"system": "demo", "kind": "pr", "id": "1"}                    one entity
//	{"system": "demo", "kind": "pr", "id": "*"}                    the whole kind
//	{"system": "demo", "kind": "pr", "id": "hotfix-*"}             an id glob
//	"demo:pr:*"                                                    the same, as an address
//	{}                                                             everything
//
// A Subject is a plain Match too: on a firing it is matched against the entity the firing is
// about — including a reflected task's own subject (its `flow`), so `subject: "demo:pr:*"`
// on a completion trigger advances the PR that task was about. There is no deferred `$var`
// form: the pattern IS the reference (see Trigger.resolveSubject).
//
//tony:schemagen=verse-match,notag,codec=custom
type Match map[string]any

// The match dimensions. `value` is the payload predicate, and the only one of the four that
// is not a string dimension — see the note on Match.
const (
	mKeyAt     = "at"   // the whole address prefix as one path: `demo.pr`, `verse.gate.question`
	mKeyPath   = "path" // the address as a PATH of any depth: `[verse-dev, drift, head, "*"]`
	mKeyRoot   = "root" // which half of the verse: empty is the world's, `verse` is verse's own
	mKeySystem = "system"
	mKeyKind   = "kind"
	mKeyID     = "id"
	mKeyValue  = "value" // the payload predicate: any tony match, at the payload root
)

// matchKeys is the closed set of dimensions a Match may carry. Its disjointness from the
// formula tag names is load-bearing and asserted in match_test.go: once `when` is a Formula
// (docs/trigger-model.md), an object is classified as a match or as a formula tag
// by its keys, so the two namespaces must not collide.
var matchKeys = map[string]bool{
	mKeyAt: true, mKeyPath: true, mKeyRoot: true, mKeySystem: true, mKeyKind: true,
	mKeyID: true, mKeyValue: true,
}

// removedKeys are dimensions a match once had, named so a charter carrying one fails with an
// explanation instead of "unknown key". `op` classified a CHANGE (set/delete); a match is now
// asked only of STATE, at a revision, and state has no op. What it was used for has a
// spelling: a create is the rising edge of "this exists" (`on`), and a delete is the rising
// edge of the sink state that precedes removal.
var removedKeys = map[string]string{
	"op": "`op` classified a change, and a match is now evaluated against state at a revision, " +
		"where nothing has one. For \"when this appears\", make it the cause: `on: {system: …, " +
		"kind: …}` fires at the commit where it first exists",
}

// Validate reports whether the match carries only known dimensions of the right shape. It is
// what makes a typo'd charter fail at install rather than silently match nothing.
func (m Match) Validate() error {
	var unknown []string
	for k := range m {
		if why, gone := removedKeys[k]; gone {
			return fmt.Errorf("match: %q is no longer a dimension: %s", k, why)
		}
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
	for _, k := range []string{mKeySystem, mKeyKind, mKeyID} {
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
	// The predicate is UNRESTRICTED in shape — any tony match, at the payload root — so the
	// only thing to refuse here is a Go value tony cannot represent at all. There is no
	// object assertion: `value` promises a value, and a payload is any tony value.
	if v, present := m[mKeyValue]; present {
		if _, ok := predicateNode(v); !ok {
			return fmt.Errorf("match: %q must be a payload predicate, got %T", mKeyValue, v)
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
//
// ID is "" for an id carrying an OPERATOR too, which reads correctly everywhere it is
// consulted: an operator is a selection over what exists, never a name, so `Names` must
// decline it and `Selects` must not depend on it. The one place "" would be wrong — the id
// filter itself — goes through matchDim, which looks at the node first.
// Root names the half of the verse a match is about: empty selects what the world is about,
// `verse` what verse writes about its own running (a task's mirror, a posed question, a rule).
// A charter that wants to react to verse's own state says so, which is what keeps a rule about
// PRs from quietly selecting the records of the tasks reviewing them.
func (m Match) Root() string   { return m.str(mKeyRoot) }
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

// Predicate is the payload predicate — a tony match evaluated at the payload root, nil when
// unset. It is a NODE, not a map of equalities: that is what carries an operator
// (`value: {stage: !glob "review*"}`) through to tony.Match, and what lets a predicate be a
// scalar so a scalar payload can be matched.
func (m Match) Predicate() *ir.Node {
	n, _ := predicateNode(m[mKeyValue])
	return n
}

// HasPredicate reports whether a payload predicate is set at all.
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
	r := entity.RefAt(append(m.prefix(), m.ID())...)
	if r.System() == "" || r.Kind() == "" || r.ID() == "" {
		return entity.Ref{}, false
	}
	// A PAYLOAD PREDICATE makes it a selection, whatever the three segments say. Naming is by
	// identity alone — that is what lets a named subject be a set of one whether or not the
	// entity exists — and the moment a match says what the entity must LOOK like, that
	// answer is no longer available: an entity that is not there does not satisfy
	// `{state: green}`, and one that is there might not either.
	//
	// Without this, a match's predicate was silently dropped wherever the three segments were
	// definite, and the failure was quiet in both directions. As a `when`, `{system: ci, kind:
	// status, id: build, value: {state: green}}` asked only whether ci:status:build EXISTED, so
	// a rule guarded on green fired on red. As an `on`, it was worse than wrong: the predicate
	// held from the moment the entity appeared, so the edge had already risen and the rule
	// never fired at all.
	if m.HasPredicate() {
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
	if strings.ContainsRune(r.System()+r.Kind()+r.ID(), '*') {
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
// dispatch a `when` against one change event. It delegates the system/kind/op/value
// dimensions to the existing typed matcher and ANDs the `id` dimension, which Match owns (so
// entity.Pattern stays as it is). An invalid match matches nothing.
func (m Match) MatchEvent(ev entity.Event) bool {
	p, err := m.pattern()
	if err != nil {
		return false
	}
	return p.MatchEvent(ev) && m.matchID(ev.Ref.ID())
}

// Select returns the refs of every stored entity the match picks — the SET face, used to
// resolve a set-valued Subject and to read a formula's facts. An invalid match picks none.
//
// The result is sorted by ref, so a fan-out acts on its members in a stable order (which
// matters when a budget truncates it) and a formula's witness is a stable audit trail.
func (m Match) Select(r entity.Reader) []entity.Ref {
	if ref, names := m.Names(); names {
		// A NAMED entity is a set of one whether or not it stands there yet. Selection asks
		// what exists; naming asks for one thing by identity, and an action's `patch` on it
		// brings it into being — which is how a trigger publishes a fact nothing has written
		// before. Reading "every entity this selects" literally would have made that
		// unspellable: the only way to create an entity would be to already have it.
		//
		// It is safe precisely because it is not a glob: a name cannot widen. `demo:pr:*`
		// still selects what is there.
		//
		// Except when it is LEAVING. You may name something that does not exist yet; you may
		// not name something on its way out. Without this a rule reacting to a departure could
		// act on the departing entity, and since a patch over an absent entity CREATES, the
		// removal landing first meant the rule resurrected it — stripped to whatever the rule
		// wrote, and without the marker, so it came back as an ordinary live entity. React to a
		// departure by writing somewhere else; the thing that is going is not a subject.
		if e, ok, err := r.Get(ref); err == nil && ok && entity.Vanishing(e.Payload) {
			return nil
		}
		return []entity.Ref{ref}
	}
	p, err := m.pattern()
	if err != nil {
		return nil
	}
	refs := p.Select(r)
	if m.hasDim(mKeyID) {
		kept := refs[:0]
		for _, r := range refs {
			if m.matchID(r.ID()) {
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
	p := entity.Pattern{
		Root:      m.Root(),
		System:    m.System(),
		Kind:      m.Kind(),
		Predicate: m.Predicate(),
	}
	if m.Deep() {
		// The path is the authority when there is one: the three dimensions cannot hold an
		// address this long, and a Pattern carrying both would have two answers.
		p.Path = m.Segs()
	}
	return p, nil
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

// predicateNode normalizes the two ways a payload predicate arrives into the one form that is
// evaluated: a NODE from a parsed charter (which preserves any operators it carries), or a Go
// map from a predicate the engine constructed itself (quorumFormula), converted here.
func predicateNode(v any) (*ir.Node, bool) {
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
	// Match in Go (the engine's own quorumFormula, a test) can write `value: 42` and mean
	// it. Conversion failure is reported rather than swallowed — a value tony cannot
	// represent is not a predicate that matches nothing, it is a mistake to name at install.
	n, err := gomap.ToTonyIR(v)
	if err != nil {
		return nil, false
	}
	return present(n)
}

// present reports a node as set only when it really is one. A NIL MAP held in an interface
// is not a nil interface, so `value: (map[string]any)(nil)` reaches the conversion, which
// answers nil — and a nil node handed to the encoder as a value panics inside CloneTo. An
// absent predicate and an empty one are both "no predicate"; neither is a node.
func present(n *ir.Node) (*ir.Node, bool) { return n, n != nil }

// MatchRef reports whether a bare REF satisfies the match's const dimensions
// (system/kind/id). It is the face used to address things by identity when there is no
// payload or change to look at — steering live tasks by their subject, where the subject
// is a ref string. `value` is not consulted here: a caller wanting a payload predicate must
// read the entity and use MatchEvent.
func (m Match) MatchRef(ref entity.Ref) bool {
	if m.Deep() {
		return entity.Pattern{Path: m.Segs()}.MatchRef(ref)
	}
	if s := m.System(); s != "" && s != ref.System() {
		return false
	}
	if k := m.Kind(); k != "" && k != ref.Kind() {
		return false
	}
	return m.matchID(ref.ID())
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
			"on a completion trigger names the PR its task was about (see docs/trigger-model.md)", s)
	}
	// A leading `verse` is the reserved root, and the three an address has follow it — the
	// same prefix rule ParseRef applies, so a charter names verse's own half the way an
	// operator does (`verse:gate:question:*`).
	rest, reserved := strings.CutPrefix(s, entity.VerseRoot+":")
	parts := strings.SplitN(rest, ":", 3) // an id may itself contain colons
	if reserved {
		parts = append([]string{entity.VerseRoot}, parts...)
	}
	return ParseSteps(parts)
}

// ParseSteps is ParseExpr over an address already SPLIT INTO STEPS — the `!verse-path [demo pr
// 1]` spelling, and the shared body of both.
//
// The steps are positional: a leading `verse` is the reserved root, then system, kind and id.
// `*` is a wildcard, which is simply an unset dimension, and an empty step is a mistake rather
// than a wildcard — `[demo, "", 1]` is somebody's template that expanded to nothing.
func ParseSteps(steps []string) (Match, error) {
	keys := []string{mKeySystem, mKeyKind, mKeyID}
	if len(steps) > 0 && strings.TrimSpace(steps[0]) == entity.VerseRoot {
		keys = append([]string{mKeyRoot}, keys...)
	}
	// DEEPER THAN THE THREE DIMENSIONS is a PATH, and the dimensions have nowhere to put it —
	// so it is carried whole rather than truncated or refused. `{system, kind, id}` stays the
	// reading of the first three, which is every address written so far; `path` is the general
	// one, and Match.Segs is where the two meet. See docs/working/entity-tree.md.
	if len(steps) > len(keys) {
		out := make([]string, 0, len(steps))
		for i, seg := range steps {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				return nil, fmt.Errorf("empty step %d in address %q", i+1, strings.Join(steps, " "))
			}
			out = append(out, seg)
		}
		return Match{mKeyPath: out}, nil
	}
	m := Match{}
	for i, key := range keys {
		if i >= len(steps) {
			break
		}
		seg := strings.TrimSpace(steps[i])
		if seg == "" {
			return nil, fmt.Errorf("empty step %d in address %q", i+1, strings.Join(steps, " "))
		}
		if seg == "*" {
			continue // a wildcard is simply an unset dimension
		}
		m[key] = seg
	}
	return m, nil
}

// Segs is the address a match names, as segments — the general reading that `path` carries
// directly and `{root, system, kind, id}` is the first three (or four) of.
//
// A `*` is a named wildcard rather than an absence, because a path has to say which POSITION is
// unconstrained: `[demo, "*", 1]` and `[demo, 1]` are different questions, where the dimension
// spelling could leave `kind` unset and mean the first. An unset dimension is dropped, so a
// short address stays short.
func (m Match) Segs() []string {
	if p, ok := m[mKeyPath].([]string); ok {
		return p
	}
	var out []string
	if r := m.Root(); r != "" {
		out = append(out, r)
	}
	for _, k := range []string{mKeySystem, mKeyKind, mKeyID} {
		v := m.str(k)
		if v == "" && m.dim(k) == nil {
			break // an unset dimension ends the address: what follows it is unconstrained too
		}
		if v == "" {
			v = "*" // an operator: a position this cannot spell as a literal
		}
		out = append(out, v)
	}
	return out
}

// Deep reports whether the match names an address deeper than the three dimensions can hold.
func (m Match) Deep() bool { _, ok := m[mKeyPath].([]string); return ok }

// prefix is the address levels a match fixes above the id: its root when it names one, then
// system and kind. It is what a watch path and a store prefilter are built from, so the two
// halves of the verse are watched at the depth each one has.
func (m Match) prefix() []string {
	var out []string
	if r := m.Root(); r != "" {
		out = append(out, r)
	}
	return append(out, m.System(), m.Kind())
}

// Subtree is a watched slice of the verse: the address levels above an entity's id, root
// included. It is an ARRAY rather than a slice because it is a map key in half the places it
// is used — one watch per distinct subtree — and a slice is not comparable.
//
// The root is first and is usually empty: a rule is about what the world is about. A rule that
// says `root: verse` watches verse's own half, one level deeper, and `Path` is what turns
// either shape into the segments a watch takes.
type Subtree struct {
	// kp is the address as a KINDED PATH — `demo.pr`, `verse.task.instance` — which is the
	// spelling the store, a read and a watch all take, and the one go-tony can compare.
	//
	// A string rather than a parsed *KPath because a Subtree is a MAP KEY (one watch per
	// distinct subtree) and a pointer is neither comparable by value nor stable. Parsing is
	// what the tree operations below do, and they are asked at install and at watch setup
	// rather than per event.
	kp string
}

// SubtreeAt builds the subtree standing at these segments, a leading `verse` being the root
// like any other segment.
func SubtreeAt(segs ...string) Subtree {
	for len(segs) > 0 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	var out []string
	for _, sg := range segs {
		if sg != "" {
			out = append(out, sg)
		}
	}
	return Subtree{kp: entity.JoinSegs(out)}
}

// SubtreeOfPath builds the subtree a kinded path names.
func SubtreeOfPath(at string) Subtree { return Subtree{kp: at} }

// KPath is the subtree as the kinded path it is.
func (s Subtree) KPath() string { return s.kp }

// Path is the subtree as segments — the form WatchSubtree and AsOf take.
func (s Subtree) Path() []string {
	segs, err := entity.PathSegs(s.kp)
	if err != nil {
		return nil
	}
	return segs
}

// Named is how many segments the subtree names, root included. Zero is the whole verse, which
// is not something a watch may open.
func (s Subtree) Named() int { return len(s.Path()) }

// Parent is the subtree one level up, and the zero subtree at the top.
func (s Subtree) Parent() Subtree {
	segs := s.Path()
	if len(segs) == 0 {
		return Subtree{}
	}
	return SubtreeAt(segs[:len(segs)-1]...)
}

// root is the half of the verse this subtree is in — `verse` for verse's own, empty for the
// world's, which is also what a subtree naming nothing at all is.
func (s Subtree) root() string {
	if segs := s.Path(); len(segs) > 0 && segs[0] == entity.VerseRoot {
		return entity.VerseRoot
	}
	return ""
}

// covers reports whether an address falls in this subtree — every segment this one names must
// match, and what it does not name is below it.
//
// It is go-tony's own prefix test (`MatchesPrefix`), which is the point of holding a kpath: a
// subtree is a path and "is this address under it" is a question the path grammar already
// answers, wildcards and quoting included. Hand-rolled segment loops got the quoting wrong the
// moment an id held a dot.
func (s Subtree) covers(ref entity.Ref) bool {
	if s.kp == "" {
		return ref.Root() == "" // the world's half: an address in verse's own is not in it
	}
	mine, err := kpath.Parse(s.kp)
	if err != nil {
		return false
	}
	theirs, err := kpath.Parse(entity.JoinSegs(ref.Segs()))
	if err != nil {
		return false
	}
	return mine.MatchesPrefix(theirs)
}

// subtreeOf is the subtree a match reads.
func subtreeOf(m Match) Subtree {
	if m.Deep() {
		// The watch is the path UP TO ITS FIRST WILDCARD, and never the leaf itself: a subtree
		// watch at the entity would see that entity's own changes and not its appearance, which
		// is what the three-dimension reading avoids by watching (root, system, kind) and
		// leaving the id to the pattern.
		segs := m.Segs()
		cut := len(segs) - 1
		for i, seg := range segs {
			if seg == "*" && i < cut {
				cut = i
				break
			}
		}
		return SubtreeAt(segs[:cut]...)
	}
	return SubtreeAt(append(rootSegs(m.Root()), m.System(), m.Kind())...)
}

// rootSegs is the leading root segment of a path, or none.
func rootSegs(root string) []string {
	if root == "" {
		return nil
	}
	return []string{root}
}

// paths renders subtrees as the segment lists a read or a watch takes.
func paths(sts []Subtree) [][]string {
	out := make([][]string, 0, len(sts))
	for _, st := range sts {
		out = append(out, st.Path())
	}
	return out
}
