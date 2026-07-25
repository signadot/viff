package codec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/format"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/parse"
)

// These tests pin behaviour of go-tony that verse's design depends on. They are not
// testing verse. They exist because each of these facts, if it silently changed upstream,
// would break verse somewhere far from here — so it is worth failing loudly and locally
// instead.

// Valid JSON is valid tony. This is why a JSON document can be read by the tony parser
// without knowing it is JSON, and why `entity put demo:pr:1 '{"stage":"open"}'` keeps
// working after tony becomes the default input format.
//
// It does NOT license sniffing: the converse is false (a tony document is not valid JSON),
// and YAML is a third thing again. Format still comes from the suffix, the media type or
// the flag. This only means the common case degrades gracefully rather than erroring.
func TestUpstreamJSONParsesAsTony(t *testing.T) {
	for _, in := range []string{
		`{"stage":"open"}`,
		`{"stage": "open", "n": 2}`,
		`[{"a":1},{"b":2}]`,
		`{"nested":{"x":[1,2,3]},"b":true,"z":null}`,
		`"bare string"`,
		`42`,
	} {
		asTony, err := parse.Parse([]byte(in), parse.ParseTony())
		if err != nil {
			t.Errorf("tony parser rejected valid JSON %s: %v", in, err)
			continue
		}
		asJSON, err := parse.Parse([]byte(in), parse.ParseJSON())
		if err != nil {
			t.Fatalf("JSON parser rejected %s: %v", in, err)
		}
		if !asTony.DeepEqual(asJSON) {
			t.Errorf("%s parsed differently as tony vs json", in)
		}
	}
}

// Empty input parses to (nil, nil) — a nil node AND a nil error, which reads as success.
//
// But WHITESPACE-only input does not: a document of spaces is a parse error ("extraneous
// indent"), because leading whitespace is significant in a YAML-family format and there
// is no document for it to be significant to. Those two facts together are why ParseNode
// trims before parsing: "empty" from a network body or a here-doc is rarely byte-empty,
// and an empty request body should not be an indentation complaint.
func TestUpstreamEmptyInputIsNilNilButBlankErrors(t *testing.T) {
	n, err := parse.Parse(nil, parse.ParseTony())
	if err != nil || n != nil {
		t.Errorf("parse(empty) = (%v, %v), want (nil, nil) — the contract changed", n, err)
	}

	for _, in := range []string{"   ", "\n\t\n", "  \n  "} {
		if _, err := parse.Parse([]byte(in), parse.ParseTony()); err == nil {
			t.Logf("parse(%q) no longer errors — ParseNode's trim is now belt-and-braces", in)
		}
	}

	// Whatever upstream does with blank input, ParseNode absorbs it.
	for _, in := range []string{"", "   ", "\n\t\n", "  \n  "} {
		got, err := ParseNode([]byte(in), Tony)
		if err != nil {
			t.Errorf("ParseNode(%q) errored: %v", in, err)
			continue
		}
		if got == nil || got.Type != ir.NullType {
			t.Errorf("ParseNode(%q) = %v, want a null node", in, got)
		}
	}
}

// Encoding a tagged node as JSON is a HARD ERROR, not a silent drop. This is the reason
// Detag exists, and the reason every generated verse wire type carries `notag`: a stray
// tag would not degrade a JSON response, it would fail it.
func TestUpstreamJSONRejectsTags(t *testing.T) {
	n := ir.FromInt(2).WithTag("!quorum")
	var sb strings.Builder
	err := encode.Encode(n, &sb, encode.EncodeFormat(format.JSONFormat))
	if err == nil {
		t.Fatalf("encoding a tagged node as JSON succeeded (%q) — Detag may be unnecessary", sb.String())
	}

	// YAML, by contrast, does carry tags.
	sb.Reset()
	if err := encode.Encode(n, &sb, encode.EncodeFormat(format.YAMLFormat)); err != nil {
		t.Fatalf("encoding a tagged node as YAML failed: %v", err)
	}
	if !strings.Contains(sb.String(), "!quorum") {
		t.Errorf("YAML output lost the tag: %q", sb.String())
	}
}

// ── the dispatch rules ───────────────────────────────────────────────────────
//
// Whether a type's hand-written ToTonyIR is actually CALLED depends on how it is reached.
// These two tests pin the rules, because between them they decide which verse types can
// be code-generated and which must be hand-written (see the trigger package).

type dispLeaf struct{ V string }

func (l dispLeaf) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	dispCalls = append(dispCalls, "leaf")
	dispFormats = append(dispFormats, encode.FormatFromOpts(gomap.ToEncodeOptions(opts...)...))
	return ir.FromString("LEAF"), nil
}

type dispMap map[string]any

func (m dispMap) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	dispCalls = append(dispCalls, "map")
	return ir.FromString("MAP"), nil
}

type dispText uint8

func (t dispText) MarshalText() ([]byte, error) { return []byte("TEXT"), nil }

var (
	dispCalls   []string
	dispFormats []format.Format
)

type dispHost struct {
	ValLeaf dispLeaf  `tony:"field=valLeaf"`
	PtrLeaf *dispLeaf `tony:"field=ptrLeaf"`
	ValMap  dispMap   `tony:"field=valMap"`
	ValText dispText  `tony:"field=valText"`
}

// On the REFLECTION path, gomap checks for a ToTonyIR method only inside its pointer
// branch (gomap/to.go). So a hand-written codec on a type used as a VALUE field is
// skipped and the field is walked structurally instead — silently, with plausible-looking
// output. encoding.TextMarshaler is checked for values too, which is why process.Mode can
// rely on it.
func TestUpstreamReflectionDispatchesOnPointersOnly(t *testing.T) {
	dispCalls, dispFormats = nil, nil
	h := &dispHost{PtrLeaf: &dispLeaf{}, ValMap: dispMap{"a": 1}}
	out, err := gomap.ToTony(h, gomap.EncodeFormat(format.TonyFormat))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	if strings.Contains(got, "valLeaf: LEAF") {
		t.Error("a VALUE struct field dispatched to ToTonyIR — reflection dispatch widened; " +
			"verse's hand-written/codegen split can be simplified")
	}
	if !strings.Contains(got, "ptrLeaf: LEAF") {
		t.Error("a POINTER struct field did not dispatch to ToTonyIR")
	}
	if strings.Contains(got, "valMap: MAP") {
		t.Error("a VALUE named-map field dispatched to ToTonyIR — trigger.Match could then " +
			"be reached from generated code")
	}
	if !strings.Contains(got, "valText: TEXT") {
		t.Error("a VALUE field's MarshalText was not honored — process.Mode depends on it")
	}

	// When the method IS reached, the options carry the target format. Detag does not
	// rely on this, but it is what a format-aware codec would need.
	for _, f := range dispFormats {
		if f != format.TonyFormat {
			t.Errorf("expected tony as the propagated target format, got %v", f)
		}
	}
	dispCalls, dispFormats = nil, nil
	if _, err := gomap.ToTony(h, gomap.EncodeFormat(format.JSONFormat)); err != nil {
		t.Fatal(err)
	}
	for _, f := range dispFormats {
		if f != format.JSONFormat {
			t.Errorf("options did not carry the JSON target to a dispatched method (got %v)", f)
		}
	}
}

// On the CODEGEN path, a nested field's codec IS called — but as `s.Field.ToTonyIR()`,
// with the options DROPPED. So a nested type cannot see the target format, which is why
// verse renders JSON with a Detag pass over the finished document rather than making each
// type format-aware.
//
// This is asserted against go-tony's own generated code, so it tracks upstream rather
// than a fixture of ours.
func TestUpstreamCodegenDropsNestedOpts(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/signadot/tony-format/go-tony").Output()
	if err != nil {
		t.Skipf("cannot locate the go-tony module: %v", err)
	}
	gen := filepath.Join(strings.TrimSpace(string(out)), "system", "logd", "api", "api_gen.go")
	src, err := os.ReadFile(gen)
	if err != nil {
		t.Skipf("cannot read %s: %v", gen, err)
	}

	// A nested field call, generated by the same tool verse runs.
	if !strings.Contains(string(src), "s.Match.ToTonyIR()") {
		t.Errorf("upstream generated code no longer contains `s.Match.ToTonyIR()`.\n"+
			"If nested calls now pass opts..., a nested codec CAN see the target format "+
			"and Detag stops being load-bearing — revisit codec.Detag and the "+
			"hand-written types in package trigger. (%s)", gen)
	}
	// The self-call inside the type's own ToTony wrapper does pass them — showing the
	// omission above is specific to nested fields, not a tool-wide absence of options.
	if !strings.Contains(string(src), "s.ToTonyIR(opts...)") {
		t.Errorf("upstream generated code no longer self-calls with opts...; "+
			"the option-passing model has changed (%s)", gen)
	}
}
