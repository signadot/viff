package stock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/runner/runnerd"
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
	if err := sb.Wrap(cmd, work, runnerd.Network{Mode: runnerd.NetworkNone}, []string{keep}); err != nil {
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

// A READ-ONLY PATH MAY NOT REACH WHAT THE RUNNER KEEPS FROM ITS TASKS: its work, its mirrors, and
// its control directory, whose sockets a read-only mount would still let a task connect to. A
// path that holds one, is one, or lies in one is refused, links followed; one beside them is not.
func TestAReadOnlyPathMayNotReachTheRunnersOwn(t *testing.T) {
	base := t.TempDir()
	run := filepath.Join(base, "var", "verse")
	work, control, mirrors := filepath.Join(run, "work"), filepath.Join(run, "control"), filepath.Join(base, "mirrors")
	cache := filepath.Join(base, "cache", "go")
	for _, d := range []string{work, control, mirrors, cache} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "sneaky")
	if err := os.Symlink(run, link); err != nil {
		t.Fatal(err)
	}
	kept := map[string]string{"work": work, "control": control, "mirrors": mirrors}
	for p, want := range map[string]string{
		run:                            "control", // the parent of control and work
		control:                        "control", // the directory itself
		filepath.Join(work, "task-1"):  "work",    // a task's own
		filepath.Join(mirrors, "o/r"):  "mirrors", // every repository the runner holds
		link:                           "control", // the parent again, by a link
		filepath.Join(base, "var", ""): "control", // further up
	} {
		err := checkReadOnly([]string{cache, p}, kept)
		if err == nil || !strings.Contains(err.Error(), "the runner's "+want) {
			t.Errorf("readOnly %s: %v, want refused for its %s", p, err, want)
		}
	}
	if err := checkReadOnly([]string{cache, filepath.Join(base, "var", "other")}, kept); err != nil {
		t.Errorf("paths beside the runner's own: %v", err)
	}
}
