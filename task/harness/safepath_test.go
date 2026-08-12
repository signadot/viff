package harness

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/task"
)

// A path may not leave the working directory, by traversal OR through a symlink.
//
// The symlink half is the one that was missing, and it is not a hypothetical shape: a working
// directory here is frequently a git checkout, git checks symlinks out as symlinks, and
// `link/pwned` contains no `..` — so the lexical check approved it and the write followed the
// link off the end of the world. No shell, no traversal, nothing clever from the model; just a
// repository that happens to carry a symlink.
func TestSafePathRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink whose target is a FILE outside, named directly.
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(root, "leaf")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "target.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path string
		ok         bool
	}{
		{"an ordinary file", "real.txt", true},
		{"a file that does not exist yet — a write names one", "new.txt", true},
		{"a nested file that does not exist yet", "sub/dir/new.txt", true},
		{"traversal", "../pwned", false},
		{"traversal, buried", "a/../../pwned", false},
		// Not refused, and worth pinning as behaviour rather than assumed either way:
		// filepath.Join strips the leading separator, so an absolute path is REINTERPRETED as
		// root-relative. `<root>/etc/passwd` is inside the root, so it is safe — the model
		// simply reads something that is almost certainly not there. The frame already tells
		// it every path is relative to the working directory.
		{"an absolute path, rooted rather than refused", "/etc/passwd", true},
		{"through a symlinked directory", "link/pwned", false},
		{"a symlink to a file outside", "leaf", false},
		{"deeper through a symlinked directory", "link/a/b/pwned", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safePath(root, tc.path)
			if tc.ok && err != nil {
				t.Fatalf("safePath(%q) = %v, want it allowed", tc.path, err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("safePath(%q) = %q, want refused", tc.path, got)
				}
				return
			}
			// An allowed path must genuinely land inside the root once resolved, which is the
			// property the caller relies on rather than the absence of an error.
			real, rerr := resolveExisting(got)
			if rerr != nil {
				t.Fatalf("resolving %q: %v", got, rerr)
			}
			realRoot, _ := filepath.EvalSymlinks(root)
			if escapes(realRoot, real) {
				t.Fatalf("safePath allowed %q, which resolves to %q, outside %q", tc.path, real, realRoot)
			}
		})
	}
}

// The root itself is very often reached through a symlink — /var is /private/var on macOS, which
// is where every temp dir lives — so resolving the candidate without resolving the root would
// refuse every legitimate write. This is the regression that would make the fix worse than the
// bug.
func TestSafePathAllowsASymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(base, "via-link")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, p := range []string{"a.txt", "sub/b.txt"} {
		if _, err := safePath(linkedRoot, p); err != nil {
			t.Errorf("safePath(<symlinked root>, %q) = %v, want it allowed", p, err)
		}
	}
	// And it still refuses an escape from a symlinked root.
	if _, err := safePath(linkedRoot, "../pwned"); err == nil {
		t.Error("traversal out of a symlinked root was allowed")
	}
}

// The refusal says which kind it was, because "escapes the working directory" sent a reader
// looking for a `..` that is not in the path.
func TestSafePathNamesTheSymlinkCase(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := safePath(root, "link/x")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want it to name the symlink", err)
	}
}

// A filesystem error tells the agent what to do next, in the paths it was given.
//
// Both halves come from one line a drive produced:
//
//	ERROR: read /Users/scott/.verse-coderunner/workspaces/docsdrift-e1f3.../verse/trigger: is a directory
//
// It names no remedy, so the model spends a turn deducing that list_dir exists; and it speaks in
// absolute paths after the frame told the agent every path is relative to its working directory.
func TestFilesystemErrorsAreActionableAndRelative(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "trigger"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &fsExec{root: root, log: io.Discard}
	fail := func(tool, path string) string {
		t.Helper()
		out, _, err := e.Execute(context.Background(), task.ToolUse{
			Name: tool, Input: map[string]any{"path": path}})
		if err != nil {
			t.Fatal(err)
		}
		return out.Output
	}

	// read_file on a directory names the tool that would have worked.
	if got := fail("read_file", "trigger"); !strings.Contains(got, "use list_dir") {
		t.Errorf("read_file on a directory does not name the remedy: %q", got)
	}
	// list_dir on a file, symmetrically.
	if got := fail("list_dir", "note.md"); !strings.Contains(got, "use read_file") {
		t.Errorf("list_dir on a file does not name the remedy: %q", got)
	}
	// A missing path is a missing path, said plainly.
	if got := fail("read_file", "nope.md"); !strings.Contains(got, "no such file") {
		t.Errorf("a missing file: %q", got)
	}
	// And no message leaks the host path the agent was never told about.
	for _, p := range []string{"trigger", "note.md", "nope.md"} {
		for _, tool := range []string{"read_file", "list_dir"} {
			if got := fail(tool, p); strings.Contains(got, root) {
				t.Errorf("%s %s leaks the working directory into the agent's view: %q", tool, p, got)
			}
		}
	}
}
