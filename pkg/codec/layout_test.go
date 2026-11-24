package codec

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// render is the shape a document comes back as, which is what these tests are about.
func render(t *testing.T, n *ir.Node) string {
	t.Helper()
	b, err := NodeBytes(n, Tony)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(b)
}

// The bracket goes and the multi-line string stays a block. Note WHY the second half holds:
// not because `!literal` is spared, but because the parser never attached it — a block
// literal carries no tag at all, and the encoder derives the block form from the string.
// Asserted anyway, because that is the property people care about.
func TestStripBracketsDropsBracesAndKeepsBlocks(t *testing.T) {
	src := `name: probe
patch: { stage: reviewing, n: 1 }
task: |
  line one
  line two
`
	n, err := ParseNode([]byte(src), Tony)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := render(t, StripBrackets(n))
	if strings.Contains(got, "{") {
		t.Errorf("a bracket survived StripBrackets:\n%s", got)
	}
	if !strings.Contains(got, "|") || !strings.Contains(got, "line one\n") {
		t.Errorf("the block string did not survive StripBrackets:\n%s", got)
	}
}

// The two strips differ on an EXPLICIT `!literal` — a tag an author wrote to demand a
// rendering, as opposed to `!bracket`, which the parser attached because of how the source
// happened to be punctuated. Ingest drops the artifact and keeps the instruction; a dialect
// conversion drops both, since neither survives the crossing meaningfully.
// A SINGLE-line string is the case where `!literal` is the only thing doing any work: a
// multi-line string renders as a block whatever its tags, so the encoder's own default hides
// the difference there.
func TestStripsDifferOnAnExplicitLiteral(t *testing.T) {
	lit := ir.FromString("one").WithTag(literalTag)
	if got := render(t, StripBrackets(lit)); !strings.Contains(got, "|") {
		t.Errorf("StripBrackets discarded an explicit !literal: %q", got)
	}
	if got := render(t, StripLayout(lit)); strings.Contains(got, "|") {
		t.Errorf("StripLayout kept an explicit !literal, so the two strips are the same: %q", got)
	}
}

// StripBrackets does not modify its input: a caller re-rendering the ORIGINAL document must
// still get the original document.
func TestStripBracketsDoesNotMutate(t *testing.T) {
	n, err := ParseNode([]byte(`patch: { stage: open }`), Tony)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	before := render(t, n)
	_ = StripBrackets(n)
	if after := render(t, n); after != before {
		t.Errorf("input was modified:\nbefore %q\nafter  %q", before, after)
	}
}

// A document arriving over the wire becomes a VALUE viff holds, and how the operator's
// file happened to be punctuated is not part of it. Read is the one place that boundary is
// crossed, so it is the one place the hint is dropped — a per-route strip would have to find
// every field that is an opaque tony node (a charter's `patch`, a match's `fields`, an
// entity payload) and missing one is invisible until someone notices braces coming back.
func TestReadDropsTheBracketHint(t *testing.T) {
	body := `{"at":"demo.pr.\"1\"","payload":{"stage":"open","tags":["a","b"]}}`
	r := httptest.NewRequest("POST", "/entity", strings.NewReader(body))
	r.Header.Set("Content-Type", MediaJSON)

	var got struct {
		At      string   `tony:"field=at"`
		Payload *ir.Node `tony:"field=payload"`
	}
	if err := Read(r, &got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.At != `demo.pr."1"` {
		t.Errorf(`at = %q, want demo.pr."1"`, got.At)
	}
	// JSON is read BY the tony parser, so a JSON body is tony written in brackets. Keeping
	// the hint is how `entity put '{"stage":"open"}'` came back as bracket-shaped tony.
	if out := render(t, got.Payload); strings.Contains(out, "{") || strings.Contains(out, "[") {
		t.Errorf("a JSON body's brackets reached the stored value:\n%s", out)
	}
}
