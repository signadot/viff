package codec

import (
	"github.com/signadot/tony-format/go-tony/ir"
)

// formatTags are hints the encoder consumes itself — whether to bracket a collection,
// whether a string was a block literal. They carry no meaning, so they must stay on the
// node rather than becoming part of the value.
var formatTags = []string{"!bracket", "!literal"}

// StripLayout removes the formatting-only hints a parser attaches to record how the SOURCE
// document happened to be written — whether a collection was bracketed, whether a string
// was a block literal.
//
// Those hints are a property of the document that was read, not of the value, so they must
// not survive a change of dialect. Keeping them is why a JSON-shaped response re-rendered
// as tony comes back as `[ { name: x } ]` — bracket-shaped tony, which is valid but is not
// what someone who asked for tony wanted to see. Dropping them yields the block form.
//
// This matters MORE now that JSON input is read by the tony parser: a JSON body is no
// longer a different dialect that gets converted, it is tony written in brackets, and its
// layout hints would otherwise ride all the way through to the output.
//
// Only for conversion: rendering a document back in its own format should preserve how it
// was written. The input is not modified.
func StripLayout(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	return stripLayout(n.Clone())
}

func stripLayout(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	if n.Tag != "" {
		for _, f := range formatTags {
			n.Tag = ir.TagRemove(n.Tag, f)
		}
	}
	for i, v := range n.Values {
		n.Values[i] = stripLayout(v)
	}
	return n
}
