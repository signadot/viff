package entity_test

import (
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// THE STORE GUARD SEES UNDER A COMMENT, and this pins it because the walks beside it did not.
//
// Two walks in trigger/ stopped at a CommentType wrapper — a head comment makes the value under
// it a wrapper node — so a `!get-path` under a comment was never expanded and a `$subject.commit`
// under one was never refused. The same blindness HERE would be worse than either: CheckPatchable
// is what stops a match operator reaching the log, and an operator that reaches the log makes
// every later read of every entity fail. Two staging deployments went that way before the guard
// existed.
//
// It is immune for a reason worth keeping: it recurses over `n.Values` unconditionally rather
// than switching on the node's type, so a wrapper is walked like anything else. This test is
// what says so out loud, in the four placements a person writing a charter would produce.
func TestTheWriteGuardSeesUnderAComment(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"plain", "{shape: !irtype 0}"},
		{"under a head comment", "# why this shape is here\n{shape: !irtype 0}"},
		{"the field under a comment", "{ok: 1\n# why\nshape: !irtype 0}"},
		{"nested under a comment", "{a: {b: \n# why\n{shape: !irtype 0}}}"},
	} {
		n, err := codec.ParseNode([]byte(tc.src), codec.Tony)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if entity.CheckPatchable(n) == nil {
			t.Errorf("%s: a match operator would reach the log, and every later read would fail",
				tc.name)
		}
		// And the escape still works through the wrapper, so the legitimate way to store one
		// is not blocked by the same shape.
		if err := entity.CheckPatchable(entity.AsData(n)); err != nil {
			t.Errorf("%s: escaped data was refused: %v", tc.name, err)
		}
	}
}
