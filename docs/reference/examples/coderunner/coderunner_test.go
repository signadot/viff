package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/signadot/verse/codec"
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
		{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/verse/auto/$task"},
	}}
	worktrees := t.TempDir()

	ws, err := m.Open(context.Background(), "p1", env, worktrees)
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
	if rc.Branch != "verse/auto/p1" || rc.Base != "main" {
		t.Errorf("RepoContext = %+v, want the task's branch and its base", rc)
	}
	// Workspace-relative, not repo-relative: the path says which repository it is in, which
	// is what the agent sees and what a charter's `paths:` predicate is written against.
	if len(rc.ChangedFiles) != 1 || rc.ChangedFiles[0] != "svc/new.txt" {
		t.Errorf("ChangedFiles = %v, want [svc/new.txt]", rc.ChangedFiles)
	}
	if rc.PushTarget != "refs/heads/verse/auto/p1" {
		t.Errorf("PushTarget = %q, want $task expanded", rc.PushTarget)
	}

	l := task.Launch{ID: "p1", Environment: "test", Provenance: task.Provenance{
		Principal: "scott", Subject: "demo:pr:1", Chain: "review",
	}}
	if _, err := deliver(t, ws, l, "fixed the flaky test\n\ndetails", autoDestination); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	// Staged on the way out and dropped once the far side had it: what is left under
	// refs/pending is exactly the work this runner holds that the world does not.
	if held, err := m.Staged(env.Repos[0]); err != nil || len(held) != 0 {
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
		{Name: "svc", Repo: svcOrigin, Base: "main", Push: "refs/heads/verse/auto/$task"},
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

	if _, err := deliver(t, ws, task.Launch{ID: "p1"}, "did the thing", autoDestination); err != nil {
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
	env := Environment{Name: "test", Repos: []Repo{
		{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/verse/auto/$task"},
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
	env := Environment{Name: "read-only", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}}}
	ws, err := m.Open(context.Background(), "p1", env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "svc", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := deliver(t, ws, task.Launch{ID: "p1"}, "done", autoDestination)
	if err != nil || len(d.Staged) != 0 || len(d.Loose) != 0 {
		t.Fatalf("delivery = %+v (%v) — a read-only environment pushes nowhere and stages nothing", d, err)
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
		{Name: "svc", Repo: intended, Base: "main", Push: "refs/heads/verse/auto/$task"},
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

	if _, err := deliver(t, ws, task.Launch{ID: "p1"}, "work", autoDestination); err != nil {
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
		{Name: "svc", Repo: svc, Base: "main", Push: "refs/heads/verse/auto/$task"},
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
	// One being changed, one only read: the asymmetry the sample is meant to demonstrate, and
	// the one checkDelivering enforces — a closing delivery is ONE action, so a second repo
	// with somewhere to send work would be silently held.
	var delivering int
	for _, r := range multi.Repos {
		if len(r.Dests()) > 0 {
			delivering++
		}
	}
	if delivering != 1 {
		t.Errorf("verse-dev has %d repos with destinations; the sample should show exactly one",
			delivering)
	}
}

// `push:` and a destination named `auto` are one thing spelled two ways, and the ref they
// resolve to is the SAME STRING.
//
// That is the whole of the first step, and byte-identity is the assertion because the ref is
// computed on both sides of a process boundary: the child names the ref its delivery gate
// matches on and the parent writes it, so a resolution that differed by anything at all would
// leave a gate matching a ref nobody pushes. See childConfig.Bases.
func TestADestinationNamedAutoIsWhatPushAlwaysMeant(t *testing.T) {
	const ref = "refs/heads/verse/auto/$sha/$task"
	head := "7281b71624be224b6f9b47530353077b1bb7f62a"
	branch := autoBranchPrefix + "comment-drift@x"

	short := &Tree{Repo: Repo{Name: "verse", Repo: "…", Base: "main", Push: ref}, Head: head}
	long := &Tree{Head: head, Repo: Repo{Name: "verse", Repo: "…", Base: "main",
		Destinations: []Destination{{Name: autoDestination, Ref: ref}}}}

	want := "refs/heads/verse/auto/7281b716/comment-drift@x"
	for what, tree := range map[string]*Tree{"push:": short, "destinations:": long} {
		if got := tree.PushTarget(branch); got != want {
			t.Errorf("%s resolves to %q, want %q", what, got, want)
		}
		if got := tree.DestRef(autoDestination, branch); got != want {
			t.Errorf("%s by name resolves to %q, want %q", what, got, want)
		}
		// A name the environment did not declare is nowhere, which is what refuses it before
		// anything is gated and before any credential is near it.
		if got := tree.DestRef("main", branch); got != "" {
			t.Errorf("%s resolved an undeclared destination to %q", what, got)
		}
	}
}

// Every check about destinations is at STARTUP, because each of them is a request that would
// otherwise resolve to nothing at the moment a task tries to deliver — after the work exists
// and with the run already over.
func TestDestinationsAreCheckedWhenTheConfigIsRead(t *testing.T) {
	base := Repo{Name: "svc", Repo: ".", Base: "main"}
	with := func(r Repo) *Config {
		return &Config{Environments: []Environment{{Name: "e", Repos: []Repo{r}}}}
	}
	dir := t.TempDir()
	for name, cfg := range map[string]*Config{
		"both spellings at once": with(Repo{Name: base.Name, Repo: base.Repo, Base: base.Base,
			Push:         "refs/heads/a",
			Destinations: []Destination{{Name: autoDestination, Ref: "refs/heads/b"}}}),
		"a destination with no name": with(Repo{Name: base.Name, Repo: base.Repo, Base: base.Base,
			Destinations: []Destination{{Ref: "refs/heads/a"}}}),
		"a destination with no ref": with(Repo{Name: base.Name, Repo: base.Repo, Base: base.Base,
			Destinations: []Destination{{Name: "main"}}}),
		"two destinations with one name": with(Repo{Name: base.Name, Repo: base.Repo, Base: base.Base,
			Destinations: []Destination{{Name: "main", Ref: "refs/heads/a"}, {Name: "main", Ref: "refs/heads/b"}}}),
	} {
		path := filepath.Join(dir, safeSeg(name)+".tony")
		blob, err := codec.Marshal(cfg, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, blob, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%s loaded — it would fail at the first delivery instead", name)
		}
	}
}

// The DEPLOYED config loads, and every check this runner makes at startup passes on it.
//
// It is a different file from the one beside this program and it is the one that matters: the
// example is read, the deployment is run. Nothing checked it, and every check added here fails
// the same way — the runner refuses to start rather than accepting work it cannot do, so a
// config that does not load is a pod that crashloops and a drive that stops with no charter to
// blame. Cheaper to find here.
//
// Read out of the ConfigMap by hand rather than through a yaml library, because the whole of
// what is needed is "the block under this key, dedented", and adding a dependency to this
// example to get it would be the larger change.
func TestTheDeployedConfigLoads(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "coderunner", "coderunner.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const key = "coderunner.tony: |"
	i := strings.Index(string(blob), key)
	if i < 0 {
		t.Fatalf("no %q in the deployment — this test is looking in the wrong place", key)
	}
	body := string(blob)[i+strings.Index(string(blob)[i:], "\n")+1:]
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		if !strings.HasPrefix(line, "    ") {
			break // the block ended: back at the ConfigMap's own indentation
		}
		out = append(out, strings.TrimPrefix(line, "    "))
	}
	path := filepath.Join(t.TempDir(), "deployed.tony")
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("the deployed config does not load: %v", err)
	}
	if _, err := c.Environment(""); err != nil {
		t.Errorf("its default environment does not resolve: %v", err)
	}
	// The merge charter's prerequisite: somewhere with a consequence to send work, and the tool
	// to ask for it.
	merge, err := c.Environment("verse-merge")
	if err != nil {
		t.Fatalf("verse-merge: %v", err)
	}
	if _, ok := merge.Repos[0].Dest("main"); !ok {
		t.Error("verse-merge declares no `main` destination, so comment-drift.tony cannot merge")
	}
	if !slices.Contains(merge.Tools, pushToolName) {
		t.Errorf("verse-merge does not offer %q, so its destinations are inert", pushToolName)
	}

	// And the other half of the split, which is the half that erodes. `main` and `push` could
	// have been two more lines on the FIXING environment, and the cost would have been paid
	// somewhere else: it is shared, its charters spawn with `vector: auto`, and the containment
	// would then be "every charter remembers to narrow its own spawn" rather than anything the
	// runner holds. What holds it is that a task in verse-fix cannot ask for a destination at
	// all, and there is no consequential one there to ask for.
	fix, err := c.Environment("verse-fix")
	if err != nil {
		t.Fatalf("verse-fix: %v", err)
	}
	if slices.Contains(fix.Tools, pushToolName) {
		t.Errorf("verse-fix offers %q, so a fix task can reach whatever it declares — the split "+
			"is what stops that, and this is where it comes undone", pushToolName)
	}
	for _, d := range fix.Repos[0].Dests() {
		if d.Name != autoDestination {
			t.Errorf("verse-fix declares destination %q — a fix is a PROPOSAL, and somewhere with "+
				"a consequence is a different environment", d.Name)
		}
	}
}

// An environment that can be approved to push somewhere it has no route to is refused when the
// config is read.
//
// This is the one half of the operational story a program can hold for itself. The TASK does the
// pushing now, so the forge has to be on the egress allowlist — and without this check the
// failure lands at the worst possible moment: after the work is done, after somebody approved
// it, as a connection error from inside a sandbox. The other half (`receive.denyNonFastForwards`
// and `receive.denyDeletes`) is the receiving end's, fails open, and cannot be checked from here
// at all, which is why it is written into the shipped configs where somebody will read it.
func TestAnEnvironmentWithNoRouteToItsForgeIsRefused(t *testing.T) {
	env := Environment{
		Name:   "e",
		Repos:  []Repo{{Name: "svc", Repo: "https://github.com/signadot/verse", Base: "main"}},
		Egress: []string{"api.portkey.ai", "proxy.golang.org"},
	}
	// No destinations: nothing leaves, so no route is owed.
	if err := checkReachable(env); err != nil {
		t.Fatalf("a read-only environment was refused a route it does not need: %v", err)
	}
	env.Repos[0].Push = "refs/heads/verse/auto/$task"
	err := checkReachable(env)
	if err == nil {
		t.Fatal("an environment that may push to github.com with no route there loaded")
	}
	if !strings.Contains(err.Error(), "github.com") {
		t.Errorf("the refusal does not name what is missing from the allowlist: %v", err)
	}
	env.Egress = append(env.Egress, "github.com")
	if err := checkReachable(env); err != nil {
		t.Fatalf("the forge is on the allowlist and it was still refused: %v", err)
	}
	// A local path is a route nobody needs, which is what every test in this file runs on.
	env.Repos[0].Repo, env.Egress = t.TempDir(), []string{"api.portkey.ai"}
	if err := checkReachable(env); err != nil {
		t.Errorf("a local remote was asked for an egress rule: %v", err)
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

// deliver drives the three steps a push is, in one process — commit, stage, push — and returns
// what the runner's half did and what the push said.
//
// The production path runs those three across a process boundary: the TASK commits (push.go's
// push), the RUNNER stages when it releases the credential (Workspace.Stage), and the task
// pushes with what it was given. This helper is the same three calls with the boundary taken out,
// which is what lets a test drive git against real repositories without a child process. It is
// deliberately not a function the runner has: a single `Deliver` that did all three is exactly
// what step 4 of the plan took apart, and reintroducing one here for the tests' convenience would
// leave a second answer to "what happens when work leaves".
func deliver(t *testing.T, ws *Workspace, l task.Launch, result, dest string) (Delivery, error) {
	t.Helper()
	tree, target := ws.Destination(dest)
	if tree == nil {
		return Delivery{}, nil
	}
	work, err := tree.commitWork(l, result)
	if err != nil {
		return Delivery{Err: err, Loose: []string{tree.Repo.Name}}, err
	}
	if !work {
		return Delivery{}, nil
	}
	d := ws.Stage(context.Background(), l.ID, tree, target)
	if d.Err != nil {
		return d, d.Err
	}
	if err := tree.pushAs(context.Background(), ws.Branch, target, nil); err != nil {
		return d, err
	}
	// What the runner does after a task ends: the sweep asks the remote and drops what landed.
	if _, err := ws.mirrors.Reflect(context.Background(), tree.Repo, nil); err != nil {
		return d, err
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

// A command turn is gated as a delegation and is NOT a sub-task spawn.
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
	d := &commandDriver{name: "claude", prompt: "work"}
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
	pushes := Environment{Name: "p", Repos: []Repo{
		{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/verse/auto/$task"},
	}}
	if !(&Workspace{Env: pushes, Trees: []*Tree{{Repo: pushes.Repos[0]}}}).Deliverable() {
		t.Error("an environment with a push target reports nothing to deliver")
	}
	// Read-only: nothing was ever going to leave, so removing it destroys nothing.
	ro := Repo{Name: "svc", Repo: origin, Base: "main"}
	if (&Workspace{Trees: []*Tree{{Repo: ro}}}).Deliverable() {
		t.Error("a read-only environment claims it delivers")
	}

	ws, err := m.Open(context.Background(), "p1", pushes, t.TempDir())
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
	d, perr := deliver(t, ws, task.Launch{ID: "p1"}, "work", autoDestination)
	if perr == nil {
		t.Fatal("expected the push to fail once its remote was gone")
	}
	if d.Err != nil {
		t.Fatalf("staging failed too, so this proves nothing about the mirror: %v", d.Err)
	}
	want := stagedRef(stagePending, "p1", "svc", "refs/heads/verse/auto/p1")
	if len(d.Staged) != 1 || d.Staged[0] != want {
		t.Fatalf("Staged = %v, want [%s] — the work has nowhere to wait", d.Staged, want)
	}
	// pending, not held: this delivery was authorized, so a retry may complete it.
	if !strings.Contains(d.Staged[0], "refs/pending/") {
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
	if msg := commitAt(t, m.dirFor(origin), want); !strings.Contains(msg, "Verse-Task: p1") {
		t.Fatalf("the mirror does not hold the work at %s: %q", want, msg)
	}
}

// An environment's `tools` is a FLOOR, and withholding the shell is the containment available
// without a kernel.
//
// read_file/list_dir/write_file are confined to the workspace by harness.safePath; run_bash gets
// the root as a working directory and nothing else, so it is the one tool that can leave. An
// environment that does not offer it cannot be escaped through it whatever the agent decides —
// which is a stronger statement than any prompt, and the only one available on a machine with
// neither runc nor bwrap.
func TestToolFloorIsCheckedAtStartup(t *testing.T) {
	if _, err := LoadConfig("coderunner.tony"); err != nil {
		t.Fatalf("the shipped config: %v", err)
	}
	// A tool the harness does not have is refused when the runner starts, not at the first
	// firing — the same rule harness.selectTools applies, moved earlier.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.tony")
	if err := os.WriteFile(bad, []byte(`environments:
- name: e
  repos:
  - {name: r, repo: ".", base: main}
  tools: [read_file, run_bash_but_spelled_wrong]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := func() error { _, e := LoadConfig(bad); return e }()
	if err == nil || !strings.Contains(err.Error(), "does not have") {
		t.Fatalf("a misspelled tool in a floor was accepted: %v", err)
	}
}

// The drive's own environment withholds the shell, which is the property that made this
// runner safe to point at a real checkout again.
func TestDriveEnvironmentOffersNoShell(t *testing.T) {
	c, err := LoadConfig("../../../working/verse-dev/coderunner.tony")
	if err != nil {
		t.Skipf("drive config not readable from here: %v", err)
	}
	e, err := c.Environment("")
	if err != nil {
		t.Fatal(err)
	}
	if e.Driver != "llm" {
		t.Errorf("driver = %q; a foreign agent cannot be gated per action", e.Driver)
	}
	if len(e.Tools) == 0 {
		t.Fatal("no tool floor: the agent gets every tool, including a shell")
	}
	if slices.Contains(e.Tools, "run_bash") {
		t.Error("the drive environment offers run_bash — the one tool that can leave the workspace")
	}
	for _, want := range []string{"read_file", "write_file"} {
		if !slices.Contains(e.Tools, want) {
			t.Errorf("the floor withholds %s, which the work needs", want)
		}
	}
}

// A command that declares no way to resume gets one turn, and says so rather than dropping a
// steer or looping on a contract it can never repair.
//
// Both needs are the same capability, which is why re-invoking is the one thing NOT folded away
// when the claude driver became a configured command: a driver that runs once cannot honour a
// steer (nowhere to put it) and cannot repair an answer (cannot ask again).
func TestCommandWithoutResumeCannotBeSteered(t *testing.T) {
	d := &commandDriver{name: "oneshot", cmd: Command{Argv: []string{"true"}}, started: true}
	d.Fold([]string{"try the other thing"})
	_, more, err := d.Next(context.Background(), task.StepResult{})
	if more {
		t.Fatal("a command with no resume asked for another turn it cannot take")
	}
	if err == nil || !strings.Contains(err.Error(), "no way to resume") {
		t.Fatalf("err = %v — a dropped steer must be reported, not silent", err)
	}

	// And with resume declared, the steer becomes the next turn.
	d2 := &commandDriver{name: "agent", cmd: Command{
		Argv: []string{"x"}, Resume: []string{"--resume", "$session"},
	}, started: true}
	d2.Fold([]string{"try the other thing"})
	tu, more, err := d2.Next(context.Background(), task.StepResult{})
	if err != nil || !more {
		t.Fatalf("a resumable command dropped a steer: more=%v err=%v", more, err)
	}
	if txt, _ := tu.Input["text"].(string); !strings.Contains(txt, "try the other thing") {
		t.Errorf("the next turn does not carry the steer: %q", txt)
	}
}

// argv substitution is the whole of what this runner knows about any particular program.
func TestCommandArgv(t *testing.T) {
	c := Command{
		Argv:   []string{"claude", "-p", "$prompt", "--output-format", "json"},
		Resume: []string{"--resume", "$session"},
	}
	first := c.argv("do the thing", "")
	if !slices.Equal(first, []string{"claude", "-p", "do the thing", "--output-format", "json"}) {
		t.Errorf("first turn argv = %v", first)
	}
	next := c.argv("and now this", "sess-1")
	if !slices.Contains(next, "--resume") || !slices.Contains(next, "sess-1") {
		t.Errorf("resumed argv = %v", next)
	}
}

// bwrap builds a private filesystem and cannot build a distinct principal, and the network is
// one switch rather than two leaves.
//
// The last part is why a catalogue of buildable POINTS is not the same thing as a product of
// chains: `--unshare-net` takes ingress and egress together, so bwrap builds (full, full) and
// (none, none) and nothing else. A per-leaf ceiling cannot say that, which is exactly the case
// Provides exists to catch.
func TestBwrapProvidesAndRefuses(t *testing.T) {
	b := bwrapConfiner{bin: "/nonexistent/bwrap"} // Provides is a claim, not an invocation

	if err := b.Provides(Point{"filesystem.scope": "private", "filesystem.readOnly": "yes"}); err != nil {
		t.Errorf("bwrap refuses a private filesystem, which is the whole reason it is here: %v", err)
	}
	if err := b.Provides(Point{"principal": "distinct"}); err == nil {
		t.Error("bwrap claimed a distinct principal; --unshare-user maps the caller to itself")
	}
	if err := b.Provides(Point{"network.ingress": "none", "network.egress": "none"}); err != nil {
		t.Errorf("bwrap refuses an unshared network: %v", err)
	}
	// The combination it cannot build, and the one a naive per-leaf check would accept because
	// each leaf on its own is within reach.
	err := b.Provides(Point{"network.ingress": "none", "network.egress": "full"})
	if err == nil {
		t.Fatal("bwrap accepted ingress:none with egress:full — one flag cannot do that")
	}
	if !strings.Contains(err.Error(), "together") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if err := b.Provides(Point{"network.egress": "restricted"}); err == nil {
		t.Error("bwrap claimed `restricted`, which needs a proxy rather than a flag")
	}
}

// Confine wraps the command rather than replacing it, and the workspace is the only writable
// path.
func TestBwrapConfineWrapsTheCommand(t *testing.T) {
	b := bwrapConfiner{bin: "/usr/bin/bwrap"}
	cmd := exec.Command("claude", "-p", "do the thing")
	if err := b.Confine(cmd, Point{"filesystem.scope": "private", "network.egress": "none"}, Sandbox{Dir: "/w"}); err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/usr/bin/bwrap" {
		t.Errorf("Path = %q, want the bwrap binary", cmd.Path)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--die-with-parent", "--unshare-pid", "--unshare-net",
		"--bind /w /w", "--chdir /w", "-- claude -p do the thing"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q:\n%s", want, joined)
		}
	}
	// The system is read-only; nothing binds it writable.
	if strings.Contains(joined, "--bind /usr") {
		t.Error("/usr is bound writable")
	}
	// And with the network shared, no unshare.
	cmd2 := exec.Command("x")
	_ = b.Confine(cmd2, Point{"network.egress": "full"}, Sandbox{Dir: "/w"})
	if strings.Contains(strings.Join(cmd2.Args, " "), "--unshare-net") {
		t.Error("the network was unshared for a point that asked for full egress")
	}
	// A read-only workspace binds read-only, and the bind is still a bind. This rung had no test
	// and did not work: the flag was patched into the argv by index, and the index was off by
	// one, so the source path became the word `--ro-bind`.
	cmd3 := exec.Command("x")
	_ = b.Confine(cmd3, Point{"filesystem.readOnly": "yes"}, Sandbox{Dir: "/w"})
	ro := strings.Join(cmd3.Args, " ")
	if !strings.Contains(ro, "--ro-bind /w /w") {
		t.Errorf("a read-only workspace is not bound read-only:\n%s", ro)
	}
	if strings.Contains(ro, "--bind /w") {
		t.Errorf("the workspace is also bound writable:\n%s", ro)
	}
}

// The task process is reached by an inherited DESCRIPTOR, so confining it must not disturb the
// descriptors — a confiner that rebuilt the command rather than rewriting it would drop them, and
// the symptom would be a child that starts and never answers.
func TestConfiningKeepsTheInheritedDescriptors(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "fd")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for name, c := range map[string]Confiner{
		"host":  hostConfiner{},
		"bwrap": bwrapConfiner{bin: "/usr/bin/bwrap"},
	} {
		cmd := exec.Command("coderunner", "-task")
		cmd.ExtraFiles = []*os.File{f, f}
		if err := c.Confine(cmd, Point{}, Sandbox{Dir: t.TempDir()}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(cmd.ExtraFiles) != 2 {
			t.Errorf("%s: the confiner dropped the control listener and the lifeline", name)
		}
		if !strings.Contains(strings.Join(cmd.Args, " "), "coderunner -task") {
			t.Errorf("%s: the task process is no longer what gets run: %v", name, cmd.Args)
		}
	}
}

// A confiner is named by the deployment, and an unknown or unavailable one fails at STARTUP.
func TestConfinerNamed(t *testing.T) {
	if _, err := confinerNamed("host"); err != nil {
		t.Errorf("host: %v", err)
	}
	if _, err := confinerNamed("nonsense"); err == nil {
		t.Error("an unknown confiner name was accepted")
	}
	// bwrap resolves or refuses depending on the machine; either is correct, and silence is
	// not — the point is that it never falls back to a weaker one.
	if c, err := confinerNamed("bwrap"); err != nil {
		if !strings.Contains(err.Error(), "bubblewrap is not on PATH") {
			t.Errorf("bwrap refusal does not say why: %v", err)
		}
	} else if _, ok := c.(bwrapConfiner); !ok {
		t.Errorf("-confine bwrap resolved to %T", c)
	}
}

// What the containment of a secret in a task process rests on is READ, not assumed.
//
// `principal: runner` — the strongest rung any confiner here reaches — puts the task process
// and the agent's shell under one uid, so what separates a credential in the first from the
// second is yama and nothing else. This does not enforce it, deliberately (see ptraceScope);
// what it must do is SAY something true on every host, including the ones where the answer is
// "there is no barrier here".
func TestThePtraceBarrierIsReadRatherThanAssumed(t *testing.T) {
	got := ptraceScope()
	if got == "" {
		t.Fatal("the runner says nothing about the setting every secret in a task process depends on")
	}
	// Whatever the host, the sentence has to name the thing at risk. A log line that reads as a
	// kernel trivium is one nobody acts on.
	for _, want := range []string{"secret", "task process"} {
		if !strings.Contains(got, want) {
			t.Errorf("ptraceScope() = %q, which does not say what is at stake (%q)", got, want)
		}
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
	env := Environment{Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}}}

	// Relative, from a working directory that is not where the workspace will be read from.
	rel, err := filepath.Rel(mustGetwd(t), t.TempDir())
	if err != nil {
		t.Skipf("no relative path from here to a temp dir: %v", err)
	}
	ws, err := m.Open(context.Background(), "p1", env, rel)
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

// A local remote is made ABSOLUTE when the config is read, because the side that resolves it is
// no longer the side that wrote it.
//
// The parent used to do every push and resolved a relative path against its own working
// directory — where whoever wrote the config was standing. The task pushes now, with its cwd set
// to the workspace, so the same string names two different places and only one of them exists.
// What it looks like is the whole flow working and the last step failing with `push
// refs/heads/main: repository not found`, against a repository the runner cloned from a moment
// before. Found by driving a merge, not by reading.
func TestALocalRemoteIsResolvedWhereItWasWritten(t *testing.T) {
	dir := t.TempDir()
	origin := initRepo(t)
	rel, err := filepath.Rel(mustGetwd(t), origin)
	if err != nil {
		t.Skipf("no relative path from here to the temp origin: %v", err)
	}
	write := func(repo string) *Config {
		t.Helper()
		cfg := &Config{Environments: []Environment{{
			Name:  "e",
			Repos: []Repo{{Name: "svc", Repo: repo, Base: "main", Push: "refs/heads/x/$task"}},
		}}}
		blob, err := codec.Marshal(cfg, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "c.tony")
		if err := os.WriteFile(path, blob, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := write(rel).Environments[0].Repos[0].Repo; !filepath.IsAbs(got) {
		t.Errorf("a relative local remote stayed relative (%q) — the task process resolves it "+
			"from the workspace and will not find it", got)
	}
	// A url is left exactly as written: it has no working directory to be relative to, and
	// rewriting one would be this runner editing the operator's remote.
	for _, remote := range []string{
		"https://github.com/signadot/verse",
		"git@github.com:signadot/verse.git",
		"ssh://git@github.com/signadot/verse",
	} {
		if got := write(remote).Environments[0].Repos[0].Repo; got != remote {
			t.Errorf("remote %q was rewritten to %q", remote, got)
		}
	}
}

// A contract-shaped answer becomes the commit BODY, and the subject says what ran.
//
// `git log --oneline` on main carries one of these forever: a task with an `answer:` contract
// returns a document, `firstLine` took it as the subject, and the result was
// `{"files": 1, "fixed": 1, "findings": [{"symbol": …` cut at 72 characters. The rest of the
// answer was dropped entirely — the body held nothing but trailers.
func TestAStructuredAnswerIsTheBodyNotTheSubject(t *testing.T) {
	l := task.Launch{ID: "run-1", Environment: "verse-fix", Provenance: task.Provenance{
		Chain: "comment-drift", Subject: "drift:head:head-moved@git:head:verse",
	}}
	answer := "{\"files\": 1, \"fixed\": 1, \"findings\": [{\"symbol\": \"main.shortSha\"}]}"
	msg := commitMessage(l, answer)

	subject, _, _ := strings.Cut(msg, "\n")
	if strings.Contains(subject, "{") {
		t.Errorf("the subject is the document: %q", subject)
	}
	if subject != "comment-drift on drift:head:head-moved@git:head:verse" {
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
	if got, _, _ := strings.Cut(commitMessage(l, "fixed the flaky test\n\ndetails"), "\n"); got != "fixed the flaky test" {
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
		Chain: "review-range", Subject: "verse-dev:drift:at",
	}}
	for _, answer := range []string{
		"```json\n{\"files\": 2, \"findings\": []}\n```",
		"```\n{files: 2}\n```",
		"```tony\nfiles: 2\n```",
	} {
		msg := commitMessage(l, answer)
		subject, _, _ := strings.Cut(msg, "\n")
		if strings.HasPrefix(subject, "```") {
			t.Errorf("the subject is the fence: %q", subject)
		}
		if subject != "review-range on verse-dev:drift:at" {
			t.Errorf("subject = %q, want what fired and about what", subject)
		}
		if !strings.Contains(msg, answer) {
			t.Errorf("the answer is not in the message at all: %q", msg)
		}
	}
}

// A destination that RETIRES deletes a ref the environment permits, and refuses everything else.
//
// This is the one act here that destroys something, and it is also the one where the task names
// something in the WORLD: a sweep learns stale branch names by asking the remote, so it cannot
// be handed a ref the way every other destination is. The glob is what keeps both true — the
// environment says which refs may ever be removed, the task picks inside that.
func TestARetiringDestinationDeletesOnlyWhatItPermits(t *testing.T) {
	origin := initRepo(t)
	m := NewMirrors(t.TempDir(), TokenAuth(""))
	env := Environment{Name: "e", Repos: []Repo{{
		Name: "svc", Repo: origin, Base: "main",
		Destinations: []Destination{
			{Name: autoDestination, Ref: "refs/heads/verse/auto/$task"},
			{Name: "retire", Ref: "refs/heads/verse/auto/*/*", Delete: true},
		},
	}}}
	ws, err := m.Open(context.Background(), "p1", env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Two branches on the remote: one a retiring destination covers, one it does not.
	seed := commitAt(t, origin, "refs/heads/main")
	_ = seed
	head, err := git.PlainOpen(origin)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := head.Reference(plumbing.ReferenceName("refs/heads/main"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"refs/heads/verse/auto/abc123/task-1", "refs/heads/main"} {
		if err := head.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(name), ref.Hash())); err != nil {
			t.Fatal(err)
		}
	}

	// Inside the glob: resolved, and the ref is normalised to its full form.
	tree, full := ws.Retirable("retire", "verse/auto/abc123/task-1")
	if tree == nil || full != "refs/heads/verse/auto/abc123/task-1" {
		t.Fatalf("a permitted ref did not resolve: %v %q", tree != nil, full)
	}
	// Outside it, in every direction that matters.
	for name, bad := range map[string]string{
		"a protected branch":        "main",
		"one segment short":         "verse/auto/loose",
		"another repository's area": "someone/else/branch",
		"nothing at all":            "",
	} {
		if tr, got := ws.Retirable("retire", bad); tr != nil {
			t.Errorf("%s: %q resolved to %q — the environment did not permit it", name, bad, got)
		}
	}
	// A destination that does not delete never retires, whatever ref it is handed.
	if tr, _ := ws.Retirable(autoDestination, "verse/auto/abc123/task-1"); tr != nil {
		t.Error("a delivering destination was used to delete")
	}

	// And the delete really removes it, against a real remote.
	if err := tree.deleteRef(context.Background(), full, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := head.Reference(plumbing.ReferenceName(full), true); err == nil {
		t.Error("the branch is still on the remote")
	}
	if _, err := head.Reference(plumbing.ReferenceName("refs/heads/main"), true); err != nil {
		t.Errorf("main was disturbed by retiring another branch: %v", err)
	}
}

// A retiring destination's ref is a pattern, and the two ways of writing it that would
// silently do nothing are refused when the config is read.
func TestARetiringDestinationsRefIsCheckedAtStartup(t *testing.T) {
	dir := t.TempDir()
	for name, ref := range map[string]string{
		// `$task` expands to THIS task's ref, so a sweep would retire exactly one branch: its
		// own. It would look like a sweep that found nothing.
		"a template": "refs/heads/verse/auto/$sha/$task",
		// One fixed ref is a switch, not a set — and a switch somebody pulls once by accident.
		"a single ref": "refs/heads/main",
	} {
		cfg := &Config{Environments: []Environment{{Name: "e", Repos: []Repo{{
			Name: "svc", Repo: ".", Base: "main",
			Destinations: []Destination{{Name: "retire", Ref: ref, Delete: true}},
		}}}}}
		blob, err := codec.Marshal(cfg, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, safeSeg(name)+".tony")
		if err := os.WriteFile(path, blob, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%s (%q) loaded — it would retire nothing, or too much, and say neither", name, ref)
		}
	}
}
