package api

import (
	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
)

// Filter is a reader's if and trim, applied to what viff answered: Keep says which items, as
// tony.Match decides, and Trim how much of each, as tony.Trim projects. `viff`'s --if and
// --trim, viff mcp's if and trim, and the viff system's tools apply the one Filter, so a
// pattern answers the same wherever it is given. Either may be nil.
type Filter struct{ Keep, Trim *ir.Node }

// None reports whether the filter asks nothing.
func (f Filter) None() bool { return f.Keep == nil && f.Trim == nil }

// Item applies both halves to one item: whether it survives, and what of it is written. A
// pattern tony.Match cannot apply is a misuse of if, never an item that did not match.
func (f Filter) Item(n *ir.Node) (*ir.Node, bool, error) {
	if f.Keep != nil {
		ok, err := tony.Match(n, f.Keep)
		if err != nil {
			return nil, false, misuse("if", err)
		}
		if !ok {
			return nil, false, nil
		}
	}
	if f.Trim != nil {
		return tony.Trim(f.Trim, n), true, nil
	}
	return n, true, nil
}

// Apply filters an answer: an array by its elements, anything else as one item. What keeps
// nothing is an empty list: the question was asked and nothing kept.
func (f Filter) Apply(n *ir.Node) (*ir.Node, error) {
	if f.None() || n == nil {
		return n, nil
	}
	if n.Type != ir.ArrayType {
		out, keep, err := f.Item(n)
		if err != nil {
			return nil, err
		}
		if !keep {
			return ir.FromSlice(nil), nil
		}
		return out, nil
	}
	var kept []*ir.Node
	for _, v := range n.Values {
		out, keep, err := f.Item(v)
		if err != nil {
			return nil, err
		}
		if keep {
			kept = append(kept, out)
		}
	}
	return ir.FromSlice(kept), nil
}
