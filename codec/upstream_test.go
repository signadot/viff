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

// Blank input — byte-empty or whitespace-only — parses to (nil, nil): a nil node AND a nil
// error, which reads as success rather than as "no document".
//
// Before v0.0.88 a document of spaces was instead a parse error ("extraneous indent"),
// because indentation is significant in a YAML-family format and there was no document for
// it to be significant to. That is fixed; ParseNode's trim is now belt-and-braces rather
// than load-bearing, and is kept because the (nil, nil) half still needs absorbing.
func TestUpstreamBlankInputIsNilNil(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t\n", "  \n  "} {
		n, err := parse.Parse([]byte(in), parse.ParseTony())
		if err != nil {
			t.Errorf("parse(%q) errored: %v — blank input is meant to be (nil, nil)", in, err)
		}
		if n != nil {
			t.Errorf("parse(%q) returned a node — the (nil, nil) contract changed", in)
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

// A hand-written codec is reached however the value is held — value or pointer, struct or
// named map. Before v0.0.88 gomap checked for the method only inside its pointer branch, so
// a codec on a value field was skipped silently and the field walked structurally instead;
// that is what forced verse's charter types to be hand-written all the way up. Now the
// dispatch is uniform, which is what lets a generated type hold a hand-written one.
func TestUpstreamReflectionDispatchIsUniform(t *testing.T) {
	dispCalls, dispFormats = nil, nil
	h := &dispHost{PtrLeaf: &dispLeaf{}, ValMap: dispMap{"a": 1}}
	out, err := gomap.ToTony(h, gomap.EncodeFormat(format.TonyFormat))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	for _, want := range []string{"valLeaf: LEAF", "ptrLeaf: LEAF", "valMap: MAP"} {
		if !strings.Contains(got, want) {
			t.Errorf("no dispatch for %q — a hand-written codec is being skipped: %s", want, got)
		}
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

// `omitzero` is very nearly a no-op upstream: the generator consults it only for
// reflect.Bool, and the reflection path reads only `field=`. The guards on slices, maps and
// pointers are unconditional — present whether or not the field carries the tag — which is
// what makes it look like the tag works.
//
// codec/omitzero.go exists solely because of this. When this test starts failing, upstream
// honors the tag and pruneZero can be deleted for any type that has a generated codec.
// Filed as tony-format#f69agjyeh12ksa25cnn0 item 10.
func TestUpstreamOmitzeroStillIgnored(t *testing.T) {
	type probe struct {
		ID    string `tony:"field=id"`
		Task  string `tony:"field=task,omitzero"`
		Count int    `tony:"field=count,omitzero"`
	}
	out, err := gomap.ToTony(&probe{ID: "x"}, gomap.EncodeFormat(format.TonyFormat))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `task: ""`) || !strings.Contains(got, "count: 0") {
		t.Errorf("go-tony now honors omitzero. THIS IS THE TRIGGER TO ADOPT tony-codegen: "+
			"generated codecs can omit their own empty fields, so codegen stops being an "+
			"addition on top of pruneZero and becomes a replacement for it. See the note in "+
			"the Makefile and codec/omitzero.go.\n%s", got)
	}
}

// The other half of the same defect, and the sharper one: `omit` is honored by codegen but
// ignored by reflection, so whether a field marked for exclusion reaches the wire depends on
// whether its type happened to be generated. No verse type relies on `omit` — this pin is
// here so that stays a deliberate choice rather than a lucky one.
func TestUpstreamOmitStillIgnoredByReflection(t *testing.T) {
	type probe struct {
		ID     string `tony:"field=id"`
		Secret string `tony:"field=secret,omit"`
	}
	out, err := gomap.ToTony(&probe{ID: "x", Secret: "shh"}, gomap.EncodeFormat(format.TonyFormat))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "secret") {
		t.Logf("reflection now honors `omit` — verse types may rely on it:\n%s", out)
	}
}
