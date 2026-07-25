package process

import (
	"encoding"
	"encoding/json"
	"testing"
)

// Mode's vocabulary must be one vocabulary, whatever encoding asks for it. The text form
// is the primitive (tony's gomap honors encoding.TextMarshaler on both its reflection and
// generated paths); the JSON form delegates to it.
var (
	_ encoding.TextMarshaler   = Auto
	_ encoding.TextUnmarshaler = (*Mode)(nil)
)

func TestModeTextRoundTrip(t *testing.T) {
	for _, m := range []Mode{Auto, Ask, Park} {
		b, err := m.MarshalText()
		if err != nil {
			t.Fatalf("MarshalText(%v): %v", m, err)
		}
		var got Mode
		if err := got.UnmarshalText(b); err != nil {
			t.Fatalf("UnmarshalText(%q): %v", b, err)
		}
		if got != m {
			t.Errorf("round trip: %v -> %q -> %v", m, b, got)
		}
	}
}

func TestModeUnmarshalTextAccepts(t *testing.T) {
	cases := map[string]Mode{
		"auto": Auto, "AUTO": Auto, "Auto": Auto, "0": Auto, " auto ": Auto,
		"ask": Ask, "ASK": Ask, "1": Ask,
		"park": Park, "PARK": Park, "2": Park,
	}
	for in, want := range cases {
		var got Mode
		if err := got.UnmarshalText([]byte(in)); err != nil {
			t.Errorf("UnmarshalText(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("UnmarshalText(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"", "3", "blocked", "auto ask"} {
		var got Mode
		if err := got.UnmarshalText([]byte(in)); err == nil {
			t.Errorf("UnmarshalText(%q) accepted, want an error", in)
		}
	}
}

// The JSON surface is unchanged by the move to text: a quoted lowercase name out, a name
// or a number in.
func TestModeJSONSurfaceUnchanged(t *testing.T) {
	b, err := json.Marshal(Ask)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"ask"` {
		t.Errorf("json.Marshal(Ask) = %s, want \"ask\"", b)
	}
	for _, in := range []string{`"park"`, `"PARK"`, `2`} {
		var got Mode
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Errorf("json.Unmarshal(%s): %v", in, err)
			continue
		}
		if got != Park {
			t.Errorf("json.Unmarshal(%s) = %v, want Park", in, got)
		}
	}
}
