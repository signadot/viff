package answer

import (
	"fmt"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"

	"github.com/signadot/verse/codec"
)

// THE VALIDATOR VALIDATES, BUT IT DOES NOT EXPLAIN — and the explanation is the whole
// design, since what goes back to the model must be specific enough to act on.
//
// A tony schema's `accept` clause is a MATCH, and schema.Validate is tony.MatchWith with a
// wrapper: a document that fails comes back as "document does not match schema", naming
// neither the path nor the reason nor how many things are wrong. Feeding that to a model is
// the useless kind of error — it says something is wrong and nothing about what, which is
// exactly what a repair loop cannot use.
//
// So this walks the pattern itself and reports what a match cannot. It is deliberately only
// half an explainer, and knows which half:
//
//   - STRUCTURE it can explain, because object matching is a plain field walk (every
//     pattern field must be present; extra document fields are ignored — matching is
//     open-world, which is right: an answer carrying more than it was asked for is still an
//     answer). Absent, present-but-wrong, and wrong-length are all distinguishable here.
//   - OPERATORS it cannot see into. `!or`, `!and`, `!not`, `!all`, `!glob`, `!irtype` all go
//     through Op.Match, which returns one bit. At a tagged node this reports the path, the
//     pattern as written, and the value found, which for `!or [bug, nit, risk]` against
//     "critical" is already everything a model needs — but inside `!all` over a list it
//     cannot name WHICH element failed, because only the operator knows.
//
// That last gap is upstream work, filed as go-tony issue axp74f1wh12krznjcxn0 (an
// explanation option for tony.Match). When it lands, this file collapses into a call that
// passes the option through and formats what comes back; Mismatch is shaped to receive it
// (a path, the expected node, the found node, a reason) so the seam is only this file.
func explain(doc, pattern *ir.Node, ctx *mergeop.OpContext) []Mismatch {
	var out []Mismatch
	walk(&out, "", doc, pattern, ctx)
	return out
}

// Reason says which KIND of wrong this is. A model repairs an absent field and a wrong
// value differently, and the bare match cannot tell them apart at all.
type Reason string

const (
	Absent   Reason = "absent"   // the contract requires this path; the answer has no such field
	Wrong    Reason = "wrong"    // present, and not what the contract accepts
	WrongLen Reason = "wronglen" // a list of the wrong length
)

// Mismatch is one thing wrong with an answer, at one path.
type Mismatch struct {
	Path     string // kpath into the ANSWER: ".class", ".findings[2].severity"
	Reason   Reason
	Expected string // the pattern at that path, as tony
	Found    string // the value at that path, as tony; empty when Reason is Absent
}

func (m Mismatch) String() string {
	at := m.Path
	if at == "" {
		at = "(the answer)"
	}
	switch m.Reason {
	case Absent:
		return fmt.Sprintf("%s: required, but absent — expected %s", at, m.Expected)
	case WrongLen:
		return fmt.Sprintf("%s: %s", at, m.Expected)
	default:
		return fmt.Sprintf("%s: expected %s, found %s", at, m.Expected, m.Found)
	}
}

func walk(out *[]Mismatch, path string, doc, pattern *ir.Node, ctx *mergeop.OpContext) {
	if pattern == nil {
		return
	}
	// A tagged node is an operator: opaque from here, so ask the matcher for its one bit
	// and report the pattern as written. SplitChild is how MatchWith itself decides this,
	// so the two agree on what counts as an operator.
	if _, tag, _, _, err := mergeop.SplitChild(pattern); err != nil || tag != "" {
		leaf(out, path, doc, pattern, ctx)
		return
	}
	switch pattern.Type {
	case ir.NullType:
		return // null is the presence test, and we only get here when the field is present
	case ir.ObjectType:
		if doc.Type != ir.ObjectType {
			leaf(out, path, doc, pattern, ctx)
			return
		}
		for i, f := range pattern.Fields {
			sub := pattern.Values[i]
			child, ok := field(doc, f.String)
			if !ok {
				*out = append(*out, Mismatch{
					Path: path + "." + f.String, Reason: Absent, Expected: render(sub),
				})
				continue
			}
			walk(out, path+"."+f.String, child, sub, ctx)
		}
	case ir.ArrayType:
		if doc.Type != ir.ArrayType {
			leaf(out, path, doc, pattern, ctx)
			return
		}
		// A plain list pattern matches element-wise and length-exactly (`!all` is the
		// operator for "every element, however many"), so a length difference is the
		// whole story and reporting each index on top of it would be noise.
		if len(doc.Values) != len(pattern.Values) {
			*out = append(*out, Mismatch{
				Path:   path,
				Reason: WrongLen,
				Expected: fmt.Sprintf("expected a list of exactly %d, found %d",
					len(pattern.Values), len(doc.Values)),
			})
			return
		}
		for i := range pattern.Values {
			walk(out, fmt.Sprintf("%s[%d]", path, i), doc.Values[i], pattern.Values[i], ctx)
		}
	default:
		leaf(out, path, doc, pattern, ctx)
	}
}

// leaf reports a path the walk cannot decompose further: a scalar, a type disagreement, or
// an operator. It re-runs the match on just this subtree so the report never claims a
// failure the matcher would not agree with — the walk decides WHERE to look, the matcher
// stays the authority on what matches.
func leaf(out *[]Mismatch, path string, doc, pattern *ir.Node, ctx *mergeop.OpContext) {
	ok, err := tony.MatchWith(doc, pattern, ctx)
	if err == nil && ok {
		return
	}
	*out = append(*out, Mismatch{
		Path: path, Reason: Wrong, Expected: render(pattern), Found: render(doc),
	})
}

// field looks up a field by name, distinguishing ABSENT from present-and-null: a contract
// writes `{class: null}` to mean "present, whatever the value", so an answer that omits the
// field entirely and one that sets it to null are the two sides of the only question that
// pattern asks.
func field(obj *ir.Node, name string) (*ir.Node, bool) {
	for i, f := range obj.Fields {
		if f.String == name {
			return obj.Values[i], true
		}
	}
	return nil, false
}

// render prints a node as one line of tony, bounded. Bounded because `found` can be an
// arbitrarily large subtree and this goes into a model prompt — an answer that failed
// because it was enormous should not be re-sent in full to say so.
func render(n *ir.Node) string {
	if n == nil {
		return "nothing"
	}
	b, err := codec.NodeBytesWire(n, codec.Tony)
	if err != nil {
		return fmt.Sprintf("(%s)", n.Type)
	}
	s := strings.TrimSpace(string(b))
	const max = 160
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
