package trigger

// `!get-path` and `!list-paths` are the references that reach beyond the firing's own subject.
//
// A charter could already CONDITION on outside state — `when` and `on` both name other entities
// — but it could not copy any of it: every `$reference` an action can write reads the subject,
// or the task that just finished.
//
//	repo:   !get-path git.repo.acme
//	branch: !get-path git.repo.acme.branch
//	all:    !list-paths { system: git, kind: repo, id: "*", path: branch }
//	ref:    !get-path { system: git, kind: repo, id: $subject.value.head, path: branch }
//
// ONE ADDRESS, all the way down. A verse is one document — `{system: {kind: {id: payload}}}`
// (entity/doc.go, and `verse data` answers it) — so an entity address is a path prefix and the
// path simply continues it into the payload. That used to need the object form's `path:` key,
// because an id may contain dots and the colon spelling could not say where the id ended; the
// dot spelling can, quoting where it must, so a whole address fits in one string (see
// splitAddress). The object form stays for what a string cannot hold: a `value:` predicate, and
// a `$reference` id that reads better as its own key.
//
// THE PAIR IS ONE GRAMMAR SPLIT BY CARDINALITY, and the names say which. `!get-path` answers with ONE
// node and refuses anything that could name several; `!list-paths` answers with a list and is
// where the globs live. They mirror go-tony's own split: GetKPath returns one node and rejects a
// wildcard outright ("any field .* in get"), ListKPath collects.
//
// Telling them apart by NAME rather than by what the address happens to contain is the point. A
// value slot takes one thing, and "this is a list" should be legible in the charter rather than
// depending on whether some id turned out to hold a `*`.
//
// A PAYLOAD PREDICATE IS THE LIST FORM'S, and only the list form's:
//
//	green: !list-paths { system: ci, kind: status, id: "*", value: {state: green}, path: sha }
//
// `!get-path` refuses one for the reason it refuses a globbed id — it can name several, and a
// value slot takes one. `!list-paths` takes one because it has already paid for it. The refusal
// used to cover both, inherited from `!read`, which named exactly one entity and bought four
// things by doing so: no ordering to define, no "it matched three", no truncation, and no cost
// that grows with the verse. The cardinality split paid three of them on the list side — the
// order is Match.Select's, "it matched three" is what a list IS, and `id: "*"` already walks a
// whole kind — so what was left was the refusal without its argument. Narrowing a walk of the
// kind is the CHEAPER read, not the more expensive one. (Truncation is the fourth and is still
// unpaid, for globs and predicates alike; see docs/working/path-read-predicates.md.)
//
// It does not reintroduce a read that slides, because a firing's reads do not slide: every path
// read is answered from ONE snapshot taken at the causing commit (occasion.snapshot), so a
// predicate is evaluated against the same world the rule was evaluated against.
//
// SYSTEM AND KIND ARE ALWAYS LITERAL, in both. That is not style: the level check reads exactly
// those two segments, so a reference or a glob there would make it undecidable at install — the
// same reason a stratum's own pattern may not constrain an id.
//
// THEY COPY, AND DO NOT TRACK. Whatever is read is read once, at the commit that caused the
// firing. If it changes afterwards nothing re-runs: this rule fires again only when its own
// subject changes, or when an `on`/`when` it carries covers the target. Copying is the point,
// but it is the thing someone will file a bug about.
//
// IN A DERIVE THEY ARE CONSTRAINED, and this is the part that is not free. Derive inertness
// rests on a fact these break: that a reference cannot reach another entity at all, which is
// what guarantees the levels above cannot influence what a derive writes — and therefore why a
// rule may read any level without a rule governing reads. So a path read inside a derive must
// resolve AT OR BELOW the rule's own stratum: an entity there is written only by rules at that
// level or derived into from below it, never from above, so inertness holds exactly. Decidable
// at install by running Matches.contains in the opposite direction from check #2 (checkLevels).
// In a plain patch there is no such concern and none is imposed.

import (
	"fmt"
	"strings"

	tonyeval "github.com/signadot/tony-format/go-tony/eval"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/entity"
)

// The two tags, and the payload-path key they share.
const (
	GetPathTag   = "get-path"
	ListPathsTag = "list-paths"
	pathKey      = "path"
)

// addressPattern reads an address written as a path — `[verse-dev, drift, finding, "*"]`, or
// the same as a dotted string — into the per-segment matches a path is.
func addressPattern(v *ir.Node) ([]*ir.Node, error) {
	switch {
	case v == nil:
		return nil, fmt.Errorf("is empty")
	case v.Type == ir.StringType:
		segs, err := entity.PathSegs(v.String)
		if err != nil {
			return nil, err
		}
		out := make([]*ir.Node, 0, len(segs))
		for _, sg := range segs {
			out = append(out, ir.FromString(sg))
		}
		return out, nil
	case v.Type == ir.ArrayType:
		m, err := PathMatch(v.Values)
		if err != nil {
			return nil, err
		}
		return m.SegNodes(), nil
	}
	return nil, fmt.Errorf("is a list of segments or a dotted path; got %v", v.Type)
}

// validateAt is validate for an address written as a path. The rules are the same ones, said
// about segments rather than about three named dimensions.
//
// WHAT IS FIXED AT INSTALL is the point of both: the level check asks whether a read lands at or
// below the rule's own stratum, and it can only ask that of the segments that are literal. So
// every segment but the LAST must be one — the last is the entity being named, which may glob
// in a list read and may be a `$reference` in either.
func (p pathSpec) validateAt() error {
	if len(p.At) < 2 {
		return fmt.Errorf("!%s: an address names a place and something in it; %q names %d segment(s)",
			p.kind, p.target(), len(p.At))
	}
	for i, seg := range p.At[:len(p.At)-1] {
		lit, ok := entity.PathLiteral(seg)
		if !ok || strings.ContainsAny(lit, "$*?[") {
			return fmt.Errorf("!%s %s: segment %d is not literal — everything above the entity "+
				"has to be, because it is what says whether the read is at or below the rule's "+
				"own level, and a check that cannot be made at install is not a check",
				p.kind, p.target(), i+1)
		}
	}
	if p.kind != GetPathTag {
		return nil
	}
	// What makes it a GET: neither the entity nor the field may name several things.
	if lit, ok := entity.PathLiteral(p.At[len(p.At)-1]); !ok || strings.ContainsAny(lit, "*?[") {
		return fmt.Errorf("!%s %s: the last segment can name several entities. Use !%s, which "+
			"answers with a list", p.kind, p.target(), ListPathsTag)
	}
	if strings.Contains(p.Path, "*") {
		return fmt.Errorf("!%s %s: the path %q has a wildcard, which can name several values. "+
			"Use !%s, which answers with a list", p.kind, p.target(), p.Path, ListPathsTag)
	}
	return nil
}

// checkNoBinding refuses a `.[name]` let reference anywhere in a read — in its address, its
// payload path, or its `value:` predicate.
//
// THE READ IS ANSWERED BEFORE THE LET IS EVALUATED, and that order is not incidental: a
// condition's reads are resolved against the snapshot first (conditionMatch), and only then is
// the resulting document handed to tony as a match. So when `!list-paths` runs, a sibling
// binding beside it is still an unsubstituted `.[name]` string — a name tony would have bound,
// asked for at a moment when nothing has bound anything.
//
//	value: !let                                     # what it reads as
//	  let:
//	  - tip:   !get-path {at: [git, head, verse], path: sha}
//	  - fresh: !list-paths {at: [m, proposal, "*"], value: {base: .[tip]}, path: branch}
//	  in: {branches: .[fresh]}
//
// Tony's own `let` DOES bind a sibling — `let: [{a: x}, {b: .[a]}]` resolves, checked — so this
// reads as though it works, and every part of it is spelled correctly. What happens instead is
// that `{base: .[tip]}` is matched against each candidate payload with `.[tip]` as a literal
// string, nothing has that value, and `fresh` binds to the EMPTY LIST. The rule then goes on to
// fire or not on an answer nobody computed. It was filed as a panic (git-issue
// jwqdhfrxh12krpvbg9n0), which go-tony v0.0.179 fixed; what the panic was covering is this.
//
// Refusing is the whole fix available here. Resolving it would mean verse expanding let
// bindings in order, ahead of tony, so a read could stand in a binding's scope — a real change
// to when substitution happens, and the open half of that issue.
func (p pathSpec) checkNoBinding() error {
	for _, seg := range p.At {
		if lit, ok := entity.PathLiteral(seg); ok {
			if name := tonyeval.GetRaw(lit); name != "" {
				return p.bindingRefusal(name, "its address")
			}
		}
	}
	if name := tonyeval.GetRaw(p.ID); name != "" {
		return p.bindingRefusal(name, "its id")
	}
	if name := tonyeval.GetRaw(p.Path); name != "" {
		return p.bindingRefusal(name, "its "+pathKey)
	}
	if name := firstLetRef(p.Value); name != "" {
		return p.bindingRefusal(name, "its "+mKeyValue)
	}
	return nil
}

func (p pathSpec) bindingRefusal(name, where string) error {
	return fmt.Errorf("!%s %s: %s names the binding .[%s], and a read is answered BEFORE the "+
		"`!let` around it is evaluated — so the name is not bound yet and the read would "+
		"quietly answer nothing. Read the entity directly here, or bind what this read answers "+
		"and compare in `in:`", p.kind, p.target(), where, name)
}

// firstLetRef is the first `.[name]` reference in a document, or "" for none. Whole-node only,
// which is what a binding reference is — tony reads `.[x]` as a reference when it is the entire
// string, so `"a.[x]b"` is text and is left alone here too.
func firstLetRef(n *ir.Node) string {
	if n == nil {
		return ""
	}
	u := ir.Uncomment(n)
	if u == nil {
		return ""
	}
	if u.Type == ir.StringType {
		return tonyeval.GetRaw(u.String)
	}
	for _, v := range u.Values {
		if name := firstLetRef(v); name != "" {
			return name
		}
	}
	return ""
}

// pathKind reports which of the two a node is, or "" for anything else.
func pathKind(n *ir.Node) string {
	if n == nil {
		return ""
	}
	switch tagName(n) {
	case GetPathTag:
		return GetPathTag
	case ListPathsTag:
		return ListPathsTag
	}
	return ""
}

// pathSpec is one path read, as far as it is knowable at install.
type pathSpec struct {
	kind         string // GetPathTag or ListPathsTag
	System, Kind string // always literal
	ID           string // literal, a $reference, or (list only) a glob
	// At is the address as a PATH when the read was written with one — `at: [verse-dev, drift,
	// finding, "*"]` — and is what the three above are a reading of. `path` below is the FIELD
	// path inside the payload and keeps that name, which is why the address is `at:` here and
	// `path:` in a match: in a read the two would collide, and only one of them can be `path`.
	At   []*ir.Node
	Path string // a kpath into the payload; "" is the whole payload
	// Value is the payload predicate, list only — "of the ones that are there, the ones
	// that look like this". nil is no predicate, which is every id the address selects.
	Value *ir.Node
}

func (p pathSpec) target() string {
	if len(p.At) > 0 {
		return strings.Join(p.match().Segs(), ".")
	}
	return p.System + ":" + p.Kind
}

// match is the read's address as a pattern — what it selects, and what the level check compares
// against a stratum.
func (p pathSpec) match() Match {
	if len(p.At) > 0 {
		m := Match{mKeyPath: p.At}
		if p.Value != nil {
			m[mKeyValue] = p.Value
		}
		return m
	}
	m := Match{mKeySystem: p.System, mKeyKind: p.Kind, mKeyID: p.ID}
	if p.Value != nil {
		m[mKeyValue] = p.Value
	}
	return m
}

// place is the read's address as a PATTERN OF A PLACE — the address minus the entity being
// named, which is what a stratum owns and what the level check compares against.
//
// A read names an entity; a stratum names where entities live. Comparing the two directly would
// ask whether a stratum owns one id, which it never does.
func (p pathSpec) place() Match {
	if len(p.At) > 0 {
		return Match{mKeyPath: p.At[:len(p.At)-1]}
	}
	return Match{mKeySystem: p.System, mKeyKind: p.Kind}
}

// ref is the entity a one-node read names, for an address written as a path.
func (p pathSpec) ref() entity.Ref {
	if len(p.At) > 0 {
		return entity.RefAt(p.match().Segs()...)
	}
	return entity.RefAt(p.System, p.Kind, p.ID)
}

// String is the address the read names — what the level check's refusals quote when they say
// which entity a derive reached for. The predicate is deliberately not in it: those messages
// are about WHERE a read lands, which is the two literal segments and nothing else.
func (p pathSpec) String() string { return p.target() + ":" + p.ID }

// readSpec pulls a path read out of a node in either spelling, and reports what is wrong with it.
//
// THE STRING FORM GOES ALL THE WAY DOWN now. It used to address an entity and stop, because an
// id may itself contain dots and "git:repo:abc.branch" has no unambiguous split into an id and
// a path — so reaching into the payload needed the object form's `path:`, and one address was
// spelled across four keys.
//
// The delimiter is what dissolves that. A ref is a kinded path whose delimiter is `:`, so the
// colon spelling names an entity WHOLE — dots and all belong to the id — and the dot spelling
// carries the path, quoting an id that needs it: `git.repo."abc.branch".sha`. Two spellings of
// one grammar, each unambiguous, instead of one grammar split by cardinality across an object.
//
// The object form stays, and is not merely tolerated: `value:` has nowhere to live in a string,
// and a `$reference` id reads better as its own key than buried in an address.
func readSpec(n *ir.Node) (pathSpec, error) {
	kind := pathKind(n)
	if kind == "" {
		return pathSpec{}, fmt.Errorf("not a path read")
	}
	spec := pathSpec{kind: kind}
	body := unwrap(n)
	if body == nil {
		return spec, fmt.Errorf("!%s has no address", kind)
	}
	switch body.Type {
	case ir.StringType:
		// ParseExpr refuses a '$' outright — there is no deferred form for a SUBJECT — so the
		// string spelling is split here instead. The two segments the checks read are still
		// checked exactly; the id is carried as written and expanded at fire time.
		ref, path, err := splitAddress(body.String)
		if err != nil {
			return spec, fmt.Errorf("!%s %q: %w — a path read starts from one entity, so an "+
				"address names system, kind and id, and may then continue into the payload "+
				"(git.repo.abc.sha)", kind, body.String, err)
		}
		spec.System, spec.Kind, spec.ID, spec.Path = ref.System(), ref.Kind(), ref.ID(), path
	case ir.ObjectType:
		for i, f := range body.Fields {
			v := unwrap(body.Values[i])
			// `value` is the one key whose content is a tony match rather than a string, so
			// it is taken before the string check the address segments answer to.
			if f.String == mKeyValue {
				if kind == GetPathTag {
					return spec, fmt.Errorf("!%s carries a payload predicate, which can name "+
						"several entities — an entity that is not there does not satisfy it, and "+
						"one that is there might not either. A value slot takes one thing. Use "+
						"!%s, which answers with a list", kind, ListPathsTag)
				}
				spec.Value = body.Values[i]
				continue
			}
			// `at` is the address as a PATH — a list of segments, each a match, exactly as a
			// condition's `path:` takes. It is taken before the string check for the same
			// reason `value` is: it is not a string, and it is the only address form that can
			// name a place deeper than three segments.
			if f.String == mKeyAt {
				at, err := addressPattern(v)
				if err != nil {
					return spec, fmt.Errorf("!%s: %s: %w", kind, mKeyAt, err)
				}
				spec.At = at
				continue
			}
			if v.Type != ir.StringType {
				return spec, fmt.Errorf("!%s: %q must be a string", kind, f.String)
			}
			switch f.String {
			case mKeySystem:
				spec.System = v.String
			case mKeyKind:
				spec.Kind = v.String
			case mKeyID:
				spec.ID = v.String
			case pathKey:
				spec.Path = v.String
			default:
				return spec, fmt.Errorf("!%s: unknown key %q (want %s, or %s/%s/%s, and %s%s)",
					kind, f.String, mKeyAt, mKeySystem, mKeyKind, mKeyID, pathKey,
					map[bool]string{true: ", " + mKeyValue}[kind == ListPathsTag])
			}
		}
	default:
		return spec, fmt.Errorf("!%s: want an object or a \"system:kind:id\" string, got %v", kind, body.Type)
	}
	return spec, spec.validate()
}

// splitAddress splits one address into the entity it starts from and the kpath it continues
// into the payload with, empty when it names the entity whole.
//
// WHICH SPELLING IT IS comes first, and it is decided by WHERE THE COLONS ARE — the rule
// entity.DottedAddr states, asked here rather than restated. `Ref.String` only ever puts a colon
// in the last segment, so a dotted string whose FIRST segment holds one is `system:kind:<id with
// dots>` and nothing else; anything else with a dot in it is a path.
//
// ParseRef used to be tried first, which is the same answer for every address whose segments are
// plain and the wrong one as soon as a segment is quoted:
//
//	verse-dev.drift.head."head-moved@git:head:verse@aaaa1111".result.report
//
// SplitN on `:` found three parts inside the quotes and ParseRef accepted them, so the entity was
// `verse-dev.drift.head."head-moved@git` : `head` : `verse@aaaa1111".result.report` and the read
// answered "nothing stands there" — for an address of exactly the shape every derived entity has
// (git-issue p5qcmpgch12ksd4wgnn0). The colon rule reads it as the six segments it is.
//
// THE ENTITY IS THE FIRST THREE SEGMENTS and the rest is the payload path — rendered back through
// KPath.String, so `(scott)` and `[0]` survive into the read that will follow them.
// (entity.ParsePath is the WRITE-side sibling, and refuses those, because a write cannot reach
// into an array.)
//
// Counting to three is not where a package-deep entity ends — `verse-dev.drift.head.abc` is one
// entity, four segments — and it does not have to be. A verse is ONE DOCUMENT and entity-hood is
// a LENS over it (docs/working/entity-tree.md), so the node at `verse-dev:drift:head` is the
// subtree holding `abc`, and a kpath continuing `abc.result.report` through it reaches the same
// value the deep entity's own read would. Checked, not assumed: both spellings answer `hello`
// against the same fixture. What the count cannot survive is a WRONG SPLIT of the segments
// themselves, which is what the rule above is for.
func splitAddress(s string) (entity.Ref, string, error) {
	if _, dotted := entity.DottedAddr(s); !dotted {
		if ref, err := entity.ParseRef(s); err == nil {
			return ref, "", nil
		}
	}
	kp, err := kpath.Parse(s)
	if err != nil {
		return entity.Ref{}, "", err
	}
	segs := make([]string, 0, entity.Depth)
	for i := 0; i < entity.Depth; i++ {
		if kp == nil {
			return entity.Ref{}, "", fmt.Errorf("names %d of the %d address segments", i, entity.Depth)
		}
		if kp.Field == nil {
			return entity.Ref{}, "", fmt.Errorf("an address segment is a name, not an index or a wildcard")
		}
		segs = append(segs, *kp.Field)
		kp = kp.Next
	}
	ref := entity.RefAt(segs...)
	if kp == nil {
		return ref, "", nil
	}
	return ref, kp.String(), nil
}

// validate enforces what the two segments the checks read must be, and what each cardinality
// may say about the rest.
func (p pathSpec) validate() error {
	if err := p.checkNoBinding(); err != nil {
		return err
	}
	if len(p.At) > 0 {
		return p.validateAt()
	}
	for _, seg := range []struct{ what, v string }{{mKeySystem, p.System}, {mKeyKind, p.Kind}, {mKeyID, p.ID}} {
		if seg.v == "" {
			return fmt.Errorf("!%s has no %s: a path read starts from an entity address, and all "+
				"three segments have to be there", p.kind, seg.what)
		}
	}
	if strings.ContainsAny(p.System+p.Kind, "$*?[") {
		return fmt.Errorf("!%s %s: the system and the kind must be literal — no reference and no "+
			"glob. Those two are what says whether the read is at or below the rule's own level, "+
			"and a check that cannot be made at install is not a check", p.kind, p.target())
	}
	if p.kind == GetPathTag {
		// What makes it a GET. Either of these could name several things, and a value slot
		// takes one — so they are the list form's business, named as such in the charter rather
		// than inferred from whether an id happened to hold a '*'.
		if strings.ContainsAny(p.ID, "*?[") {
			return fmt.Errorf("!%s %s:%s: the id globs, which can name several entities. Use "+
				"!%s, which answers with a list", p.kind, p.target(), p.ID, ListPathsTag)
		}
		if strings.Contains(p.Path, "*") {
			return fmt.Errorf("!%s %s: the path %q has a wildcard, which can name several values. "+
				"Use !%s, which answers with a list", p.kind, p.target(), p.Path, ListPathsTag)
		}
	}
	return nil
}

// eachPathRead walks a document and calls fn for every path read in it, reporting the first
// fault.
func eachPathRead(n entity.Payload, fn func(*ir.Node) error) error {
	// PAST THE COMMENT WRAPPER FIRST. A head comment above a list element wraps it, and a walk
	// that asks the type without unwrapping stops at the comment — so a read under one was not
	// counted among what a rule reads, and the subtree it names was not in the snapshot the
	// condition was evaluated against. Same reason derives() unwraps, and the same class of bug.
	n = unwrap(n)
	if n == nil {
		return nil
	}
	if pathKind(n) != "" {
		return fn(n)
	}
	switch n.Type {
	case ir.ObjectType, ir.ArrayType:
		for _, v := range n.Values {
			if err := eachPathRead(unwrap(v), fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// readTargets collects what every path read in a document addresses, or the first fault.
func readTargets(n entity.Payload) ([]pathSpec, error) {
	var out []pathSpec
	err := eachPathRead(n, func(node *ir.Node) error {
		spec, err := readSpec(node)
		if err != nil {
			return err
		}
		out = append(out, spec)
		return nil
	})
	return out, err
}

// resolveRead answers one path read at fire time: expand the references in its address, then
// read from the firing's snapshot.
func (e *Engine) resolveRead(node *ir.Node, root entity.Payload, oc occasion) (entity.Payload, error) {
	spec, err := readAddress(node, root)
	if err != nil {
		return nil, err
	}
	snap, err := oc.snapshot(e)
	if err != nil {
		return nil, fmt.Errorf("!%s %s: %w", spec.kind, spec.target(), err)
	}
	return spec.answer(snap)
}

// resolveReadAt is the same against a snapshot the caller already holds. A CONDITION has one —
// the reader its own predicates are evaluated against — so its reads are answered from the world
// the condition is deciding about, with no second replay and no chance of the two disagreeing.
func resolveReadAt(node *ir.Node, root entity.Payload, snap entity.Snapshot) (entity.Payload, error) {
	spec, err := readAddress(node, root)
	if err != nil {
		return nil, err
	}
	return spec.answer(snap)
}

// readAddress expands a read's address and interprets it.
//
// The address is expanded FIRST — `id: $subject.value.head` is the point of the feature — and
// expanded with no reader of its own: an address is not a place for another read, and allowing
// one would make how many entities a firing touches a question only the store can answer.
func readAddress(node *ir.Node, root entity.Payload) (pathSpec, error) {
	kind := pathKind(node)
	addr, err := expandPatch(untag(node), root, nil)
	if err != nil {
		return pathSpec{}, err
	}
	return readSpec(retag(addr, kind))
}

// answer reads whichever cardinality this spec is.
func (p pathSpec) answer(snap entity.Snapshot) (entity.Payload, error) {
	if p.kind == GetPathTag {
		return p.get(snap)
	}
	return p.list(snap)
}

// get answers the one-node form.
func (p pathSpec) get(snap entity.Snapshot) (entity.Payload, error) {
	ref := p.ref()
	ent, ok, err := snap.Get(ref)
	if err != nil {
		return nil, fmt.Errorf("!%s %s: %w", p.kind, ref, err)
	}
	if !ok {
		// Absence is a FAILURE, like any unresolved reference: the firing asked for that
		// entity and there is none, and writing null instead would launder "nothing is there"
		// into "something is there, and it is nothing" — a difference the store records and a
		// match can ask about.
		return nil, fmt.Errorf("!%s %s: nothing stands there", p.kind, ref)
	}
	payload := payloadOrNull(ent.Payload)
	if p.Path == "" {
		return payload, nil
	}
	v, err := payload.GetKPath(p.Path)
	if err != nil {
		return nil, fmt.Errorf("!%s %s: %s %q: %w", p.kind, ref, pathKey, p.Path, err)
	}
	if v == nil {
		return nil, fmt.Errorf("!%s %s: %s %q is not there", p.kind, ref, pathKey, p.Path)
	}
	return v, nil
}

// list answers the many-node form.
//
// Order is the selection's — Match.Select sorts by ref — then document order within each
// payload. Deterministic, which a list written into an entity has to be: a charter re-deriving
// the same set in a different order would write a real change every lap.
func (p pathSpec) list(snap entity.Snapshot) (entity.Payload, error) {
	m := p.match() // the address, at whatever depth, plus the payload predicate if there is one
	out := []*ir.Node{}
	for _, ref := range m.Select(snap) {
		ent, ok, err := snap.Get(ref)
		if err != nil {
			return nil, fmt.Errorf("!%s %s: %w", p.kind, ref, err)
		}
		if !ok {
			continue
		}
		payload := payloadOrNull(ent.Payload)
		if p.Path == "" {
			out = append(out, payload)
			continue
		}
		vals, err := payload.ListKPath(nil, p.Path)
		if err != nil {
			return nil, fmt.Errorf("!%s %s: %s %q: %w", p.kind, ref, pathKey, p.Path, err)
		}
		out = append(out, vals...)
	}
	// EMPTY IS A LIST, where an absent `!get-path` is a failure — and the asymmetry is the
	// right way round. "Which one" has an answer or it does not; "all of them" of nothing is
	// legitimately nothing, and a charter asking for every matching value has already said it
	// will take however many there are.
	return ir.FromSlice(out), nil
}

func payloadOrNull(p entity.Payload) entity.Payload {
	if p == nil {
		return ir.Null()
	}
	return p
}

// untag strips the tag so the address underneath can be walked as ordinary content; retag puts
// it back, so readSpec still knows which cardinality it is answering for.
func untag(n *ir.Node) *ir.Node {
	out := n.Clone()
	out.Tag = ir.TagRemove(ir.TagRemove(out.Tag, "!"+GetPathTag), "!"+ListPathsTag)
	return out
}

func retag(n *ir.Node, kind string) *ir.Node { return n.Clone().WithTag("!" + kind) }

// readerFor binds a firing's occasion into the reader the expander calls.
func (e *Engine) readerFor(oc occasion) readFn {
	return func(node *ir.Node, root entity.Payload) (entity.Payload, error) {
		return e.resolveRead(node, root, oc)
	}
}
