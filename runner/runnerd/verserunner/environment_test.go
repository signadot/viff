package verserunner

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

	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/task"
)

// sampleConfig is the coderunner example's configuration, which this runner reads.
const sampleConfig = "../../../docs/reference/examples/coderunner/coderunner.tony"

// The config shipped beside this program must load.
//
// It is the same discipline that makes the examples compile with the tree: a sample nobody
// parses goes wrong the first time a field moves — or, as happened here, the first time
// somebody writes a nested list in a syntax the format does not have — and the person who
// finds out is whoever copied it.
func TestShippedConfigLoads(t *testing.T) {
	_, c, err := Load(sampleConfig)
	if err != nil {
		t.Fatalf("the config shipped beside this program does not load: %v", err)
	}
	if _, err := c.Environment(""); err != nil {
		t.Errorf("its default environment does not resolve: %v", err)
	}
	if _, err := c.Environment("verse-dev"); err != nil {
		t.Errorf("verse-dev: %v", err)
	}
}

// deployedConfig is the runner config the deployment ships, written out of its ConfigMap to a
// file LoadConfig can read. By hand rather than through a yaml library, because the whole of
// what is needed is "the block under this key, dedented", and adding a dependency to this
// example to get it would be the larger change.
func deployedConfig(t *testing.T) string {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "coderunner", "coderunner.yaml"))
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
	return path
}

// The DEPLOYED config loads, and every check this runner makes at startup passes on it.
//
// It is a different file from the one beside this program and it is the one that matters: the
// example is read, the deployment is run. Nothing checked it, and every check added here fails
// the same way — the runner refuses to start rather than accepting work it cannot do, so a
// config that does not load is a pod that crashloops and a drive that stops with no charter to
// blame. Cheaper to find here.
func TestTheDeployedConfigLoads(t *testing.T) {
	path := deployedConfig(t)
	_, c, err := Load(path)
	if err != nil {
		t.Fatalf("the deployed config does not load: %v", err)
	}
	if _, err := c.Environment(""); err != nil {
		t.Errorf("its default environment does not resolve: %v", err)
	}
	// Every environment the charters name, and the tool each needs: delivery is the agent's,
	// and an agent without `push` delivers nothing. What each may push is the charter's, on its
	// spawn, and the runner checks it there. No route to the forge is owed: the runner pushes.
	for _, name := range []string{"verse-fix", "verse-merge", "signadot-fix", "signadot-pr"} {
		e, err := c.Environment(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !slices.Contains(e.Tools, delivery.PushToolName) {
			t.Errorf("%s does not offer %q, so a task there cannot deliver", name, delivery.PushToolName)
		}
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

// An environment's `tools` is a FLOOR, and withholding the shell is the containment available
// without a kernel.
//
// read_file/list_dir/write_file are confined to the workspace by harness.safePath; run_bash gets
// the root as a working directory and nothing else, so it is the one tool that can leave. An
// environment that does not offer it cannot be escaped through it whatever the agent decides —
// which is a stronger statement than any prompt, and the only one available on a machine with
// neither runc nor bwrap.
func TestToolFloorIsCheckedAtStartup(t *testing.T) {
	if _, _, err := Load(sampleConfig); err != nil {
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
	_, _, err := Load(bad)
	if err == nil || !strings.Contains(err.Error(), "does not have") {
		t.Fatalf("a misspelled tool in a floor was accepted: %v", err)
	}
}

// The deployed runner's default environment withholds the shell, which is the property that
// makes it safe to point at a real checkout.
func TestTheDeployedEnvironmentOffersNoShell(t *testing.T) {
	_, c, err := Load(deployedConfig(t))
	if err != nil {
		t.Fatal(err)
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

// initRepo makes a bare repository with one commit on `main`, ready to be cloned from and
// pushed to. Pure go-git, like the code under test: a test that shelled out would be checking
// that the git binary agrees with go-git rather than that this program works.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "svc.git")
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
