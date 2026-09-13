package source_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/source"
)

// A COMMIT'S PARENTS ARE A LIST, which is what they are: none for a root commit, two for a
// merge. git's `%P` is a space-separated string and that is what this used to store — a set
// written as text, which no operator over lists can read. `!key()` names an element of a list
// in a match and in a patch alike, so the shape is what decides whether "did this commit land
// on top of that one" is a predicate a charter can write or a substring test it has to fake.
func TestCommitParentsAreAList(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	root := commit(t, dir, "a.txt", "one", "root")
	second := commit(t, dir, "a.txt", "two", "second")

	// A merge, for the case the string shape could never carry: two parents, first-parent
	// first, which is the order git reports and the order a reader of the payload expects.
	gitRun(t, dir, "checkout", "-b", "side", root)
	side := commit(t, dir, "b.txt", "side", "on the side")
	gitRun(t, dir, "checkout", "main")
	gitRun(t, dir, "merge", "--no-ff", "-m", "merge side", "side")
	mergeSha := revParse(t, dir, "HEAD")

	store := entitytest.Store(t)
	g := source.NewGit()
	g.AddRepo("R", dir)
	if err := source.NewReflector(g, store, 0).Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	parentsOf := func(sha string) *ir.Node {
		e, ok, _ := store.Get(entity.RefAt(source.GitSystem, "R", "commit", sha))
		if !ok {
			t.Fatalf("missing git.R.commit.%s", sha)
		}
		p := ir.Get(e.Payload, "parents")
		if p == nil {
			t.Fatalf("git.R.commit.%s carries no parents at all", sha)
		}
		return p
	}

	for _, c := range []struct {
		what string
		sha  string
		want []string
	}{
		{"a root commit has none", root, nil},
		{"one parent is a list of one", second, []string{root}},
		{"a merge names both, first parent first", mergeSha, []string{second, side}},
	} {
		got := parentsOf(c.sha)
		if got.Type != ir.ArrayType {
			t.Errorf("%s: parents is %s, not a list", c.what, got.Type)
			continue
		}
		if len(got.Values) != len(c.want) {
			t.Errorf("%s: parents has %d elements, want %d", c.what, len(got.Values), len(c.want))
			continue
		}
		for i, want := range c.want {
			if got.Values[i].String != want {
				t.Errorf("%s: parents[%d] = %q, want %q", c.what, i, got.Values[i].String, want)
			}
		}
	}

	// WHY IT IS A LIST, asked the way a charter asks it: `!key()` treats a list as a set keyed
	// by the element itself, so "this commit is a child of that one" is one predicate over the
	// payload — and it answers no for a sha that is merely a parent of something else.
	match := func(sha, of string) bool {
		t.Helper()
		pat, err := codec.ParseNode([]byte("{parents: !key() ["+sha+"]}"), codec.Tony)
		if err != nil {
			t.Fatalf("pattern: %v", err)
		}
		e, _, _ := store.Get(entity.RefAt(source.GitSystem, "R", "commit", of))
		return (entity.Pattern{Predicate: pat}).MatchEvent(entity.Event{
			Ref: entity.RefAt(source.GitSystem, "R", "commit", of), Op: entity.Set, To: e.Payload,
		})
	}
	if !match(second, mergeSha) || !match(side, mergeSha) {
		t.Error("!key() did not find a parent of the merge commit")
	}
	if match(root, mergeSha) {
		t.Error("!key() found the grandparent among the merge commit's parents")
	}
}

func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", rev)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", rev, err)
	}
	return string(out[:len(out)-1])
}
