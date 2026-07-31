package trigger

// `!read <match>` is the one reference that reaches an entity other than the firing's subject.
//
// A charter could already CONDITION on outside state — `when` and `on` both name other entities
// — but it could not copy any of it: every reference an action can write reads the subject, or
// the process that just finished. `!read` closes that, and everything below is about the one
// place doing so is dangerous.
//
// IT NAMES EXACTLY ONE ENTITY. The match must give all three segments and carry no payload
// predicate (Match.Names, which already draws that line). That is a restriction chosen for what
// it buys rather than one inherited from the matcher: naming makes this a lookup instead of a
// query, so there is no ordering to define, no "it matched three, now what", no truncation, and
// no cost that grows with the verse. The id may still be a reference, so the address is as
// dynamic as anyone needs; what is fixed is that exactly one entity answers.
//
// The verb is `read` and not `select` because Match.Select returns a SET, and a name that
// promises the thing this rules out is worse than a longer one.
//
// IT COPIES, AND DOES NOT TRACK. The entity is read once, at the commit that caused the firing.
// If it changes afterwards nothing re-derives: this rule fires again only when its own subject
// changes, or when an `on`/`when` it carries covers the target. That is the intended semantics —
// copying is the whole point — but it is the thing someone will file a bug about, so it is said
// here and in the docs.
//
// IN A DERIVE IT IS CONSTRAINED, and this is the part that is not free. Derive inertness rests
// on a fact `!read` breaks: that a reference cannot reach another entity at all, which is what
// guarantees the levels above cannot influence what a derive writes — and therefore why a rule
// may read any level without a rule governing reads. So a `!read` inside a derive must resolve
// AT OR BELOW the rule's own stratum: an entity at or below is written only by rules at that
// level or derived into from below it, never from above, so inertness holds exactly. It is
// decidable at install by running Matches.contains in the opposite direction from check #2 (see
// checkLevels). In a plain patch there is no such concern and none is imposed.

import (
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
)

// ReadTag is the tag: `why: !read { system: git, kind: repo, id: $subject.value.headCommit }`,
// or `why: !read "git:repo:$subject.value.headCommit"`.
const ReadTag = "read"

// isRead reports whether a node is a `!read`.
func isRead(n *ir.Node) bool { return n != nil && tagName(n) == ReadTag }

// readTarget is the part of a `!read` that is knowable at install: the system and kind it names.
// The id is not, and does not need to be — every check this feature needs reads the two segments
// a stratum's pattern constrains.
type readTarget struct{ System, Kind string }

func (r readTarget) String() string { return r.System + ":" + r.Kind }

// readSpec pulls the address out of a `!read` node in either spelling, and reports what is wrong
// with it.
//
// SYSTEM AND KIND MUST BE LITERAL; the id may be a reference. That is not a stylistic line: the
// level check for a derive reads exactly those two segments, so a reference there would make the
// check undecidable at install — the same reason a stratum's own pattern may not constrain an id.
// The id is where the dynamism was wanted anyway.
func readSpec(n *ir.Node) (readTarget, error) {
	n = unwrap(n)
	if n == nil {
		return readTarget{}, fmt.Errorf("!read has no address")
	}
	switch n.Type {
	case ir.StringType:
		// ParseExpr refuses a '$' outright — there is no deferred subject form for a SUBJECT —
		// so the string spelling is split here instead. The two segments this cares about are
		// still checked exactly; the id is carried through as written and expanded at fire time.
		parts := strings.SplitN(n.String, ":", 3)
		if len(parts) != 3 {
			return readTarget{}, fmt.Errorf("!read %q: want \"system:kind:id\" — all three, because "+
				"a read names ONE entity", n.String)
		}
		return literalTarget(parts[0], parts[1], parts[2])
	case ir.ObjectType:
		var sys, kind, id string
		for i, f := range n.Fields {
			v := unwrap(n.Values[i])
			switch f.String {
			case mKeySystem, mKeyKind, mKeyID:
				if v.Type != ir.StringType {
					return readTarget{}, fmt.Errorf("!read: %q must be a string", f.String)
				}
				switch f.String {
				case mKeySystem:
					sys = v.String
				case mKeyKind:
					kind = v.String
				default:
					id = v.String
				}
			case mKeyValue:
				return readTarget{}, fmt.Errorf("!read carries a payload predicate: a read names ONE " +
					"entity by identity, and a predicate makes it a selection over whatever happens " +
					"to match — which has no single answer to write")
			default:
				return readTarget{}, fmt.Errorf("!read: unknown key %q (want %s, %s, %s)",
					f.String, mKeySystem, mKeyKind, mKeyID)
			}
		}
		return literalTarget(sys, kind, id)
	}
	return readTarget{}, fmt.Errorf("!read: want an object or a \"system:kind:id\" string, got %v", n.Type)
}

// literalTarget enforces what all three segments must be: present, and — for the two the level
// check reads — literal.
func literalTarget(sys, kind, id string) (readTarget, error) {
	for _, seg := range []struct{ what, v string }{{mKeySystem, sys}, {mKeyKind, kind}, {mKeyID, id}} {
		if seg.v == "" || seg.v == "*" {
			return readTarget{}, fmt.Errorf("!read has no %s: a read names ONE entity, so all three "+
				"segments have to be definite — there is no \"whichever matched\" to write", seg.what)
		}
	}
	if strings.ContainsRune(sys+kind, '$') {
		return readTarget{}, fmt.Errorf("!read %s:%s: the system and the kind must be literal, and "+
			"only the id may be a reference. Those two are what says whether the read is at or "+
			"below the rule's own level, and a check that cannot be made at install is not a check",
			sys, kind)
	}
	if strings.ContainsAny(sys+kind, "*?[") {
		return readTarget{}, fmt.Errorf("!read %s:%s: no globbing — a read names one entity", sys, kind)
	}
	return readTarget{System: sys, Kind: kind}, nil
}

// eachRead walks a document and calls fn for every `!read` in it, reporting the first fault.
func eachRead(n entity.Payload, fn func(*ir.Node) error) error {
	if n == nil {
		return nil
	}
	if isRead(n) {
		return fn(n)
	}
	switch n.Type {
	case ir.ObjectType, ir.ArrayType:
		for _, v := range n.Values {
			if err := eachRead(unwrap(v), fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// readTargets collects what every `!read` in a document names, or the first fault.
func readTargets(n entity.Payload) ([]readTarget, error) {
	var out []readTarget
	err := eachRead(n, func(node *ir.Node) error {
		t, err := readSpec(node)
		if err != nil {
			return err
		}
		out = append(out, t)
		return nil
	})
	return out, err
}

// resolveRead answers one `!read` at fire time: expand the references inside its address, then
// read the named entity from the firing's snapshot.
func (e *Engine) resolveRead(node *ir.Node, root entity.Payload, oc occasion) (entity.Payload, error) {
	// The address is expanded FIRST — `id: $subject.value.headCommit` is the point of the
	// feature — and expanded with no reader of its own: an address is not a place for another
	// read, and allowing one would make how many entities a firing touches a question only the
	// store can answer.
	addr, err := expandPatch(untag(node), root, nil)
	if err != nil {
		return nil, err
	}
	var m Match
	if err := m.FromTonyIR(addr); err != nil {
		return nil, fmt.Errorf("!read: %w", err)
	}
	ref, names := m.Names()
	if !names {
		return nil, fmt.Errorf("!read %v does not name one entity once its references are "+
			"resolved", map[string]any(m))
	}
	snap, err := oc.snapshot(e)
	if err != nil {
		return nil, fmt.Errorf("!read %s: %w", ref, err)
	}
	ent, ok, err := snap.Get(ref)
	if err != nil {
		return nil, fmt.Errorf("!read %s at commit %d: %w", ref, oc.commit.Rev, err)
	}
	if !ok {
		// Absent is a FAILURE, like any unresolved reference: the firing asked for that
		// entity's data and there is none, and writing null instead would launder "there is
		// nothing there" into "there is something there, and it is nothing" — a difference the
		// store records and a match can ask about.
		return nil, fmt.Errorf("!read %s: nothing stands there at commit %d", ref, oc.commit.Rev)
	}
	if ent.Payload == nil {
		return ir.Null(), nil
	}
	return ent.Payload, nil
}

// untag strips the `!read` so the address underneath can be decoded as an ordinary match.
func untag(n *ir.Node) *ir.Node {
	out := n.Clone()
	out.Tag = ir.TagRemove(out.Tag, "!"+ReadTag)
	return out
}

// readerFor binds a firing's occasion into the reader the expander calls.
func (e *Engine) readerFor(oc occasion) readFn {
	return func(node *ir.Node, root entity.Payload) (entity.Payload, error) {
		return e.resolveRead(node, root, oc)
	}
}
