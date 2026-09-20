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
// overrides cleared.
//
// IT NAMES A USER, the test's own: a request that writes names who makes it, and the server
// refuses one that does not, so a CLI with nobody configured can read and nothing else. A test
// about that CLI — the usage error for an answer with no `--as`, the refusal itself — clears
// VERSE_USER for itself.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "verse-cli-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("VERSE_CONFIG", filepath.Join(dir, "none.tony"))
	for _, k := range []string{"VERSE_AGENT", "VERSE_ADDR", "VERSE_SCOPE"} {
		os.Unsetenv(k)
	}
	os.Setenv("VERSE_USER", tester)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// tester is who the package's CLI acts as unless a test says otherwise.
const tester = "user:tester@example.com"
