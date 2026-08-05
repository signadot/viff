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

// A large tool result reaches the model whole — not clipped to the old ~6 KB cap —
// so an agent validates a real file rather than a slice. (The truncation lives in
// modelContent, shared by every tool, so this holds for run_bash/list_dir too.)
func TestToolResultNotTruncated(t *testing.T) {
	big := strings.Repeat("line of source code\n", 3000) // ~60 KB, well over the old cap
	ex := &fsExec{root: t.TempDir(), log: io.Discard, content: fakeContent{"big.go": big}}
	res, _, err := ex.Execute(context.Background(),
		task.ToolUse{Name: "read_file", Input: map[string]any{"path": "big.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "(truncated)") {
		t.Fatal("a ~60 KB read_file result was truncated in what the model sees")
	}
	if want := strings.TrimRight(big, "\n"); !strings.Contains(res.Output, want) {
		t.Fatalf("model result missing full content: got %d bytes, want ~%d", len(res.Output), len(want))
	}
}

// fakeContent is a ContentReader backed by an in-memory map (stands in for the docd
// content controller).
type fakeContent map[string]string

func (f fakeContent) ReadContent(p string) (string, bool, error) { s, ok := f[p]; return s, ok, nil }

// With a ContentReader, read_file reads through the verse (not the filesystem): the
// bytes come from the reader, and a path it doesn't know is a not-found error — even
// when a same-named file exists on disk under root.
func TestReadFileThroughVerse(t *testing.T) {
	root := t.TempDir()
	// A decoy on disk proves read_file is NOT hitting the filesystem when federated.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("on-disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := &fsExec{root: root, log: io.Discard, content: fakeContent{"a.txt": "from-the-verse"}}

	if _, _, err := ex.Execute(context.Background(),
		task.ToolUse{Name: "read_file", Input: map[string]any{"path": "a.txt"}}); err != nil {
		t.Fatal(err)
	}
	if got := ex.Records()[0].Stdout; got != "from-the-verse" {
		t.Fatalf("read_file content = %q, want the verse's bytes (not the on-disk decoy)", got)
	}

	if _, _, err := ex.Execute(context.Background(),
		task.ToolUse{Name: "read_file", Input: map[string]any{"path": "missing"}}); err != nil {
		t.Fatal(err)
	}
	if ex.Records()[1].Err == "" {
		t.Fatal("read_file of an unfederated path should error, not fall through to disk")
	}
}

// Without a ContentReader, read_file reads the local filesystem under root (the
// unfederated default).
func TestReadFileFromDiskWhenUnfederated(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("on-disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := &fsExec{root: root, log: io.Discard} // content nil
	if _, _, err := ex.Execute(context.Background(),
		task.ToolUse{Name: "read_file", Input: map[string]any{"path": "a.txt"}}); err != nil {
		t.Fatal(err)
	}
	if got := ex.Records()[0].Stdout; got != "on-disk" {
		t.Fatalf("read_file content = %q, want on-disk bytes", got)
	}
}
