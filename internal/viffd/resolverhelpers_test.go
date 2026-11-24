package viffd

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// mustInstall fails loudly rather than leaving a test to wonder why nothing owns its question:
// an add that is refused returns a body saying why, and swallowing it costs an afternoon.
func mustInstall(t *testing.T, base, doc string) {
	t.Helper()
	resp := installResolver(t, base, doc)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("installing %s: %s: %s", doc, resp.Status, strings.TrimSpace(string(raw)))
	}
}
