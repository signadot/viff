package codec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

type sample struct {
	Name  string         `tony:"field=name"`
	N     int            `tony:"field=n"`
	Tags  []string       `tony:"field=tags"`
	Extra map[string]any `tony:"field=extra"`
}

// One value, three dialects, same meaning. This is the whole premise of the package.
func TestRoundTripEveryFormat(t *testing.T) {
	in := sample{
		Name:  "ship-it",
		N:     2,
		Tags:  []string{"a", "b"},
		Extra: map[string]any{"nested": map[string]any{"x": int64(1)}},
	}
	for _, f := range []Format{Tony, JSON, YAML} {
		t.Run(f.String(), func(t *testing.T) {
			b, err := Marshal(in, f)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got sample
			if err := Unmarshal(b, &got, f); err != nil {
				t.Fatalf("Unmarshal(%q): %v", b, err)
			}
			if got.Name != in.Name || got.N != in.N || len(got.Tags) != 2 {
				t.Errorf("round trip lost data: %+v (wire: %s)", got, b)
			}
		})
	}
}

// The wire form is what a framed protocol needs: one line, so an SSE `data:` frame or a
// newline-delimited stream stays one record.
func TestMarshalWireIsSingleLine(t *testing.T) {
	in := sample{Name: "x", N: 1, Tags: []string{"a", "b"}, Extra: map[string]any{"k": "v"}}
	for _, f := range []Format{Tony, JSON} {
		b, err := MarshalWire(in, f)
		if err != nil {
			t.Fatalf("%v: %v", f, err)
		}
		if strings.Contains(strings.TrimRight(string(b), "\n"), "\n") {
			t.Errorf("%v wire form is multi-line: %q", f, b)
		}
	}
}

// A number read out of a decoded `any` must not depend on which decoder produced it.
func TestAsInt64(t *testing.T) {
	for _, v := range []any{int(2), int8(2), int16(2), int32(2), int64(2),
		uint(2), uint8(2), uint16(2), uint32(2), uint64(2), float32(2), float64(2)} {
		got, ok := AsInt64(v)
		if !ok || got != 2 {
			t.Errorf("AsInt64(%v (%T)) = (%d, %v), want (2, true)", v, v, got, ok)
		}
	}
	// A non-integral float is not an integer — reporting 2 for 2.5 would turn a malformed
	// threshold into a plausible one.
	for _, v := range []any{2.5, "2", true, nil, []any{2}} {
		if got, ok := AsInt64(v); ok {
			t.Errorf("AsInt64(%v (%T)) = (%d, true), want false", v, v, got)
		}
	}
}

func TestUnwrapComments(t *testing.T) {
	n, err := ParseNode([]byte("# a comment\nstage: open\n"), Tony)
	if err != nil {
		t.Fatal(err)
	}
	if u := Unwrap(n); u == nil || u.Type != ir.ObjectType {
		t.Fatalf("Unwrap gave %v, want the object beneath the comment", u)
	}
	if Unwrap(nil) != nil {
		t.Error("Unwrap(nil) should be nil")
	}
}

func TestForPath(t *testing.T) {
	SetDefault(Tony)
	t.Cleanup(func() { SetDefault(Tony) })

	for path, want := range map[string]Format{
		"charter.tony": Tony,
		"charter.json": JSON,
		"charter.yaml": YAML,
		"charter.yml":  YAML,
		"CHARTER.JSON": JSON, // case-insensitive
		"a/b/c.tony":   Tony,
	} {
		if got, ok := ForPathStrict(path); !ok || got != want {
			t.Errorf("ForPathStrict(%q) = (%v, %v), want (%v, true)", path, got, ok, want)
		}
	}
	// No suffix, or one that names nothing, defers to the default rather than guessing.
	for _, path := range []string{"charter", "charter.txt", "", "-"} {
		if _, ok := ForPathStrict(path); ok {
			t.Errorf("ForPathStrict(%q) claimed to know the format", path)
		}
	}
	SetDefault(JSON)
	if got := ForPath("charter"); got != JSON {
		t.Errorf("ForPath on an extensionless file = %v, want the default (json)", got)
	}
	if got := ForPath("charter.tony"); got != Tony {
		t.Errorf("a suffix must beat the default, got %v", got)
	}
}

func TestReadSource(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A named file's suffix decides, regardless of the hint.
	_, f, err := ReadSource(nil, p, Tony)
	if err != nil || f != JSON {
		t.Errorf("ReadSource(%s) format = %v (err %v), want json", p, f, err)
	}
	// stdin has no name, so the hint decides.
	data, f, err := ReadSource(strings.NewReader("a: 1\n"), "-", YAML)
	if err != nil || f != YAML || strings.TrimSpace(string(data)) != "a: 1" {
		t.Errorf("ReadSource(stdin) = (%q, %v, %v)", data, f, err)
	}
}

func TestMediaTypes(t *testing.T) {
	SetDefault(Tony)
	for ct, want := range map[string]Format{
		"application/tony":                Tony,
		"application/tony; charset=utf-8": Tony,
		"Application/TONY":                Tony,
		"application/x-tony":              Tony,
		"text/tony":                       Tony,
		"application/vnd.signadot.tony":   Tony,
		"application/json":                JSON,
		"application/json;charset=utf-8":  JSON,
		"application/yaml":                YAML,
		"text/x-yaml":                     YAML,
	} {
		if got, ok := FormatForMedia(ct); !ok || got != want {
			t.Errorf("FormatForMedia(%q) = (%v, %v), want (%v, true)", ct, got, ok, want)
		}
	}
	for _, ct := range []string{"text/plain", "", "application/octet-stream"} {
		if _, ok := FormatForMedia(ct); ok {
			t.Errorf("FormatForMedia(%q) claimed a format", ct)
		}
	}
	if MediaType(Tony) != MediaTony || MediaType(JSON) != MediaJSON || MediaType(YAML) != MediaYAML {
		t.Error("MediaType does not round-trip its own constants")
	}
}

func TestNegotiate(t *testing.T) {
	cases := []struct {
		accept string
		want   Format
	}{
		{"", Tony},                                               // absent → fallback
		{"*/*", Tony},                                            // no preference → fallback
		{"application/json", JSON},                               // explicit
		{"application/yaml", YAML},                               // explicit
		{"text/plain", Tony},                                     // nothing we can produce → fallback
		{"application/json, application/tony", JSON},             // equal q → first wins
		{"application/json;q=0.5, application/tony", Tony},       // q beats order
		{"application/tony;q=0.2, application/json;q=0.9", JSON}, // q beats order, reversed
		{"application/json;q=0, application/yaml", YAML},         // q=0 is a refusal
		{"application/*, application/json;q=0.1", Tony},          // type/* → fallback, outranks q=0.1
	}
	for _, c := range cases {
		if got := Negotiate(c.accept, Tony); got != c.want {
			t.Errorf("Negotiate(%q) = %v, want %v", c.accept, got, c.want)
		}
	}
	// The fallback really is the fallback.
	if got := Negotiate("*/*", JSON); got != JSON {
		t.Errorf("Negotiate(*/*, json) = %v, want json", got)
	}
}

func TestRecompact(t *testing.T) {
	got := Recompact("stage: open\nn: 2\n", Tony, Tony)
	if strings.Contains(got, "\n") {
		t.Errorf("Recompact left a newline: %q", got)
	}
	// Input it cannot parse passes through — on a live stream, a surprising line is
	// better shown than swallowed.
	const junk = "not a document: [[["
	if got := Recompact(junk, JSON, Tony); got != junk {
		t.Errorf("Recompact(%q) = %q, want it unchanged", junk, got)
	}
}
