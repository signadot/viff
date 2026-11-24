package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The command tree reads the config layers every time a test builds it, and those are the
// developer's own: a `user:` in ~/.config/viff.tony would make every test's requests theirs,
// and a gate answered without --as would go to the wire instead of to the usage error the test
// expects. So the package runs against a config that names nothing — VIFF_CONFIG replaces both
// layers, and a file that is not there is an absent layer — with the environment's own
// overrides cleared.
//
// IT NAMES A USER, the test's own: a request that writes names who makes it, and the server
// refuses one that does not, so a CLI with nobody configured can read and nothing else. A test
// about that CLI — the usage error for an answer with no `--as`, the refusal itself — clears
// VIFF_USER for itself.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "viff-cli-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("VIFF_CONFIG", filepath.Join(dir, "none.tony"))
	for _, k := range []string{"VIFF_AGENT", "VIFF_ADDR", "VIFF_SCOPE"} {
		os.Unsetenv(k)
	}
	os.Setenv("VIFF_USER", tester)
	// The scripts these tests run call the task's tools with viff-call-tool, which the
	// local runner finds on PATH: built once, here, from the tree under test.
	if err := buildCallTool(dir); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// buildCallTool builds viff-call-tool into dir and puts dir first on PATH.
func buildCallTool(dir string) error {
	bin := filepath.Join(dir, "viff-call-tool")
	cmd := exec.Command("go", "build", "-o", bin, "go.viff.dev/viff/cmd/viff-call-tool")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building viff-call-tool: %v\n%s", err, out)
	}
	return os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// tester is who the package's CLI acts as unless a test says otherwise.
const tester = "user:tester@example.com"
