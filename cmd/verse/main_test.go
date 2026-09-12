package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The command tree reads the config layers every time a test builds it, and those are the
// developer's own: a `user:` in ~/.config/verse.tony would make every test's requests theirs,
// and a gate answered without --as would go to the wire instead of to the usage error the test
// expects. So the package runs against a config that names nothing — VERSE_CONFIG replaces both
// layers, and a file that is not there is an absent layer — with the environment's own
// overrides cleared. A test that wants a user sets VERSE_USER itself.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "verse-cli-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("VERSE_CONFIG", filepath.Join(dir, "none.tony"))
	for _, k := range []string{"VERSE_USER", "VERSE_ADDR", "VERSE_SCOPE"} {
		os.Unsetenv(k)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
