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

// Why codec.Detag exists, pinned to the one fact that keeps it necessary.
//
// Detag rewrites `!tag value` into `{tag: value}` in a pass over the FINISHED document,
// because JSON cannot carry tags and a type cannot render itself differently per format.
// The obvious alternative — let each type emit the right shape itself — needs the type to
// know which format it is being rendered to.
//
// Through v0.0.89 that was impossible for a second, cruder reason: codegen emitted nested
// field calls as `s.Field.ToTonyIR()`, dropping the options entirely. v0.0.90 fixed that,
// and the options now reach nested calls in both directions. But the format still cannot
// be READ from them: gomap.EncodeFormat folds it into an encode.EncodeOption, whose target
// EncState.format is unexported with no accessor. The value arrives and is unreachable.
//
// So the blocker moved rather than lifted, and this pin moved with it. v0.0.90 added
// GetMapComments — the read-an-option-back pattern — so the missing piece is now a
// specific, small upstream ask: GetEncodeFormat(opts ...MapOption) format.Format. When
// that lands, this test fails, and making the trigger types format-aware becomes possible.
func TestNestedCodecStillCannotSeeTheTargetFormat(t *testing.T) {
	// Half one, which v0.0.90 fixed: the option now reaches nested calls, and lands on an
	// EncState. It is applied here to show the value genuinely arrives — what it cannot do
	// is come back out.
	encOpts := gomap.ToEncodeOptions(gomap.EncodeFormat(format.JSONFormat))
	if len(encOpts) == 0 {
		t.Fatal("EncodeFormat produced no encode option")
	}
	es := &encode.EncState{}
	for _, o := range encOpts {
		o(es)
	}
	// Half two, which it did not: nothing can read it back. Asserted against gomap's own
	// source, so it tracks upstream rather than a fixture of ours — the same approach the
	// nested-opts pin used before v0.0.90 closed that gap.
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/signadot/tony-format/go-tony").Output()
	if err != nil {
		t.Skipf("cannot locate the go-tony module: %v", err)
	}
	dir := strings.TrimSpace(string(out))

	src, err := os.ReadFile(filepath.Join(dir, "gomap", "options.go"))
	if err != nil {
		t.Skipf("cannot read gomap/options.go: %v", err)
	}
	// The read-an-option-back pattern exists (v0.0.90 added it for comments), so a format
	// getter would be a small addition rather than a design change. If this vanishes, the
	// option model changed and the reasoning below needs redoing.
	if !strings.Contains(string(src), "func GetMapComments(") {
		t.Errorf("gomap no longer exposes GetMapComments; the option-accessor model changed")
	}
	if strings.Contains(string(src), "func GetEncodeFormat(") {
		t.Errorf("gomap now exposes GetEncodeFormat: a nested codec CAN determine its " +
			"target format, so codec.Detag can be retired in favour of making each type " +
			"format-aware — revisit codec/detag.go and the hand-written codecs in trigger")
	}

	// And the reason a getter is needed at all: the field it would read is unexported.
	enc, err := os.ReadFile(filepath.Join(dir, "encode", "encode.go"))
	if err != nil {
		t.Skipf("cannot read encode/encode.go: %v", err)
	}
	if strings.Contains(string(enc), "\tFormat ") || strings.Contains(string(enc), ") Format()") {
		t.Errorf("EncState now exposes its format directly; Detag's justification is gone")
	}
}

// gomap honors `omitzero` on the reflection path: an empty scalar, collection or pointer
// carrying the tag is absent from the document, and a field WITHOUT the tag survives even
// when empty. Both halves matter — the second is what lets a caller say "explicitly empty".
//
// Before v0.0.89 the tag was consulted only for reflect.Bool in generated code and not at
// all under reflection, and this package carried its own pruning pass to compensate. That
// pass is gone; this pin is what stands in its place.
func TestUpstreamHonorsOmitzero(t *testing.T) {
	type probe struct {
		ID        string   `tony:"field=id"`
		Task      string   `tony:"field=task,omitzero"`
		Count     int      `tony:"field=count,omitzero"`
		MtSlice   []string `tony:"field=mtSlice,omitzero"`
		KeepStr   string   `tony:"field=keepStr"`
		KeepSlice []string `tony:"field=keepSlice"`
	}
	out, err := gomap.ToTony(&probe{ID: "x", MtSlice: []string{}}, gomap.EncodeFormat(format.TonyFormat))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, gone := range []string{"task", "count", "mtSlice"} {
		if strings.Contains(got, gone) {
			t.Errorf("omitzero field %q was emitted — codec would need its own pruning pass "+
				"again:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"id:", "keepStr:", "keepSlice:"} {
		if !strings.Contains(got, kept) {
			t.Errorf("field %q was dropped without an omitzero tag — an explicitly empty "+
				"value must survive:\n%s", kept, got)
		}
	}
}

// `omit`, `-` and `field=-` exclude a field on the reflection path as well as in generated
// code. Before v0.0.89 reflection ignored all three, so a field explicitly marked for
// exclusion was serialized — under its GO field name when no `field=` was given. No verse
// type relies on `omit` yet; this pin is what makes relying on it safe.
func TestUpstreamHonorsOmit(t *testing.T) {
	type probe struct {
		ID     string `tony:"field=id"`
		Secret string `tony:"field=secret,omit"`
		Dashed string `tony:"-"`
		Named  string `tony:"field=-"`
	}
	out, err := gomap.ToTony(&probe{ID: "x", Secret: "shh", Dashed: "d", Named: "n"},
		gomap.EncodeFormat(format.TonyFormat))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, leaked := range []string{"secret", "shh", "Dashed", "Named"} {
		if strings.Contains(got, leaked) {
			t.Errorf("a field marked for exclusion reached the document (%q):\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, "id: x") {
		t.Errorf("exclusion took an ordinary field with it:\n%s", got)
	}
}
