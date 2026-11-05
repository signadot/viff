package github

import (
	"testing"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	verseio "github.com/signadot/verse/pkg/io"
)

// EVERY BODY SHAPE HAS TO SURVIVE THE WIRE, and this is the test that would have caught what
// shipping did not.
//
// A multi-line string renders as a tony block scalar, and a block scalar whose content has a
// line ending in whitespace — or a trailing blank line — emits the next sibling key at column
// zero. The document is imbalanced, verse refuses the whole reflection as undecodable, and the
// way that presents is the bad way: every pass fails, the slice freezes at what it last held,
// `lastPass` keeps advancing because passes are running, and the source looks alive while
// reflecting nothing.
//
// It was found as a CRLF problem, because GitHub's web UI submits CRLF and `test 2\r\n` is a
// line ending in whitespace. Fixing only that fixed one comment: markdown's hard line break is
// TWO TRAILING SPACES, so the next body would have broken it again. Hence a table rather than a
// case — the shapes are what is interesting, and they are cheap. go-tony v0.0.119 fixed the
// render, so these now pass on the text as written rather than on a trimmed version of it, and
// that is what this keeps true.
//
// Asserted through the encode-render-parse the HTTP contract does, rather than on the payload,
// because a payload that is right and unserialisable is exactly what this was. It is an internal
// test so it can call `text` without the package exporting it for a test's benefit.
func TestEveryBodyShapeSurvivesTheWire(t *testing.T) {
	for _, body := range []string{
		"test 2",
		"test 2\n",
		"a\nb",
		"a\nb\n",
		"a\n\nb",
		"a\nb\n\n", // trailing blank line
		" leading space",
		"trailing space \n",             // the general case; CRLF is one instance of it
		"a\nb\n   \n",                   // a whitespace-only line
		"markdown break  \nnext line\n", // two trailing spaces: a hard line break
		"tab\t\nx\n",
		"test 2\r\n", // what GitHub's web UI actually submits
		"a\r\nb\r\n",
		"- a list\n- of things\n",
		"```\ncode\n```\n",
		"", "\n", "\n\n\n", "   \n", "a\n \n b\n",
	} {
		want := text(body)
		rn := verseio.Reflection{}
		rn.Add(entity.RefAt("github", "x", "y", "comment", "1", "2"),
			entity.Obj{"author": "ana", "body": want, "id": 2, "on": 1})
		_ = want
		node, err := codec.ToNode(rn)
		if err != nil {
			t.Errorf("%q encode: %v", body, err)
			continue
		}
		wire, err := codec.NodeBytes(node, codec.Tony)
		if err != nil {
			t.Errorf("%q render: %v", body, err)
			continue
		}
		parsed, err := codec.ParseNode(wire, codec.Tony)
		if err != nil {
			t.Errorf("%q did not survive the wire: %v\n%s", body, err, wire)
			continue
		}
		var back verseio.Reflection
		if err := codec.FromNode(parsed, &back); err != nil {
			t.Errorf("%q decode: %v", body, err)
			continue
		}
		got, _ := entity.ToObj(back.Entities[0].Payload)["body"].(string)
		if got != want {
			t.Errorf("%q normalised to %q and came back %q", body, want, got)
		}
	}
}

// What the normalisation does, which since go-tony v0.0.119 is only line endings: everything
// else about the text is the author's and is carried through.
func TestTextNormalisation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"test 2\r\n", "test 2\n"},
		{"a\r\nb", "a\nb"},
		{"trailing  \n", "trailing  \n"}, // markdown's hard line break, kept
		{"a\nb\n\n\n", "a\nb\n\n\n"},
		{"   \n", "   \n"},
		{"unchanged", "unchanged"},
		{"", ""},
	} {
		if got := text(tc.in); got != tc.want {
			t.Errorf("text(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
