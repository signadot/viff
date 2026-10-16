package verserunner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/runner/runnerd/workspace"
	"github.com/signadot/verse/task"
)

// A task runs in its own process, and everything about it — the read plane, the result, what
// became of its work — comes back through the contract as if it had not.
//
// This is the whole claim of the hop, so it is asserted end to end: a real launch over
// client.HTTP against the parent's own handler, a real child process, a real repository. The
// program here has no tools, so nothing it did can leave: delivery is a tool, and what a task did
// not push is HELD in the runner's mirror rather than lost with its workspace.
func TestATaskRunsInItsOwnProcessAndItsWorkIsHeld(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: "sh",
			Env:    []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {
			Argv: []string{"/bin/sh", "-c", "echo hello > svc/out.txt; echo done"},
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := client.HTTP(srv.URL)
	h := rt.Spawn(task.Launch{
		ID: "t1", Repos: rw(origin), Prompt: "write the file",
		Vector: &task.Vector{Default: task.Auto},
	})

	if st := awaitTerminal(t, h); st != task.Done {
		t.Fatalf("task ended %s (%s)", st, h.Err())
	}
	// The program's last word travelled back as the task's result, which is what a charter's
	// `$result` derefs.
	if got := h.Result(); got != "done" {
		t.Errorf("result = %q, want %q", got, "done")
	}
	// Nothing left: no tool asked to push, so no branch appeared.
	if commitOn(origin, "refs/heads/verse/auto/t1") != "" {
		t.Fatal("work left the sandbox without a push being asked for")
	}
	// And the work is findable: held in the mirror, listed by the descriptor.
	waitFor(t, "the work to be held in the mirror", func() bool {
		held, err := r.mirrors.Staged(origin)
		return err == nil && len(held) == 1 && held[0].Task == "t1" && strings.Contains(held[0].Why, "authorized")
	})
}

// A gate raised inside the child is answerable from outside it.
//
// This is the property the hop had to preserve and the one it could plausibly have broken:
// gating is not an in-process call, it is a question on the read plane and an answer on the
// control plane, and both now cross a process boundary before they reach verse.
func TestAGateInsideTheChildIsAnsweredFromOutside(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: "sh",
			Env:    []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "echo ran"}}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := client.HTTP(srv.URL)
	// No vector: the command driver's own default asks before it hands work to a program.
	h := rt.Spawn(task.Launch{ID: "t2", Repos: ro(origin), Prompt: "run it"})
	frames, cancel := h.Subscribe()
	defer cancel()

	res, ok := h.(interface {
		Resolve(decision, choice, say, by string) error
	})
	if !ok {
		t.Fatal("the remote handle cannot answer a gate, so nothing can")
	}
	var status task.Status
	var asked bool
	for f := range frames {
		switch {
		case f.Kind == task.EventPending:
			asked = true
			if err := res.Resolve(f.ID, "proceed", "", "human:test"); err != nil {
				t.Fatalf("answering the gate across the hop: %v", err)
			}
		case f.Kind == task.EventStatus && task.Status(f.Text).Terminal():
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
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: "sh", Env: []string{"PATH"},
		}},
		// The agent kills the task process it runs in, mid-task: the process dies without the
		// task reaching an end.
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "kill -9 $PPID; sleep 5"}}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := client.HTTP(srv.URL)
	rt.Spawn(task.Launch{ID: "t3", Repos: ro(origin), Prompt: "die", Vector: &task.Vector{Default: task.Auto}})
	// The contract keeps answering about the task after the process that ran it is gone —
	// which is the half a bare proxy cannot do.
	lister, _ := rt.(task.Lister)
	waitFor(t, "the runner to report the task failed", func() bool {
		snap, err := lister.TaskSnapshot("t3")
		return err == nil && snap.Status == task.Failed && strings.Contains(snap.Err, "exited")
	})
	snap, _ := lister.TaskSnapshot("t3")
	if !strings.Contains(snap.Err, "before the task ended") {
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
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: "sh", Env: []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "echo ran"}}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{ID: "t4", Repos: ro(origin), Prompt: "sit at the gate"})
	frames, cancel := h.Subscribe()
	defer cancel()
	for f := range frames {
		if f.Kind == task.EventPending {
			break
		}
	}
	socks, _ := filepath.Glob(filepath.Join(r.control, "*.sock"))
	if len(socks) == 0 {
		t.Fatal("no control socket for a task that is running")
	}
	for _, sock := range socks {
		if strings.HasPrefix(sock, filepath.Join(r.workspaces, "t4")) || strings.HasPrefix(sock, r.workspaces) {
			t.Errorf("the control socket %s is under the workspaces %s — the task can dial its own "+
				"control plane", sock, r.workspaces)
		}
		// And it is short enough to bind: a unix path over about a hundred bytes cannot be
		// listened on at all, which is why it does not live beside the data.
		if len(sock) > 100 {
			t.Errorf("control socket path is %d bytes: %s", len(sock), sock)
		}
	}
}

// A launch this runner will not host is REFUSED, and the refusal reaches the caller as a refusal.
//
// Before the hop these came back as a task that failed immediately, which reads downstream as
// work that was attempted. client.HTTP turns a non-2xx spawn into a failed handle carrying the
// runner's own words, so saying no on the wire is now the accurate thing.
func TestAnUnhostableLaunchIsRefusedOnTheWire(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name: "e",
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := client.HTTP(srv.URL)
	h := rt.Spawn(task.Launch{ID: "t5", Repos: ro(origin), Prompt: "x", Environment: "nowhere"})
	if err := h.Err(); !strings.Contains(err, "no environment") {
		t.Errorf("spawn into an unknown environment reported %q", err)
	}
	h = rt.Spawn(task.Launch{ID: "t6", Repos: ro(origin), Prompt: "x", Driver: "nope"})
	if err := h.Err(); !strings.Contains(err, "no driver") {
		t.Errorf("spawn with an unknown driver reported %q", err)
	}
	if r.running() != 0 {
		t.Error("a refused launch started a process anyway")
	}
}

// testRunner builds a standalone runner whose task processes are this test binary, unconfined,
// with a model key so the agent is offered.
func testRunner(t *testing.T, cfg *Config) *Runner {
	return testRunnerWith(t, cfg, Secrets{PortkeyAPIKey: "pk-test"})
}

func testRunnerWith(t *testing.T, cfg *Config, s Secrets) *Runner {
	t.Helper()
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	// Short, because a unix socket path has about a hundred bytes to live in and a temp dir
	// under a test name does not leave many.
	control, err := os.MkdirTemp("", "vrt-")
	if err != nil {
		t.Fatal(err)
	}
	logf, err := os.Create(filepath.Join(t.TempDir(), "runner.log"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r, err := Standalone{
		Config: *cfg, Secrets: s, Sandbox: runnerd.Unconfined{},
		Command: []string{os.Args[0], "-test.run=^$", taskArg},
		Work:    root, Control: control, Data: root, Log: logf,
		CallTool: callToolBinary(t),
	}.Start()
	if err != nil {
		t.Fatal(err)
	}
	// The waits that exist for a human reader, shortened, so a test exercises the whole
	// lifecycle rather than a prefix of it.
	r.h.LingerAfterEnd, r.h.StopGrace = 300*time.Millisecond, 2*time.Second
	t.Cleanup(func() {
		r.Shutdown()
		os.RemoveAll(control)
		if t.Failed() {
			if out, err := os.ReadFile(logf.Name()); err == nil && len(out) > 0 {
				t.Logf("runner log:\n%s", out)
			}
		}
		logf.Close()
	})
	return r
}

func awaitTerminal(t *testing.T, h task.Handle) task.Status {
	t.Helper()
	frames, cancel := h.Subscribe()
	defer cancel()
	for f := range frames {
		if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
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

// authorOn is who the commit at ref says made it, as `Name <email>`.
func authorOn(repoDir, ref string) string {
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
	return c.Author.Name + " <" + c.Author.Email + ">"
}

// The `llm` driver runs on the far side of the hop too, and its failures come back as failures.
//
// It is the branch the command driver's tests cannot reach: agent.Build, the environment's tool
// floor, and model credentials that arrived on stdin rather than from an environment variable.
// The model here is deliberately unreachable, because what is being checked is that the loop runs
// in the child and reports what happened — not what a model would say.
func TestTheLLMDriverRunsInTheChildToo(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: driverLLM, Tools: []string{"read_file", "list_dir"},
		}},
		// Port 1: refused immediately, so this test costs a connect and not a timeout.
		Models: Models{BaseURL: "http://127.0.0.1:1", Model: "none"},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "t7", Repos: ro(origin), Prompt: "read something", Vector: &task.Vector{Default: task.Auto},
	})
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
	if env := buildEnv(nil); env == nil {
		t.Fatal("an empty allowlist yields a nil env, which exec reads as `inherit everything` — " +
			"so the task gets the runner's whole environment including the push credential")
	}
	if got := len(buildEnv(nil)); got != 0 {
		t.Errorf("an empty allowlist yielded %d variables", got)
	}
	// And one that declares a name gets that name and no other.
	env := buildEnv([]string{"ALLOWED_ONE"})
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
	t.Setenv("CODERUNNER_GIT_TOKEN", "ghp-not-for-the-task")
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: "sh", Env: []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "env"}}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "t8", Repos: ro(origin), Prompt: "print your environment", Vector: &task.Vector{Default: task.Auto},
	})
	if st := awaitTerminal(t, h); st != task.Done {
		t.Fatalf("task ended %s (%s)", st, h.Err())
	}
	if strings.Contains(h.Result(), "ghp-not-for-the-task") {
		t.Errorf("the agent can read the runner's push credential:\n%s", h.Result())
	}
	// The agent's own environment is rebuilt by the command driver, so the check above would pass
	// even if the PROCESS around it were dirty — and that process is what the harness's run_bash
	// inherits. So assert the boundary itself: the environment this runner readies for the
	// process, which runnerd starts it with as given, is the allowlist and nothing else.
	prep, err := r.Prepare(context.Background(), task.SpawnRequest{ID: "t9", Environment: "e", Driver: "sh", Repos: ro(origin)})
	if err != nil {
		t.Fatal(err)
	}
	defer prep.Ended(task.Snapshot{})
	if prep.Env == nil {
		t.Fatal("the task process would be started with a nil env, which exec reads as `inherit " +
			"everything` — the push credential is in the task's process tree")
	}
	for _, kv := range prep.Env {
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
	origin := initRepo(t)
	r := testRunner(t, deliveringConfig(origin))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := client.HTTP(srv.URL)
	// A charter that wants to be asked before work leaves says exactly this, and could not have
	// before: `push` is the verb, and the action carrying it did not exist.
	h := rt.Spawn(task.Launch{
		ID: "d1", Repos: rw(origin), Calls: deliverCalls("d1", "echo hello > svc/out.txt"),
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "delivery", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision, choice, say, by string) error
	})

	var gate, exec, terminal string
	for f := range frames {
		switch {
		case f.Kind == task.EventPending && f.Tool == delivery.PushToolName:
			gate = f.Gate
			if err := res.Resolve(f.ID, "proceed", "", "human:test"); err != nil {
				t.Fatalf("approving the delivery: %v", err)
			}
		case f.Kind == task.EventExec && f.Tool == delivery.PushToolName:
			exec = f.Text
		case f.Kind == task.EventStatus && task.Status(f.Text).Terminal():
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
		_, err := os.Stat(filepath.Join(r.workspaces, "d1"))
		return os.IsNotExist(err)
	})
}

// A refused delivery means nothing is pushed, and the work is kept where somebody can get it.
//
// This is the case that could not exist before: the push happened whatever the vector said, so
// "decline this push" was unsayable. Now it is a decision, and the consequence of the decision is
// that the runner does not act.
func TestARefusedDeliveryPushesNothingAndKeepsTheWork(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, deliveringConfig(origin))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "d2", Repos: rw(origin), Calls: deliverCalls("d2", "echo hello > svc/out.txt"),
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "delivery", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision, choice, say, by string) error
	})
	var terminal string
	for f := range frames {
		if f.Kind == task.EventPending && f.Tool == delivery.PushToolName {
			if err := res.Resolve(f.ID, "deny", "not this branch", "human:test"); err != nil {
				t.Fatal(err)
			}
		}
		if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
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
	// And it is findable: a refusal is somebody deciding where the work goes, not deciding to
	// destroy it, so the work is held in the mirror once the task is gone.
	waitFor(t, "the work to be held in the mirror", func() bool {
		held, err := r.mirrors.Staged(origin)
		return err == nil && len(held) == 1 && held[0].Task == "d2"
	})
}

// deliveringConfig is a workspace whose task is a call list the charter wrote: do the work,
// commit it with the trailers, push the task's own branch. A list rather than an agent, because
// what is under test is the hop and the push, and nothing here turns on a model's judgement.
func deliveringConfig(origin string) *Config {
	return &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: task.DriverCalls,
			Env:    []string{"PATH"},
		}},
	}
}

// deliverCalls is the list: a shell step that does the work and commits it with the trailers a
// delivery must carry, then a push of the task's own branch. The push does not end the list on a
// refusal — a remote or a gate declining is an answer, as a charter writes `fatal: [err]`.
func deliverCalls(id, work string) []task.Call {
	return []task.Call{
		{Tool: "run_bash", Input: map[string]any{"command": fmt.Sprintf(
			"%s && git -C svc add -A && printf 'work\\n\\nVerse-Task: %s\\n' | git -C svc commit -q -F -",
			work, id)}},
		{Tool: delivery.PushToolName, Input: map[string]any{}, Fatal: &[]task.StepOutcome{task.FatalErr}},
	}
}

// A task that changed nothing says so, rather than reporting an empty push.
//
// The read plane carried `exec deliver: "pushed "` — a success with nothing after it — for a run
// that correctly found nothing to change. That is finding 10 in a new place: "nothing to do" and
// "something went wrong" rendering identically, on the one frame that says where the work went.
// Caught by a drive, on the first clean run.
func TestATaskThatChangedNothingSaysSo(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, deliveringConfig(origin))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		// A list that reads and reports, and commits nothing — the shape of a verification task.
		ID: "n1", Repos: rw(origin), Vector: &task.Vector{Default: task.Auto},
		Calls: []task.Call{
			{Tool: "run_bash", Input: map[string]any{"command": "echo no drift found"}},
			{Tool: delivery.PushToolName, Input: map[string]any{}},
		},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	var deliver, terminal string
	for f := range frames {
		if f.Kind == task.EventExec && f.Tool == delivery.PushToolName {
			deliver = f.Text
		}
		if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
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
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: "sh",
			Env:    []string{"PATH"},
		}},
		Agents: map[string]Command{"sh": {Argv: []string{"/bin/sh", "-c", "echo ran"}}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	rt := client.HTTP(srv.URL)
	id := "comment-drift@drift.head.head-moved@git.head.verse"
	for i, want := range []string{"ran", "ran"} {
		h := rt.Spawn(task.Launch{ID: id, Prompt: "go", Repos: ro(origin), Vector: &task.Vector{Default: task.Auto}})
		if st := awaitTerminal(t, h); st != task.Done {
			t.Fatalf("run %d ended %s (%s)", i+1, st, h.Err())
		}
		if got := h.Result(); got != want {
			t.Fatalf("run %d result = %q, want %q", i+1, got, want)
		}
		// The process has to be gone before the next spawn, or this is testing the retry path
		// rather than the re-run one. Its workspace goes once it is.
		waitFor(t, "the task process to be reaped", func() bool {
			_, err := os.Stat(filepath.Join(r.workspaces, workspace.SafeSeg(id)))
			return os.IsNotExist(err)
		})
	}
}

// The AGENT can propose a push to a branch it names, within what the spawn permits — and the
// vector decides it per branch.
//
// Merging a proposal into main, moving a promotion ref or retiring a stale branch each used to
// need a flow of their own. Here the spawn says which branches a repository permits, the agent
// names one, and the gate matches the ref that will actually be touched.
//
// The gate is written on `resources:` rather than on `branches:`. `GateMatch.Branches` globs
// RepoContext.Branch — the branch the WORK is on, which is the same for every action this task
// takes — while the thing that differs between two pushes is the ref, which is exactly what a
// `gitref` resource predicate selects on.
func TestTheAgentProposesAPushToABranch(t *testing.T) {
	origin := initRepo(t)
	model := fakeModel(t,
		call("write_file", `{"path":"svc/merged.txt","content":"x\n"}`),
		commitWithTrailers("g1", "Verse-Principal: user:ana@example.com"),
		call(delivery.PushToolName, `{"branch":"main"}`),
		say("pushed it"))
	r := testRunner(t, allowanceConfig(origin, model))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "g1", Repos: rwMain(origin), Prompt: "merge it",
		Provenance: task.Provenance{Principal: "user:ana@example.com"},
		// Ask about main; everything else runs.
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "to-main", Mode: task.Ask,
			Match: task.GateMatch{Resources: []task.ResourcePred{
				{Kind: task.KindGitRef, Pattern: "refs/heads/main"},
			}},
		}}},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision, choice, say, by string) error
	})

	var asked, actions []string
	var terminal string
	for f := range frames {
		switch {
		case f.Kind == task.EventPending:
			asked = append(asked, f.Tool)
			actions = append(actions, f.Action)
			if err := res.Resolve(f.ID, "proceed", "", "human:test"); err != nil {
				t.Fatalf("approving %s: %v", f.Tool, err)
			}
		case f.Kind == task.EventStatus && task.Status(f.Text).Terminal():
			terminal = f.Text
		}
		if terminal != "" {
			break
		}
	}
	if terminal != string(task.Done) {
		t.Fatalf("task ended %s (%s)", terminal, h.Err())
	}
	// Exactly one question, about the branch the charter named: the commit ran under `auto`,
	// and the push to main is the one thing the gate matched.
	if len(asked) != 1 || asked[0] != delivery.PushToolName {
		t.Fatalf("questions posed = %v, want exactly one about %q", asked, delivery.PushToolName)
	}
	// The question says where the work lands, resolved to the ref, with the repository.
	if actions[0] != "svc → refs/heads/main" {
		t.Errorf("the push's question reads %q, want the repository and the ref", actions[0])
	}
	waitFor(t, "the agent's push to land on main", func() bool {
		return strings.Contains(commitOn(origin, "refs/heads/main"), "Verse-Task: g1")
	})
	// The agent ran `git commit` with no identity of its own; the clone's is the person who
	// launched the task, at an address that says verse did it for them.
	if got, want := authorOn(origin, "refs/heads/main"), "ana@example.com (verse llm) <ana+verse-llm-runner@example.com>"; got != want {
		t.Errorf("the commit's author is %q, want %q", got, want)
	}
	// And nothing went anywhere the agent did not send it: delivery is the agent's, and it
	// pushed to main alone.
	if got := commitOn(origin, "refs/heads/verse/auto/g1"); got != "" {
		t.Errorf("something was delivered to the task's own branch, which the agent never asked for: %q", got)
	}
	// The parent staged what the CHILD committed, and the sweep that follows a task settles it
	// rather than undoing it.
	//
	// Both halves are load-bearing and neither is obvious. The task commits and pushes, so the
	// parent stages a commit made in another process a moment earlier — stage the wrong one and
	// the mirror holds the BASE, which is the shape of a bug this file has caught before. And
	// the sweep pushes every pending ref to its target, so a stale staged commit would not
	// merely be useless, it would try to reset main to the base after the correct push landed.
	waitFor(t, "the staged copy to settle", func() bool {
		work, err := r.mirrors.Staged(origin)
		return err == nil && len(work) == 0
	})
	if !strings.Contains(commitOn(origin, "refs/heads/main"), "Verse-Task: g1") {
		t.Fatal("the sweep after the task pushed something other than the work, so what the " +
			"parent staged was not what the task committed")
	}
}

// A branch the spawn does not permit poses no question and pushes nothing.
//
// "Refused before the gate" is what that comes to in a loop where classification runs first: a
// branch outside the allowance carries no push effect, so no capability gate can match it and
// nobody is asked to approve something that was never going to happen. The executor then says so
// in the agent's own vocabulary — what it may push instead.
func TestABranchOutsideTheAllowanceIsRefusedWithoutAsking(t *testing.T) {
	origin := initRepo(t)
	// `release` is not a branch this spawn permits. The model asks for it anyway, which is what
	// a model ignoring its brief would do.
	model := fakeModel(t, call(delivery.PushToolName, `{"branch":"release"}`), say("could not"))
	r := testRunner(t, allowanceConfig(origin, model))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "g2", Repos: rwMain(origin), Prompt: "push it somewhere",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "any-push", Mode: task.Ask,
			Match: task.GateMatch{Verbs: []task.Verb{task.Push}},
		}}},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision, choice, say, by string) error
	})

	var asked int
	var refused, terminal string
	for f := range frames {
		switch {
		case f.Kind == task.EventPending:
			asked++
			_ = res.Resolve(f.ID, "proceed", "", "human:test")
		case f.Kind == task.EventExec && f.Tool == delivery.PushToolName && refused == "":
			refused = f.Text
		case f.Kind == task.EventStatus && task.Status(f.Text).Terminal():
			terminal = f.Text
		}
		if terminal != "" {
			break
		}
	}
	if terminal != string(task.Done) {
		t.Fatalf("task ended %s (%s)", terminal, h.Err())
	}
	if asked > 0 {
		t.Fatal("somebody was asked to approve a push to a branch the spawn does not permit")
	}
	if !strings.Contains(refused, "not one svc may push to") || !strings.Contains(refused, "main") {
		t.Errorf("the refusal does not say what the agent could have pushed instead: %q", refused)
	}
	if commitOn(origin, "refs/heads/release") != "" {
		t.Fatal("a ref the spawn never permitted was written")
	}
}

// The agent sees upstream move only when it asks. `fetch` is an action of the loop — a network
// read the vector gates — performed on the parent's side with the credential it already holds,
// and what the agent gets back is the checkout's `mirror/*` moved and the objects to read them.
func TestTheAgentFetchesWhatMovedUpstream(t *testing.T) {
	origin := initRepo(t)
	model := fakeModel(t,
		call("run_bash", `{"command":"git -C svc log -1 --format=%s mirror/main"}`),
		call(delivery.FetchToolName, `{"repo":"svc"}`),
		call("run_bash", `{"command":"git -C svc log -1 --format=%s mirror/main"}`),
		say("caught up"))
	r := testRunner(t, allowanceConfig(origin, model))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "f1", Repos: rwMain(origin), Prompt: "see what moved",
		// Everything asks, so the test holds the first command while upstream moves, and sees
		// the fetch asked about as a network read.
		Vector: &task.Vector{Default: task.Ask},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision, choice, say, by string) error
	})

	var asked, actions, execs []string
	var terminal string
	for f := range frames {
		switch {
		case f.Kind == task.EventPending:
			if len(asked) == 0 {
				// The task has started from the base it has; now upstream moves.
				advanceOrigin(t, origin, "moved")
			}
			asked = append(asked, f.Tool)
			actions = append(actions, f.Action)
			if err := res.Resolve(f.ID, "proceed", "", "human:test"); err != nil {
				t.Fatalf("approving %s: %v", f.Tool, err)
			}
		case f.Kind == task.EventExec:
			execs = append(execs, f.Text)
		case f.Kind == task.EventStatus && task.Status(f.Text).Terminal():
			terminal = f.Text
		}
		if terminal != "" {
			break
		}
	}
	if terminal != string(task.Done) {
		t.Fatalf("task ended %s (%s)", terminal, h.Err())
	}
	if want := []string{"run_bash", delivery.FetchToolName, "run_bash"}; !slices.Equal(asked, want) {
		t.Fatalf("questions posed = %v, want %v", asked, want)
	}
	if !strings.Contains(actions[1], "svc") || !strings.Contains(actions[1], origin) {
		t.Errorf("the fetch's question reads %q, want the repository and where it fetches from", actions[1])
	}
	joined := strings.Join(execs, "\n")
	if !strings.Contains(joined, "fetched svc: mirror/main ") {
		t.Errorf("the fetch did not say what moved:\n%s", joined)
	}
	// Before the fetch mirror/main was the seed; after it, the commit that landed meanwhile.
	if n := strings.Count(joined, "seed"); n != 1 {
		t.Errorf("mirror/main read as the seed %d times, want once (before the fetch):\n%s", n, joined)
	}
	if !strings.Contains(joined, "moved") {
		t.Errorf("after the fetch mirror/main does not show the commit that moved upstream:\n%s", joined)
	}
}

// allowanceConfig is an environment whose repository permits two kinds of push: the task's own
// branches, and `main`, which is what a merge proposal wants.
func allowanceConfig(origin, model string) *Config {
	return &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: driverLLM,
			Tools:  []string{"read_file", "write_file", "run_bash", delivery.PushToolName, delivery.FetchToolName},
			Env:    []string{"PATH", "HOME"},
		}},
		Models: Models{BaseURL: model, Model: "fake"},
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

// commitWithTrailers is the turn in which a scripted agent commits what it wrote, with the
// trailers a delivery must carry: what the brief tells a real agent to do.
func commitWithTrailers(taskID string, more ...string) string {
	trailers := "Verse-Task: " + taskID + `\n`
	for _, t := range more {
		trailers += t + `\n`
	}
	return call("run_bash", fmt.Sprintf(`{"command":"git -C svc add -A && printf 'merged\n\n%s' | git -C svc commit -q -F -"}`, trailers))
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

// No approval, no push — and the gate is the ONLY path to one.
//
// This is what makes the autonomy load-bearing rather than advisory. The task holds no git
// credential at all: its trees are opened without one, and the push is made by the runner over
// the channel it answers, in reply to a request the loop only makes after a gate has said yes
// about that exact branch. A refusal is therefore not "the push was skipped", it is "the
// ability to push never existed".
func TestApprovalIsWhatMakesTheRunnerPush(t *testing.T) {
	origin := initRepo(t)
	before := commitOn(origin, "refs/heads/main")
	model := fakeModel(t,
		call("write_file", `{"path":"svc/merged.txt","content":"x\n"}`),
		commitWithTrailers("g3"),
		call(delivery.PushToolName, `{"branch":"main"}`),
		say("I was not allowed to"))
	r := testRunner(t, allowanceConfig(origin, model))
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "g3", Repos: rwMain(origin), Prompt: "merge it",
		Vector: &task.Vector{Default: task.Auto, Gates: []task.Gate{{
			Name: "to-main", Mode: task.Ask,
			Match: task.GateMatch{Resources: []task.ResourcePred{
				{Kind: task.KindGitRef, Pattern: "refs/heads/main"},
			}},
		}}},
	})
	frames, cancel := h.Subscribe()
	defer cancel()
	res := h.(interface {
		Resolve(decision, choice, say, by string) error
	})

	var terminal string
	for f := range frames {
		switch {
		case f.Kind == task.EventPending && f.Tool == delivery.PushToolName:
			if err := res.Resolve(f.ID, "deny", "not main, not today", "human:test"); err != nil {
				t.Fatal(err)
			}
		case f.Kind == task.EventStatus && task.Status(f.Text).Terminal():
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
	// And nothing left by another door: the agent decided one delivery, it was refused, and
	// there is no finisher to send the work somewhere the agent did not ask for.
	if got := commitOn(origin, "refs/heads/verse/auto/g3"); got != "" {
		t.Errorf("the refused work was delivered to auto anyway: %q", got)
	}
}

// The credential a push is made with is the runner's own forge token, and a runner with none
// refuses the push to a forge outright rather than letting the task report the forge's opinion
// of this runner's configuration — at the end of a run, after the gate approved it. A local path
// needs no token at all, which is what every test in this file runs on.
func TestTheRunnersTokenIsWhatAPushIsMadeWith(t *testing.T) {
	r := &Runner{token: "the-runners-own"}
	if got, err := r.credentialFor(workspace.Repo{Name: "svc", Repo: "https://github.com/acme/app"}); err != nil || got != "the-runners-own" {
		t.Errorf("a forge got %q (%v), want the runner's token", got, err)
	}
	none := &Runner{}
	if _, err := none.credentialFor(workspace.Repo{Name: "svc", Repo: "https://github.com/acme/app"}); err == nil {
		t.Error("a runner with no token released an empty credential for a forge")
	} else if !strings.Contains(err.Error(), "git-token-env") {
		t.Errorf("the refusal does not say how to give the runner a token: %v", err)
	}
	if got, err := none.credentialFor(workspace.Repo{Name: "svc", Repo: "/tmp/x.git"}); err != nil || got != "" {
		t.Errorf("a local path was refused a push for want of a token: %q %v", got, err)
	}
}

// A call list runs on the far side of the hop, with this runner's workspace under it.
//
// It is the whole of step 3 from the outside: the charter writes the actions, the child runs
// them in the checkout, and the runner cannot tell that no model was involved. The command
// writes a file so the assertion is about the WORKSPACE rather than about an exit code — a
// driver that proposed nothing would also exit zero.
func TestACallListRunsInTheChildToo(t *testing.T) {
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: task.DriverCalls, Env: []string{"PATH"},
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "t-calls", Repos: ro(origin), Vector: &task.Vector{Default: task.Auto},
		Calls: []task.Call{{Tool: "run_bash",
			Input: map[string]any{"command": "echo listed > svc/ran.txt"}}},
	})
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
	origin := initRepo(t)
	r := testRunner(t, &Config{
		Default: "e",
		Environments: []Environment{{
			Name:   "e",
			Driver: task.DriverCalls, Env: []string{"PATH"},
		}},
	})
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := client.HTTP(srv.URL).Spawn(task.Launch{
		ID: "t-nolist", Repos: ro(origin), Prompt: "please merge the branch and push it",
		Vector: &task.Vector{Default: task.Auto},
	})
	if st := awaitTerminal(t, h); st != task.Failed {
		t.Fatalf("a calls spawn with no calls ended %s", st)
	}
	if e := h.Err(); !strings.Contains(e, "calls") {
		t.Errorf("the refusal does not say what it could not read: %q", e)
	}
}

// ro, rw and rwMain are the repositories a spawn carries in these tests: the test origin, read
// only; writable on the task's own branches; and writable on those and on main.
func ro(origin string) []task.Repo { return []task.Repo{{Repo: origin, From: "main"}} }

func rw(origin string) []task.Repo {
	return []task.Repo{{Repo: origin, From: "main", Access: task.AccessWrite, Push: &task.RepoPush{New: []string{"verse/auto/*"}}}}
}

func rwMain(origin string) []task.Repo {
	return []task.Repo{{Repo: origin, From: "main", Access: task.AccessWrite,
		Push: &task.RepoPush{New: []string{"verse/auto/*"}, Existing: []string{"main"}}}}
}
