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
// (entity/doc.go, and `verse state` answers it) — so an entity address is a path prefix and the
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
	// Value is the payload predicate, list only — "of the ones that are there, the ones
	// that look like this". nil is no predicate, which is every id the address selects.
	Value *ir.Node
}

func (p pathSpec) target() string { return p.System + ":" + p.Kind }

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
		spec.System, spec.Kind, spec.ID, spec.Path = ref.System, ref.Kind, ref.ID, path
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
				return spec, fmt.Errorf("!%s: unknown key %q (want %s, %s, %s, %s%s)",
					kind, f.String, mKeySystem, mKeyKind, mKeyID, pathKey,
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
// A REF IS TRIED FIRST, so every address that ever worked still names what it named: an id
// carrying colons or dots is the id. Only a string that is not a ref is read as a kinded path,
// and then the first three segments are the entity and the rest is the payload path — rendered
// back through KPath.String, so `(scott)` and `[0]` survive into the read that will follow
// them. (entity.ParsePath is the WRITE-side sibling, and refuses those, because a write cannot
// reach into an array.)
func splitAddress(s string) (entity.Ref, string, error) {
	if ref, err := entity.ParseRef(s); err == nil {
		return ref, "", nil
	}
	kp, err := kpath.Parse(s)
	if err != nil {
		return entity.Ref{}, "", err
	}
	var ref entity.Ref
	into := []*string{&ref.System, &ref.Kind, &ref.ID}
	for i := 0; i < 3; i++ {
		if kp == nil {
			return entity.Ref{}, "", fmt.Errorf("names %d of the 3 address segments", i)
		}
		if kp.Field == nil {
			return entity.Ref{}, "", fmt.Errorf("an address segment is a name, not an index or a wildcard")
		}
		*into[i] = *kp.Field
		kp = kp.Next
	}
	if kp == nil {
		return ref, "", nil
	}
	return ref, kp.String(), nil
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
	if p.Value != nil {
		m[mKeyValue] = p.Value // narrows the selection; Match.Select already resolves it
	}
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
