package codec

import (
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
)

// formatTags are hints the encoder consumes itself — whether to bracket a collection,
// whether a string was a block literal. They carry no meaning, so they must stay on the
// node rather than becoming object keys.
var formatTags = []string{"!bracket", "!literal"}

// Detag rewrites tagged nodes into the tag-keyed object form, so a document that uses
// tags can be rendered in a dialect that has none:
//
//	!until [ !quorum 2 ]   ->   {"until": [{"quorum": 2}]}
//
// JSON has no tag syntax at all, and go-tony does not guess: encoding a tagged node as
// JSON returns an error rather than dropping the tag (encode/encode.go, pinned by
// TestUpstreamJSONRejectsTags). Something has to bridge that, and doing it here — once,
// over a whole document — is what keeps verse's types out of it.
//
// That placement is not just tidiness. A type CANNOT reliably know which dialect it is
// being rendered into: tony-codegen emits nested field calls as `s.Field.ToTonyIR()`,
// dropping the options that carry the target format (pinned by
// TestUpstreamCodegenDropsNestedOpts). So a format-aware ToTonyIR would see the target
// only when it happened to be the top-level value. One pass over the finished IR has no
// such blind spot.
//
// The tag-keyed form is not an invention for this purpose: it is the surface verse's JSON
// charters already use, and the charter decoder accepts both spellings. Detag is
// therefore the JSON *rendering* of the same grammar, not a lossy fallback.
//
// The input is not modified.
func Detag(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	return detag(n.Clone())
}

// detag rewrites in place; it is only ever called on a clone.
func detag(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	// A comment wrapper holds the real value in Values[0]. JSON has no comments either,
	// so unwrap and carry on rather than descending into a node the encoder will drop.
	if n.Type == ir.CommentType {
		if len(n.Values) == 0 {
			return ir.Null()
		}
		return detag(n.Values[0])
	}

	for i, v := range n.Values {
		n.Values[i] = detag(v)
	}

	sem, keep := splitTag(n.Tag)
	if sem == "" {
		n.Tag = keep
		return n
	}
	n.Tag = keep
	return ir.FromKeyVals([]ir.KeyVal{{
		Key: ir.FromString(strings.TrimPrefix(sem, "!")),
		Val: n,
	}})
}

// splitTag separates a node's semantic tag from the formatting-only hints, which stay
// where they are.
func splitTag(tag string) (sem, keep string) {
	sem = tag
	for _, f := range formatTags {
		if ir.TagHas(sem, f) {
			sem = ir.TagRemove(sem, f)
			keep += f
		}
	}
	return sem, keep
}
