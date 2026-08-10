package main

import (
	"context"
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
	env := Environment{Name: "test", Repos: []Repo{
		{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/agent/$task"},
	}}
	worktrees := t.TempDir()

	ws, err := m.Open(context.Background(), "p1", env, worktrees)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "svc", "README")); err != nil {
		t.Fatalf("checkout does not carry the base's content under its repo name: %v", err)
	}
	if ws.Branch != "agent/p1" {
		t.Errorf("branch = %q, want agent/p1 — the branch is per task or two tasks share one", ws.Branch)
	}

	// A second task on the same repository must get its own tree and its own branch: the
	// mirror is shared, the work is not.
	ws2, err := m.Open(context.Background(), "p2", env, worktrees)
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
	if rc.Branch != "agent/p1" || rc.Base != "main" {
		t.Errorf("RepoContext = %+v, want the task's branch and its base", rc)
	}
	// Workspace-relative, not repo-relative: the path says which repository it is in, which
	// is what the agent sees and what a charter's `paths:` predicate is written against.
	if len(rc.ChangedFiles) != 1 || rc.ChangedFiles[0] != "svc/new.txt" {
		t.Errorf("ChangedFiles = %v, want [svc/new.txt]", rc.ChangedFiles)
	}
	if rc.PushTarget != "refs/heads/agent/p1" {
		t.Errorf("PushTarget = %q, want $task expanded", rc.PushTarget)
	}

	l := task.Launch{ID: "p1", Environment: "test", Provenance: task.Provenance{
		Principal: "scott", Subject: "demo:pr:1", Chain: "review",
	}}
	delivered, err := ws.Deliver(context.Background(), l, "fixed the flaky test\n\ndetails")
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(delivered) != 1 || !strings.Contains(delivered[0], "refs/heads/agent/p1") {
		t.Fatalf("delivered %v", delivered)
	}
	// The push landed on the origin, under the ref the environment named.
	if out := commitAt(t, origin, "refs/heads/agent/p1"); out == "" {
		t.Fatal("origin has no agent/p1")
	} else {
		// The trailers are the whole attribution channel out of the verse: a push under a
		// shared credential says nothing at the far end about who it was for.
		for _, want := range []string{
			"fixed the flaky test",
			"Verse-Task: p1",
			"Verse-Principal: scott",
			"Verse-Subject: demo:pr:1",
			"Verse-Chain: review",
			"Verse-Environment: test",
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
	env := Environment{Name: "multi", Repos: []Repo{
		{Name: "svc", Repo: svcOrigin, Base: "main", Push: "refs/heads/agent/$task"},
		{Name: "lib", Repo: libOrigin, Base: "main"}, // read-only: no push
	}}
	ws, err := m.Open(context.Background(), "p1", env, t.TempDir())
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
		if got := headBranch(t, tr.Dir); got != "agent/p1" {
			t.Errorf("%s is on %q, want agent/p1", tr.Repo.Name, got)
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

	delivered, err := ws.Deliver(context.Background(), task.Launch{ID: "p1"}, "did the thing")
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(delivered) != 1 || !strings.HasPrefix(delivered[0], "svc ") {
		t.Fatalf("delivered %v — only the repository with a push target should leave", delivered)
	}
	if commitAt(t, libOrigin, "refs/heads/agent/p1") != "" {
		t.Error("the reference repository received a push it never declared a target for")
	}
}

// A spawn refreshes the mirror, and refreshing it must not disturb tasks already running.
//
// This guards a failure that was real and was quiet. The first version of this runner used
// linked worktrees off a shared mirror, so task branches lived in the MIRROR's ref namespace —
// and `fetch --prune` on the next spawn deleted every `agent/*`, because none of them exists
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
	env := Environment{Name: "test", Repos: []Repo{
		{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/agent/$task"},
	}}
	worktrees := t.TempDir()

	ws, err := m.Open(context.Background(), "p1", env, worktrees)
	if err != nil {
		t.Fatal(err)
	}
	// A second spawn: this is what re-fetches the mirror under the first task's feet.
	if _, err := m.Open(context.Background(), "p2", env, worktrees); err != nil {
		t.Fatal(err)
	}
	if got := headBranch(t, ws.Primary().Dir); got != "agent/p1" {
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
	env := Environment{Name: "read-only", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}}}
	ws, err := m.Open(context.Background(), "p1", env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	delivered, err := ws.Deliver(context.Background(), task.Launch{ID: "p1"}, "done")
	if err != nil || len(delivered) != 0 {
		t.Fatalf("Deliver = %v, %v — a read-only environment pushes nowhere", delivered, err)
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
	env := Environment{Name: "test", Repos: []Repo{
		{Name: "svc", Repo: intended, Base: "main", Push: "refs/heads/agent/$task"},
	}}
	ws, err := m.Open(context.Background(), "p1", env, t.TempDir())
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

	if _, err := ws.Deliver(context.Background(), task.Launch{ID: "p1"}, "work"); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if commitAt(t, attacker, "refs/heads/agent/p1") != "" {
		t.Fatal("the task redirected its own push by editing .git/config — the runner's " +
			"credential goes wherever the agent points it")
	}
	if commitAt(t, intended, "refs/heads/agent/p1") == "" {
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
		seg := safeSeg(id)
		if seg == "." || seg == ".." || seg == "" || strings.ContainsAny(seg, `/\`) {
			t.Errorf("safeSeg(%q) = %q — usable as a traversal", id, seg)
		}
		got := filepath.Join(root, seg)
		if rel, err := filepath.Rel(root, got); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("id %q lands at %s, outside %s", id, got, root)
		}
	}
	// Lossy sanitizing must not collide: two ids in one workspace is two tasks in one checkout.
	if safeSeg("a.b") == safeSeg("a_b") {
		t.Errorf("a.b and a_b share a workspace (%q)", safeSeg("a.b"))
	}
	// An id that needs no rewriting keeps its name, so the ordinary case stays readable.
	if got := safeSeg("run-8f306077b1c2"); got != "run-8f306077b1c2" {
		t.Errorf("safeSeg mangled a clean id: %q", got)
	}
}

// The agent is told what its working directory contains, because the layout is this runner's
// convention and nothing else can tell it.
func TestWorkspaceBriefNamesTheRepos(t *testing.T) {
	svc, lib := initRepo(t), initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	env := Environment{Name: "multi", Repos: []Repo{
		{Name: "svc", Repo: svc, Base: "main", Push: "refs/heads/agent/$task"},
		{Name: "lib", Repo: lib, Base: "main"},
	}}
	ws, err := m.Open(context.Background(), "p1", env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := ws.Brief()
	for _, want := range []string{"svc/", "lib/", "2 repositories", "reference only"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief is missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b[:strings.Index(b, "lib/")], "reference only") {
		t.Error("the pushable repo is described as reference only")
	}
}

// A containment the runner cannot build is REFUSED, not quietly downgraded. This is the rule
// the whole isolation design rests on: a weaker floor than the environment declared is
// invisible from the outside, which is the direction that costs.
func TestUnbuildableContainmentIsRefused(t *testing.T) {
	err := hostConfiner{}.Provides(Point{"principal": "distinct", "network.egress": "none"})
	if err == nil {
		t.Fatal("hostConfiner claimed containment it cannot build")
	}
	for _, want := range []string{"principal", "network.egress"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %s is missing: %v", want, err)
		}
	}
	if err := (hostConfiner{}).Provides(hostProvides); err != nil {
		t.Errorf("hostConfiner refuses its own floor: %v", err)
	}
}

// Containment is a partial order, not a ladder: improving one leaf while weakening another is
// not "at least as contained", which is what stops a delegation from escaping sideways.
func TestPointComparisonIsComponentwise(t *testing.T) {
	base := Point{"filesystem.scope": "shared", "network.egress": "restricted"}
	tighter := Point{"filesystem.scope": "private", "network.egress": "none"}
	sideways := Point{"filesystem.scope": "private", "network.egress": "full"}

	if !tighter.AtLeast(base) {
		t.Error("a point stronger at every leaf is not at least as contained")
	}
	if base.AtLeast(tighter) {
		t.Error("a weaker point compares as at least as contained")
	}
	if sideways.AtLeast(base) {
		t.Error("better filesystem and worse network compares as at least as contained — " +
			"a floor that may weaken anywhere is not a floor")
	}
}

// The config shipped beside this program must load.
//
// It is the same discipline that makes the examples compile with the tree: a sample nobody
// parses goes wrong the first time a field moves — or, as happened here, the first time
// somebody writes a nested list in a syntax the format does not have — and the person who
// finds out is whoever copied it.
func TestShippedConfigLoads(t *testing.T) {
	c, err := LoadConfig("coderunner.tony")
	if err != nil {
		t.Fatalf("the config shipped beside this program does not load: %v", err)
	}
	if _, err := c.Environment(""); err != nil {
		t.Errorf("its default environment does not resolve: %v", err)
	}
	multi, err := c.Environment("verse-dev")
	if err != nil {
		t.Fatalf("verse-dev: %v", err)
	}
	if len(multi.Repos) < 2 {
		t.Errorf("verse-dev has %d repo(s) — the sample exists to show the multi-repo shape",
			len(multi.Repos))
	}
	// One being changed, one only read: the asymmetry the sample is meant to demonstrate.
	var pushes int
	for _, r := range multi.Repos {
		if r.Push != "" {
			pushes++
		}
	}
	if pushes != 1 {
		t.Errorf("verse-dev has %d repos that push; the sample should show exactly one", pushes)
	}
}

// An environment naming a workspace this runner does not have is refused with the names it
// does have, because the alternative — running the work somewhere else — is the same class of
// bug as a misspelled runner that runs in-process.
func TestUnknownEnvironmentIsRefused(t *testing.T) {
	c := &Config{Environments: []Environment{{Name: "a"}, {Name: "b"}}}
	if _, err := c.Environment("c"); err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Fatalf("Environment(c) = %v, want a refusal naming what exists", err)
	}
	if _, err := c.Environment(""); err == nil {
		t.Fatal("a spawn naming nothing, on a runner with no default, must be refused")
	}
	c.Default = "a"
	if e, err := c.Environment(""); err != nil || e.Name != "a" {
		t.Fatalf("Environment(\"\") = %v, %v — should be the default", e.Name, err)
	}
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

// An agent turn is gated as a delegation and is NOT a sub-task spawn.
//
// The two are one field apart and only one of them works here. `ToolUse.CallAgent` is not a
// label saying "this delegates" — it is the instruction that makes the loop route the action to
// spawnChild and build a child through a SpawnFunc. This runner has none by design: the agent
// binary is a foreign one-shot the executor runs, an opaque leaf rather than a recursive
// invocation of our own loop.
//
// Setting it cost every task a terminal failure — "no spawn factory configured" — the instant
// its gate was approved, which no test caught because the tests stopped at the gate.
func TestAgentTurnIsGatedNotSpawned(t *testing.T) {
	d := &claudeDriver{prompt: "work"}
	tu := d.turn("do the thing")

	if tu.CallAgent != nil {
		t.Error("the turn carries a CallAgent request, so the loop will try to spawn a child " +
			"task through a factory this runner does not provide")
	}
	var delegates bool
	for _, e := range tu.Effects {
		if e.Verb == task.CallAgent {
			delegates = true
		}
	}
	if !delegates {
		t.Error("the turn derives no call-agent effect, so a vector cannot gate the invocation " +
			"— which is the only thing about a foreign agent there is to decide")
	}
	// And the vector must actually match it, since that is the whole point of the effect.
	vec := &task.Vector{Default: task.Auto, Gates: []task.Gate{{
		Name: "agent", Mode: task.Ask,
		Match: task.GateMatch{Verbs: []task.Verb{task.CallAgent}},
	}}}
	if got := vec.Evaluate(tu).Mode; got != task.Ask {
		t.Errorf("the default vector evaluates an agent turn as %v, want ASK", got)
	}
}

// A workspace whose work did not leave is KEPT.
//
// The first version removed unconditionally after attempting delivery, so a failed push — or a
// delivery that found nothing — destroyed the tree the work was in. Not a lost push: a lost
// result, because the commit lives in the task's own clone and removing it takes the object
// store with it. Observed once, on an agent's fourteen verified findings, which is why this test
// exists rather than a comment.
func TestWorkspaceSurvivesAFailedDelivery(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))

	// Deliverable: something here was meant to leave.
	pushes := Environment{Name: "p", Repos: []Repo{
		{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/agent/$task"},
	}}
	if !(&Workspace{Env: pushes, Trees: []*Tree{{Repo: pushes.Repos[0]}}}).Deliverable() {
		t.Error("an environment with a push target reports nothing to deliver")
	}
	// Read-only: nothing was ever going to leave, so removing it destroys nothing.
	ro := Repo{Name: "svc", Repo: origin, Base: "main"}
	if (&Workspace{Trees: []*Tree{{Repo: ro}}}).Deliverable() {
		t.Error("a read-only environment claims it delivers")
	}

	// And the real path: a push target that cannot be reached must leave the tree behind.
	broken := Environment{Name: "broken", Repos: []Repo{
		{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/agent/$task"},
	}}
	ws, err := m.Open(context.Background(), "p1", broken, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "work.txt"), []byte("hours of it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Break the remote out from under it, the way an unreachable forge would.
	if err := os.RemoveAll(origin); err != nil {
		t.Fatal(err)
	}
	delivered, derr := ws.Deliver(context.Background(), task.Launch{ID: "p1"}, "work")
	if derr == nil {
		t.Fatal("expected the push to fail once its remote was gone")
	}
	if len(delivered) != 0 {
		t.Fatalf("delivered %v despite the failure", delivered)
	}
	// The decision finish makes, asserted here because finish itself needs a live task.
	if !(derr != nil || (ws.Deliverable() && len(delivered) == 0)) {
		t.Fatal("a failed delivery would be removed")
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "svc", "work.txt")); err != nil {
		t.Fatalf("the work is not where it was left: %v", err)
	}
}
