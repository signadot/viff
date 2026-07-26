package codec

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// The charter shape this exists for: a tagged document must still render as JSON.
func TestDetagCharterShape(t *testing.T) {
	// !until [ !quorum 2, !fact motion:deadline ]
	doc := ir.FromSlice([]*ir.Node{
		ir.FromInt(2).WithTag("!quorum"),
		ir.FromString("motion:deadline").WithTag("!fact"),
	}).WithTag("!until")

	// As tony, tags survive verbatim.
	tony, err := NodeBytesWire(doc, Tony)
	if err != nil {
		t.Fatalf("tony: %v", err)
	}
	for _, want := range []string{"!until", "!quorum", "!fact"} {
		if !strings.Contains(string(tony), want) {
			t.Errorf("tony output lost %s: %s", want, tony)
		}
	}

	// As JSON — which cannot carry a tag at all — they become keys.
	js, err := NodeBytesWire(doc, JSON)
	if err != nil {
		t.Fatalf("json: %v (encoding a tagged document as JSON must not fail)", err)
	}
	got := string(js)
	for _, want := range []string{`"until"`, `"quorum"`, `"fact"`} {
		if !strings.Contains(got, want) {
			t.Errorf("json output missing %s: %s", want, got)
		}
	}
	if strings.Contains(got, "!") {
		t.Errorf("json output still carries a tag sigil: %s", got)
	}
}

// Detag must not mutate its argument: the same document is often rendered twice (once to
// a client, once to a log), and the second render must not see the first's rewrite.
func TestDetagDoesNotMutateInput(t *testing.T) {
	doc := ir.FromInt(2).WithTag("!quorum")
	Detag(doc)
	if doc.Tag != "!quorum" {
		t.Fatalf("Detag mutated its input: tag is now %q", doc.Tag)
	}
	// And rendering as JSON twice gives the same answer.
	a, err := NodeBytesWire(doc, JSON)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NodeBytesWire(doc, JSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("rendering twice differed: %q vs %q", a, b)
	}
}

func TestDetagUntaggedIsUnchanged(t *testing.T) {
	doc, err := ParseNode([]byte("stage: open\nn: 2\n"), Tony)
	if err != nil {
		t.Fatal(err)
	}
	if !Detag(doc).DeepEqual(doc) {
		t.Error("Detag altered an untagged document")
	}
}

// Formatting hints are the encoder's own business — they carry no meaning and must not
// become object keys.
func TestDetagKeepsFormattingHints(t *testing.T) {
	doc := ir.FromSlice([]*ir.Node{ir.FromInt(1)}).WithTag("!bracket")
	out := Detag(doc)
	if out.Type != ir.ArrayType {
		t.Fatalf("a formatting-only tag was turned into an object: %v", out.Type)
	}
	if !ir.TagHas(out.Tag, "!bracket") {
		t.Errorf("the formatting hint was dropped: tag %q", out.Tag)
	}

	// With a semantic tag alongside it, the semantic one becomes the key and the hint
	// stays on the value.
	doc = ir.FromSlice([]*ir.Node{ir.FromInt(1)}).WithTag("!until!bracket")
	out = Detag(doc)
	if out.Type != ir.ObjectType {
		t.Fatalf("the semantic tag did not become a key: %v", out.Type)
	}
}

// Comments are a wrapper node and JSON has no comments; Detag steps past them rather
// than descending into a node the encoder will drop.
func TestDetagUnwrapsComments(t *testing.T) {
	doc, err := ParseNode([]byte("# why\nstage: open\n"), Tony)
	if err != nil {
		t.Fatal(err)
	}
	js, err := NodeBytesWire(doc, JSON)
	if err != nil {
		t.Fatalf("a commented document failed to render as JSON: %v", err)
	}
	if !strings.Contains(string(js), `"stage"`) {
		t.Errorf("the value under the comment was lost: %s", js)
	}
}

// Nested tags, at every depth, in both collection kinds.
func TestDetagRecurses(t *testing.T) {
	doc := ir.FromKeyVals([]ir.KeyVal{
		{Key: ir.FromString("when"), Val: ir.FromSlice([]*ir.Node{
			ir.FromKeyVals([]ir.KeyVal{
				{Key: ir.FromString("inner"), Val: ir.FromInt(1).WithTag("!quorum")},
			}),
		}).WithTag("!until")},
	})
	js, err := NodeBytesWire(doc, JSON)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	got := string(js)
	if !strings.Contains(got, `"until"`) || !strings.Contains(got, `"quorum"`) {
		t.Errorf("a nested tag was not rewritten: %s", got)
	}
	if strings.Contains(got, "!") {
		t.Errorf("json output still carries a tag sigil: %s", got)
	}
}

// YAML keeps its tags — Detag applies to JSON only, because YAML can say !until.
func TestDetagNotAppliedToYAML(t *testing.T) {
	doc := ir.FromInt(2).WithTag("!quorum")
	out, err := NodeBytes(doc, YAML)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "!quorum") {
		t.Errorf("YAML output lost the tag: %q", out)
	}
}

// A document's layout hints describe how it was WRITTEN, not what it means, so they must
// not survive a change of dialect: a JSON response asked for as tony should come back as
// block tony, not as JSON's shape in tony's syntax.
func TestStripLayout(t *testing.T) {
	jsonDoc := []byte(`[{"name":"x","when":{"kind":"pr"}}]`)
	n, err := ParseNode(jsonDoc, JSON)
	if err != nil {
		t.Fatal(err)
	}

	// Kept: rendering back as tony preserves the brackets the source had.
	kept, err := NodeBytes(n, Tony)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kept), "[") {
		t.Errorf("layout hints were not preserved when they should be: %s", kept)
	}

	// Stripped: the block form.
	stripped, err := NodeBytes(StripLayout(n), Tony)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stripped), "[") || strings.Contains(string(stripped), "{") {
		t.Errorf("StripLayout left the source's brackets: %s", stripped)
	}
	if !strings.Contains(string(stripped), "- name: x") {
		t.Errorf("expected a block list: %s", stripped)
	}

	// The input is untouched — the same node may be rendered more than once.
	again, err := NodeBytes(n, Tony)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(kept) {
		t.Errorf("StripLayout mutated its input:\n first: %s\nsecond: %s", kept, again)
	}
}
