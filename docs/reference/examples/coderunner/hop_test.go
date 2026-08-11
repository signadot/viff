package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// TestMain is what makes the hop testable at all: `coderunner -task` is a mode of THIS program,
// so under `go test` the child is this test binary, re-executed with the same argument the real
// runner passes. No build step, no fixture binary, and the code under test is the code that
// ships.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "-task" {
		if err := runTaskProcess(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A task runs in its own process, and everything about it — the read plane, the result, the
// delivery — comes back through the contract as if it had not.
//
// This is the whole claim of the hop, so it is asserted end to end: a real launch over
// runner.HTTP against the parent's own handler, a real child process, a real push landing on a
// real origin.
func TestATaskRunsInItsOwnProcessAndDelivers(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Repos:  []Repo{{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/agent/$task"}},
			Driver: "sh",
			Env:    []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {
			Argv: []string{"/bin/sh", "-c", "echo hello > svc/out.txt; echo done"},
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := runner.HTTP(srv.URL)
	h := rt.Spawn(task.Launch{
		ID: "t1", Prompt: "write the file",
		// Auto: this test is about the hop, not about the gate. The gate over the hop is the
		// next test.
		Vector: &task.Vector{Default: task.Auto},
	}, nil)

	if st := awaitTerminal(t, h); st != task.Done {
		t.Fatalf("task ended %s (%s)", st, h.Err())
	}
	// The program's last word travelled back as the task's result, which is what a charter's
	// `$result` derefs.
	if got := h.Result(); got != "done" {
		t.Errorf("result = %q, want %q", got, "done")
	}
	// And the work left, pushed by the PARENT under a credential the child never had.
	waitFor(t, "the branch to land on origin", func() bool {
		return commitOn(origin, "refs/heads/agent/t1") != ""
	})
}

// A gate raised inside the child is answerable from outside it.
//
// This is the property the hop had to preserve and the one it could plausibly have broken:
// gating is not an in-process call, it is a question on the read plane and an answer on the
// control plane, and both now cross a process boundary before they reach verse.
func TestAGateInsideTheChildIsAnsweredFromOutside(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Repos:  []Repo{{Name: "svc", Repo: origin, Base: "main"}},
			Driver: "sh",
			Env:    []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "echo ran"}}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := runner.HTTP(srv.URL)
	// No vector: the command driver's own default asks before it hands work to a program.
	h := rt.Spawn(task.Launch{ID: "t2", Prompt: "run it"}, nil)
	frames, cancel := h.Subscribe()
	defer cancel()

	res, ok := h.(interface {
		Resolve(decision string, proceed bool, say, by string) error
	})
	if !ok {
		t.Fatal("the remote handle cannot answer a gate, so nothing can")
	}
	var status task.Status
	var asked bool
	for f := range frames {
		switch {
		case f.Kind == task.EvPending:
			asked = true
			if err := res.Resolve(f.ID, true, "", "human:test"); err != nil {
				t.Fatalf("answering the gate across the hop: %v", err)
			}
		case f.Kind == task.EvStatus && task.Status(f.Text).Terminal():
			status = task.Status(f.Text)
		}
		if status != "" {
			break
		}
	}
	if !asked {
		t.Fatal("no gate was posed, so this test proved nothing about answering one")
	}
	if status != task.Done {
		t.Fatalf("task ended %s (%s)", status, h.Err())
	}
	if got := h.Result(); got != "ran" {
		t.Errorf("result = %q — the approved invocation did not produce it", got)
	}
}

// A task process that dies becomes a FAILED task, with the exit status in it.
//
// An in-process task could not have this failure; a child can, and a runner that noticed only by
// its event stream going quiet would leave verse waiting forever on work nobody is doing. The
// parent has to be the one that says so, because by then there is nothing else left to ask.
func TestADeadTaskProcessBecomesAFailedTask(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}},
			Driver: "sh", Env: []string{"PATH"},
		}},
		// Blocked on its gate forever, so the process is killed mid-task rather than racing
		// its own completion.
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "echo ran"}}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := runner.HTTP(srv.URL)
	h := rt.Spawn(task.Launch{ID: "t3", Prompt: "wait at the gate"}, nil)
	frames, cancel := h.Subscribe()
	defer cancel()
	for f := range frames {
		if f.Kind == task.EvPending {
			break
		}
	}
	c := r.childOf("t3")
	if c == nil {
		t.Fatal("no child for a task that is running")
	}
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// The contract keeps answering about the task after the process that ran it is gone —
	// which is the half a bare proxy cannot do.
	lister, _ := rt.(task.Lister)
	waitFor(t, "the runner to report the task failed", func() bool {
		snap, err := lister.TaskSnapshot("t3")
		return err == nil && snap.Status == task.Failed && strings.Contains(snap.Err, "exited")
	})
	snap, _ := lister.TaskSnapshot("t3")
	if !strings.Contains(snap.Err, "terminal status") {
		t.Errorf("failure detail %q does not say what was missing", snap.Err)
	}
}

// The control socket is not somewhere the task can reach.
//
// It is the one piece of the hop that is not a property of the contract: a unix socket the child
// could dial by name would be a control plane the AGENT could dial by name, and answering its own
// gates is the whole of what the vector is for. The child gets a listening descriptor instead,
// and the only path lives out here.
func TestTheControlPlaneIsNotInsideTheWorkspace(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}},
			Driver: "sh", Env: []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "echo ran"}}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{ID: "t4", Prompt: "sit at the gate"}, nil)
	frames, cancel := h.Subscribe()
	defer cancel()
	for f := range frames {
		if f.Kind == task.EvPending {
			break
		}
	}
	c := r.childOf("t4")
	if c == nil {
		t.Fatal("no child for a task that is running")
	}
	if strings.HasPrefix(c.sock, c.ws.Root) {
		t.Errorf("the control socket %s is inside the workspace %s — the task can dial its own "+
			"control plane", c.sock, c.ws.Root)
	}
	if strings.HasPrefix(c.sock, r.workspaces) {
		t.Errorf("the control socket %s is under the workspace root %s", c.sock, r.workspaces)
	}
	// And it is short enough to bind: a unix path over about a hundred bytes cannot be listened
	// on at all, which is why it does not live beside the data.
	if len(c.sock) > 100 {
		t.Errorf("control socket path is %d bytes: %s", len(c.sock), c.sock)
	}
}

// A launch this runner will not host is REFUSED, and the refusal reaches the caller as a refusal.
//
// Before the hop these came back as a task that failed immediately, which reads downstream as
// work that was attempted. runner.HTTP turns a non-2xx spawn into a failed handle carrying the
// runner's own words, so saying no on the wire is now the accurate thing.
func TestAnUnhostableLaunchIsRefusedOnTheWire(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}},
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := runner.HTTP(srv.URL)
	h := rt.Spawn(task.Launch{ID: "t5", Prompt: "x", Environment: "nowhere"}, nil)
	if err := h.Err(); !strings.Contains(err, "no environment") {
		t.Errorf("spawn into an unknown environment reported %q", err)
	}
	h = rt.Spawn(task.Launch{ID: "t6", Prompt: "x", Driver: "nope"}, nil)
	if err := h.Err(); !strings.Contains(err, "no driver") {
		t.Errorf("spawn with an unknown driver reported %q", err)
	}
	if r.running() != 0 {
		t.Error("a refused launch started a process anyway")
	}
}

// testRunner builds a parent whose child is this test binary.
func testRunner(t *testing.T, cfg *Config) *Runner {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Short, because a unix socket path has about a hundred bytes to live in and a temp dir
	// under a test name does not leave many.
	control, err := os.MkdirTemp("", "crt-")
	if err != nil {
		t.Fatal(err)
	}
	logf, err := os.Create(filepath.Join(t.TempDir(), "runner.log"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r := &Runner{
		cfg:        cfg,
		mirrors:    NewMirrors(filepath.Join(root, "mirrors"), TokenAuth("")),
		workspaces: filepath.Join(root, "workspaces"),
		conf:       hostConfiner{},
		log:        logf,
		self:       self,
		control:    control,
		children:   map[string]*child{},
	}
	t.Cleanup(func() {
		r.Shutdown()
		if t.Failed() {
			if out, err := os.ReadFile(logf.Name()); err == nil && len(out) > 0 {
				t.Logf("runner log:\n%s", out)
			}
		}
		logf.Close()
	})
	return r
}

// fastLifecycle shrinks the waits that exist for a human reader, so a test exercises the whole
// lifecycle rather than a prefix of it.
func fastLifecycle(t *testing.T) {
	t.Helper()
	linger, grace := lingerAfterTerminal, stopGrace
	lingerAfterTerminal, stopGrace = 300*time.Millisecond, 2*time.Second
	t.Cleanup(func() { lingerAfterTerminal, stopGrace = linger, grace })
}

func awaitTerminal(t *testing.T, h task.Handle) task.Status {
	t.Helper()
	frames, cancel := h.Subscribe()
	defer cancel()
	for f := range frames {
		if f.Kind == task.EvStatus && task.Status(f.Text).Terminal() {
			return task.Status(f.Text)
		}
	}
	t.Fatal("the read plane closed without a terminal status")
	return ""
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// commitOn is commitAt without a *testing.T, so it can be polled.
func commitOn(repoDir, ref string) string {
	r, err := git.PlainOpen(repoDir)
	if err != nil {
		return ""
	}
	h, err := r.Reference(plumbing.ReferenceName(ref), true)
	if err != nil {
		return ""
	}
	c, err := r.CommitObject(h.Hash())
	if err != nil {
		return ""
	}
	return c.Message
}

// The `llm` driver runs on the far side of the hop too, and its failures come back as failures.
//
// It is the branch the command driver's tests cannot reach: harness.Build, the environment's tool
// floor, and model credentials that arrived on stdin rather than from an environment variable.
// The model here is deliberately unreachable, because what is being checked is that the loop runs
// in the child and reports what happened — not what a model would say.
func TestTheLLMDriverRunsInTheChildToo(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}},
			Driver: driverLLM, Tools: []string{"read_file", "list_dir"},
		}},
		// Port 1: refused immediately, so this test costs a connect and not a timeout.
		LLM: LLM{BaseURL: "http://127.0.0.1:1", Model: "none"},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{
		ID: "t7", Prompt: "read something", Vector: &task.Vector{Default: task.Auto},
	}, nil)
	if st := awaitTerminal(t, h); st != task.Failed {
		t.Fatalf("a task whose model is unreachable ended %s", st)
	}
	if e := h.Err(); e == "" {
		t.Error("the failure crossed the hop with nothing to say about it")
	}
}
