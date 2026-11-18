package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// A mirror's refs stay refs git reads, once git has packed them.
//
// This one runs the git binary, because the property is the two agreeing: go-git writes the
// mirror's refs, git packs them with a peel line under each annotated tag, and git is what reads
// them at the next push into the mirror. A fetch that removes a ref from under its peel line
// leaves a file go-git reads and git refuses.
func TestAMirrorsRefsStayReadableByGitOnceTheyArePacked(t *testing.T) {
	origin := initRepo(t)
	or, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	main, err := or.Reference("refs/heads/main", true)
	if err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@invalid", When: time.Now()}
	for _, tag := range []string{"v1", "v2"} {
		if _, err := or.CreateTag(tag, main.Hash(), &git.CreateTagOptions{Tagger: sig, Message: tag}); err != nil {
			t.Fatal(err)
		}
	}
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	dir, err := m.Ensure(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	gitIn := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	refs := func() string {
		t.Helper()
		return strings.Join(strings.Fields(gitIn("for-each-ref", "--format=%(refname)")), " ")
	}
	loose := func() (names []string) {
		t.Helper()
		for _, d := range []string{"refs/heads", "refs/tags"} {
			es, err := os.ReadDir(filepath.Join(dir, d))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			for _, e := range es {
				names = append(names, d+"/"+e.Name())
			}
		}
		return names
	}

	// A push into the mirror must not start a collection that outlives it.
	if got := strings.TrimSpace(gitIn("config", "gc.auto")) + " " + strings.TrimSpace(gitIn("config", "receive.autogc")); got != "0 false" {
		t.Fatalf("gc.auto, receive.autogc = %q, want them 0 and false", got)
	}

	// A fetch that finds nothing moved leaves the refs where they are, packed.
	gitIn("pack-refs", "--all")
	if _, err := m.Ensure(context.Background(), origin); err != nil {
		t.Fatal(err)
	}
	if got := loose(); len(got) != 0 {
		t.Errorf("a fetch that found nothing moved rewrote %v", got)
	}
	if got, want := refs(), "refs/heads/main refs/tags/v1 refs/tags/v2"; got != want {
		t.Fatalf("the mirror's refs, as git reads them: %q, want %q", got, want)
	}

	// A tag removed upstream goes whole, peel line and all, and alone.
	gitIn("pack-refs", "--all")
	if err := or.DeleteTag("v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), origin); err != nil {
		t.Fatal(err)
	}
	if got, want := refs(), "refs/heads/main refs/tags/v2"; got != want {
		t.Fatalf("the mirror's refs, as git reads them: %q, want %q", got, want)
	}
	packed, err := os.ReadFile(filepath.Join(dir, "packed-refs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(packed), "^") {
		t.Errorf("packed-refs holds peel lines:\n%s", packed)
	}
}
