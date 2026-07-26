package process

import (
	"encoding"
	"testing"
)

// Mode's vocabulary must be one vocabulary, whatever encoding asks for it — the text form
// is it, and gomap honors encoding.TextMarshaler on both its reflection and generated paths.
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
