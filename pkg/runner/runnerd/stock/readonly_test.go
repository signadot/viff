package stock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/runner/runnerd"
)

// READ ONLY IS ABSOLUTE AND BELOW /: a relative path names nothing a sandbox can bind, and / would
// be the whole machine.
func TestReadOnlyPathsAreAbsoluteAndBelowRoot(t *testing.T) {
	for _, p := range []string{"cache", "./cache", "/"} {
		c := Config{Script: &Script{}, ReadOnly: []string{p}}
		if err := c.Check(); err == nil || !strings.Contains(err.Error(), "readOnly") {
			t.Errorf("readOnly %q: %v, want refused", p, err)
		}
	}
	if err := (&Config{Script: &Script{}, ReadOnly: []string{"/srv/cache/go"}}).Check(); err != nil {
		t.Errorf("an absolute path: %v", err)
	}
}

// A READ-ONLY PATH IS INSIDE THE SANDBOX AND CANNOT BE WRITTEN: a task reads what the operator
// keeps there, and a write is refused by the mount, whatever the task's own rights to the file.
func TestAReadOnlyPathIsReadableAndNotWritableInTheSandbox(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bwrap")
	}
	sb, err := runnerd.Bwrap()
	if err != nil {
		t.Skip(err)
	}
	keep := t.TempDir()
	if err := os.WriteFile(filepath.Join(keep, "mod.txt"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "cat "+keep+"/mod.txt && ! touch "+keep+"/new 2>/dev/null")
	if err := sb.Wrap(cmd, work, runnerd.Network{Mode: runnerd.NetworkNone}, []string{keep}, ""); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "kept" {
		t.Fatalf("in the sandbox: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(keep, "new")); err == nil {
		t.Fatal("the task wrote a read-only path")
	}
}
