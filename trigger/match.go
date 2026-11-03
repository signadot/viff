package trigger

import (
	"fmt"
	"sort"
	"strings"

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
// It is map-backed and carries two things: a PATH, the address the match reaches, and a payload
// predicate `value`. The path is a list of segments, each a name, a bare `*` (any one segment),
// or a tony operator over one segment (`!glob "hotfix-*"`, `!or [open, merged]`). There are no
// positions with names of their own — an address is a path from the top of the tree, `verse` a
// segment like any other.
//
// `value` IS A TONY MATCH TOO, evaluated at the payload ROOT. A payload is an ir.Node
// (entity.Payload), so tony.Match(payload, predicate) needs no conversion. entity.Pattern
// carries the predicate as a node and evaluates nothing itself.
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
//	{path: "demo.pr.*", value: {stage: open}}   the prs whose stage is open
//	{path: "demo.count", value: 42}                the node demo.count, holding 42
//	{path: 'demo.pr."1"'}                       one node
//	{path: [demo, pr, !glob "hotfix-*"]}            a family of them
//	demo.pr.*                                       the prs, as a kinded path
//	{}                                              everything
//
// A Subject is a plain Match too: on a firing it is matched against the entity the firing is
// about — including a reflected task's own subject (its `flow`), so `subject: demo.pr.*`
// on a completion trigger advances the PR that task was about. There is no deferred reference
// form: the pattern IS the reference (see Trigger.resolveSubject).
//
//tony:schemagen=verse-match,notag,codec=custom
type Match map[string]any

// The keys a match carries: the address it reaches and the payload predicate.
const (
	mKeyPath  = "path"  // the address as a path: `[verse-dev, drift, head, "*"]`
	mKeyValue = "value" // the payload predicate: any tony match, at the payload root
)

// matchKeys is the closed set of keys a Match may carry. Its disjointness from the formula tag
// names is load-bearing and asserted in match_test.go: an object is classified as a match or as a
// formula tag by its keys, so the two namespaces must not collide.
var matchKeys = map[string]bool{mKeyPath: true, mKeyValue: true}

// removedKeys are keys a match does not take, named so a charter carrying one fails with the
// spelling it wants instead of "unknown key".
var removedKeys = map[string]string{
	"op": "`op` classified a change, and a match is evaluated against state at a revision, " +
		"where nothing has one. For \"when this appears\", make it the cause: `on: \"a.kinded.path\"` " +
		"fires at the commit where it first exists",
	"system": addressIsAPath, "kind": addressIsAPath, "id": addressIsAPath,
	"root": addressIsAPath, "at": addressIsAPath,
}

// addressIsAPath is the refusal for every positional key: an address is a path.
const addressIsAPath = "an address is a path from the top of the tree — write `path: [demo, pr, " +
	"\"*\"]` (or the string `demo.pr.*`); `verse` is its first segment when it is verse's own, and " +
	"a glob inside a segment is `!glob \"hotfix-*\"`"

// Validate reports whether the match carries only known keys of the right shape. It is what makes
// a typo'd charter fail at install rather than silently match nothing.
func (m Match) Validate() error {
	var unknown []string
	for k := range m {
		if why, gone := removedKeys[k]; gone {
			return fmt.Errorf("match: %q is not a key: %s", k, why)
		}
		if !matchKeys[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("match: unknown key(s) %s (want %s and %s)",
			strings.Join(unknown, ", "), mKeyPath, mKeyValue)
	}
	if p, present := m[mKeyPath]; present {
		segs, ok := p.([]*ir.Node)
		if !ok || len(segs) == 0 {
			return fmt.Errorf("match: %q is a list of segments", mKeyPath)
		}
		for i, n := range segs {
			if n == nil {
				return fmt.Errorf("match: segment %d of the path is empty", i+1)
			}
			if isLayoutTag(n.Tag) {
				if _, ok := stepText(n); !ok {
					return fmt.Errorf("match: segment %d is a name or a match over one; got %v", i+1, n.Type)
				}
				continue
			}
			// Both halves of "fails at install rather than by matching nothing forever after":
			// every tag must NAME an operator (tony would skip an unknown one), and the operator
			// must ACCEPT a string operand — `!glob 5` is refused by the probe.
			if err := checkOps(n); err != nil {
				return fmt.Errorf("match: segment %d: %w", i+1, err)
			}
			if _, err := entity.SafeMatch(ir.FromString(""), n); err != nil {
				return fmt.Errorf("match: segment %d: %w", i+1, err)
			}
		}
	}
	// The predicate is UNRESTRICTED in shape — any tony match, at the payload root — so what is
	// refused here is a Go value tony cannot represent at all, and a tag that names no operator:
	// tony skips a tag it does not know, so `{stage: !gloob "op*"}` would match by plain
	// equality against a value no payload holds, and the rule would install and never fire.
	if v, present := m[mKeyValue]; present {
		pred, ok := predicateNode(v)
		if !ok {
			return fmt.Errorf("match: %q must be a payload predicate, got %T", mKeyValue, v)
		}
		if err := checkOps(pred); err != nil {
			return fmt.Errorf("match: %q: %w", mKeyValue, err)
		}
	}
	return nil
}

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

// Names reports whether the match designates exactly ONE node by identity: every segment a
// literal name, at whatever length, and no payload predicate. This is the case a Subject may
// *name* rather than select — an action's `patch` on it creates the node if nothing stands there
// yet, which is how a trigger publishes a fact nothing has written before. A `*` or an operator
// in any segment makes it a selection over what exists instead.
//
// A PAYLOAD PREDICATE makes it a selection, whatever the segments say. Naming is by identity
// alone, and the moment a match says what the node must LOOK like, that answer is no longer
// available: a node that is not there does not satisfy `{state: green}`, and one that is might not.
func (m Match) Names() (entity.Ref, bool) {
	segs := m.SegNodes()
	if len(segs) == 0 || m.HasPredicate() {
		return entity.Ref{}, false
	}
	lits := make([]string, 0, len(segs))
	for _, n := range segs {
		lit, ok := entity.PathLiteral(n)
		if !ok || entity.PathIsDescend(n) {
			return entity.Ref{}, false // a descent names a set of places rather than one
		}
		lits = append(lits, lit)
	}
	return entity.RefAt(lits...), true
}

// IsEmpty reports whether the match constrains nothing (matches everything).
func (m Match) IsEmpty() bool { return len(m) == 0 }

// Selects reports whether the match names a SET to select from the store: a path whose first
// segment is a literal, so a selection is anchored at a place rather than swept from the root.
func (m Match) Selects() bool {
	segs := m.SegNodes()
	if len(segs) == 0 {
		return false
	}
	if entity.PathIsDescend(segs[0]) {
		return false // a descent at the head names no place to read under
	}
	_, ok := entity.PathLiteral(segs[0])
	return ok
}

// MatchEvent reports whether an observed change satisfies the match — the EDGE face, used to
// dispatch a `when` against one change event. An invalid match matches nothing.
func (m Match) MatchEvent(ev entity.Event) bool {
	p, err := m.pattern()
	if err != nil {
		return false
	}
	return p.MatchEvent(ev)
}

// Select returns the refs of every stored node the match picks — the SET face, used to resolve a
// set-valued Subject and to read a formula's facts. An invalid match picks none.
//
// The result is sorted by ref, so a fan-out acts on its members in a stable order (which matters
// when a budget truncates it) and a formula's witness is a stable audit trail.
func (m Match) Select(r entity.Reader) []entity.Ref {
	if ref, names := m.Names(); names {
		// A NAMED node is a set of one whether or not it stands there yet. Selection asks what
		// exists; naming asks for one thing by identity, and an action's `patch` on it brings it
		// into being. It is safe because a name cannot widen.
		//
		// Except when it is LEAVING. You may name something that does not exist yet; you may not
		// name something on its way out: a patch over an absent node CREATES, so a rule reacting
		// to a departure would resurrect what is going. React to a departure by writing elsewhere.
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
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs
}

// pattern is the match as the store's selector: its path and its predicate.
func (m Match) pattern() (entity.Pattern, error) {
	if err := m.Validate(); err != nil {
		return entity.Pattern{}, err
	}
	return entity.Pattern{Path: m.SegNodes(), Predicate: m.Predicate()}, nil
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
		name = strings.TrimPrefix(name, "!")
		// A path read is verse's own, answered before tony is handed the match (path.go), so
		// the one of the two that tony has no operator for is an operator here all the same.
		if name == ListPathsTag {
			continue
		}
		if mergeop.Lookup(name) == nil {
			name = "!" + name
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

// MatchRef reports whether a bare REF is at or under what the match's path reaches. It is the
// face used to address things by identity when there is no payload or change to look at —
// steering live tasks by their subject. `value` is not consulted: a caller wanting a payload
// predicate reads the node and uses MatchEvent.
func (m Match) MatchRef(ref entity.Ref) bool { return entity.Pattern{Path: m.SegNodes()}.MatchRef(ref) }

// ParseExpr parses an address expression into a Match — the ONE string surface for addressing by
// identity, shared by the CLI (`verse flow --subject <addr>`) and the charter wire. It is a KINDED
// PATH, and a whole `*` segment is any one segment:
//
//	demo.pr.1         one node
//	demo.pr.*         the prs
//	demo.pr           the node demo.pr itself
//	*                 every top-level node
//	demo.pr."a.b"     an id holding a dot
//
// A glob inside a segment has no string spelling: it is `!glob` in `path:`.
func ParseExpr(s string) (Match, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty address: want a kinded path, e.g. demo.pr.*")
	}
	if strings.ContainsRune(s, '$') {
		return nil, fmt.Errorf("address %q has a $var; there is no deferred subject form — a "+
			"subject is a pattern matched against the node a firing is about, e.g. \"demo.pr.*\" "+
			"on a completion trigger names the PR its task was about (see docs/reference/condition.md)", s)
	}
	// AS SEGMENTS, KINDS KEPT (entity.PatternNodes): a bare `*` is the wildcard, a bare `..` the
	// descent, and a quoted `"*"` or `".."` the name, spelled as the match over it — the same
	// meaning the list form gives those characters. Flattened to text, a quoted `*` read back as
	// the wildcard and a rule about one node fired on every node beside it.
	//
	// A SUBJECT IS A QUESTION, so it takes `..`: `verse-dev..finding` selects every finding
	// beneath verse-dev, at whatever depth the rule that derived it put one.
	segs, err := entity.PatternNodes(s)
	if err != nil {
		return nil, err
	}
	return PathMatch(segs)
}

// ParseSteps is an address already SPLIT INTO STEPS, as a path: each step one segment, `*` any one.
// An empty step is a mistake rather than a wildcard — `[demo, "", 1]` is somebody's template that
// expanded to nothing.
func ParseSteps(steps []string) (Match, error) {
	out := make([]*ir.Node, 0, len(steps))
	for i, seg := range steps {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return nil, fmt.Errorf("empty step %d in address %q", i+1, strings.Join(steps, " "))
		}
		out = append(out, ir.FromString(seg))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("match: a path names no segments")
	}
	return Match{mKeyPath: out}, nil
}

// Segs is the path as names, an operator read as `*` — what a prefix, a watch and a message want.
func (m Match) Segs() []string {
	p := m.SegNodes()
	out := make([]string, 0, len(p))
	for _, seg := range p {
		lit, ok := entity.PathLiteral(seg)
		if !ok {
			lit = "*" // an operator names no literal: for a prefix it is unconstrained
		}
		out = append(out, lit)
	}
	return out
}

// SegNodes is the path as PATTERNS, one per segment — the reading that keeps an operator.
func (m Match) SegNodes() []*ir.Node {
	p, _ := m[mKeyPath].([]*ir.Node)
	return p
}

// PathMatch is a path given as nodes — `path: ""…""` and `!verse-path [a b c]` alike.
func PathMatch(steps []*ir.Node) (Match, error) {
	segs := make([]*ir.Node, 0, len(steps))
	for i, n := range steps {
		u := ir.Uncomment(n)
		if u == nil {
			return nil, fmt.Errorf("match: segment %d of a path is empty", i+1)
		}
		// A NAME is its text, whatever scalar it was written as — `1` is the id of half the nodes
		// anybody writes down — and a tag the parser attached to record layout is no tag.
		if isLayoutTag(u.Tag) {
			text, ok := stepText(u)
			if !ok {
				return nil, fmt.Errorf("match: segment %d is a name or a match over one; got %v",
					i+1, u.Type)
			}
			if u.Type != ir.StringType {
				u = ir.FromString(text)
			}
		}
		segs = append(segs, u)
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("match: a path names no segments")
	}
	return Match{mKeyPath: segs}, nil
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
	// pat is the whole pattern this subtree was cut from, as a kinded path — `demo.pr.*`,
	// `verse-dev..finding` — when a match named it. The watch is opened with it: the prefix
	// says where to read and the pattern says which nodes are reported, which is the only way
	// to say a question that reaches across depths.
	pat string
	// depth is how deep the entities this subtree is watched FOR stand — the whole address's
	// depth, not the depth below the prefix. A watch cannot work it out (a document does not
	// say where its entities end), and the thing that knows is whatever named the subtree: a
	// match knows how deep its subjects are, and a caller naming one entity knows its depth.
	//
	// It is part of the value because a Subtree is a map key and one watch is opened per
	// distinct one — two rules reading one prefix at two depths are two watches, which is
	// exactly right.
	depth int
}

// Depth is how deep the entities under this subtree stand. See the field.
func (s Subtree) Depth() int { return s.depth }

// Pattern is the question this subtree is watched with: the match's own path where one named it,
// and the prefix at its depth otherwise.
func (s Subtree) Pattern() *kpath.KPath {
	if s.pat != "" {
		if kp, err := kpath.Parse(s.pat); err == nil {
			return kp
		}
	}
	return entity.PrefixPattern(s.Path(), s.depth)
}

// SubtreeAt builds the subtree standing at these segments, a leading `verse` being the root
// like any other segment.
//
// Its depth is the segments' own: this names A PLACE, and the caller that watches one named
// this way is watching what stands AT it. A prefix whose entities are deeper comes from
// subtreeOf, which asks the match.
func SubtreeAt(segs ...string) Subtree {
	return Subtree{kp: entity.JoinSegs(segs), depth: len(segs)}
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
		return true // the root: every address is under it
	}
	mine := s.segments()
	return mine != nil && entity.Under(mine, ref.Segs(), entity.KPathAdmits)
}

// pattern is the subtree as one kinded path — the root (nil) when it names no path, which every
// change is under.
func (s Subtree) pattern() *kpath.KPath {
	if s.kp == "" {
		return nil
	}
	kp, err := kpath.Parse(s.kp)
	if err != nil {
		return nil
	}
	return kp
}

// segments is the subtree as kinded-path segments, wildcards kept as wildcards and a quoted `*`
// as a name — which Path, a reader of stored names, cannot say.
func (s Subtree) segments() []*kpath.KPath {
	kp, err := kpath.Parse(s.kp)
	if err != nil {
		return nil
	}
	return entity.KPathSegments(kp)
}

// subtreeOf is the subtree a match reads: the path UP TO ITS FIRST WILDCARD, and never the leaf
// itself — a subtree watch at the node would see that node's own changes and not its appearance —
// watched for nodes at the path's own depth.
func subtreeOf(m Match) Subtree {
	segs := m.Segs()
	cut := len(segs) - 1
	if cut < 0 {
		cut = 0
	}
	for i, seg := range segs {
		if (seg == "*" || seg == "..") && i < cut {
			cut = i
			break
		}
	}
	// A PREFIX, watched for nodes deeper than the prefix itself.
	st := SubtreeAt(segs[:cut]...)
	st.depth = len(segs)
	if kp, err := entity.KPathOf(segs); err == nil {
		st.pat = kp // the whole question, for the watch: a `*` and a `..` are segments of it
	}
	return st
}

// paths renders subtrees as the segment lists a read or a watch takes.
func paths(sts []Subtree) [][]string {
	out := make([][]string, 0, len(sts))
	for _, st := range sts {
		out = append(out, st.Path())
	}
	return out
}
