package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// A checkout per task, off a real repository, delivered back to it.
//
// It drives go-git against real repositories rather than mocking it, because everything worth
// getting wrong here is git's opinion and not ours: which ref a checkout starts from, whether a
// second task on the same mirror collides, and whether a push lands where the launch said.
// A fake would agree with whatever this file believed while writing it — which is how the
// mirror HEAD and ref-namespace defects below got in.
func TestCheckoutPerTaskAndDelivery(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin},
	}
	worktrees := t.TempDir()

	ws, err := m.Open(context.Background(), "p1", repos, worktrees, Author{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "svc", "README")); err != nil {
		t.Fatalf("checkout does not carry the base's content under its repo name: %v", err)
	}
	if ws.Branch != "verse/auto/p1" {
		t.Errorf("branch = %q, want verse/auto/p1 — the branch is per task or two tasks share one", ws.Branch)
	}

	// A second task on the same repository must get its own tree and its own branch: the
	// mirror is shared, the work is not.
	ws2, err := m.Open(context.Background(), "p2", repos, worktrees, Author{})
	if err != nil {
		t.Fatalf("second checkout on the same mirror: %v", err)
	}
	if ws2.Root == ws.Root || ws2.Branch == ws.Branch {
		t.Fatalf("two tasks share a workspace: %s/%s vs %s/%s", ws.Root, ws.Branch, ws2.Root, ws2.Branch)
	}

	// What the gate matches on, once the task has touched something.
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "new.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ws.Branch != "verse/auto/p1" || ws.Trees[0].Repo.From != "main" {
		t.Errorf("the workspace is on %s from %s, want the task's branch and its base", ws.Branch, ws.Trees[0].Repo.From)
	}
	if got := ws.Trees[0].Changed(); len(got) != 1 || got[0] != "new.txt" {
		t.Errorf("Changed = %v, want [new.txt]", got)
	}

	l := &runner.Launch{ID: "p1", Provenance: runner.Provenance{
		Principal: "walter", Subject: `demo.pr."1"`, Chain: "review",
	}}
	if _, err := deliver(t, ws, l, "fixed the flaky test\n\ndetails", ""); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	// Staged on the way out and dropped once the far side had it: what is left under
	// refs/pending is exactly the work this runner holds that the world does not.
	if held, err := m.Staged(origin); err != nil || len(held) != 0 {
		t.Errorf("work is still staged after a successful delivery: %v (%v)", held, err)
	}
	// The push landed on the origin, under the ref the task named.
	if out := commitAt(t, origin, "refs/heads/verse/auto/p1"); out == "" {
		t.Fatal("origin has no verse/auto/p1")
	} else {
		// The trailers are the whole attribution channel out of the verse: a push under a
		// shared credential says nothing at the far end about who it was for.
		for _, want := range []string{
			"fixed the flaky test",
			"Verse-Task: p1",
			"Verse-Principal: walter",
			`Verse-Subject: demo.pr."1"`,
			"Verse-Chain: review",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("commit message is missing %q:\n%s", want, out)
			}
		}
	}

	if err := ws.Remove(context.Background()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(ws.Root); !os.IsNotExist(err) {
		t.Errorf("workspace survived removal at %s", ws.Root)
	}
}

// A workspace spans several repositories: one being changed, one only there to be read.
//
// That asymmetry is the reason `push` is per repository rather than per launch. A
// reference repository that could push back is a blast radius nobody asked for, and an agent
// that cannot see it at all is one guessing about the code it is changing against.
func TestMultipleReposOneWorkspace(t *testing.T) {
	svcOrigin, libOrigin := initRepo(t), initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: svcOrigin},
		{Repo: runner.Repo{Repo: "acme/lib", From: "main"}, Remote: libOrigin}, // read-only: no push
	}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, name := range []string{"svc", "lib"} {
		if _, err := os.Stat(filepath.Join(ws.Root, name, "README")); err != nil {
			t.Fatalf("%s is not checked out in the workspace: %v", name, err)
		}
	}
	// The same task branch in both, so a gate matching on branch has one answer.
	for _, tr := range ws.Trees {
		if got := headBranch(t, tr.Dir); got != "verse/auto/p1" {
			t.Errorf("%s is on %q, want verse/auto/p1", tr.Repo.Name(), got)
		}
	}

	// The agent changes both. Only the one with a push target should leave.
	for _, name := range []string{"svc", "lib"} {
		if err := os.WriteFile(filepath.Join(ws.Root, name, "new.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tr := range ws.Trees {
		if got := tr.Changed(); len(got) != 1 || got[0] != "new.txt" {
			t.Errorf("%s: Changed = %v, want [new.txt]", tr.Repo.Name(), got)
		}
	}

	if _, err := deliver(t, ws, &runner.Launch{ID: "p1"}, "did the thing", ""); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if commitAt(t, svcOrigin, "refs/heads/verse/auto/p1") == "" {
		t.Fatal("the repository with a destination did not deliver")
	}
	if commitAt(t, libOrigin, "refs/heads/verse/auto/p1") != "" {
		t.Error("the reference repository received a push it never declared a target for")
	}
}

// A launch refreshes the mirror, and refreshing it must not disturb tasks already running.
//
// This guards a failure that was real and was quiet. The first version of this runner used
// linked worktrees off a shared mirror, so task branches lived in the MIRROR's ref namespace —
// and `fetch --prune` on the next launch deleted every `verse/auto/*`, because none of them exists
// upstream. Nothing errored; a running worktree was simply left on a ref that was gone, its
// whole tree read as untracked, and its work could not be committed.
//
// A clone per task makes it structurally impossible: each task's refs live inside its own
// repository, and the mirror holds upstream refs only. The test stays anyway — the property
// ("a later launch does not disturb an earlier one") is what matters, and the next person to
// reach for shared state should have to make it fail.
func TestFetchDoesNotPruneRunningTasksBranches(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin},
	}
	worktrees := t.TempDir()

	ws, err := m.Open(context.Background(), "p1", repos, worktrees, Author{})
	if err != nil {
		t.Fatal(err)
	}
	// A second launch: this is what re-fetches the mirror under the first task's feet.
	if _, err := m.Open(context.Background(), "p2", repos, worktrees, Author{}); err != nil {
		t.Fatal(err)
	}
	if got := headBranch(t, ws.Trees[0].Dir); got != "verse/auto/p1" {
		t.Fatalf("first task's HEAD = %q — a second launch disturbed its branch", got)
	}
	if clean, st := isClean(t, ws.Trees[0].Dir); !clean {
		t.Fatalf("first task's tree is no longer clean after a second launch:\n%s", st)
	}
}

// Nothing leaves a repository the launch gives no push, and that is a configuration rather
// than a failure — but it must not look like a delivery.
func TestNoPushTargetDeliversNothing(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := deliver(t, ws, &runner.Launch{ID: "p1"}, "done", "")
	if err != nil || len(d.Staged) != 0 || len(d.Loose) != 0 {
		t.Fatalf("delivery = %+v (%v) — a read-only repository pushes nowhere and stages nothing", d, err)
	}
}

// A task cannot redirect its own delivery.
//
// This is a real defect that shipped in the first cut, and the shape of it is worth keeping in
// front of whoever changes Deliver. `.git/config` lives inside the workspace — the one place the
// agent is supposed to be able to write — and go-git resolves a RemoteName through
// Repository.Config() at PUSH time, re-reading that file. So a push by name went wherever the
// agent last wrote, carrying this runner's credential to a host of the agent's choosing.
//
// The fix is that delivery pins an in-memory remote built from the configured url. The test
// writes the attack rather than describing it: if this fails, an agent can exfiltrate the token.
func TestTaskCannotRedirectItsOwnPush(t *testing.T) {
	intended, attacker := initRepo(t), initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: intended},
	}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	// The agent, doing what an agent can do: writing files in its own working directory.
	cfgPath := filepath.Join(ws.Trees[0].Dir, ".git", "config")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), intended) {
		t.Skipf("the task repo does not name the configured remote in .git/config; nothing to redirect")
	}
	if err := os.WriteFile(cfgPath, []byte(strings.ReplaceAll(string(b), intended, attacker)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Trees[0].Dir, "payload.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := deliver(t, ws, &runner.Launch{ID: "p1"}, "work", ""); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if commitAt(t, attacker, "refs/heads/verse/auto/p1") != "" {
		t.Fatal("the task redirected its own push by editing .git/config — the runner's " +
			"credential goes wherever the agent points it")
	}
	if commitAt(t, intended, "refs/heads/verse/auto/p1") == "" {
		t.Fatal("the push did not reach the remote the launch named")
	}
}

// A task id becomes a directory name and a ref component, and it is chosen by whoever launches —
// including a client posting straight at this runner. `.` and `..` are the ids that matter:
// `filepath.Join(root, "..")` is the parent of every workspace, and Remove does os.RemoveAll on
// whatever this produced.
func TestTaskIDCannotEscapeTheWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"..", ".", "../..", "a/../../b", ""} {
		seg := SafeSeg(id)
		if seg == "." || seg == ".." || seg == "" || strings.ContainsAny(seg, `/\`) {
			t.Errorf("SafeSeg(%q) = %q — usable as a traversal", id, seg)
		}
		got := filepath.Join(root, seg)
		if rel, err := filepath.Rel(root, got); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("id %q lands at %s, outside %s", id, got, root)
		}
	}
	// Lossy sanitizing must not collide: two ids in one workspace is two tasks in one checkout.
	if SafeSeg("a.b") == SafeSeg("a_b") {
		t.Errorf("a.b and a_b share a workspace (%q)", SafeSeg("a.b"))
	}
	// An id that needs no rewriting keeps its name, so the ordinary case stays readable.
	if got := SafeSeg("run-8f306077b1c2"); got != "run-8f306077b1c2" {
		t.Errorf("safeSeg mangled a clean id: %q", got)
	}
}

// The agent is told what its working directory contains, because the layout is this runner's
// convention and nothing else can tell it.
func TestWorkspaceBriefNamesTheRepos(t *testing.T) {
	svc, lib := initRepo(t), initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: svc},
		{Repo: runner.Repo{Repo: "acme/lib", From: "main"}, Remote: lib},
	}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	b := ws.Brief()
	for _, want := range []string{"svc/", "lib/", "2 repositories", "acme/svc at main", ws.Branch} {
		if !strings.Contains(b, want) {
			t.Errorf("brief is missing %q:\n%s", want, b)
		}
	}
}

// A launch's repo becomes a workspace's: fetched from the forge the runner names, checked out
// under its name, and what the launch did not say is left for the mirror to answer.
func TestARepoIsBuiltFromTheLaunchs(t *testing.T) {
	repos, err := FromLaunch([]runner.Repo{
		{Repo: "acme/app", From: "release"},
		{Repo: "acme/docs"},
	}, "https://github.com/")
	if err != nil {
		t.Fatal(err)
	}
	if r := repos[0]; r.Name() != "app" || r.Remote != "https://github.com/acme/app" || r.From != "release" {
		t.Errorf("acme/app became %+v", r)
	}
	if r := repos[1]; r.Name() != "docs" || r.Remote != "https://github.com/acme/docs" || r.From != "" {
		t.Errorf("acme/docs became %+v", r)
	}
	// A forge on this filesystem, as the tests and a runner without a network use.
	repos, err = FromLaunch([]runner.Repo{{Repo: "acme/app"}}, "/srv/forge")
	if err != nil || repos[0].Remote != "/srv/forge/acme/app" {
		t.Errorf("a local forge: %+v, %v", repos, err)
	}
	// What a launch's check refuses is refused here too (two repositories in one directory are
	// the environment's to refuse).
	if _, err := FromLaunch([]runner.Repo{{Repo: "acme/app.git"}}, "https://github.com"); err == nil {
		t.Error("a repository named with .git was checked out")
	}
}

// deliver drives the three steps a push is, in one process — commit, stage, push — and returns
// what the runner's half did and what the push said. An empty branch is the task's own.
//
// The production path runs those three across a process boundary: the TASK commits and asks,
// the RUNNER checks the branch against the allowance and stages when it releases the credential
// (Workspace.Stage), and the task pushes with what it was given. This helper is the same calls
// with the boundary taken out, which is what lets a test drive git against real repositories
// without a child process.
func deliver(t *testing.T, ws *Workspace, l *runner.Launch, result, branch string) (Delivery, error) {
	t.Helper()
	if len(ws.Trees) == 0 {
		return Delivery{}, nil
	}
	tree := ws.Trees[0]
	if branch == "" {
		branch = ws.Branch
	}
	target, err := runner.BranchRef(branch)
	if err != nil {
		return Delivery{}, err
	}
	force := false
	work, err := tree.CommitWork(l, result)
	if err != nil {
		return Delivery{Err: err, Loose: []string{tree.Repo.Name()}}, err
	}
	if !work {
		return Delivery{}, nil
	}
	d := ws.Stage(context.Background(), l.ID, tree, target, force, tipOf(t, tree))
	if d.Err != nil {
		return d, d.Err
	}
	// The runner pushes the staged ref from its mirror, which is the same act the sweep performs
	// for anything that did not land.
	why, err := ws.mirrors.Deliver(context.Background(), tree.Repo.Remote, d.Staged[0], target, force, nil)
	if err != nil {
		return d, err
	}
	if why != "" {
		return d, fmt.Errorf("refused: %s", why)
	}
	return Delivery{}, nil
}

// initRepo makes a bare repository with one commit on `main`, ready to be cloned from and
// pushed to. Pure go-git, like the code under test: a test that shelled out would be checking
// that the git binary agrees with go-git rather than that this program works.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "origin.git")
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	// Seed it through a scratch working clone, since a bare repository has no worktree to
	// commit in.
	work := t.TempDir()
	wr, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wr.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	wt, err := wr.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@invalid", When: time.Now()}
	if _, err := wt.Commit("seed", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
	head, err := wr.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := wr.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{
		config.RefSpec("+" + head.Name().String() + ":refs/heads/main"),
	}}); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	return dir
}

// advanceOrigin puts one more commit on origin's main, from a scratch clone, and returns its sha:
// upstream moving under a task that has already started.
func advanceOrigin(t *testing.T, origin, msg string) string {
	t.Helper()
	work := t.TempDir()
	wr, err := git.PlainClone(work, false, &git.CloneOptions{URL: origin, ReferenceName: "refs/heads/main"})
	if err != nil {
		t.Fatalf("clone origin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, msg+".txt"), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, err := wr.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "t", Email: "t@invalid", When: time.Now()}
	h, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	if err := wr.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"refs/heads/main:refs/heads/main"}}); err != nil {
		t.Fatalf("push to origin: %v", err)
	}
	return h.String()
}

// A task's checkout sees upstream as it was at launch. A refresh fetches the mirror and moves the
// checkout's `mirror/*` refs — with the objects behind them — and says what moved; the task's own
// branch does not move.
func TestARefreshBringsTheCheckoutUpToUpstream(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Tree("svc")
	before, _ := tree.repo.Reference(plumbing.NewRemoteReferenceName("mirror", "main"), true)

	moved := advanceOrigin(t, origin, "moved")

	mirror, err := m.Ensure(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tree.Refresh(mirror)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mirror/main " + ShortSha(before.Hash().String()) + ".." + ShortSha(moved)}
	if !slices.Equal(got, want) {
		t.Errorf("refresh reported %v, want %v", got, want)
	}
	after, err := tree.repo.Reference(plumbing.NewRemoteReferenceName("mirror", "main"), true)
	if err != nil || after.Hash().String() != moved {
		t.Errorf("mirror/main = %v, %v; want %s", after, err, moved)
	}
	// Read through a fresh handle, as the task's git does: a go-git repository opened before the
	// fetch has already listed the packs it knows.
	fresh, err := git.PlainOpen(tree.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.CommitObject(plumbing.NewHash(moved)); err != nil {
		t.Errorf("the checkout does not have the fetched commit's objects: %v", err)
	}
	head, _ := tree.repo.Head()
	if head.Hash().String() != tree.Head {
		t.Errorf("the task's own branch moved to %s", head.Hash())
	}
	// Again, with nothing new: nothing moves and nothing is said to have.
	if got, err := tree.Refresh(mirror); err != nil || len(got) != 0 {
		t.Errorf("a second refresh reported %v, %v; want nothing", got, err)
	}
}

// headBranch is the branch a checkout is on, and lastCommit is the message at a ref — the two
// things these tests ask git about.
func headBranch(t *testing.T, dir string) string {
	t.Helper()
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	h, err := r.Head()
	if err != nil {
		t.Fatalf("head %s: %v", dir, err)
	}
	return h.Name().Short()
}

func isClean(t *testing.T, dir string) (bool, string) {
	t.Helper()
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree %s: %v", dir, err)
	}
	st, err := wt.Status()
	if err != nil {
		t.Fatalf("status %s: %v", dir, err)
	}
	return st.IsClean(), st.String()
}

// commitAt returns the commit message at a ref, or "" if the ref does not exist.
func commitAt(t *testing.T, repoDir, ref string) string {
	t.Helper()
	r, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("open %s: %v", repoDir, err)
	}
	h, err := r.Reference(plumbing.ReferenceName(ref), true)
	if err != nil {
		return ""
	}
	c, err := r.CommitObject(h.Hash())
	if err != nil {
		t.Fatalf("commit %s: %v", ref, err)
	}
	return c.Message
}

// Work that could not be pushed is not lost: it is in the mirror, and the workspace is then
// disposable.
//
// The first version of this test protected a different mechanism — the workspace was KEPT on a
// failed delivery, because the commit lived only in the task's own clone and os.RemoveAll took
// the object store with it. Observed once, on an agent's fourteen verified findings, which is why
// there is a test here at all. Staging keeps the same property by a better means: the work goes
// into the runner's mirror BEFORE the push is attempted, so a delivery that fails leaves nothing
// behind that a directory removal could destroy.
func TestWorkThatCouldNotBePushedIsInTheMirror(t *testing.T) {
	origin := initRepo(t)
	mirrors := t.TempDir()
	m := NewMirrors(mirrors, TokenAuth(""))

	// Deliverable: something here was meant to leave.
	pushes := []Repo{
		{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin},
	}
	allows := &runner.Launch{Environment: pushing("acme/svc")}
	if !(&Workspace{Repos: pushes, Trees: []*Tree{{Repo: pushes[0]}}}).Deliverable(allows) {
		t.Error("a workspace with a push target reports nothing to deliver")
	}
	// Read-only: an environment that gives no push, whatever the autonomy says, so nothing was
	// ever going to leave, and removing it destroys nothing.
	fetchOnly := systems.Environment{Systems: systems.Systems{"git": {{Attributes: map[string]string{"repo": "acme/svc"},
		Verbs: map[string][]systems.Shape{"fetch": {{}}}}}}}
	allowAll := task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}
	for _, e := range []systems.Environment{{}, fetchOnly} {
		if (&Workspace{Trees: []*Tree{{Repo: pushes[0]}}}).Deliverable(&runner.Launch{Environment: e, Autonomy: allowAll}) {
			t.Errorf("a workspace under %+v, which gives no push, claims it delivers", e)
		}
	}

	ws, err := m.Open(context.Background(), "p1", pushes, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("hours of it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Break the remote out from under it, the way an unreachable forge would. The MIRROR is
	// still here — it is this runner's own disk — which is the whole point.
	if err := os.RemoveAll(origin); err != nil {
		t.Fatal(err)
	}
	d, perr := deliver(t, ws, &runner.Launch{ID: "p1"}, "work", "")
	if perr == nil {
		t.Fatal("expected the push to fail once its remote was gone")
	}
	if d.Err != nil {
		t.Fatalf("staging failed too, so this proves nothing about the mirror: %v", d.Err)
	}
	// Under the pending namespace, not the forced one: a first push of a branch the task creates
	// makes it only if nothing is there, and so does its retry.
	want := StagedRef(StagePending, "p1", "svc", "refs/heads/verse/auto/p1")
	if len(d.Staged) != 1 || d.Staged[0] != want {
		t.Fatalf("Staged = %v, want [%s] — the work has nowhere to wait", d.Staged, want)
	}
	// pending, not held: this delivery was authorized, so a retry may complete it.
	if !strings.Contains(d.Staged[0], "refs/pending") {
		t.Errorf("approved work waits under %q, which a retry will not sweep", d.Staged[0])
	}
	// Nothing loose, so the workspace can go — which is the improvement over keeping it.
	if len(d.Loose) != 0 {
		t.Fatalf("Loose = %v after a successful stage", d.Loose)
	}
	// And the commit really is in the mirror, reachable by that ref, after the workspace is gone.
	if err := ws.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if msg := commitAt(t, m.Dir(origin), want); !strings.Contains(msg, "Verse-Task: p1") {
		t.Fatalf("the mirror does not hold the work at %s: %q", want, msg)
	}
}

// A workspace root is ABSOLUTE however the runner was told where to put it.
//
// It crosses a process boundary into a different working directory: the path travels to the task
// process in childConfig, and that process is started with its cwd set to the workspace itself.
// So a relative root resolves against the runner on one side and against the workspace on the
// other, and the child dies with `repository does not exist` naming a path that is correct from
// where you are reading it.
//
// Nothing here could have caught that, which is why the test says so: every other test passes a
// `t.TempDir()`, and those are absolute already. It took starting the runner with
// `-data scratchpad/…` — the ordinary thing to type — against a real verse.
func TestAWorkspaceRootIsAbsoluteWhereverItWasToldToGo(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}

	// Relative, from a working directory that is not where the workspace will be read from.
	rel, err := filepath.Rel(mustGetwd(t), t.TempDir())
	if err != nil {
		t.Skipf("no relative path from here to a temp dir: %v", err)
	}
	ws, err := m.Open(context.Background(), "p1", repos, rel, Author{})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(ws.Root) {
		t.Fatalf("workspace root %q is relative — the task process reads it from somewhere else "+
			"and will not find it", ws.Root)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "svc", "README")); err != nil {
		t.Fatalf("the absolute root does not hold the checkout: %v", err)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

// A contract-shaped answer becomes the commit BODY, and the subject says what ran.
//
// `git log --oneline` on main carries one of these forever: a task with an `answer:` contract
// returns a document, `firstLine` took it as the subject, and the result was
// `{"files": 1, "fixed": 1, "findings": [{"symbol": …` cut at 72 characters. The rest of the
// answer was dropped entirely — the body held nothing but trailers.
func TestAStructuredAnswerIsTheBodyNotTheSubject(t *testing.T) {
	l := &runner.Launch{ID: "run-1", Provenance: runner.Provenance{
		Chain: "comment-drift", Subject: "drift.head.head-moved@git.head.verse",
	}}
	answer := "{\"files\": 1, \"fixed\": 1, \"findings\": [{\"symbol\": \"main.shortSha\"}]}"
	msg := CommitMessage(l, answer)

	subject, _, _ := strings.Cut(msg, "\n")
	if strings.Contains(subject, "{") {
		t.Errorf("the subject is the document: %q", subject)
	}
	if subject != "comment-drift on drift.head.head-moved@git.head.verse" {
		t.Errorf("subject = %q, want what fired and about what", subject)
	}
	if !strings.Contains(msg, answer) {
		t.Error("the answer is not in the message at all — it used to be dropped after line one")
	}
	// The trailers must still be the LAST paragraph or git parses none of them, which is the
	// trap that makes putting a document in a message worth a test at all.
	last := msg[strings.LastIndex(strings.TrimRight(msg, "\n"), "\n\n")+2:]
	for _, line := range strings.Split(strings.TrimRight(last, "\n"), "\n") {
		if !strings.HasPrefix(line, "Verse-") {
			t.Errorf("the last paragraph holds %q, so git reads no trailers at all", line)
		}
	}
	// Prose is still quoted as it always was.
	if got, _, _ := strings.Cut(CommitMessage(l, "fixed the flaky test\n\ndetails"), "\n"); got != "fixed the flaky test" {
		t.Errorf("a prose result stopped being the subject: %q", got)
	}
}

// A FENCED document is a document, and its fence is not a subject line.
//
// A model asked for a document commonly returns it fenced, so the answer's first line is
// ```json — which is what every branch the drift flow delivered carried. `git log --oneline` on
// main read as a wall of ```json and `git shortlog` grouped them as one subject
// (git-issue qwbacp11h12krq1tgsn0).
func TestAFencedAnswerIsTheBodyNotTheSubject(t *testing.T) {
	l := &runner.Launch{ID: "run-1", Provenance: runner.Provenance{
		Chain: "review-range", Subject: "verse-dev.drift.at",
	}}
	for _, answer := range []string{
		"```json\n{\"files\": 2, \"findings\": []}\n```",
		"```\n{files: 2}\n```",
		"```tony\nfiles: 2\n```",
	} {
		msg := CommitMessage(l, answer)
		subject, _, _ := strings.Cut(msg, "\n")
		if strings.HasPrefix(subject, "```") {
			t.Errorf("the subject is the fence: %q", subject)
		}
		if subject != "review-range on verse-dev.drift.at" {
			t.Errorf("subject = %q, want what fired and about what", subject)
		}
		if !strings.Contains(msg, answer) {
			t.Errorf("the answer is not in the message at all: %q", msg)
		}
	}
}

// A run may DELETE a branch its launch's `delete` names, and nothing else.
//
// This is the one act here that destroys something, and it is also the one where the task names
// something in the WORLD: a sweep learns stale branch names by asking the remote. The `delete`
// patterns are what keep both true — the launch says which branches may go, the task picks
// inside that, and a protected branch is refused before anything is gated.
func TestOnlyARunsOwnBranchesMayBeDeleted(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	// Two branches on the remote: one a run created, one it did not.
	head, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := head.Reference(plumbing.ReferenceName("refs/heads/main"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := head.Storer.SetReference(plumbing.NewHashReference("refs/heads/verse/auto/task-1", ref.Hash())); err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]

	// A branch the task names resolves to its full ref, and a name git refuses does not.
	full, err := runner.BranchRef("verse/auto/task-1")
	if err != nil || full != "refs/heads/verse/auto/task-1" {
		t.Fatalf("a delete did not resolve: %q %v", full, err)
	}
	for name, bad := range map[string]string{
		"nothing at all": "",
		"an option":      "-D",
		"a climb":        "verse/../main",
	} {
		if got, err := runner.BranchRef(bad); err == nil {
			t.Errorf("%s: %q resolved to %q", name, bad, got)
		}
	}
	_ = tree

	// And the delete really removes it, against a real remote.
	if err := m.DeleteRef(context.Background(), origin, full, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := head.Reference(plumbing.ReferenceName(full), true); err == nil {
		t.Error("the branch is still on the remote")
	}
	if _, err := head.Reference(plumbing.ReferenceName("refs/heads/main"), true); err != nil {
		t.Errorf("main was disturbed by deleting another branch: %v", err)
	}
}

// A push to an existing branch is a fast-forward unless the launch said `force`, and the remote's
// refusal of anything else is an answer rather than a failure.
func TestAnExistingBranchFastForwardsUnlessForced(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	// Upstream moves after the checkout, so the task's branch no longer descends from main.
	advanceOrigin(t, origin, "moved")
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = deliver(t, ws, &runner.Launch{ID: "p1"}, "work", "main")
	if err == nil {
		t.Fatal("a non-fast-forward push to main landed without force")
	}
	if Refusal(err) == "" {
		t.Errorf("the remote's refusal reads as a failure: %v", err)
	}
	if !strings.Contains(commitAt(t, origin, "refs/heads/main"), "moved") {
		t.Fatal("main was rewritten by a push the launch did not permit to force")
	}
}

// AN AGENT'S DELIVERY IS WHAT IT COMMITTED, each commit carrying the trailers, and nothing it left
// uncommitted. A commit without them is refused with what is missing, so the agent re-commits and
// stays the author; nothing here rewrites its commits.
// A person's launch commits as that person at a tagged address; anything else commits as the
// runner, which has the one mailbox it can vouch for.
func TestTheWorkspaceCommitsAsThePersonWhoLaunchedIt(t *testing.T) {
	cases := []struct{ principal, driver, name, email string }{
		{"user:ana@example.com", "codex", "ana@example.com (verse codex)", "ana+verse-codex-runner@example.com"},
		{"user:ana@example.com", "llm", "ana@example.com (verse llm)", "ana+verse-llm-runner@example.com"},
		{"user:ana@example.com", "", "ana@example.com (verse code)", "ana+verse-code-runner@example.com"},
		{"agent:reviewer", "codex", RunnerAuthor.Name, RunnerAuthor.Email},
		{"user:ana", "codex", RunnerAuthor.Name, RunnerAuthor.Email},
		{"", "codex", RunnerAuthor.Name, RunnerAuthor.Email},
	}
	for _, c := range cases {
		if got := AuthorFor(c.principal, c.driver); got.Name != c.name || got.Email != c.email {
			t.Errorf("AuthorFor(%q, %q) = %q <%s>, want %q <%s>", c.principal, c.driver, got.Name, got.Email, c.name, c.email)
		}
	}

	// And the checkout carries it, where `git commit` reads it.
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), AuthorFor("user:ana@example.com", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ws.Trees[0].repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User.Name != "ana@example.com (verse codex)" || cfg.User.Email != "ana+verse-codex-runner@example.com" {
		t.Errorf("the clone's identity is %q <%s>", cfg.User.Name, cfg.User.Email)
	}
}

func TestAnAgentsDeliveryIsItsCommitsWithTrailers(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin},
	}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]
	l := &runner.Launch{ID: "p1", Provenance: runner.Provenance{Principal: "user:ana"}}

	// Edited, not committed: nothing to deliver, and no refusal either.
	if err := os.WriteFile(filepath.Join(tree.Dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, why, _, err := tree.AuthoredWork(l)
	if err != nil || ok || why != "" {
		t.Fatalf("uncommitted edits: ok=%v why=%q err=%v, want nothing to deliver", ok, why, err)
	}

	commit := func(msg string) {
		wt, err := tree.repo.Worktree()
		if err != nil {
			t.Fatal(err)
		}
		if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			t.Fatal(err)
		}
		sig := &object.Signature{Name: "agent", Email: "agent@example.com", When: time.Now()}
		if _, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
			t.Fatal(err)
		}
	}
	// Committed without the trailers: refused, naming what is missing.
	commit("add a")
	ok, why, _, err = tree.AuthoredWork(l)
	if err != nil || !ok || !strings.Contains(why, "Verse-Task: p1") || !strings.Contains(why, "Verse-Principal: user:ana") {
		t.Fatalf("a commit without trailers: ok=%v why=%q err=%v", ok, why, err)
	}
	// Committed with them: deliverable, and the uncommitted scratch beside it is not its concern.
	if err := os.WriteFile(filepath.Join(tree.Dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit("add b\n\nVerse-Task: p1\nVerse-Principal: user:ana\n")
	if err := os.WriteFile(filepath.Join(tree.Dir, "scratch.txt"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, why, _, err = tree.AuthoredWork(l)
	// The first commit still lacks them, so the branch as a whole is still refused: every commit
	// delivered carries the trailers, not only the tip.
	if err != nil || !ok || !strings.Contains(why, "These commits do not") {
		t.Fatalf("a branch with one bad commit: ok=%v why=%q err=%v", ok, why, err)
	}
	if got := MissingTrailers("add b\n\nVerse-Task: p1\nVerse-Principal: user:ana\n", l); got != "" {
		t.Errorf("a complete trailer block reads as %q", got)
	}
	if got := MissingTrailers("add b\n\nVerse-Task: other\nVerse-Principal: user:ana\n", l); !strings.Contains(got, "Verse-Task") {
		t.Errorf("a wrong task id reads as %q", got)
	}
	if got := MissingTrailers("Verse-Task: p1\n\nwords after the trailers\n", l); got == "" {
		t.Error("trailers that are not the last paragraph were accepted")
	}
}

// A checkout has one remote, the mirror. The real repository is not configured in it, because a
// remote the task's shell can `git push` to is a route out of the workspace that passes no gate
// — and for a repository on the same filesystem, a route that works without a credential.
func TestTheCheckoutHasNoRouteUpstream(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainOpen(filepath.Join(ws.Root, "svc"))
	if err != nil {
		t.Fatal(err)
	}
	remotes, err := repo.Remotes()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range remotes {
		for _, u := range r.Config().URLs {
			if u == origin {
				t.Fatalf("remote %q names the real repository %s: the task could push there itself", r.Config().Name, u)
			}
		}
	}
	if _, err := repo.Remote("origin"); err == nil {
		t.Fatal("the checkout has an `origin`")
	}
}

// A fetch that fails while the mirror still has the repository opens the workspace on what the
// mirror had, and SAYS so: a task whose base is older than upstream is a surprise worth naming,
// and the place to name it is the workspace, which the runner then writes to its log.
func TestAFailedFetchIsReportedNotSwallowed(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	// A forge this test can't stop, stood for by an address nothing listens on: the mirror is
	// seeded from origin as if an earlier launch had fetched it, and then the forge is down.
	remote := "http://127.0.0.1:1/acme/svc"
	made, err := m.Ensure(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(made, m.Dir(remote)); err != nil {
		t.Fatal(err)
	}
	seeded, err := git.PlainOpen(m.Dir(remote))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := seeded.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Remotes["origin"].URLs = []string{remote}
	if err := seeded.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: remote}}
	ws, err := m.Open(context.Background(), "p2", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatalf("a mirror that has the repository should still open a workspace: %v", err)
	}
	if len(ws.Warnings) != 1 || !strings.Contains(ws.Warnings[0], "using what the mirror already had") {
		t.Fatalf("Warnings = %q, want the failed fetch named", ws.Warnings)
	}
	if !strings.Contains(ws.Warnings[0], "svc") {
		t.Fatalf("the warning does not say which repository: %q", ws.Warnings[0])
	}
}

// A repository that can't be fetched the first time is an error, and leaves no mirror behind: an
// empty one would read as a repository with no branches.
func TestAFirstFetchThatFailsIsAnError(t *testing.T) {
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	missing := filepath.Join(t.TempDir(), "acme", "nope")
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/nope"}, Remote: missing}}
	_, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err == nil || !strings.Contains(err.Error(), "fetch "+missing) {
		t.Fatalf("Open = %v, want the failed fetch", err)
	}
	if _, err := os.Stat(m.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("a mirror was left for a repository that was never fetched: %v", err)
	}
}

// A fetch the forge refuses for want of credentials doesn't fall back to what the mirror holds:
// a mirror is shared, and what it holds was fetched with another task's credential. A forge that
// can't be reached does: a task off a stale base beats none.
func TestARefusedFetchIsNotServedFromTheMirror(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("fetch: %w", transport.ErrAuthenticationRequired), true},
		{fmt.Errorf("fetch: %w", transport.ErrAuthorizationFailed), true},
		{fmt.Errorf("fetch: %w", transport.ErrRepositoryNotFound), true},
		{errors.New("dial tcp: connection refused"), false},
	} {
		if got := refused(c.err); got != c.want {
			t.Errorf("refused(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// A DELIVERY AFTER A REBASE IS THE TASK'S COMMITS ALONE: upstream moves, the checkout fetches,
// and the task's work is put on top of the new mirror/main, as its brief tells it to. The commits
// between are upstream's, already there and nobody's to re-sign; only the task's own are read for
// the trailers. With nothing of its own on top, there is nothing to deliver.
func TestADeliveryAfterARebaseIsTheTasksCommitsAlone(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]
	l := &runner.Launch{ID: "p1"}

	// Upstream moves twice, and the checkout fetches it.
	advanceOrigin(t, origin, "upstream-1")
	advanceOrigin(t, origin, "upstream-2")
	mirror, err := m.Ensure(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Refresh(mirror); err != nil {
		t.Fatal(err)
	}
	upstream, err := tree.repo.Reference("refs/remotes/mirror/main", true)
	if err != nil {
		t.Fatal(err)
	}

	// The task's branch is put on mirror/main: nothing of its own yet.
	wt, err := tree.repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Reset(&git.ResetOptions{Commit: upstream.Hash(), Mode: git.HardReset}); err != nil {
		t.Fatal(err)
	}
	if ok, why, _, err := tree.AuthoredWork(l); err != nil || ok || why != "" {
		t.Fatalf("on upstream with nothing of its own: ok=%v why=%q err=%v, want nothing to deliver", ok, why, err)
	}

	// Its work on top, signed: deliverable, and upstream's two unsigned commits beneath it are
	// not read.
	if err := os.WriteFile(filepath.Join(tree.Dir, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "a", Email: "a@invalid", When: time.Now()}
	if _, err := wt.Commit("work\n\nVerse-Task: p1\n", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
	if ok, why, _, err := tree.AuthoredWork(l); err != nil || !ok || why != "" {
		t.Fatalf("its work on upstream: ok=%v why=%q err=%v, want it deliverable", ok, why, err)
	}
}

// A DELIVERY AFTER A MERGE OF UPSTREAM READS BOTH SIDES: the task commits, upstream moves, and the
// task merges mirror/main into its branch. Upstream's side isn't read for the trailers; the task's
// side is, so an unsigned commit of its own beneath the merge is still refused.
func TestADeliveryAfterAMergeOfUpstreamReadsTheTasksSide(t *testing.T) {
	for _, signed := range []bool{false, true} {
		origin := initRepo(t)
		m := NewMirrors(t.TempDir(), TokenAuth(""))
		repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
		ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
		if err != nil {
			t.Fatal(err)
		}
		tree, l := ws.Trees[0], &runner.Launch{ID: "p1"}
		wt, err := tree.repo.Worktree()
		if err != nil {
			t.Fatal(err)
		}
		sig := &object.Signature{Name: "a", Email: "a@invalid", When: time.Now()}
		commit := func(file, msg string, parents ...plumbing.Hash) plumbing.Hash {
			t.Helper()
			if err := os.WriteFile(filepath.Join(tree.Dir, file), []byte(file+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
				t.Fatal(err)
			}
			h, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig, Parents: parents})
			if err != nil {
				t.Fatal(err)
			}
			return h
		}
		own := "own"
		if signed {
			own = "own\n\nVerse-Task: p1\n"
		}
		mine := commit("own.txt", own)

		advanceOrigin(t, origin, "upstream-1")
		mirror, err := m.Ensure(context.Background(), origin)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tree.Refresh(mirror); err != nil {
			t.Fatal(err)
		}
		upstream, err := tree.repo.Reference("refs/remotes/mirror/main", true)
		if err != nil {
			t.Fatal(err)
		}
		if wt, err = tree.repo.Worktree(); err != nil { // the refreshed repository's
			t.Fatal(err)
		}
		commit("merge.txt", "merge mirror/main\n\nVerse-Task: p1\n", mine, upstream.Hash())

		ok, why, _, err := tree.AuthoredWork(l)
		switch {
		case err != nil || !ok:
			t.Fatalf("signed=%v: ok=%v err=%v", signed, ok, err)
		case signed && why != "":
			t.Errorf("signed on both sides: refused %q", why)
		case !signed && !strings.Contains(why, ShortSha(mine.String())):
			t.Errorf("an unsigned commit beneath the merge: %q, want it named", why)
		case strings.Contains(why, "upstream-1") || strings.Contains(why, ShortSha(upstream.Hash().String())):
			t.Errorf("upstream's commit was read: %q", why)
		}
	}
}

// tipOf is what a tree has checked out.
func tipOf(t *testing.T, tree *Tree) plumbing.Hash {
	t.Helper()
	h, err := tree.Tip()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A REF THE TASK WRITES DOESN'T MAKE ITS COMMIT UPSTREAM (Walter's probe on #22): the task commits
// C without the trailers and D with them, and points a mirror/* ref of its checkout at C, as `git
// update-ref refs/remotes/mirror/evil <C>` does. C is still the task's, and the delivery is
// refused naming it, as it is without the ref.
func TestARefTheTaskWritesDoesntMakeItsCommitUpstream(t *testing.T) {
	for _, cheat := range []bool{false, true} {
		origin := initRepo(t)
		m := NewMirrors(t.TempDir(), TokenAuth(""))
		repos := []Repo{{Repo: runner.Repo{Repo: "acme/svc", From: "main"}, Remote: origin}}
		ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
		if err != nil {
			t.Fatal(err)
		}
		tree := ws.Trees[0]
		wt, err := tree.repo.Worktree()
		if err != nil {
			t.Fatal(err)
		}
		sig := &object.Signature{Name: "a", Email: "a@invalid", When: time.Now()}
		commit := func(file, msg string) plumbing.Hash {
			if err := os.WriteFile(filepath.Join(tree.Dir, file), []byte(file+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
				t.Fatal(err)
			}
			h, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig})
			if err != nil {
				t.Fatal(err)
			}
			return h
		}
		c := commit("c.txt", "unsigned\n")
		if cheat {
			if err := tree.repo.Storer.SetReference(plumbing.NewHashReference("refs/remotes/mirror/evil", c)); err != nil {
				t.Fatal(err)
			}
		}
		commit("d.txt", "signed\n\nVerse-Task: p1\n")
		ok, why, _, err := tree.AuthoredWork(&runner.Launch{ID: "p1"})
		if err != nil || !ok || !strings.Contains(why, ShortSha(c.String())) {
			t.Fatalf("cheat=%v: ok=%v why=%q err=%v, want the unsigned commit refused by name", cheat, ok, why, err)
		}
	}
}
