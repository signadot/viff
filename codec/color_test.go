package codec

import (
	"bytes"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// The colored encoder must produce the SAME document, only decorated: whatever escapes it
// adds, stripping them has to give back exactly what the plain encoder wrote. A renderer
// that quietly reformats when colour is on would make `--color always | …` a different
// document from the piped one.
func TestEncodeNodeColorRendersTheSameDocument(t *testing.T) {
	doc := ir.FromKeyVals([]ir.KeyVal{
		{Key: ir.FromString("ref"), Val: ir.FromString("demo:pr:1")},
		{Key: ir.FromString("n"), Val: ir.FromInt(2)},
	})
	for _, f := range []Format{Tony, YAML} {
		plain, err := NodeBytes(doc, f)
		if err != nil {
			t.Fatalf("%v plain: %v", f, err)
		}
		var buf bytes.Buffer
		if err := EncodeNodeColor(&buf, doc, f); err != nil {
			t.Fatalf("%v color: %v", f, err)
		}
		if got := stripANSI(buf.String()); got != string(plain) {
			t.Errorf("%v: colored output differs once escapes are stripped:\n plain: %q\n got:   %q",
				f, plain, got)
		}
	}
}

// …and a COMMENTED document is the case that matters, because colour is what a terminal
// gets and a terminal is where a human reads the comments they wrote.
//
// The test above passed while this failed: it rendered a document with nothing said about
// it, so "the same document" was true of the only part the colored path was rendering. The
// coloured entry points were written separately from encodeNode and never got its
// EncodeComments(true), so reading a rule back printed a stripped charter to the operator
// and the full one to `| cat` — the format flag was not the difference, the pipe was.
func TestEncodeNodeColorKeepsComments(t *testing.T) {
	src := "# why this rule exists\nname: r # the short name\ncondition: {subject: demo:pr}\n"
	doc, err := ParseNode([]byte(src), Tony)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := EncodeNodeColor(&buf, doc, Tony); err != nil {
		t.Fatalf("color: %v", err)
	}
	got := stripANSI(buf.String())
	plain, err := NodeBytes(doc, Tony)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	if got != string(plain) {
		t.Errorf("colored output differs once escapes are stripped:\n plain: %q\n got:   %q", plain, got)
	}
	for _, want := range []string{"# why this rule exists", "# the short name"} {
		if !strings.Contains(got, want) {
			t.Errorf("the colored rendering dropped %q:\n%s", want, got)
		}
	}
}

// The colored path refuses JSON exactly as the plain one does. It is a separate entry
// point with its own encode call, so it needs its own gate — `-o json --color always`
// must not succeed where `-o json` fails.
func TestEncodeNodeColorRefusesJSON(t *testing.T) {
	doc := ir.FromInt(2).WithTag("!quorum")
	var buf bytes.Buffer
	if err := EncodeNodeColor(&buf, doc, JSON); err == nil {
		t.Errorf("colored JSON rendered %q — the color path has its own gate to close", buf.String())
	}
}

// stripANSI removes CSI sequences.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // the 'm'
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
