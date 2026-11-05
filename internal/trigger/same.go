package trigger

import (
	"sort"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/codec"
)

// Whether two declarations say the same thing, asked of a rule a record holds against the one
// standing under its name (placeRecord): the same value is the same rule, with nothing to
// place again, and the same value in other words is a rule whose record moved and whose
// engine copy follows.

// SameValue reports whether a and b say the same thing, as IR: keys in canonical order,
// comments stripped, compared by value. Not by their encodings, which keep how a text was
// written — `patch: {seen: a}` from a file and the same patch restored from the store print
// differently and are one rule.
func SameValue(a, b any) bool {
	na, err := codec.ToNode(a)
	if err != nil {
		return false
	}
	nb, err := codec.ToNode(b)
	if err != nil {
		return false
	}
	return Canonical(ir.StripComments(na)).DeepEqual(Canonical(ir.StripComments(nb)))
}

// SameText is SameValue asked about the WORDS as well: the same thing said with the same
// comments. A rule whose value is the same and whose text is not has had its reasons edited —
// the lines its author wrote above it — which is a change to the record and not to what the
// rule does (follow.go, placeRecord).
func SameText(a, b any) bool {
	na, err := codec.ToNode(a)
	if err != nil {
		return false
	}
	nb, err := codec.ToNode(b)
	if err != nil {
		return false
	}
	return canonicalise(na, true).DeepEqualWithComments(canonicalise(nb, true))
}

// Canonical is n with every object's keys in the ir's own order, at every depth: a sparse
// array is an object whose keys are numbers, ordered as numbers, and ir.Compare is what orders
// a key of either kind. An array keeps its order, which is part of what it says.
//
// AND WITHOUT PRESENTATION TAGS (ir.IsPresentation), which record how a value was written and
// not what it is: a rule parsed from `patch: {seen: a}` carries !bracket on the patch, and the
// same rule restored from the store does not, so compared with the tag every such rule changed
// at a restart.
func Canonical(n *ir.Node) *ir.Node { return canonicalise(n, false) }

// canonicalise is Canonical, keeping the comments where asked: a comment wrapping a value stays
// around the canonical value, and one attached to a node stays attached, so a comparison with
// comments (SameText) sees them in the same order on both sides.
func canonicalise(n *ir.Node, comments bool) *ir.Node {
	if n == nil {
		return nil
	}
	switch n.Type {
	case ir.CommentType:
		if !comments {
			return canonicalise(ir.Uncomment(n), false)
		}
		out := shell(n)
		for i, v := range n.Values {
			out.Values[i] = canonicalise(v, true)
		}
		return out
	case ir.ObjectType:
		kvs := make([]ir.KeyVal, 0, len(n.Fields))
		for i, k := range n.Fields {
			kvs = append(kvs, ir.KeyVal{Key: k, Val: canonicalise(n.Values[i], comments)})
		}
		sort.SliceStable(kvs, func(i, j int) bool { return ir.Compare(kvs[i].Key, kvs[j].Key) < 0 })
		out := ir.FromKeyVals(kvs)
		out.Tag = ir.StripPresentation(n.Tag)
		if comments {
			out.Comment = n.Comment
		}
		return out
	case ir.ArrayType:
		out := shell(n)
		out.Tag = ir.StripPresentation(n.Tag)
		for i, v := range n.Values {
			out.Values[i] = canonicalise(v, comments)
		}
		return out
	}
	if t := ir.StripPresentation(n.Tag); t != n.Tag {
		out := n.Clone()
		out.Tag = t
		return out
	}
	return n
}

// shell is n's own fields over room for as many values as n has, for a branch that rebuilds
// every value: a Clone would copy the whole subtree and then have each copy overwritten.
func shell(n *ir.Node) *ir.Node {
	out := *n
	out.Values = make([]*ir.Node, len(n.Values))
	return &out
}
