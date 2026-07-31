package trigger

// `!get-path` and `!list-paths` are the references that reach beyond the firing's own subject.
//
// A charter could already CONDITION on outside state — `when` and `on` both name other entities
// — but it could not copy any of it: every `$reference` an action can write reads the subject,
// or the process that just finished.
//
//	repo:   !get-path { system: git, kind: repo, id: $subject.value.head }
//	branch: !get-path { system: git, kind: repo, id: $subject.value.head, path: branch }
//	all:    !list-paths { system: git, kind: repo, id: "*", path: branch }
//
// THE PAIR IS ONE GRAMMAR SPLIT BY CARDINALITY, and the names say which. A verse is one
// document — `{system: {kind: {id: payload}}}` (entity/snapshot.go) — so an entity address is a
// path prefix and `path:` simply continues it into the payload. `!get-path` answers with ONE
// node and refuses anything that could name several; `!list-paths` answers with a list and is
// where the globs live. They mirror go-tony's own split: GetKPath returns one node and rejects a
// wildcard outright ("any field .* in get"), ListKPath collects.
//
// Telling them apart by NAME rather than by what the address happens to contain is the point. A
// value slot takes one thing, and "this is a list" should be legible in the charter rather than
// depending on whether some id turned out to hold a `*`.
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

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
)

// The two tags, and the payload-path key they share.
const (
	GetPathTag   = "get-path"
	ListPathsTag = "list-paths"
	pathKey      = "path"
)

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
	Path         string // a kpath into the payload; "" is the whole payload
}

func (p pathSpec) target() string { return p.System + ":" + p.Kind }

// readSpec pulls a path read out of a node in either spelling, and reports what is wrong with it.
//
// The string form addresses an ENTITY and stops there: an id may itself contain dots, so
// "git:repo:abc.branch" has no unambiguous split into an id and a path. Going into the payload
// is the object form's `path:`, which needs no guessing.
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
		parts := strings.SplitN(body.String, ":", 3)
		if len(parts) != 3 {
			return spec, fmt.Errorf("!%s %q: want \"system:kind:id\" — all three, because a path "+
				"read starts from one entity. To go into the payload use the object form's `path:`",
				kind, body.String)
		}
		spec.System, spec.Kind, spec.ID = parts[0], parts[1], parts[2]
	case ir.ObjectType:
		for i, f := range body.Fields {
			v := unwrap(body.Values[i])
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
			case mKeyValue:
				return spec, fmt.Errorf("!%s carries a payload predicate: a path read addresses "+
					"entities by identity. Selecting by what they LOOK like is a different question, "+
					"and one whose answer changes as rules fire", kind)
			default:
				return spec, fmt.Errorf("!%s: unknown key %q (want %s, %s, %s, %s)",
					kind, f.String, mKeySystem, mKeyKind, mKeyID, pathKey)
			}
		}
	default:
		return spec, fmt.Errorf("!%s: want an object or a \"system:kind:id\" string, got %v", kind, body.Type)
	}
	return spec, spec.validate()
}

// validate enforces what the two segments the checks read must be, and what each cardinality
// may say about the rest.
func (p pathSpec) validate() error {
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
	// The address is expanded FIRST — `id: $subject.value.head` is the point of the feature —
	// and expanded with no reader of its own: an address is not a place for another read, and
	// allowing one would make how many entities a firing touches a question only the store can
	// answer.
	kind := pathKind(node)
	addr, err := expandPatch(untag(node), root, nil)
	if err != nil {
		return nil, err
	}
	spec, err := readSpec(retag(addr, kind))
	if err != nil {
		return nil, err
	}
	snap, err := oc.snapshot(e)
	if err != nil {
		return nil, fmt.Errorf("!%s %s: %w", spec.kind, spec.target(), err)
	}
	if spec.kind == GetPathTag {
		return spec.get(snap)
	}
	return spec.list(snap)
}

// get answers the one-node form.
func (p pathSpec) get(snap entity.Snapshot) (entity.Payload, error) {
	ref := entity.Ref{System: p.System, Kind: p.Kind, ID: p.ID}
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
	m := Match{mKeySystem: p.System, mKeyKind: p.Kind, mKeyID: p.ID}
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
