package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/signadot/verse/runner"
)

// A checkout has the mirror's tags as a clone would: `refs/tags/<name>` for each, an annotated
// tag's ref its tag object, so `git describe --tags` and `git log <tag>..` answer. The objects
// were always there, linked in with the mirror's; without the refs, every annotated tag object
// sat in the store with nothing naming it (652tv83sh12ksdtfwdn0).
func TestACheckoutHasTheMirrorsTags(t *testing.T) {
	origin := initRepo(t)
	seed := tagOrigin(t, origin, "v1", "lite")
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Tree("svc")
	// Read through a fresh handle, as the task's git does.
	repo, err := git.PlainOpen(tree.Dir)
	if err != nil {
		t.Fatal(err)
	}
	annotated, err := repo.Tag("v1")
	if err != nil {
		t.Fatalf("refs/tags/v1 is missing from the checkout: %v", err)
	}
	tag, err := repo.TagObject(annotated.Hash())
	if err != nil {
		t.Fatalf("v1 is annotated in the origin, and the checkout's ref is not a tag object: %v", err)
	}
	if tag.Target.String() != seed {
		t.Errorf("v1 points at %s, want %s", tag.Target, seed)
	}
	if lite, err := repo.Tag("lite"); err != nil || lite.Hash().String() != seed {
		t.Errorf("lightweight tag lite = %v, %v; want %s", lite, err, seed)
	}
	// Packed, as a clone has them: one write at the launch, not a file per tag.
	if loose, err := os.ReadDir(filepath.Join(tree.Dir, ".git", "refs", "tags")); err == nil && len(loose) != 0 {
		t.Errorf("a fresh checkout has %d loose tag refs, want them all in packed-refs", len(loose))
	}

	// A tag made after the launch arrives with a refresh, and is reported beside the branches.
	moved := advanceOrigin(t, origin, "moved")
	tagOrigin(t, origin, "v2")
	mirror, err := m.Ensure(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tree.Refresh(mirror)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := tree.repo.Tag("v2")
	if err != nil {
		t.Fatalf("refs/tags/v2 did not arrive with the refresh: %v", err)
	}
	want := []string{
		"mirror/main " + ShortSha(seed) + ".." + ShortSha(moved),
		"tags/v2 new.." + ShortSha(v2.Hash().String()),
	}
	if !slices.Equal(got, want) {
		t.Errorf("refresh reported %v, want %v", got, want)
	}
	// And nothing is said to have moved when nothing did.
	if got, err := tree.Refresh(mirror); err != nil || len(got) != 0 {
		t.Errorf("a second refresh reported %v, %v; want nothing", got, err)
	}
}

// tagOrigin tags the origin's main at its tip: the first name annotated, any others lightweight.
// It returns the commit tagged.
func tagOrigin(t *testing.T, origin string, names ...string) string {
	t.Helper()
	work := t.TempDir()
	wr, err := git.PlainClone(work, false, &git.CloneOptions{URL: origin, ReferenceName: "refs/heads/main"})
	if err != nil {
		t.Fatalf("clone origin: %v", err)
	}
	head, err := wr.Head()
	if err != nil {
		t.Fatal(err)
	}
	var specs []config.RefSpec
	for i, name := range names {
		var opts *git.CreateTagOptions
		if i == 0 {
			opts = &git.CreateTagOptions{Tagger: &object.Signature{Name: "t", Email: "t@invalid", When: time.Now()}, Message: name}
		}
		if _, err := wr.CreateTag(name, head.Hash(), opts); err != nil {
			t.Fatalf("tag %s: %v", name, err)
		}
		specs = append(specs, config.RefSpec("refs/tags/"+name+":refs/tags/"+name))
	}
	if err := wr.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: specs}); err != nil {
		t.Fatalf("push tags: %v", err)
	}
	return head.Hash().String()
}
