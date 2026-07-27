package source_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/source"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "t@example.com")
	gitRun(t, dir, "config", "user.name", "Tester")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
}

func commit(t *testing.T, dir, file, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", file)
	gitRun(t, dir, "commit", "-m", msg)
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:len(out)-1])
}

// The git Source is multi-repo native: two repos' refs share names without colliding, each
// scoped by its repo id; per-repo reconcile tombstones only its own removed refs; commits
// are content-addressed and immutable; and a targeted reconcile touches only its repo.
func TestGitReconcileMultiRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	store := entitytest.Store(t)

	dirA, dirB := t.TempDir(), t.TempDir()
	initRepo(t, dirA)
	initRepo(t, dirB)
	shaA := commit(t, dirA, "a.txt", "hello A", "first on A")
	commit(t, dirB, "b.txt", "hello B", "first on B")
	gitRun(t, dirA, "branch", "feature")
	gitRun(t, dirB, "branch", "dev")

	g := source.NewGit()
	g.AddRepo("A", dirA)
	g.AddRepo("B", dirB)
	if err := g.Reconcile(ctx, store, entity.Ref{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	get := func(kind, id string) entity.Obj {
		e, ok, _ := store.Get(entity.Ref{System: source.GitSystem, Kind: kind, ID: id})
		if !ok {
			t.Fatalf("missing git:%s:%s", kind, id)
		}
		return entity.ToObj(e.Payload)
	}

	// main exists in BOTH repos, no collision, distinct shas + repo fields.
	mainA := get("ref", "A/main")
	mainB := get("ref", "B/main")
	if mainA["sha"] == mainB["sha"] {
		t.Fatal("distinct repos' main should have distinct shas")
	}
	if mainA["repo"] != "A" || mainB["repo"] != "B" || mainA["name"] != "main" {
		t.Fatalf("ref repo/name fields wrong: %+v %+v", mainA, mainB)
	}
	get("ref", "A/feature")
	get("ref", "B/dev")

	// head + commit reflected per repo.
	if h := get("head", "A"); h["ref"] != "main" || h["repo"] != "A" || h["dirty"] != false {
		t.Fatalf("git:head:A = %+v", h)
	}
	if c := get("commit", "A/"+shaA); c["msg"] != "first on A" || c["repo"] != "A" || c["sha"] != shaA {
		t.Fatalf("git:commit:A/%s = %+v", shaA, c)
	}
	// a commit is repo-scoped, not a global sha key.
	if _, ok, _ := store.Get(entity.Ref{System: source.GitSystem, Kind: "commit", ID: shaA}); ok {
		t.Fatal("commit should be keyed git:commit:<repo>/<sha>, not by bare sha")
	}

	// Delete A's feature branch; reconcile only A → A/feature tombstones, B untouched.
	gitRun(t, dirA, "branch", "-D", "feature")
	if err := g.Reconcile(ctx, store, entity.Ref{System: source.GitSystem, Kind: "head", ID: "A"}); err != nil {
		t.Fatalf("targeted reconcile: %v", err)
	}
	if _, ok, _ := store.Get(entity.Ref{System: source.GitSystem, Kind: "ref", ID: "A/feature"}); ok {
		t.Fatal("A/feature should have tombstoned after branch delete")
	}
	get("ref", "B/dev") // B's slice untouched by A's targeted reconcile

	// A new commit on A/main advances its sha (a change a trigger would see).
	before := get("ref", "A/main")["sha"]
	commit(t, dirA, "a.txt", "hello A v2", "second on A")
	if err := g.Reconcile(ctx, store, entity.Ref{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if after := get("ref", "A/main")["sha"]; after == before {
		t.Fatal("A/main sha should have advanced after a new commit")
	}
}
