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
	for _, f := range []Format{Tony, JSON, YAML} {
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

// A tagged document still renders as JSON through the colored path — Detag has to run
// there too, or `-o json --color always` would fail where `-o json` succeeds.
func TestEncodeNodeColorDetagsForJSON(t *testing.T) {
	doc := ir.FromInt(2).WithTag("!quorum")
	var buf bytes.Buffer
	if err := EncodeNodeColor(&buf, doc, JSON); err != nil {
		t.Fatalf("a tagged document failed to render as colored JSON: %v", err)
	}
	if !strings.Contains(stripANSI(buf.String()), `"quorum"`) {
		t.Errorf("the tag was not rewritten: %q", buf.String())
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
