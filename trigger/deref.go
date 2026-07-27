package trigger

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
)

// Deref: reading a value OUT of the payload that fired an action, by kpath.
//
// An action could always NAME a value ($stage, $result) — but only inside a spawn's string
// fields, and only as text, because launch.Binding is map[string]string built with
// fmt.Sprint over the payload's top-level fields. Two consequences followed. A nested value
// was unreachable at all ($verse.review.done had no spelling), and a process's output could
// parameterize the next PROMPT but could never become a fact the next `when` matches on:
// the loop closed through state transitions the charter wrote by hand, never through what a
// process actually found.
//
// Expanding a `patch` closes it. An action can lift a value out of the payload that fired it
// and write it onto the subject, TYPED, where the match grammar can see it. That is the same
// vocabulary pairing the rest of the model uses — a Match reads, a Patch writes — with the
// reference as the third member, and it is why this is a deref rather than a template
// language: it names a value, it does not compute one.
//
// The reference grammar is tony's, not verse's. "$result.findings[0].severity" resolves by
// handing "result.findings[0].severity" to ir.Node.GetKPath, the same kpath the engine
// already uses to address an action inside its trigger (review.seq[2].onFailed). verse's
// long-standing "$name" spelling IS that kpath at depth one, so this does not add a grammar;
// it stops truncating the one already there at the first segment.

// refRoot is what a reference resolves against: the changed payload's fields, plus $subject.
//
// It is built by node rather than through entity.ToObj because a round-trip through Go
// values does not preserve tony's numbers — an int 3 comes back a float64, and the store's
// predicates are type-exact, so `patch: {n: $count}` would write a 3.0 that `fields: {n: 3}`
// then fails to match. Deep-copying the fields keeps both the type and the original
// document's parent linkage intact.
//
// $subject is set LAST, the same rule bindingFor states: it is the one reserved reference,
// and a payload field that happens to be called "subject" must not shadow it.
func refRoot(ev entity.Event, subject entity.Ref) entity.Payload {
	fields := map[string]*ir.Node{}
	for k, v := range entity.Fields(ev.To) { // nil for a scalar payload — no named parts
		fields[k] = v.DeepCopy()
	}
	fields["subject"] = ir.FromString(subject.String())
	return ir.FromMap(fields)
}

// refAt reports the length of the reference token at the start of s, or 0 when what follows
// '$' is not one. A charter may hold a literal dollar ("$5", "100$", a lone "$"), and the
// patch it sits in was never expanded before this change — so anything that is not a
// well-formed reference is left exactly as written rather than becoming an error.
func refAt(s string) int {
	if len(s) < 2 || s[0] != '$' || !identStart(s[1]) {
		return 0
	}
	i := 1
	for i < len(s) && identPart(s[i]) {
		i++
	}
	for i < len(s) {
		switch {
		case s[i] == '.' && i+1 < len(s) && identStart(s[i+1]):
			i += 2
			for i < len(s) && identPart(s[i]) {
				i++
			}
		case s[i] == '[':
			j := i + 1
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j == i+1 || j >= len(s) || s[j] != ']' {
				return i // "[" that is not an index ends the reference
			}
			i = j + 1
		default:
			return i
		}
	}
	return i
}

func identStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func identPart(c byte) bool { return identStart(c) || (c >= '0' && c <= '9') }

// deref resolves one reference (without its '$') against the root.
//
// Absent and null are DIFFERENT answers, deliberately: GetKPath returns a nil node for a
// path that is not there and a null node for a field written null, and the store records
// that difference too (a match spells the three cases apart — see docs/trigger-model.md).
// So an absent reference is an error and a null one resolves to null.
func deref(root entity.Payload, ref string) (entity.Payload, error) {
	n, err := root.GetKPath(ref)
	if err != nil {
		return nil, fmt.Errorf("$%s: %w", ref, err)
	}
	if n == nil {
		return nil, fmt.Errorf("$%s is not present in the payload that fired this action", ref)
	}
	return n, nil
}

// expandString expands the references in one patch VALUE.
//
// Two forms, and the difference is the whole point of doing this at the node level: a value
// that is exactly one reference resolves to that reference's NODE, so a number stays a
// number and an object stays an object; a value with a reference embedded in text is
// interpolated, and is a string because that is what it was written as.
func expandString(s string, root entity.Payload) (entity.Payload, error) {
	if !strings.Contains(s, "$") {
		return ir.FromString(s), nil
	}
	if n := refAt(s); n == len(s) {
		v, err := deref(root, s[1:])
		if err != nil {
			return nil, err
		}
		return v.DeepCopy(), nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		n := refAt(s[i:])
		if n == 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		ref := s[i+1 : i+n]
		v, err := deref(root, ref)
		if err != nil {
			return nil, err
		}
		txt, ok := scalarText(v)
		if !ok {
			return nil, fmt.Errorf("$%s is %v, which has no text form: reference it on its own "+
				"to write the whole value, rather than inside a string", ref, v.Type)
		}
		b.WriteString(txt)
		i += n
	}
	return ir.FromString(b.String()), nil
}

// scalarText renders a leaf for interpolation. A composite has no text form here on purpose:
// inventing one (JSON? tony?) would make `"saw $result"` mean something different depending
// on what the process happened to return, and the whole-value form already says it exactly.
func scalarText(n entity.Payload) (string, bool) {
	switch n.Type {
	case ir.StringType:
		return n.String, true
	case ir.BoolType:
		return strconv.FormatBool(n.Bool), true
	case ir.NumberType:
		switch {
		case n.Int64 != nil:
			return strconv.FormatInt(*n.Int64, 10), true
		case n.Float64 != nil:
			return strconv.FormatFloat(*n.Float64, 'g', -1, 64), true
		}
		return n.Number, n.Number != ""
	case ir.NullType:
		return "", true
	}
	return "", false
}

// expandPatch returns the patch with every reference in a string value resolved against the
// root, or an error naming the first reference that does not resolve.
//
// It walks a DEEP COPY and replaces leaves in place, rather than rebuilding the document.
// A patch is not a plain map: it carries the mergeop tags (`!delete`, `!replace`) that give
// it its write semantics, and field order besides. Rebuilding would mean re-deriving both,
// and a dropped tag turns a deletion into a write of null.
func expandPatch(p entity.Payload, root entity.Payload) (entity.Payload, error) {
	if p == nil {
		return nil, nil
	}
	cp := p.DeepCopy()
	if err := expandInto(cp, root); err != nil {
		return nil, err
	}
	return cp, nil
}

// expandInto rewrites string leaves in place. Only VALUES are expanded: a reference in a key
// would make the patch's shape depend on the payload, and the shape is what a reader of the
// charter is entitled to know without running it.
func expandInto(n entity.Payload, root entity.Payload) error {
	if n == nil {
		return nil
	}
	switch n.Type {
	case ir.ObjectType, ir.ArrayType:
		for _, v := range n.Values {
			if err := expandInto(v, root); err != nil {
				return err
			}
		}
		return nil
	case ir.StringType:
		v, err := expandString(n.String, root)
		if err != nil {
			return err
		}
		// CloneTo copies content but clears the destination's parent linkage, so restore it:
		// the node stays where it is in the document, only its value changes.
		parent, field, index := n.Parent, n.ParentField, n.ParentIndex
		v.CloneTo(n)
		n.Parent, n.ParentField, n.ParentIndex = parent, field, index
		return nil
	}
	return nil
}
