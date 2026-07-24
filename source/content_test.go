package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/system/libctl"
)

// A non-UTF-8 (binary) file must NOT be served as raw content. Doing so puts invalid
// bytes on the tony wire, which fails docd's tokenizer ("bad utf8"), resets the
// controller's session, and tombstones the whole content mount — after which every
// read_file in the session fails "file not found". The controller serves a {binary}
// marker (with size) instead, exactly as it does for an over-cap file.
func TestContentBinaryFileNotServedAsContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "goodfile"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Bytes that are not valid UTF-8 (lone 0xff/0xfe, an embedded NUL, a truncated
	// multibyte lead 0x80) — a stand-in for any binary file in a connected tree.
	if err := os.WriteFile(filepath.Join(dir, "binblob"), []byte{0xff, 0xfe, 0x00, 0x80, 0xc3, 0x28}, 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewContent(dir, "m") // dotless names => no kpath quoting, so path is m.<name>

	good := decode(t, mustMatch(t, c, "m.goodfile"))
	if good["content"] != "hello\n" {
		t.Fatalf("good file: want content %q, got %#v", "hello\n", good)
	}
	if _, marked := good["binary"]; marked {
		t.Fatalf("good (utf-8) file must not be marked binary: %#v", good)
	}

	bin := decode(t, mustMatch(t, c, "m.binblob"))
	if _, served := bin["content"]; served {
		t.Fatalf("binary file must NOT carry content (would poison the wire): %#v", bin)
	}
	if bin["binary"] != "non-utf8" {
		t.Fatalf("binary file: want binary=%q marker, got %#v", "non-utf8", bin)
	}
	if bin["size"] == nil {
		t.Fatalf("binary file should still report size: %#v", bin)
	}
}

func mustMatch(t *testing.T, c *Content, path string) *ir.Node {
	t.Helper()
	n, err := c.Match(context.Background(), path, nil, libctl.MatchParams{})
	if err != nil {
		t.Fatalf("Match(%q): %v", path, err)
	}
	return n
}

func decode(t *testing.T, n *ir.Node) map[string]any {
	t.Helper()
	var m map[string]any
	if err := gomap.FromTonyIR(n, &m); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	return m
}
