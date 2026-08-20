package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
			Repos:  []Repo{{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/verse/auto/$task"}},
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
		return commitOn(origin, "refs/heads/verse/auto/t1") != ""
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

// The push credential does not reach the task, and the mechanism is the process boundary rather
// than a variable somebody remembered to strip.
//
// The runner reads its git token out of its OWN environment, so a child started with the default
// environment inherits it — and `exec.Cmd.Env == nil` means inherit, which is what an empty
// allowlist produced. The harness's run_bash sets no env either, so the leak reached the task's
// shell. Two nils, one hole.
func TestTheTaskEnvironmentIsBuiltNotInherited(t *testing.T) {
	t.Setenv("CODERUNNER_GIT_TOKEN", "ghp-not-for-the-task")
	t.Setenv("ALLOWED_ONE", "yes")

	// An environment that declares nothing gets NOTHING — not everything.
	if env := buildEnv(nil, nil); env == nil {
		t.Fatal("an empty allowlist yields a nil env, which exec reads as `inherit everything` — " +
			"so the task gets the runner's whole environment including the push credential")
	}
	if got := len(buildEnv(nil, nil)); got != 0 {
		t.Errorf("an empty allowlist yielded %d variables", got)
	}
	// And one that declares a name gets that name and no other.
	env := buildEnv([]string{"ALLOWED_ONE"}, nil)
	if len(env) != 1 || env[0] != "ALLOWED_ONE=yes" {
		t.Fatalf("allowlist produced %v", env)
	}
	for _, kv := range env {
		if strings.Contains(kv, "ghp-not-for-the-task") {
			t.Fatal("the push credential is in the task's environment")
		}
	}
}

// End to end: a variable the environment did not name is not there when the agent looks.
func TestASecretOutsideTheAllowlistDoesNotReachTheAgent(t *testing.T) {
	fastLifecycle(t)
	t.Setenv("CODERUNNER_GIT_TOKEN", "ghp-not-for-the-task")
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}},
			Driver: "sh", Env: []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "env"}}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{
		ID: "t8", Prompt: "print your environment", Vector: &task.Vector{Default: task.Auto},
	}, nil)
	if st := awaitTerminal(t, h); st != task.Done {
		t.Fatalf("task ended %s (%s)", st, h.Err())
	}
	if strings.Contains(h.Result(), "ghp-not-for-the-task") {
		t.Errorf("the agent can read the runner's push credential:\n%s", h.Result())
	}
	// The agent's own environment is rebuilt by the command driver, so the check above would pass
	// even if the PROCESS around it were dirty — and that process is what the harness's run_bash
	// inherits. So assert the boundary itself, which is the thing that makes this a floor for
	// every driver rather than for one of them.
	c := r.childOf("t8")
	if c == nil {
		t.Fatal("no child")
	}
	if c.cmd.Env == nil {
		t.Fatal("the task process was started with a nil env, which exec reads as `inherit " +
			"everything` — the push credential is in the task's process tree")
	}
	for _, kv := range c.cmd.Env {
		if strings.Contains(kv, "ghp-not-for-the-task") {
			t.Errorf("the task process holds the runner's push credential: %s", kv)
		}
	}
}

// Delivery is an action of the task: gated by its vector, narrated on its read plane, and
// performed by the runner on the far side of the credential boundary.
//
// The push used to happen after the loop returned, which cost three things at once — the vector
// never saw it, the read plane never named the branch, and nothing downstream could chain off it.
// This asserts all three, plus the part that is specific to this runner: the task ASKS and the
// parent PUSHES, because the token is not on the task's side.
func TestDeliveryIsAGatedActionOfTheTask(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, deliveringConfig(origin))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := runner.HTTP(srv.URL)
	// A charter that wants to be asked before work leaves says exactly this, and could not have
	// before: `push` is the verb, and the action carrying it did not exist.
	h := rt.Spawn(task.Launch{
		ID: "d1", Prompt: "write the file",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "delivery", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	}, nil)
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision string, proceed bool, say, by string) error
	})

	var gate, exec, terminal string
	for f := range frames {
		switch {
		case f.Kind == task.EvPending && f.Tool == pushToolName:
			gate = f.Gate
			if err := res.Resolve(f.ID, true, "", "human:test"); err != nil {
				t.Fatalf("approving the delivery: %v", err)
			}
		case f.Kind == task.EvExec && f.Tool == pushToolName:
			exec = f.Text
		case f.Kind == task.EvStatus && task.Status(f.Text).Terminal():
			terminal = f.Text
		}
		if terminal != "" {
			break
		}
	}
	if gate != "delivery" {
		t.Fatal("no gate was posed for the push, so a charter cannot decline one")
	}
	if !strings.Contains(exec, "refs/heads/verse/auto/d1") {
		t.Errorf("the read plane does not name the branch the work went to: %q", exec)
	}
	if terminal != string(task.Done) {
		t.Fatalf("task ended %s (%s)", terminal, h.Err())
	}
	// The push landed, and the parent made it: the child never had the credential.
	waitFor(t, "the branch on origin", func() bool {
		return commitOn(origin, "refs/heads/verse/auto/d1") != ""
	})
	// And the workspace went away, because the work left.
	waitFor(t, "the workspace to be removed", func() bool {
		_, err := os.Stat(r.childOf("d1").ws.Root)
		return os.IsNotExist(err)
	})
}

// A refused delivery means nothing is pushed, and the work is kept where somebody can get it.
//
// This is the case that could not exist before: the push happened whatever the vector said, so
// "decline this push" was unsayable. Now it is a decision, and the consequence of the decision is
// that the runner does not act.
func TestARefusedDeliveryPushesNothingAndKeepsTheWork(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, deliveringConfig(origin))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{
		ID: "d2", Prompt: "write the file",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "delivery", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	}, nil)
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision string, proceed bool, say, by string) error
	})
	var terminal string
	for f := range frames {
		if f.Kind == task.EvPending && f.Tool == pushToolName {
			if err := res.Resolve(f.ID, false, "not this branch", "human:test"); err != nil {
				t.Fatal(err)
			}
		}
		if f.Kind == task.EvStatus && task.Status(f.Text).Terminal() {
			terminal = f.Text
			break
		}
	}
	// A refusal is a decision the task complied with, not a failure of it.
	if terminal != string(task.Done) {
		t.Errorf("a refused delivery ended the task %s, want done", terminal)
	}
	if commitOn(origin, "refs/heads/verse/auto/d2") != "" {
		t.Fatal("the work was pushed despite the gate refusing it")
	}
	// And it is still on disk: a refusal is somebody deciding where the work goes, not deciding
	// to destroy it.
	waitFor(t, "the workspace to be kept", func() bool {
		c := r.childOf("d2")
		if c == nil {
			return false
		}
		_, err := os.Stat(filepath.Join(c.ws.Root, "svc", "out.txt"))
		return err == nil
	})
}

func deliveringConfig(origin string) *Config {
	return &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Repos:  []Repo{{Name: "svc", Repo: origin, Base: "main", Push: "refs/heads/verse/auto/$task"}},
			Driver: "sh",
			Env:    []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {
			Argv: []string{"/bin/sh", "-c", "echo hello > svc/out.txt; echo done"},
		}},
	}
}

// A task that changed nothing says so, rather than reporting an empty push.
//
// The read plane carried `exec deliver: "pushed "` — a success with nothing after it — for a run
// that correctly found nothing to change. That is finding 10 in a new place: "nothing to do" and
// "something went wrong" rendering identically, on the one frame that says where the work went.
// Caught by a drive, on the first clean run.
func TestATaskThatChangedNothingSaysSo(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	cfg := deliveringConfig(origin)
	// An agent that reads and reports, and writes nothing — the shape of a verification task.
	cfg.Agents["sh"] = Command{Argv: []string{"/bin/sh", "-c", "echo no drift found"}}
	r := testRunner(t, cfg)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{
		ID: "n1", Prompt: "check it", Vector: &task.Vector{Default: task.Auto},
	}, nil)
	frames, cancel := h.Subscribe()
	defer cancel()
	var deliver, terminal string
	for f := range frames {
		if f.Kind == task.EvExec && f.Tool == pushToolName {
			deliver = f.Text
		}
		if f.Kind == task.EvStatus && task.Status(f.Text).Terminal() {
			terminal = f.Text
			break
		}
	}
	if terminal != string(task.Done) {
		t.Fatalf("task ended %s (%s)", terminal, h.Err())
	}
	if strings.HasPrefix(deliver, "pushed ") {
		t.Errorf("a task that changed nothing reported a push: %q", deliver)
	}
	if !strings.Contains(deliver, "nothing to push") {
		t.Errorf("the read plane does not say nothing was delivered: %q", deliver)
	}
	if commitOn(origin, "refs/heads/verse/auto/n1") != "" {
		t.Error("a branch appeared for work nobody did")
	}
}

// A child is addressed by a url built out of its task id, and a task id is not a hostname.
// Every id a TRIGGER spawns is `<chain>@<subject>`, so the @ makes the rest of it userinfo and
// the subject's colons are then read as a port: the url does not parse, `http.NewRequest`
// answers nil, and the supervisor's first readiness probe used to panic on it. So no
// charter-spawned task could start under the hop at all, while every hand-launched
// `run-<hex>` one could — which is why it survived the tests and was found on a deployment.
func TestChildURLSurvivesATriggerSpawnedTaskID(t *testing.T) {
	for _, id := range []string{
		"run-7818e07b47dd",
		"review@demo.pr.1",
		"comment-drift@drift.head.head-moved@git.head.verse",
		"review.seq.2@demo.pr.1",
	} {
		if _, err := http.NewRequest(http.MethodGet, "http://task."+hostSafe(id)+"/tasks", nil); err != nil {
			t.Errorf("task %q gives an unusable child url: %v", id, err)
		}
	}
}

// A task id that has FINISHED can be spawned again, because verse mints ids that repeat.
//
// A trigger's task id is `<chain>@<subject>` — deterministic on purpose, which is what makes a
// re-fire idempotent — and the engine's dedupe on it lasts only while the task is live. So a
// rule that fires twice about one subject asks this runner to run one id twice, and the second
// ask is a new run rather than a retry of the first.
//
// It used to be answered by the first: the child record outlives its process, so the spawn was
// proxied into a socket that had been unlinked with it, and came back 502. What that looked like
// on the deployment is a charter whose first review worked and whose every later one failed
// until the runner was restarted.
func TestAFinishedTaskIDRunsAgain(t *testing.T) {
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
	id := "comment-drift@drift.head.head-moved@git.head.verse"
	for i, want := range []string{"ran", "ran"} {
		h := rt.Spawn(task.Launch{ID: id, Prompt: "go", Vector: &task.Vector{Default: task.Auto}}, nil)
		if st := awaitTerminal(t, h); st != task.Done {
			t.Fatalf("run %d ended %s (%s)", i+1, st, h.Err())
		}
		if got := h.Result(); got != want {
			t.Fatalf("run %d result = %q, want %q", i+1, got, want)
		}
		// The process has to be gone before the next spawn, or this is testing the retry path
		// rather than the re-run one.
		waitFor(t, "the child process to be reaped", func() bool {
			c := r.childOf(id)
			return c != nil && !c.running()
		})
	}
}

// A push target may name the commit the work started from, because a task id alone is not
// unique over time.
//
// A charter's task id is a function of (chain, subject), so a rule whose subject is one
// long-lived entity — main's tip — mints the same id for every firing. Every fix then lands on
// one ref, each overwriting the last: invisible while the overwritten branch was merged or
// redundant, and lost work the first time it was not. Observed twice on the verse-dev drive
// before it cost anything.
//
// Both sides must agree on the expansion: the parent pushes it, the child names it in the effect
// its delivery gate matches on. So the sha travels in the child's config, and this asserts the
// two agree rather than merely that each works.
func TestPushTargetCarriesTheBaseSha(t *testing.T) {
	repo := Repo{Name: "verse", Repo: "…", Base: "main", Push: "refs/heads/verse/auto/$sha/$task"}
	parent := &Tree{Repo: repo, Head: "7281b71624be224b6f9b47530353077b1bb7f62a"}
	branch := autoBranchPrefix + "comment-drift@x"

	got := parent.PushTarget(branch)
	want := "refs/heads/verse/auto/7281b716/comment-drift@x"
	if got != want {
		t.Fatalf("parent target = %q, want %q", got, want)
	}

	// The child rebuilds its trees from what the parent handed over; a target computed there
	// must be the same string, or a gate matches a ref the push never wrote.
	child := &Tree{Repo: repo, Head: map[string]string{"verse": parent.Head}["verse"]}
	if child.PushTarget(branch) != got {
		t.Errorf("child target = %q, parent %q — a gate would match a ref nobody pushes",
			child.PushTarget(branch), got)
	}

	// And two reviews of two commits are two refs, which is the whole point.
	other := &Tree{Repo: repo, Head: "9d007068dc826d6bd2df9e01f70bd601a2d33f7d"}
	if other.PushTarget(branch) == got {
		t.Error("two commits share one ref; the second fix would overwrite the first")
	}
}

// The AGENT can propose a push, to a destination it names and to no ref it chooses — and the
// vector decides it per destination.
//
// This is what the delivery button could not do. `deliver` was one flow with no target, so
// merging a proposal into main, moving a promotion ref or retiring a stale branch had no way to
// be ASKED for; and the obvious repair, an operation per flow, puts a policy in a mechanism and
// leaves the next flow needing another one. Here the environment declares where work may go, the
// agent names one of those places, and the gate matches the ref that will actually be touched.
//
// The gate is written on `resources:` rather than on `branches:`. The plan says branches, and
// that is wrong about the mechanism rather than about the intent: `GateMatch.Branches` globs
// RepoContext.Branch — the branch the WORK is on, which is the same for every action this task
// takes — while the thing that differs between two destinations is the ref, which is exactly
// what a `gitref` resource predicate selects on.
func TestTheAgentProposesAPushToANamedDestination(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	model := fakeModel(t,
		call("write_file", `{"path":"svc/merged.txt","content":"x\n"}`),
		call(pushToolName, `{"destination":"main"}`),
		say("pushed it"))
	r := testRunner(t, destinationsConfig(origin, model))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{
		ID: "g1", Prompt: "merge it",
		// Ask about main; everything else — including the closing delivery to `auto` — runs.
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "to-main", Mode: task.Ask,
			Match: task.GateMatch{Resources: []task.ResourcePred{
				{Kind: task.KindGitRef, Pattern: "refs/heads/main"},
			}},
		}}},
	}, nil)
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision string, proceed bool, say, by string) error
	})

	var asked []string
	var terminal string
	for f := range frames {
		switch {
		case f.Kind == task.EvPending:
			asked = append(asked, f.Tool)
			if err := res.Resolve(f.ID, true, "", "human:test"); err != nil {
				t.Fatalf("approving %s: %v", f.Tool, err)
			}
		case f.Kind == task.EvStatus && task.Status(f.Text).Terminal():
			terminal = f.Text
		}
		if terminal != "" {
			break
		}
	}
	if terminal != string(task.Done) {
		t.Fatalf("task ended %s (%s)", terminal, h.Err())
	}
	// Exactly one question, about the destination the charter named. The closing delivery to
	// `auto` went through the same gate machinery and matched nothing — which is the
	// discrimination that did not exist when every push was one untargeted `deliver`.
	if len(asked) != 1 || asked[0] != pushToolName {
		t.Fatalf("questions posed = %v, want exactly one about %q", asked, pushToolName)
	}
	waitFor(t, "the agent's push to land on main", func() bool {
		return strings.Contains(commitOn(origin, "refs/heads/main"), "Verse-Task: g1")
	})
	// And the closing delivery still happened, ungated, to the destination it has always used.
	waitFor(t, "the delivery to auto", func() bool {
		return commitOn(origin, "refs/heads/verse/auto/g1") != ""
	})
	// The parent staged what the CHILD committed, and the sweep that follows a task settles it
	// rather than undoing it.
	//
	// Both halves are load-bearing and neither is obvious. The task commits and pushes now, so
	// the parent stages a commit made in another process a moment earlier — stage the wrong one
	// and the mirror holds the BASE, which is the shape of a bug this file has caught before. And
	// the sweep force-pushes every pending ref to its target, so a stale staged commit would not
	// merely be useless, it would reset main to the base after the correct push had landed.
	repo := r.cfg.Environments[0].Repos[0]
	waitFor(t, "the staged copy to settle", func() bool {
		work, err := r.mirrors.Staged(repo)
		return err == nil && len(work) == 0
	})
	if !strings.Contains(commitOn(origin, "refs/heads/main"), "Verse-Task: g1") {
		t.Fatal("the sweep after the task pushed something other than the work, so what the " +
			"parent staged was not what the task committed")
	}
}

// A destination the environment does not declare poses no question and pushes nothing.
//
// "Refused before the gate" is what that comes to in a loop where classification runs first: a
// name resolving to nowhere carries no push effect, so no capability gate can match it and
// nobody is asked to approve something that was never going to happen. The executor then says so
// in the agent's own vocabulary — the names it could have used instead.
func TestAnUndeclaredDestinationIsRefusedWithoutAsking(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	// `release` is not a destination this environment has. The model asks for it anyway, which
	// is what a model ignoring the enum in its own tool schema would do.
	model := fakeModel(t, call(pushToolName, `{"destination":"release"}`), say("could not"))
	r := testRunner(t, destinationsConfig(origin, model))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{
		ID: "g2", Prompt: "push it somewhere",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "any-push", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	}, nil)
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision string, proceed bool, say, by string) error
	})

	// The closing delivery to `auto` IS a push and is gated by this vector, so counting
	// questions would not say anything. What says it is the ORDER: the refusal of the
	// undeclared name arrives with nothing having been asked before it.
	var asked int
	var refused, terminal string
	askedFirst := false
	for f := range frames {
		switch {
		case f.Kind == task.EvPending:
			asked++
			_ = res.Resolve(f.ID, true, "", "human:test")
		case f.Kind == task.EvExec && f.Tool == pushToolName && refused == "":
			refused, askedFirst = f.Text, asked > 0
		case f.Kind == task.EvStatus && task.Status(f.Text).Terminal():
			terminal = f.Text
		}
		if terminal != "" {
			break
		}
	}
	if terminal != string(task.Done) {
		t.Fatalf("task ended %s (%s)", terminal, h.Err())
	}
	if askedFirst {
		t.Fatal("somebody was asked to approve a push to a destination that does not exist")
	}
	if !strings.Contains(refused, "no destination") || !strings.Contains(refused, "main") {
		t.Errorf("the refusal does not say what the agent could have asked for instead: %q", refused)
	}
	if commitOn(origin, "refs/heads/release") != "" {
		t.Fatal("a ref the environment never declared was written")
	}
}

// destinationsConfig is an environment with two places work may go: `auto`, which is what the
// closing delivery means, and `main`, which is what a merge proposal wants.
func destinationsConfig(origin, model string) *Config {
	return &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e",
			Repos: []Repo{{Name: "svc", Repo: origin, Base: "main", Destinations: []Destination{
				{Name: autoDestination, Ref: "refs/heads/verse/auto/$task"},
				{Name: "main", Ref: "refs/heads/main"},
			}}},
			Driver: driverLLM,
			Tools:  []string{"read_file", "write_file", pushToolName},
			Env:    []string{"PATH"},
		}},
		LLM: LLM{BaseURL: model, Model: "fake", APIKeyEnv: "PATH"},
	}
}

// fakeModel is a Chat Completions endpoint that replies with a scripted turn each time it is
// called, and repeats the last one after that.
//
// A fake rather than a real model, because what is under test is this runner's half: that a tool
// call becomes a derived, gated action, and what the runner does when one is approved. Nothing
// here turns on a model's judgement, so borrowing one would add a round trip and a source of
// flake and check nothing extra.
func fakeModel(t *testing.T, turns ...string) string {
	t.Helper()
	var mu sync.Mutex
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := turns[min(n, len(turns)-1)]
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// call is a turn in which the model calls one tool; say is a turn in which it stops and speaks,
// which is the only way it can stop.
func call(tool, args string) string {
	return fmt.Sprintf(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant",`+
		`"tool_calls":[{"id":"c1","type":"function","function":{"name":%q,"arguments":%q}}]}}]}`,
		tool, args)
}

func say(text string) string {
	return fmt.Sprintf(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":%q}}]}`, text)
}

// No approval, no credential — and the gate is the ONLY path to one.
//
// This is what makes the vector load-bearing rather than advisory. The task holds no git
// credential at all: its trees are opened without one, and the secret arrives over the channel
// the parent answers, in reply to a request the loop only makes after a gate has said yes about
// that exact destination. A refusal is therefore not "the push was skipped", it is "the ability
// to push never existed".
func TestApprovalIsWhatReleasesTheCredential(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	before := commitOn(origin, "refs/heads/main")
	model := fakeModel(t,
		call("write_file", `{"path":"svc/merged.txt","content":"x\n"}`),
		call(pushToolName, `{"destination":"main"}`),
		say("I was not allowed to"))
	r := testRunner(t, destinationsConfig(origin, model))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{
		ID: "g3", Prompt: "merge it",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "to-main", Mode: task.Ask,
			Match: task.GateMatch{Resources: []task.ResourcePred{
				{Kind: task.KindGitRef, Pattern: "refs/heads/main"},
			}},
		}}},
	}, nil)
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision string, proceed bool, say, by string) error
	})

	var terminal string
	for f := range frames {
		switch {
		case f.Kind == task.EvPending && f.Tool == pushToolName:
			if err := res.Resolve(f.ID, false, "not main, not today", "human:test"); err != nil {
				t.Fatal(err)
			}
		case f.Kind == task.EvStatus && task.Status(f.Text).Terminal():
			terminal = f.Text
		}
		if terminal != "" {
			break
		}
	}
	// A refusal is a decision the task complied with, not a failure of it.
	if terminal != string(task.Done) {
		t.Errorf("a refused push ended the task %s, want done", terminal)
	}
	if got := commitOn(origin, "refs/heads/main"); got != before {
		t.Fatal("main moved despite the gate refusing the push")
	}
	// The closing delivery is ungated here, so the work still leaves — to the destination it has
	// always used, and not to the one somebody declined.
	waitFor(t, "the delivery to auto", func() bool {
		return commitOn(origin, "refs/heads/verse/auto/g3") != ""
	})
}

// A destination gets exactly the credential it names, and a missing one is refused rather than
// released empty.
//
// The last part is not fussiness. An empty credential reaches the far side as an authentication
// failure, so a runner that released one would have the task report the FORGE's opinion of this
// runner's configuration — at the end of a run, after the work is done and the gate has already
// approved it.
func TestADestinationGetsTheCredentialItNames(t *testing.T) {
	t.Setenv("GH_MAIN", "pat-for-main")
	env := Environment{
		Name:        "e",
		Credentials: []Credential{{Name: "gh-main", Env: "GH_MAIN"}, {Name: "gh-gone", Env: "GH_GONE"}},
		Repos: []Repo{{Name: "svc", Repo: ".", Base: "main", Destinations: []Destination{
			{Name: autoDestination, Ref: "refs/heads/verse/auto/$task"},
			{Name: "main", Ref: "refs/heads/main", Credential: "gh-main"},
			{Name: "gone", Ref: "refs/heads/gone", Credential: "gh-gone"},
		}}},
	}
	r := &Runner{token: "the-runners-own"}

	if got, err := r.credentialFor(env, env.Repos[0], "main"); err != nil || got != "pat-for-main" {
		t.Errorf("main got %q (%v), want the credential it names", got, err)
	}
	// Naming none falls back to the runner's own token, which is what every environment relied
	// on while the parent did the pushing.
	if got, err := r.credentialFor(env, env.Repos[0], autoDestination); err != nil || got != "the-runners-own" {
		t.Errorf("auto got %q (%v), want the runner's own token", got, err)
	}
	if _, err := r.credentialFor(env, env.Repos[0], "gone"); err == nil {
		t.Error("a credential whose variable is unset was released empty")
	} else if !strings.Contains(err.Error(), "GH_GONE") {
		t.Errorf("the refusal does not name the variable to set: %v", err)
	}
	if _, err := r.credentialFor(env, env.Repos[0], "release"); err == nil {
		t.Error("a destination this repository does not declare was given a credential")
	}
}

// A call list runs on the far side of the hop, with this runner's workspace under it.
//
// It is the whole of step 3 from the outside: the charter writes the actions, the child runs
// them in the checkout, and the runner cannot tell that no model was involved. The command
// writes a file so the assertion is about the WORKSPACE rather than about an exit code — a
// driver that proposed nothing would also exit zero.
func TestACallListRunsInTheChildToo(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}},
			Driver: task.DriverCalls, Env: []string{"PATH"},
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{
		ID: "t-calls", Vector: &task.Vector{Default: task.Auto},
		Calls: []task.Call{{Tool: "run_bash",
			Input: map[string]any{"command": "echo listed > svc/ran.txt"}}},
	}, nil)
	if st := awaitTerminal(t, h); st != task.Done {
		t.Fatalf("a call list across the hop ended %s: %s", st, h.Err())
	}
	if _, err := os.Stat(filepath.Join(r.workspaces, "t-calls", "svc", "ran.txt")); err != nil {
		t.Fatalf("the call did not run in the workspace: %v", err)
	}
}

// A `calls` spawn carrying no calls fails the SPAWN rather than completing having done nothing.
// The engine checks this before the spawn crosses; this is the far end checking too, because a
// task can be posted straight at a runner without passing through a charter.
func TestAnEmptyCallListFailsAcrossTheHop(t *testing.T) {
	fastLifecycle(t)
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e", Repos: []Repo{{Name: "svc", Repo: origin, Base: "main"}},
			Driver: task.DriverCalls, Env: []string{"PATH"},
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := runner.HTTP(srv.URL).Spawn(task.Launch{
		ID: "t-nolist", Prompt: "please merge the branch and push it",
		Vector: &task.Vector{Default: task.Auto},
	}, nil)
	if st := awaitTerminal(t, h); st != task.Failed {
		t.Fatalf("a calls spawn with no calls ended %s", st)
	}
	if e := h.Err(); !strings.Contains(e, "calls") {
		t.Errorf("the refusal does not say what it could not read: %q", e)
	}
}
