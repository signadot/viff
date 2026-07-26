package codec

import (
	"github.com/signadot/tony-format/go-tony/ir"
)

// formatTags are hints the encoder consumes itself — whether to bracket a collection,
// whether a string was a block literal. They carry no meaning, so they must stay on the
// node rather than becoming part of the value.
//
// The two differ in PROVENANCE, which is why StripBrackets exists beside StripLayout.
// `!bracket` is attached by the parser, recording that a collection happened to be written
// `{ a: 1 }` rather than as a block — an artifact of reading, which nobody asked for.
// `!literal` is not attached by the parser at all (a multi-line string carries no tag; the
// encoder derives the block form from the string itself): it is a tag an AUTHOR writes to
// demand literal rendering. Discarding an incidental artifact is not the same as discarding
// an explicit instruction, so ingest drops the bracket alone.
const (
	bracketTag = "!bracket"
	literalTag = "!literal"
)

var formatTags = []string{bracketTag, literalTag}

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
	return stripTags(n.Clone(), formatTags)
}

// StripBrackets drops the bracket hint and nothing else, for a document arriving over the
// wire that verse is about to hold as a VALUE.
//
// Without it, how an operator's file happened to be punctuated becomes part of what verse
// stores, and it shows up later in a way that looks arbitrary. A charter is the clear case:
// `patch` and a match's `fields` are held as opaque tony documents, so they came back
// bracketed exactly as authored, while `condition`, `spawn` and `gates` — decoded into Go
// structs, which have no layout to carry — came back in block form no matter what. The
// resulting hybrid rendered an internal type boundary as typography, and read as though
// certain fields were mysteriously special.
//
// An explicit `!literal` survives (see formatTags): asking for a rendering is not the same
// as having been parsed with one.
func StripBrackets(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	return stripTags(n.Clone(), []string{bracketTag})
}

func stripTags(n *ir.Node, tags []string) *ir.Node {
	if n == nil {
		return nil
	}
	if n.Tag != "" {
		for _, f := range tags {
			n.Tag = ir.TagRemove(n.Tag, f)
		}
	}
	for i, v := range n.Values {
		n.Values[i] = stripTags(v, tags)
	}
	return n
}
