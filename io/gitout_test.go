package io_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/versed"
)

// refSha is what a ref stands at, or "" when there is no such ref.
func refSha(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "-q", rev)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// A configured path's tree sha rides on every ref where the path exists: the identity of the
// directory's contents, the same across refs that differ only elsewhere, and absent before the
// directory was.
func TestGitReflectsTheTreeOfAConfiguredPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	first := commit(t, dir, "README", "hello", "start")
	gitRun(t, dir, "tag", "before")
	if err := os.MkdirAll(filepath.Join(dir, "charters"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "charters/a.tony", "rules: []", "charters")
	gitRun(t, dir, "branch", "other")
	commit(t, dir, "README", "hello again", "unrelated")

	g := verseio.NewGit()
	g.AddRepo("r", dir)
	if err := g.AddTree("charters/"); err != nil {
		t.Fatal(err)
	}
	rn, err := g.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]entity.Payload{}
	for _, e := range rn.Entities {
		if segs := e.Ref.Segs(); len(segs) == 4 && segs[2] == "ref" {
			refs[segs[3]] = e.Payload
		}
	}
	want := refSha(t, dir, "main:charters")
	tree := func(ref string) string { return entity.Str(entity.Get(refs[ref], "tree"), "charters") }
	if want == "" || tree("main") != want || tree("other") != want {
		t.Errorf("tree.charters on main = %q, other = %q, want %q on both", tree("main"), tree("other"), want)
	}
	if entity.Get(refs["before"], "tree") != nil {
		t.Errorf("a ref before the directory existed carries a tree: %v", refs["before"])
	}
	if entity.Str(refs["main"], "sha") == first {
		t.Fatal("main did not move")
	}
}

type gitOutRig struct {
	store entity.Store
	out   *verseio.Out
	verse *client.Client
	id    string
}

func newGitOutRig(t *testing.T, g *verseio.Git, id string) *gitOutRig {
	t.Helper()
	store := entitytest.Store(t)
	ts := httptest.NewServer(versed.NewWithStore(versed.ScriptedBuilder, store).Handler())
	t.Cleanup(ts.Close)
	verse := client.New(strings.TrimPrefix(ts.URL, "http://"), "agent:git-io")
	sink := g.Sink()
	sink.Verse = verse // an install input installs into the rig's verse, as the io
	return &gitOutRig{store: store, id: id, verse: verse, out: verseio.NewOut(sink, g.OutRef(id), verse, nil)}
}

func (r *gitOutRig) pass(t *testing.T) {
	t.Helper()
	if err := r.out.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
}

func (r *gitOutRig) ask(t *testing.T, name string, in entity.Obj) {
	t.Helper()
	entitytest.Set(t, r.store, entity.RefAt("git", r.id, "out", name), in.Payload())
}

func (r *gitOutRig) status(t *testing.T, name string) entity.Obj {
	t.Helper()
	e, ok, err := r.store.Get(entity.RefAt("git", r.id, "out", name+verseio.StatusSuffix))
	if err != nil || !ok {
		return nil
	}
	return entity.ToObj(e.Payload)
}

// The out half writes a ref where it reads: a tag made once and never moved, a branch moved
// forward and back with what it stood at recorded, a sha the repository lacks refused for good.
func TestTheGitOutHalfTagsAndMovesABranchInARepositoryOnDisk(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	one := commit(t, dir, "a", "1", "one")
	two := commit(t, dir, "a", "2", "two")
	g := verseio.NewGit()
	g.AddRepo("r", dir)
	r := newGitOutRig(t, g, "r")

	r.ask(t, "live", entity.Obj{"to": "tag.live-verse.1", "sha": two[:10]})
	r.pass(t)
	if got := refSha(t, dir, "refs/tags/live-verse.1"); got != two {
		t.Fatalf("the tag stands at %q, want %s", got, two)
	}
	st := r.status(t, "live")
	if st["ref"] != "refs/tags/live-verse.1" || st["sha"] != two || st["at"] == nil || st["error"] != nil {
		t.Fatalf("status = %v", st)
	}
	// In step: nothing written.
	before := st["at"]
	r.pass(t)
	if st := r.status(t, "live"); st["at"] != before {
		t.Errorf("a pass with nothing to do rewrote the status: %v", st)
	}
	// A tag is not moved.
	r.ask(t, "live", entity.Obj{"to": "tag.live-verse.1", "sha": one})
	r.pass(t)
	if st := r.status(t, "live"); st["failed"] != verseio.PermanentFailure || !strings.Contains(fmt.Sprint(st["error"]), "not moved") {
		t.Errorf("moving a tag: status %v, want a permanent refusal", st)
	}
	if got := refSha(t, dir, "refs/tags/live-verse.1"); got != two {
		t.Errorf("the tag moved to %q", got)
	}

	// A branch moves, forward and back, and says what it stood at.
	r.ask(t, "head", entity.Obj{"to": "branch.verse/live", "sha": one})
	r.pass(t)
	if got := refSha(t, dir, "refs/heads/verse/live"); got != one {
		t.Fatalf("verse/live stands at %q, want %s", got, one)
	}
	if st := r.status(t, "head"); st["was"] != nil || st["sha"] != one {
		t.Errorf("status of a new branch = %v", st)
	}
	r.ask(t, "head", entity.Obj{"to": "branch.verse/live", "sha": two})
	r.pass(t)
	if got := refSha(t, dir, "refs/heads/verse/live"); got != two {
		t.Fatalf("verse/live stands at %q after a move, want %s", got, two)
	}
	if st := r.status(t, "head"); st["was"] != one || st["sha"] != two {
		t.Errorf("status after a move = %v", st)
	}
	r.ask(t, "head", entity.Obj{"to": "branch.verse/live", "sha": one})
	r.pass(t)
	if got := refSha(t, dir, "refs/heads/verse/live"); got != one {
		t.Errorf("verse/live did not move back: %q", got)
	}
	// Create only: a branch that stands is kept where it is, and said so once; one that does
	// not is made.
	r.ask(t, "kept", entity.Obj{"to": "branch.verse/live", "sha": two, "create": true})
	r.ask(t, "made", entity.Obj{"to": "branch.verse/new", "sha": two, "create": true})
	r.pass(t)
	if got := refSha(t, dir, "refs/heads/verse/live"); got != one {
		t.Errorf("create: true moved a branch that stood: %q", got)
	}
	if st := r.status(t, "kept"); st["kept"] != true || st["sha"] != one {
		t.Errorf("status of a kept branch = %v", st)
	}
	if got := refSha(t, dir, "refs/heads/verse/new"); got != two {
		t.Errorf("create: true did not make an absent branch: %q", got)
	}

	// Refused for good: a sha the repository lacks, a target that is neither kind, no sha.
	for name, in := range map[string]entity.Obj{
		"nosuch":  {"to": "tag.x", "sha": "0123456789abcdef0123456789abcdef01234567"},
		"neither": {"to": "pr.4", "sha": one},
		"nosha":   {"to": "tag.y"},
	} {
		r.ask(t, name, in)
	}
	r.pass(t)
	for _, name := range []string{"nosuch", "neither", "nosha"} {
		if st := r.status(t, name); st["failed"] != verseio.PermanentFailure {
			t.Errorf("%s: status %v, want a permanent refusal", name, st)
		}
	}
	if refSha(t, dir, "refs/tags/x") != "" || refSha(t, dir, "refs/tags/y") != "" {
		t.Error("a refused input wrote a ref")
	}
}

// A mirror pushes the ref to the remote it fetches from, with a lease on what the ref was, and
// keeps its own copy so the next reflection sees it.
func TestTheGitOutHalfPushesFromAMirror(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	work := t.TempDir()
	initRepo(t, work)
	one := commit(t, work, "a", "1", "one")
	two := commit(t, work, "a", "2", "two")
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitRun(t, work, "clone", "--bare", work, remote)

	g := verseio.NewGit()
	if _, err := g.AddMirror(context.Background(), "m", remote, filepath.Join(t.TempDir(), "m")); err != nil {
		t.Fatal(err)
	}
	r := newGitOutRig(t, g, "m")
	r.ask(t, "live", entity.Obj{"to": "tag.live-verse.1", "sha": two})
	r.ask(t, "head", entity.Obj{"to": "branch.verse/live", "sha": one})
	r.pass(t)
	if got := refSha(t, remote, "refs/tags/live-verse.1"); got != two {
		t.Errorf("the remote's tag stands at %q, want %s", got, two)
	}
	if got := refSha(t, remote, "refs/heads/verse/live"); got != one {
		t.Errorf("the remote's verse/live stands at %q, want %s", got, one)
	}
	// The next reflection carries both, from the mirror's own copy or the fetch.
	rn, err := g.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, e := range rn.Entities {
		if segs := e.Ref.Segs(); len(segs) == 4 && segs[2] == "ref" {
			seen[segs[3]] = entity.Str(e.Payload, "sha")
		}
	}
	if seen["live-verse.1"] != two || seen["verse/live"] != one {
		t.Errorf("reflected refs after the push: %v", seen)
	}

	// Somebody moved the branch on the remote meanwhile: the lease refuses the overwrite, and
	// the failure stands on the status until the next pass sees the new sha.
	three := commit(t, work, "a", "3", "three")
	gitRun(t, work, "push", remote, three+":refs/heads/verse/live")
	r.ask(t, "head", entity.Obj{"to": "branch.verse/live", "sha": two})
	r.pass(t)
	if got := refSha(t, remote, "refs/heads/verse/live"); got != three {
		t.Errorf("a stale lease overwrote the remote: %q, want %s", got, three)
	}
	if st := r.status(t, "head"); st["error"] == nil || st["failed"] == verseio.PermanentFailure {
		t.Errorf("status after a refused lease = %v, want a retryable error", st)
	}
}
