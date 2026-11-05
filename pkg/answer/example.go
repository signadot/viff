package answer

import (
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"

	"github.com/signadot/verse/pkg/codec"
)

// Example renders a document that would satisfy this contract — an INSTANCE of the shape, beside
// the pattern that specifies it.
//
// A contract is a match: `findings: !irtype []`, `class: !or [bug, nit, risk]`. That is the right
// thing to check against and the wrong thing to hand somebody who has to produce one, because a
// pattern says what an answer must SATISFY and never what one LOOKS LIKE. Three runs of one
// charter failed on that gap, each in a different way — prose instead of a document, then an
// unquoted string, then a list written YAML-style — and every attempt was a reasonable reading of
// a specification by something that needed an example.
//
// IT IS JSON, and that is deliberate against the grain of the rest of verse, which refuses to
// render json at all (codec.Writable). That refusal is about verse's OUTPUT SURFACES: json cannot
// carry a tag, so rendering a stored document as json silently flattens `!delete` and `!fact` into
// ordinary keys, and a reader cannot tell. Neither risk exists here. An example carries no tags by
// construction — stripping them to representative values is exactly what this does — and json is
// the shape a model reproduces most reliably. The other half of the same policy is what makes it
// safe to suggest: valid json is valid tony, so an answer copied from this parses.
//
// The values are a HEURISTIC and the contract remains the authority: `!irtype 0` exemplifies as
// `0`, `!or [bug, nit]` as `bug`, anything else as whatever it wraps. So the example is held to
// the contract the way an answer is, and one that fails is not shown: a repair presents it as an
// answer that satisfies the contract, and a wrong one teaches the model the mistake it is being
// asked to fix. No example is a repair with less in it.
func (c *Contract) Example() string {
	if c == nil || c.accept == nil {
		return ""
	}
	var b strings.Builder
	if err := encode.Encode(exampleOf(c.accept), &b,
		encode.EncodeFormat(codec.JSON), encode.EncodeWire(true)); err != nil {
		return ""
	}
	ex := strings.TrimSpace(b.String())
	if _, err := c.Check(ex); err != nil {
		return ""
	}
	return ex
}

// exampleOf turns one pattern node into a value that would match it.
//
// Operators are peeled with mergeop.SplitChild rather than by comparing tags, for the reason
// everything else that reads a tag does: a tag is a dot-separated CHAIN carrying the layout the
// parser saw, so `!or` and `!bracket.or` are one operator written twice.
func exampleOf(n *ir.Node) *ir.Node {
	if n == nil {
		return ir.Null()
	}
	if _, op, _, child, err := mergeop.SplitChild(n); err == nil && op != "" {
		switch op {
		case "irtype":
			// The child IS the exemplar: the operator matches by type, and what it was written
			// with is a value of that type — `!irtype "text"` is a string, `!irtype []` a list.
			return exampleOf(child)
		case "or":
			// The first alternative, because a reader has to be shown one and the first is the
			// one the author wrote first.
			if child != nil && child.Type == ir.ArrayType && len(child.Values) > 0 {
				return exampleOf(child.Values[0])
			}
		case "all":
			// `!all P` constrains every element of a LIST, so an example of it is a list —
			// peeling to P alone would show the shape of a member where a member is not what
			// the field holds. One element, because one is enough to show the shape and two
			// would imply the count matters.
			return ir.FromSlice([]*ir.Node{exampleOf(child)})
		case "and":
			// Every conjunct has to hold, so the example is one that holds for all of them.
			// base.tony writes a container of something as a type and an `!all` over its
			// members — `array(t)` is `!and [.[array], !all t]` — and the last conjunct says
			// the most there, so they are tried from the last.
			if child != nil && child.Type == ir.ArrayType {
				for i := len(child.Values) - 1; i >= 0; i-- {
					ex := exampleOf(child.Values[i])
					if ok, err := tony.Match(ex, n); err == nil && ok {
						return ex
					}
				}
			}
			return ir.FromString("…")
		case "ir":
			// `!ir` asks how a node is KEPT, so what it wraps describes a representation, not a
			// value: peeled, `!ir {int: .[number]}` would exemplify as an object holding an
			// `int`. base.tony's int and float are the two readings worth making, a number kept
			// as one or as the other.
			if child != nil && ir.Get(child, "int") != nil {
				return ir.FromInt(1)
			}
			if child != nil && ir.Get(child, "float") != nil {
				return ir.FromFloat(0.5)
			}
			return ir.FromString("…")
		}
		if child != nil {
			return exampleOf(child)
		}
		// An operator with nothing under it says something about a value without describing
		// one. A placeholder is honest where a guess would not be.
		return ir.FromString("…")
	}
	switch n.Type {
	case ir.ObjectType:
		fields := map[string]*ir.Node{}
		for k, v := range ir.ToMap(n) {
			fields[k] = exampleOf(v)
		}
		return ir.FromMap(fields)
	case ir.ArrayType:
		out := make([]*ir.Node, 0, len(n.Values))
		for _, v := range n.Values {
			out = append(out, exampleOf(v))
		}
		return ir.FromSlice(out)
	}
	// A literal in a pattern matches itself, so it exemplifies itself — as a copy, since the
	// example takes it as a child, and the pattern is what Check matches against.
	return n.Clone()
}
