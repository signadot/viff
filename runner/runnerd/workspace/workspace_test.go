package workspace

import (
	"context"
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

	"github.com/signadot/verse/task"
)

// own is the allowance a delivering test repository has: the run's own branches, and nothing
// with a consequence.
var own = &task.RepoPush{New: []string{"verse/auto/*"}}

// A checkout per task, off a real repository, delivered back to it.
//
// It drives go-git against real repositories rather than mocking it, because everything worth
// getting wrong here is git's opinion and not ours: which ref a checkout starts from, whether a
// second task on the same mirror collides, and whether a push lands where the environment said.
// A fake would agree with whatever this file believed while writing it — which is how the
// mirror HEAD and ref-namespace defects below got in.
func TestCheckoutPerTaskAndDelivery(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Name: "svc", Repo: origin, Base: "main", Access: task.AccessWrite, Push: own},
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
	rc := ws.RepoContext()
	if rc.Branch != "verse/auto/p1" || rc.Base != "main" {
		t.Errorf("RepoContext = %+v, want the task's branch and its base", rc)
	}
	// Workspace-relative, not repo-relative: the path says which repository it is in, which
	// is what the agent sees and what a charter's `paths:` predicate is written against.
	if len(rc.ChangedFiles) != 1 || rc.ChangedFiles[0] != "svc/new.txt" {
		t.Errorf("ChangedFiles = %v, want [svc/new.txt]", rc.ChangedFiles)
	}

	l := task.Launch{ID: "p1", Environment: "test", Provenance: task.Provenance{
		Principal: "scott", Subject: `demo.pr."1"`, Chain: "review",
	}}
	if _, err := deliver(t, ws, l, "fixed the flaky test\n\ndetails", ""); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	// Staged on the way out and dropped once the far side had it: what is left under
	// refs/pending is exactly the work this runner holds that the world does not.
	if held, err := m.Staged(origin); err != nil || len(held) != 0 {
		t.Errorf("work is still staged after a successful delivery: %v (%v)", held, err)
	}
	// The push landed on the origin, under the ref the environment named.
	if out := commitAt(t, origin, "refs/heads/verse/auto/p1"); out == "" {
		t.Fatal("origin has no verse/auto/p1")
	} else {
		// The trailers are the whole attribution channel out of the verse: a push under a
		// shared credential says nothing at the far end about who it was for.
		for _, want := range []string{
			"fixed the flaky test",
			"Verse-Task: p1",
			"Verse-Principal: scott",
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
// That asymmetry is the reason `push` is per repository rather than per environment. A
// reference repository that could push back is a blast radius nobody asked for, and an agent
// that cannot see it at all is one guessing about the code it is changing against.
func TestMultipleReposOneWorkspace(t *testing.T) {
	svcOrigin, libOrigin := initRepo(t), initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Name: "svc", Repo: svcOrigin, Base: "main", Access: task.AccessWrite, Push: own},
		{Name: "lib", Repo: libOrigin, Base: "main"}, // read-only: no push
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
			t.Errorf("%s is on %q, want verse/auto/p1", tr.Repo.Name, got)
		}
	}

	// The agent changes both. Only the one with a push target should leave.
	for _, name := range []string{"svc", "lib"} {
		if err := os.WriteFile(filepath.Join(ws.Root, name, "new.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rc := ws.RepoContext()
	if len(rc.ChangedFiles) != 2 {
		t.Fatalf("ChangedFiles = %v, want one per repository", rc.ChangedFiles)
	}
	for _, want := range []string{"svc/new.txt", "lib/new.txt"} {
		if !slices.Contains(rc.ChangedFiles, want) {
			t.Errorf("ChangedFiles = %v, missing %s", rc.ChangedFiles, want)
		}
	}

	if _, err := deliver(t, ws, task.Launch{ID: "p1"}, "did the thing", ""); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if commitAt(t, svcOrigin, "refs/heads/verse/auto/p1") == "" {
		t.Fatal("the repository with a destination did not deliver")
	}
	if commitAt(t, libOrigin, "refs/heads/verse/auto/p1") != "" {
		t.Error("the reference repository received a push it never declared a target for")
	}
}

// A spawn refreshes the mirror, and refreshing it must not disturb tasks already running.
//
// This guards a failure that was real and was quiet. The first version of this runner used
// linked worktrees off a shared mirror, so task branches lived in the MIRROR's ref namespace —
// and `fetch --prune` on the next spawn deleted every `verse/auto/*`, because none of them exists
// upstream. Nothing errored; a running worktree was simply left on a ref that was gone, its
// whole tree read as untracked, and its work could not be committed.
//
// A clone per task makes it structurally impossible: each task's refs live inside its own
// repository, and the mirror holds upstream refs only. The test stays anyway — the property
// ("a later spawn does not disturb an earlier one") is what matters, and the next person to
// reach for shared state should have to make it fail.
func TestFetchDoesNotPruneRunningTasksBranches(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{
		{Name: "svc", Repo: origin, Base: "main", Access: task.AccessWrite, Push: own},
	}
	worktrees := t.TempDir()

	ws, err := m.Open(context.Background(), "p1", repos, worktrees, Author{})
	if err != nil {
		t.Fatal(err)
	}
	// A second spawn: this is what re-fetches the mirror under the first task's feet.
	if _, err := m.Open(context.Background(), "p2", repos, worktrees, Author{}); err != nil {
		t.Fatal(err)
	}
	if got := headBranch(t, ws.Primary().Dir); got != "verse/auto/p1" {
		t.Fatalf("first task's HEAD = %q — a second spawn disturbed its branch", got)
	}
	if clean, st := isClean(t, ws.Primary().Dir); !clean {
		t.Fatalf("first task's tree is no longer clean after a second spawn:\n%s", st)
	}
}

// Nothing leaves an environment that names no push target, and that is a configuration rather
// than a failure — but it must not look like a delivery.
func TestNoPushTargetDeliversNothing(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Name: "svc", Repo: origin, Base: "main"}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := deliver(t, ws, task.Launch{ID: "p1"}, "done", "")
	if err != nil || len(d.Staged) != 0 || len(d.Loose) != 0 {
		t.Fatalf("delivery = %+v (%v) — a read-only repository pushes nowhere and stages nothing", d, err)
	}
	// And the allowance says so, in words an agent can act on.
	if _, _, err := ws.Trees[0].Repo.Permit("verse/auto/p1", false); err == nil || !strings.Contains(err.Error(), "may not push") {
		t.Errorf("a read-only repository permitted a push, or did not say why not: %v", err)
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
		{Name: "svc", Repo: intended, Base: "main", Access: task.AccessWrite, Push: own},
	}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	// The agent, doing what an agent can do: writing files in its own working directory.
	cfgPath := filepath.Join(ws.Primary().Dir, ".git", "config")
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
	if err := os.WriteFile(filepath.Join(ws.Primary().Dir, "payload.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := deliver(t, ws, task.Launch{ID: "p1"}, "work", ""); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if commitAt(t, attacker, "refs/heads/verse/auto/p1") != "" {
		t.Fatal("the task redirected its own push by editing .git/config — the runner's " +
			"credential goes wherever the agent points it")
	}
	if commitAt(t, intended, "refs/heads/verse/auto/p1") == "" {
		t.Fatal("the push did not reach the remote the environment configured")
	}
}

// A task id becomes a directory name and a ref component, and it is chosen by whoever spawns —
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
		{Name: "svc", Repo: svc, Base: "main", Access: task.AccessWrite, Push: own},
		{Name: "lib", Repo: lib, Base: "main"},
	}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	b := ws.Brief()
	for _, want := range []string{"svc/", "lib/", "2 repositories", "read only", "verse/auto/*", ws.Branch} {
		if !strings.Contains(b, want) {
			t.Errorf("brief is missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b[:strings.Index(b, "lib/")], "read only") {
		t.Error("the pushable repo is described as read only")
	}
}

// THE ALLOWANCE DECIDES A PUSH, on both sides of the process boundary from the same words: a
// branch a `new` pattern matches is the run's own and may be rewritten or deleted; one in
// `existing` may be updated, rewritten only under `force`, never deleted; anything else is
// refused with the allowance in words, before anything is gated and before a credential is
// anywhere near it.
func TestTheAllowanceDecidesAPush(t *testing.T) {
	r := Repo{Name: "verse", Repo: "…", Access: task.AccessWrite,
		Push: &task.RepoPush{New: []string{"verse/auto/*", "verse/train"}, Existing: []string{"main"}}}
	cases := []struct {
		branch string
		del    bool
		ref    string // "" = refused
		force  bool
		why    string
	}{
		{"verse/auto/p1", false, "refs/heads/verse/auto/p1", true, ""},
		{"refs/heads/verse/auto/p1", false, "refs/heads/verse/auto/p1", true, ""},
		{"verse/auto/p1", true, "refs/heads/verse/auto/p1", true, ""},
		{"verse/train", false, "refs/heads/verse/train", true, ""},
		{"verse/auto/a/b", false, "", false, "not one verse may push to"}, // `*` stays in its segment
		{"main", false, "refs/heads/main", false, ""},
		{"main", true, "", false, "may be updated, not deleted"},
		{"release", false, "", false, "not one verse may push to"},
		{"", false, "", false, "no branch named"},
		{"../main", false, "", false, "not a branch name git accepts"},
		{"-x", false, "", false, "not a branch name git accepts"},
	}
	for _, c := range cases {
		ref, force, err := r.Permit(c.branch, c.del)
		switch {
		case c.ref == "" && err == nil:
			t.Errorf("Permit(%q, delete=%v) allowed %s, want a refusal", c.branch, c.del, ref)
		case c.ref == "" && !strings.Contains(err.Error(), c.why):
			t.Errorf("Permit(%q, delete=%v) refused for %q, want %q in it", c.branch, c.del, err, c.why)
		case c.ref != "" && err != nil:
			t.Errorf("Permit(%q, delete=%v): %v", c.branch, c.del, err)
		case c.ref != "" && (ref != c.ref || force != c.force):
			t.Errorf("Permit(%q, delete=%v) = %s force=%v, want %s force=%v", c.branch, c.del, ref, force, c.ref, c.force)
		}
	}
	// `force` on the spawn is what lets an existing branch be rewritten; `**` crosses segments.
	r.Push = &task.RepoPush{New: []string{"verse/**"}, Existing: []string{"main"}, Force: true}
	if _, force, err := r.Permit("main", false); err != nil || !force {
		t.Errorf("existing main under force: force=%v err=%v", force, err)
	}
	if _, _, err := r.Permit("verse/auto/a/b", false); err != nil {
		t.Errorf("verse/** does not cross segments: %v", err)
	}
	// Read access, or write with no allowance, pushes nothing.
	for _, ro := range []Repo{{Name: "v", Repo: "…"}, {Name: "v", Repo: "…", Access: task.AccessWrite}} {
		if _, _, err := ro.Permit("verse/auto/p1", false); err == nil || !strings.Contains(err.Error(), "may not push") {
			t.Errorf("%+v permitted a push: %v", ro, err)
		}
	}
}

// A spawn's repository becomes a workspace's: `owner/name` is GitHub and the directory is the
// name, a url keeps its last segment, and what the spawn did not say is left for the mirror to
// answer.
func TestARepoIsBuiltFromTheSpawns(t *testing.T) {
	r, err := FromSpec(task.Repo{Repo: "acme/app", Access: task.AccessWrite, From: "release",
		Push: &task.RepoPush{New: []string{"verse/*"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "app" || r.Repo != "https://github.com/acme/app" || r.Base != "release" || !r.Writes() {
		t.Errorf("acme/app became %+v", r)
	}
	for in, want := range map[string][2]string{
		"https://github.com/acme/docs.git": {"docs", "https://github.com/acme/docs.git"},
		"git@github.com:acme/lib.git":      {"lib", "git@github.com:acme/lib.git"},
		"/tmp/scratch/x.git":               {"x", "/tmp/scratch/x.git"},
		"./here":                           {"here", abs("./here")},
	} {
		r, err := FromSpec(task.Repo{Repo: in})
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if r.Name != want[0] || r.Repo != want[1] || r.Base != "" || r.Writes() {
			t.Errorf("%s became %+v, want %s at %s, read only", in, r, want[0], want[1])
		}
	}
	// Two that would share a directory are refused together, and the checks a spawn gets apply.
	if _, err := FromSpecs([]task.Repo{{Repo: "acme/app"}, {Repo: "other/app"}}); err == nil {
		t.Error("two repositories named app checked out over each other")
	}
	if _, err := FromSpecs([]task.Repo{{Repo: "acme/app", Push: &task.RepoPush{New: []string{"x"}}}}); err == nil {
		t.Error("a push on a read-only spawn repository was accepted")
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
func deliver(t *testing.T, ws *Workspace, l task.Launch, result, branch string) (Delivery, error) {
	t.Helper()
	var tree *Tree
	for _, tr := range ws.Trees {
		if tr.Repo.Writes() {
			tree = tr
			break
		}
	}
	if tree == nil {
		return Delivery{}, nil
	}
	if branch == "" {
		branch = ws.Branch
	}
	target, force, err := tree.Repo.Permit(branch, false)
	if err != nil {
		return Delivery{}, err
	}
	work, err := tree.CommitWork(l, result)
	if err != nil {
		return Delivery{Err: err, Loose: []string{tree.Repo.Name}}, err
	}
	if !work {
		return Delivery{}, nil
	}
	d := ws.Stage(context.Background(), l.ID, tree, target, force)
	if d.Err != nil {
		return d, d.Err
	}
	// The runner pushes the staged ref from its mirror, which is the same act the sweep performs
	// for anything that did not land.
	why, err := ws.mirrors.Deliver(context.Background(), tree.Repo.Repo, d.Staged[0], target, force, nil)
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

// A task's checkout sees upstream as it was at spawn. A refresh fetches the mirror and moves the
// checkout's `mirror/*` refs — with the objects behind them — and says what moved; the task's own
// branch does not move.
func TestARefreshBringsTheCheckoutUpToUpstream(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Name: "svc", Repo: origin, Base: "main"}}
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
		{Name: "svc", Repo: origin, Base: "main", Access: task.AccessWrite, Push: own},
	}
	if !(&Workspace{Repos: pushes, Trees: []*Tree{{Repo: pushes[0]}}}).Deliverable() {
		t.Error("an environment with a push target reports nothing to deliver")
	}
	// Read-only: nothing was ever going to leave, so removing it destroys nothing.
	ro := Repo{Name: "svc", Repo: origin, Base: "main"}
	if (&Workspace{Trees: []*Tree{{Repo: ro}}}).Deliverable() {
		t.Error("a read-only environment claims it delivers")
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
	d, perr := deliver(t, ws, task.Launch{ID: "p1"}, "work", "")
	if perr == nil {
		t.Fatal("expected the push to fail once its remote was gone")
	}
	if d.Err != nil {
		t.Fatalf("staging failed too, so this proves nothing about the mirror: %v", d.Err)
	}
	// Under the FORCED pending namespace: the task's own branch may be rewritten, so its retry may
	// too, and the retry runs without the spawn that decided that.
	want := StagedRef(StagePendingForce, "p1", "svc", "refs/heads/verse/auto/p1")
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
	repos := []Repo{{Name: "svc", Repo: origin, Base: "main"}}

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
	l := task.Launch{ID: "run-1", Environment: "verse-fix", Provenance: task.Provenance{
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
	l := task.Launch{ID: "run-1", Environment: "verse-fix", Provenance: task.Provenance{
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

// A run may DELETE a branch its charter's runs created, and nothing else.
//
// This is the one act here that destroys something, and it is also the one where the task names
// something in the WORLD: a sweep learns stale branch names by asking the remote. The `new`
// patterns are what keep both true — the spawn says which branches are the run's own, the task
// picks inside that, and a protected branch is refused before anything is gated.
func TestOnlyARunsOwnBranchesMayBeDeleted(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{
		Name: "svc", Repo: origin, Base: "main", Access: task.AccessWrite,
		Push: &task.RepoPush{New: []string{"verse/auto/*"}, Existing: []string{"main"}},
	}}
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

	// Inside the allowance: resolved, and the ref is its full form.
	full, _, err := tree.Repo.Permit("verse/auto/task-1", true)
	if err != nil || full != "refs/heads/verse/auto/task-1" {
		t.Fatalf("a permitted delete did not resolve: %q %v", full, err)
	}
	// Outside it, in every direction that matters.
	for name, bad := range map[string]string{
		"a protected branch":        "main",
		"one segment too deep":      "verse/auto/a/b",
		"another repository's area": "someone/else/branch",
		"nothing at all":            "",
	} {
		if got, _, err := tree.Repo.Permit(bad, true); err == nil {
			t.Errorf("%s: %q resolved to %q — the spawn did not permit deleting it", name, bad, got)
		}
	}

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

// A push to an existing branch is a fast-forward unless the spawn said `force`, and the remote's
// refusal of anything else is an answer rather than a failure.
func TestAnExistingBranchFastForwardsUnlessForced(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Name: "svc", Repo: origin, Base: "main", Access: task.AccessWrite,
		Push: &task.RepoPush{Existing: []string{"main"}}}}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	// Upstream moves after the checkout, so the task's branch no longer descends from main.
	advanceOrigin(t, origin, "moved")
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = deliver(t, ws, task.Launch{ID: "p1"}, "work", "main")
	if err == nil {
		t.Fatal("a non-fast-forward push to main landed without force")
	}
	if Refusal(err) == "" {
		t.Errorf("the remote's refusal reads as a failure: %v", err)
	}
	if !strings.Contains(commitAt(t, origin, "refs/heads/main"), "moved") {
		t.Fatal("main was rewritten by a push the spawn did not permit to force")
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
	repos := []Repo{{Name: "svc", Repo: origin, Base: "main"}}
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
		{Name: "svc", Repo: origin, Base: "main", Access: task.AccessWrite, Push: own},
	}
	ws, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{})
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Trees[0]
	l := task.Launch{ID: "p1", Environment: "test", Provenance: task.Provenance{Principal: "user:ana"}}

	// Edited, not committed: nothing to deliver, and no refusal either.
	if err := os.WriteFile(filepath.Join(tree.Dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, why, err := tree.AuthoredWork(l)
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
	ok, why, err = tree.AuthoredWork(l)
	if err != nil || !ok || !strings.Contains(why, "Verse-Task: p1") || !strings.Contains(why, "Verse-Principal: user:ana") {
		t.Fatalf("a commit without trailers: ok=%v why=%q err=%v", ok, why, err)
	}
	// Committed with them: deliverable, and the uncommitted scratch beside it is not its concern.
	if err := os.WriteFile(filepath.Join(tree.Dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit("add b\n\nVerse-Task: p1\nVerse-Principal: user:ana\nVerse-Environment: test\n")
	if err := os.WriteFile(filepath.Join(tree.Dir, "scratch.txt"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, why, err = tree.AuthoredWork(l)
	// The first commit still lacks them, so the branch as a whole is still refused: every commit
	// delivered carries the trailers, not only the tip.
	if err != nil || !ok || !strings.Contains(why, "These commits do not") {
		t.Fatalf("a branch with one bad commit: ok=%v why=%q err=%v", ok, why, err)
	}
	if got := MissingTrailers("add b\n\nVerse-Task: p1\nVerse-Principal: user:ana\nVerse-Environment: test\n", l); got != "" {
		t.Errorf("a complete trailer block reads as %q", got)
	}
	if got := MissingTrailers("add b\n\nVerse-Task: other\nVerse-Principal: user:ana\nVerse-Environment: test\n", l); !strings.Contains(got, "Verse-Task") {
		t.Errorf("a wrong task id reads as %q", got)
	}
	if got := MissingTrailers("Verse-Task: p1\n\nwords after the trailers\n", l); got == "" {
		t.Error("trailers that are not the last paragraph were accepted")
	}
}

// abs is a path made absolute, as FromSpec makes a local remote.
func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		panic(err)
	}
	return a
}

// A checkout has one remote, the mirror. The real repository is not configured in it, because a
// remote the task's shell can `git push` to is a route out of the workspace that passes no gate
// — and for a repository on the same filesystem, a route that works without a credential.
func TestTheCheckoutHasNoRouteUpstream(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	repos := []Repo{{Name: "svc", Repo: origin, Base: "main", Access: task.AccessWrite, Push: own}}
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
	repos := []Repo{{Name: "svc", Repo: origin, Base: "main"}}
	if _, err := m.Open(context.Background(), "p1", repos, t.TempDir(), Author{}); err != nil {
		t.Fatal(err)
	}
	// Upstream goes away between two spawns.
	if err := os.RemoveAll(origin); err != nil {
		t.Fatal(err)
	}
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
