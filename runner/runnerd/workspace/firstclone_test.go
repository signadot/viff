package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
)

// A first clone finishes whatever happened to the spawn that asked for it. The spawn is bounded
// by versed's control deadline and a clone of anything real is longer, so the clone runs under
// the runner's own context; the spawn is then refused, saying so, and the next one finds the
// mirror there.
func TestAFirstCloneOutlivesTheSpawnThatAskedForIt(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Name: "svc", Repo: origin, Base: "main"}}

	gone, cancel := context.WithCancel(context.Background())
	cancel() // the caller has already given up
	_, err := m.Open(gone, "p1", repos, t.TempDir(), Author{})
	if err == nil {
		t.Fatal("a spawn whose caller was gone was handed a workspace nobody is waiting for")
	}
	if !contains(err.Error(), "ran out of time") {
		t.Errorf("the refusal does not say what happened: %v", err)
	}
	mirror := m.Dir(origin)
	if _, err := os.Stat(filepath.Join(mirror, "objects")); err != nil {
		t.Fatalf("the mirror was not made: %v", err)
	}
	r, err := git.PlainOpen(mirror)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reference("refs/heads/main", true); err != nil {
		t.Errorf("the mirror was made but not fetched: %v", err)
	}

	// The same spawn again, with a caller that waits, is served off the mirror it left behind.
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatalf("the second spawn did not find the mirror: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "svc", "README")); err != nil {
		t.Errorf("the second spawn's checkout is empty: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
