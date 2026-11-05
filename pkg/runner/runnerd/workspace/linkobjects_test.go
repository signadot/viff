package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/signadot/verse/pkg/runner"
)

// A checkout's objects are the mirror's, linked: the same file, not a copy of it. That is what
// keeps a launch against a large repository from waiting on a copy, and it holds as
// long as every object file the checkout has is one the mirror has.
func TestACheckoutSharesTheMirrorsObjectFiles(t *testing.T) {
	origin := initRepo(t)
	mirrors := t.TempDir()
	m := NewMirrors(mirrors, TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	mirror := m.Dir(origin)
	linked, files := 0, 0
	err = filepath.WalkDir(filepath.Join(ws.Root, "svc", ".git", "objects"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join(ws.Root, "svc", ".git", "objects"), p)
		a, err := os.Stat(p)
		if err != nil {
			return err
		}
		b, err := os.Stat(filepath.Join(mirror, "objects", rel))
		if err != nil {
			t.Errorf("%s is in the checkout and not in the mirror", rel)
			return nil
		}
		files++
		if os.SameFile(a, b) {
			linked++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("the checkout has no object files at all")
	}
	// Same filesystem here, so every one is a link; a copy is the fallback for when it is not.
	if linked != files {
		t.Errorf("%d of %d object files are copies rather than links", files-linked, files)
	}
	// And the refs a clone would have made are there for a task's shell to read.
	if _, err := ws.Trees[0].repo.Reference("refs/remotes/mirror/main", true); err != nil {
		t.Errorf("refs/remotes/mirror/main is missing: %v", err)
	}
}
