package entity

import (
	"fmt"
	"strconv"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
)

// Pattern is a conjunctive selector over the tree: a PATH naming the nodes it reaches, an
// optional change Op, and a payload predicate. One Pattern serves both faces of "select" —
// matching an observed change (MatchEvent, the trigger "when") and listing the stored nodes that
// satisfy it (Select, a formula's fact-read).
//
//tony:schemagen=verse-pattern,notag
type Pattern struct {
	// Path is the address the pattern reaches, one segment at a time, from the top of the tree.
	//
	// A segment is a NODE rather than a string because a segment may be an OPERATOR: `!glob
	// "head-*"` is a perfectly good thing to say about one level of an address. Carrying strings
	// would have to drop the tag, which is worse than refusing it — a pattern that reads as a glob
	// and matches literally is a rule that silently selects nothing.
	//
	// It is on the wire, and a source's Covers is why: a git source covers `git.verse.ref.*` and
	// `git.verse.head`, one place per repository at two depths. A charter writes it as
	// `trigger.Match`'s `path:`, which compiles to it.
	Path []*ir.Node `tony:"field=path,omitzero"`
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
func (p Pattern) MatchRef(ref Ref) bool { return p.pathCovers(ref) }

// pathCovers reports whether an address is at or under the pattern's path. A segment of `*`
// matches any one segment, and a segment carrying an operator is asked of the address's own
// segment as a tony match — the same grammar a dimension takes, one level at a time.
func (p Pattern) pathCovers(ref Ref) bool {
	if PathDescends(p.Path) {
		return patternReaches(p.Path, ref.Segs())
	}
	return Under(p.Path, ref.Segs(), SegMatches)
}

// PathDescends reports whether a pattern's path reaches across depths — a `..` segment.
func PathDescends(path []*ir.Node) bool {
	for _, seg := range path {
		if PathIsDescend(seg) {
			return true
		}
	}
	return false
}

// PathIsDescend reports whether a path segment is the descent `..`, which reaches the node it
// stands at and every node beneath it. It is the string `..`, as a `*` position is the string
// `*`: a name that is two dots is written as a quoted step and arrives here as any other name.
func PathIsDescend(seg *ir.Node) bool {
	lit, ok := PathLiteral(seg)
	return ok && lit == ".."
}

// patternReaches is the pattern asked of one address, `..` reaching zero or more segments —
// which is what makes a subject of different depths one question.
func patternReaches(path []*ir.Node, segs []string) bool {
	for i, seg := range path {
		if PathIsDescend(seg) {
			for n := 0; n <= len(segs); n++ {
				if patternReaches(path[i+1:], segs[n:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 || !SegMatches(seg, segs[0]) {
			return false
		}
		segs = segs[1:]
	}
	return len(segs) == 0
}

// SafeMatch is tony.Match with a floor under it: a predicate that PANICS comes back as an
// error, not as a dead process.
//
// A charter is user input, and a predicate in one is evaluated on the engine's watch goroutine
// at every commit that lands under its rule — so a panic in a match is a panic in the daemon,
// reached by a line somebody typed. Nothing else in verse lets a written rule do that: an
// unparseable one is refused at install, a malformed operator is a non-match, a read that
// cannot resolve fails its firing. This is the same promise for the case where the failure is a
// panic rather than an error.
//
// IT IS A FLOOR, NOT A FIX FOR A KNOWN DEFECT, and the distinction is why it stays. The
// operators are a moving surface — go-tony fixed a `!let` binding indexed without checking, and
// a dive whose patch deletes what it matched, in the space of one release each — and the floor
// is what makes the next one a logged refusal instead of a restart. Verifying it against any
// particular version would be verifying the wrong thing.
//
// It does NOT make the rule right — a predicate that panics is broken and its refusal to answer
// is reported as an error by every caller, which then treats it as a non-match. What it buys is
// that the OTHER rules go on running, and that the operator finds out from a log line rather
// than from a restart.
func SafeMatch(value, pattern *ir.Node) (ok bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			ok, err = false, fmt.Errorf("the predicate could not be evaluated: %v", r)
		}
	}()
	return tony.Match(value, pattern)
}

// SegMatches reports whether one address segment satisfies one path segment.
//
// EVERY ELEMENT OF A PATH IS A MATCH, in tony's own grammar, asked of that one segment: `demo`
// is an equality, `!glob "hot-*"` a glob, `!or [hot, cold]` either, `!not archived` anything
// else. Nothing here enumerates the operators — tony.Match owns them, which is what keeps a
// path segment as expressive as a payload predicate and stops this growing a second grammar.
//
// ONE CONVENTION sits above that: a BARE `*` is any segment, because that is what every other
// address spelling in verse means by it. It is the untagged case only — inside an operator a
// `*` is an ordinary character, which is how an id that IS an asterisk is named: `!glob "[*]"`,
// `!glob "\\*"` or `!or ["*"]` all say it, in the grammar rather than in an escape of verse's
// own. (`!raw` is the STORE's escape and belongs to it; a charter never writes one.)
//
// An untagged scalar is compared as its TEXT, so `!verse-path [demo pr 1]` matches the id `1` —
// an address segment is a name, whatever it was written as.
func SegMatches(want *ir.Node, seg string) bool {
	if want == nil {
		return true
	}
	if want.Tag == "" {
		text, ok := segText(want)
		return ok && (text == "*" || text == seg)
	}
	ok, err := SafeMatch(ir.FromString(seg), want)
	return err == nil && ok
}

// segText is an untagged scalar as the name it spells, and whether it spells one at all.
func segText(n *ir.Node) (string, bool) {
	switch n.Type {
	case ir.StringType:
		return n.String, true
	case ir.NumberType:
		switch {
		case n.Number != "":
			return n.Number, true
		case n.Int64 != nil:
			return strconv.FormatInt(*n.Int64, 10), true
		case n.Float64 != nil:
			return strconv.FormatFloat(*n.Float64, 'g', -1, 64), true
		}
		return "", false
	case ir.BoolType:
		if n.Bool {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

// PathLiteral is a path segment's literal text, and whether it is one at all — what a WATCH can
// narrow on. An operator has no literal: it constrains the level without naming it, so a watch
// stops there and the pattern does the rest.
func PathLiteral(want *ir.Node) (string, bool) {
	if want == nil || want.Tag != "" {
		return "", false
	}
	text, ok := segText(want)
	if !ok || text == "*" {
		return "", false
	}
	return text, true
}

// Place is the literal path this pattern is asked under — what a read narrows to, and the place
// a source's cover claims. A caller that owns what a pattern reaches collects no higher than
// this: above it is somebody else's.
func (p Pattern) Place() []string { return p.literalPrefix() }

// literalPrefix is the leading segments of the path that name a place rather than a wildcard —
// the deepest subtree a read can be narrowed to before the pattern has to do the picking.
func (p Pattern) literalPrefix() []string {
	var out []string
	for i, seg := range p.Path {
		lit, ok := PathLiteral(seg)
		if !ok || PathIsDescend(seg) || i == len(p.Path)-1 {
			break // a wildcard, an operator, a descent, or the leaf being selected: read above it
		}
		out = append(out, lit)
	}
	return out
}

// Depth is how many segments the pattern names, which is the depth of the nodes it selects.
func (p Pattern) Depth() int { return len(p.Path) }

// Select returns the refs of every stored node the pattern picks: the nodes its path reaches whose
// payload satisfies the predicate.
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
	// A pattern naming no path selects nothing: a match that lost its address must not quietly
	// become a sweep of the whole tree.
	if len(p.Path) == 0 {
		return nil
	}
	// THE PATTERN'S OWN DEPTH is what the store is asked for: a path of four segments selects
	// nodes four deep, and the literal head of it is the prefix to read under. A pattern that
	// DESCENDS has no one depth, so it reads the subtree and walks it (selectUnder).
	var ents []Entity
	if PathDescends(p.Path) {
		ents = p.selectUnder(r)
	} else {
		ents = r.ListAt(p.literalPrefix(), len(p.Path), p.pathCovers)
	}
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

// selectUnder is Select's listing for a pattern that reaches across depths: the subtree at its
// literal prefix, read once, walked for every node the pattern reaches.
//
// One read rather than one per depth, and it is the same read `verse data demo '..'` makes —
// which is the point of a descent being a pattern rather than a loop somebody writes.
func (p Pattern) selectUnder(r Reader) []Entity {
	prefix := p.literalPrefix()
	node, ok, err := r.ReadAt(prefix)
	if err != nil || !ok {
		return nil
	}
	var out []Entity
	var walk func(n *ir.Node, segs []string)
	walk = func(n *ir.Node, segs []string) {
		if patternReaches(p.Path, segs) {
			out = append(out, Entity{Ref: RefAt(segs...), Payload: nodeToPayload(n)})
		}
		if n = ir.Uncomment(n); n == nil || n.Type != ir.ObjectType {
			return
		}
		for i, f := range n.Fields {
			walk(n.Values[i], append(append([]string{}, segs...), fieldName(f)))
		}
	}
	walk(node, prefix)
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
	at := fieldAt(payload, p.At)
	if at == nil {
		return false // nothing to match against — a deleted entity, an absent one, or no such field
	}
	ok, err := SafeMatch(at, p.Predicate)
	return err == nil && ok
}

// fieldAt walks a payload to a field path, nil if it does not lead anywhere. Get answers nil
// for a scalar as well as for an absent field, so a path THROUGH a non-object stops here
// rather than needing a case of its own.
func fieldAt(payload Payload, field []string) Payload {
	for _, f := range field {
		if payload = Get(payload, f); payload == nil {
			return nil
		}
	}
	return payload
}

// MatchesPayload and MatchesChange are the pattern MINUS its address dimensions — what a READ
// asks, where the path has already said which entities are in question.
//
// They exist because the address dimensions do not mean the same thing on both faces. A rule's
// pattern selects: an unnamed root is the world's half, deliberately, so a charter cannot reach
// into verse's own by omission. A read's pattern filters what a path already chose, and a path
// that named both halves means both — so asking the pattern about the address again would
// answer a question the reader did not ask.
func (p Pattern) MatchesPayload(n Payload) bool { return p.matchPayload(n) }

// StorePattern is the predicate as a pattern the STORE matches each member of a set against,
// nil when there is none: the predicate itself, nested under the fields `at` roots it at, since
// a match of `{labels: P}` against a payload is a match of P against its `labels`. It is what
// lets a filtered listing be asked of the store rather than read and filtered here.
func (p Pattern) StorePattern() Payload {
	if p.Predicate == nil {
		return nil
	}
	out := clone(p.Predicate)
	for i := len(p.At) - 1; i >= 0; i-- {
		out = Obj{p.At[i]: out}.Payload()
	}
	return out
}

func (p Pattern) MatchesChange(ev Event) bool {
	if p.Op != "" && p.Op != ev.Op {
		return false
	}
	return p.matchPayload(ev.To)
}
