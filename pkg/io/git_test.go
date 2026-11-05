package io_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/signadot/tony-format/git-issue/issuelib"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	verseio "github.com/signadot/verse/pkg/io"
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

	g := verseio.NewGit()
	g.AddRepo("A", dirA)
	g.AddRepo("B", dirB)
	// Driven through the Reflector, which is where converge lives: the source answers with a
	// Reflection and verse writes it, so the test exercises the path Run and a hint take.
	rf := verseio.NewReflector(g, store, 0)
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	get := func(segs ...string) entity.Obj {
		ref := entity.RefAt(append([]string{verseio.GitRoot}, segs...)...)
		e, ok, _ := store.Get(ref)
		if !ok {
			t.Fatalf("missing %s", ref)
		}
		return entity.ToObj(e.Payload)
	}

	// main exists in BOTH repos, no collision, distinct shas + repo fields.
	mainA := get("A", "ref", "main")
	mainB := get("B", "ref", "main")
	if mainA["sha"] == mainB["sha"] {
		t.Fatal("distinct repos' main should have distinct shas")
	}
	if mainA["repo"] != "A" || mainB["repo"] != "B" || mainA["name"] != "main" {
		t.Fatalf("ref repo/name fields wrong: %+v %+v", mainA, mainB)
	}
	get("A", "ref", "feature")
	get("B", "ref", "dev")

	// head + commit reflected per repo.
	if h := get("A", "head"); h["ref"] != "main" || h["repo"] != "A" || h["dirty"] != false {
		t.Fatalf("git.A.head = %+v", h)
	}
	if c := get("A", "commit", shaA); c["msg"] != "first on A" || c["repo"] != "A" || c["sha"] != shaA {
		t.Fatalf("git.A.commit.%s = %+v", shaA, c)
	}
	// a commit is repo-scoped, not a global sha key.
	if _, ok, _ := store.Get(entity.RefAt(verseio.GitRoot, "commit", shaA)); ok {
		t.Fatal("a commit should stand under its repository, git.<repo>.commit.<sha>, not by bare sha")
	}

	// Delete A's feature branch; reconcile only A → A/feature tombstones, B untouched.
	gitRun(t, dirA, "branch", "-D", "feature")
	if err := rf.Reconcile(ctx, g.RepoRef("A")); err != nil {
		t.Fatalf("targeted reconcile: %v", err)
	}
	if _, ok, _ := store.Get(entity.RefAt(verseio.GitRoot, "A", "ref", "feature")); ok {
		t.Fatal("A/feature should have tombstoned after branch delete")
	}
	get("B", "ref", "dev") // B's slice untouched by A's targeted reconcile

	// A new commit on A/main advances its sha (a change a trigger would see).
	before := get("A", "ref", "main")["sha"]
	commit(t, dirA, "a.txt", "hello A v2", "second on A")
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if after := get("A", "ref", "main")["sha"]; after == before {
		t.Fatal("A/main sha should have advanced after a new commit")
	}
}

// A MIRROR is how a git source reflects a repository it does not sit next to — the deployed
// case, where the pod has no repositories until it makes one. The clone happens at startup and
// every pass fetches, so what the source answers tracks the remote rather than the copy.
func TestGitMirrorClonesAtStartupAndFetchesOnEveryPass(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	store := entitytest.Store(t)

	origin := t.TempDir()
	initRepo(t, origin)
	first := commit(t, origin, "a.txt", "one", "first")

	// The clone is AddMirror's, into a directory that does not exist yet — which is what a
	// fresh pod's volume looks like.
	into := filepath.Join(t.TempDir(), "mirrors", "origin")
	g := verseio.NewGit()
	id, err := g.AddMirror(ctx, "", origin, into)
	if err != nil {
		t.Fatalf("AddMirror: %v", err)
	}
	if id != filepath.Base(origin) {
		t.Fatalf("id %q, want the remote's base name %q", id, filepath.Base(origin))
	}

	rf := verseio.NewReflector(g, store, 0)
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	get := func(segs ...string) entity.Obj {
		ref := entity.RefAt(append([]string{verseio.GitRoot}, segs...)...)
		e, ok, _ := store.Get(ref)
		if !ok {
			t.Fatalf("missing %s", ref)
		}
		return entity.ToObj(e.Payload)
	}
	if sha := get(id, "ref", "main")["sha"]; sha != first {
		t.Fatalf("main is %v, want the remote's %s", sha, first)
	}

	// A commit lands on the remote. Nothing touched the mirror — the next pass's fetch is what
	// has to see it, and a source that reflected its local copy without fetching would answer
	// the old sha forever while looking perfectly healthy.
	second := commit(t, origin, "a.txt", "two", "second")
	if err := rf.Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("reconcile after remote commit: %v", err)
	}
	if sha := get(id, "ref", "main")["sha"]; sha != second {
		t.Fatalf("main is %v after a remote commit, want %s — the pass did not fetch", sha, second)
	}

	// Re-adding the same mirror does not re-clone: the directory already holds one, which is
	// what makes a restart on a persistent volume cheap.
	if _, err := g.AddMirror(ctx, id, origin, into); err != nil {
		t.Fatalf("re-adding an existing mirror: %v", err)
	}
}

// A mirror of a remote that is not there fails AT STARTUP, with git's own words. Stale and
// silent is the worse failure: converge would write nothing, the registration would carry no
// error, and a source whose credential expired would look like a repository where nothing has
// happened.
func TestGitMirrorFailsLoudlyWhenTheRemoteIsNotThere(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	missing := filepath.Join(t.TempDir(), "not-a-repo")
	g := verseio.NewGit()
	_, err := g.AddMirror(context.Background(), "x", missing, filepath.Join(t.TempDir(), "m"))
	if err == nil {
		t.Fatal("cloning a remote that does not exist succeeded")
	}
	// git's stderr, folded in — without it this is "exit status 128", which is the whole of
	// what a failed clone would otherwise write into a registration's lastError.
	if !strings.Contains(err.Error(), "repository") && !strings.Contains(err.Error(), "not") {
		t.Fatalf("error %v carries nothing git said", err)
	}
}

// A REPOSITORY IS NOT ONLY ITS BRANCHES AND TAGS, and the rest of it is somebody else's.
//
// `for-each-ref` with no pattern is EVERY ref, and a mirror clone maps every ref the remote
// has — so mirroring a GitHub repo reflected `refs/pull/<n>/head` and `.../merge` (4739 of
// them on one repo here) and `refs/git-issues/*`, which is git-issue's namespace. Worse than the
// volume: GitHub recreates the `/merge` refs whenever the base branch moves, so every fetch
// after any push wrote a batch of new shas and the store grew on every pass with nothing
// anybody would call activity happening.
func TestGitReflectsBranchesAndTagsAndNothingElse(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	sha := commit(t, dir, "a.txt", "one", "first")
	gitRun(t, dir, "tag", "v1")
	// The two shapes a mirror of a GitHub remote brings with it.
	gitRun(t, dir, "update-ref", "refs/pull/7/head", sha)
	gitRun(t, dir, "update-ref", "refs/pull/7/merge", sha)
	gitRun(t, dir, "update-ref", issuelib.OpenPrefix+"k0d4y1m6", sha)

	g := verseio.NewGit()
	g.AddRepo("r", dir)
	rn, err := g.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, e := range rn.Entities {
		if segs := e.Ref.Segs(); len(segs) == 4 && segs[2] == "ref" {
			refs = append(refs, segs[3])
		}
	}
	sort.Strings(refs)
	want := []string{"main", "v1"}
	if len(refs) != len(want) {
		t.Fatalf("reflected %v, want only the branch and the tag (%v)", refs, want)
	}
	for i, r := range refs {
		if r != want[i] {
			t.Fatalf("reflected %v, want %v", refs, want)
		}
	}
}
