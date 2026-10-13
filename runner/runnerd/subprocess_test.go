package runnerd_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/tasktest"
)

// taskArg makes this test binary a standalone runner's task process (TestMain).
const taskArg = "runnerd-task-process"

// As a task process, the test binary hosts the conformance driver: the spec arrives in the
// launch's prompt, since the process shares no memory with the test.
func TestMain(m *testing.M) {
	if slices.Contains(os.Args, taskArg) {
		io.Copy(io.Discard, os.Stdin)
		err := runnerd.ServeTask(func(l task.Launch, reg *task.Registry) *task.Task {
			var spec tasktest.Spec
			outcome, text, _ := strings.Cut(l.Prompt, "\n")
			sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, reg)
			switch outcome {
			case "exit": // the process dies under its running task
				return task.NewTask(l.ID, sup, dyingDriver{}, noopExec{})
			case "pwd": // the task answers where it runs
				outcome, text = string(task.Done), must(os.Getwd())
			}
			spec.Outcome, spec.Text = task.Status(outcome), text
			return task.NewTask(l.ID, sup, &conformanceDriver{spec: spec}, noopExec{})
		})
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

type dyingDriver struct{}

func (dyingDriver) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	time.Sleep(100 * time.Millisecond) // past the spawn
	os.Exit(3)
	return task.ToolUse{}, false, nil
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// newSubprocess is a SubprocessHandler whose task processes are this test binary, unconfined.
func newSubprocess(t *testing.T) (*runnerd.SubprocessHandler, task.Runner) {
	t.Helper()
	h := runnerd.NewSubprocessHandler(runnerd.SubprocessConfig{
		Command: []string{os.Args[0], "-test.run=^$", taskArg},
		Sandbox: runnerd.Unconfined{}, Work: resolvedTempDir(t), Control: shortTempDir(t),
		Log: io.Discard,
	})
	srv := httptest.NewServer(h.HTTPHandler())
	t.Cleanup(func() { srv.Close(); h.Shutdown() })
	return h, runner.HTTP(srv.URL)
}

// resolvedTempDir is t.TempDir with its symlinks resolved, as a task's working directory has
// them: on macOS the temp dir is under /var, a link to /private/var.
func resolvedTempDir(t *testing.T) string {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// shortTempDir is a directory for unix sockets, whose paths are limited to about 100 bytes:
// t.TempDir's can be longer.
func shortTempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "rd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// A task in a process of its own is held to the same properties as one in the runner's.
func TestSubprocessConformance(t *testing.T) {
	_, rt := newSubprocess(t)
	tasktest.Run(t, &subprocessProducer{rt: rt})
}

type subprocessProducer struct {
	rt task.Runner
	n  int
}

func (p *subprocessProducer) Name() string { return "subprocess" }

func (p *subprocessProducer) Caps() tasktest.Caps {
	return tasktest.Caps{FailureReason: true, CompletionAnswer: true}
}

func (p *subprocessProducer) Start(t *testing.T, spec tasktest.Spec) string {
	t.Helper()
	p.n++
	id := fmt.Sprintf("s%d", p.n)
	p.rt.Spawn(task.Launch{ID: id, Driver: spec.Driver, Prompt: string(spec.Outcome) + "\n" + spec.Text})
	return id
}

func (p *subprocessProducer) RaiseGate(t *testing.T, id, gate, reason string) string {
	t.Fatal("a hosted task's gates are raised by its driver; Caps().ExecutorGates is false")
	return ""
}

func (p *subprocessProducer) Snapshot(t *testing.T, id string) (task.Instance, bool) {
	t.Helper()
	in, err := p.rt.(task.Lister).TaskSnapshot(id)
	return in, err == nil
}

// A task process that won't start is a refused spawn, said by the runner, and no task.
func TestAProcessThatWontStartRefusesTheSpawn(t *testing.T) {
	h := runnerd.NewSubprocessHandler(runnerd.SubprocessConfig{
		Command: []string{"/nonexistent/runner"}, Sandbox: runnerd.Unconfined{},
		Work: t.TempDir(), Control: shortTempDir(t), Log: io.Discard,
	})
	srv := httptest.NewServer(h.HTTPHandler())
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/tasks", codec.MediaType(codec.Tony),
		strings.NewReader("{id: t1, prompt: hi}"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || !strings.Contains(string(body), "starting the task's process") {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	if got := h.Snapshots(); len(got) != 0 {
		t.Fatalf("a task that never started is listed: %+v", got)
	}
}

// A task runs in a directory of its own, which goes when its process does.
func TestATaskRunsInADirectoryOfItsOwn(t *testing.T) {
	defer runnerd.SetLingerAfterEnd(0)()
	h, rt := newSubprocess(t)
	work := h.Work
	in := runToEnd(t, rt, task.Launch{ID: "t1", Prompt: "pwd\n"})
	if in.Status != task.Done || filepath.Dir(in.Result) != work ||
		!strings.HasPrefix(filepath.Base(in.Result), "task-") {
		t.Fatalf("ran in %q (%+v), want a task- directory under %s", in.Result, in, work)
	}
	waitGone(t, in.Result)
	if again, err := rt.(task.Lister).TaskSnapshot("t1"); err != nil || again.Result != in.Result {
		t.Fatalf("after its process: %+v %v", again, err)
	}
}

// An ended task is answered as an InProcessHandler answers it, after its process has gone.
func TestAnEndedTaskIsAnsweredAsEmbeddedAnswersIt(t *testing.T) {
	defer runnerd.SetLingerAfterEnd(0)()
	_, rt := newSubprocess(t)
	in := runToEnd(t, rt, task.Launch{ID: "t1", Prompt: "pwd\n"})
	waitGone(t, in.Result)
	h := rt.Attach("t1")
	for name, err := range map[string]error{
		"pause": h.Pause(), "resume": h.Resume(), "cancel": h.Cancel("late"),
		"vector": h.SetVector(&task.Vector{Default: task.Auto}, "test"),
	} {
		if err != nil {
			t.Errorf("%s: %v, want nothing done and no error", name, err)
		}
	}
	if err := h.Steer("more", "test", ""); !errors.Is(err, task.ErrEnded) {
		t.Errorf("say: %v, want ErrEnded", err)
	}
	resolver := h.(interface {
		Resolve(decision, choice, say, by string) error
	})
	if err := resolver.Resolve("q1", task.Proceed, "", "test"); !errors.Is(err, task.ErrNoSuchDecision) {
		t.Errorf("resolve: %v, want ErrNoSuchDecision", err)
	}
}

// A process that exits before its task ends is a failed task, with the exit status.
func TestAProcessThatDiesFailsItsTask(t *testing.T) {
	defer runnerd.SetLingerAfterEnd(0)()
	_, rt := newSubprocess(t)
	in := runToEnd(t, rt, task.Launch{ID: "t1", Prompt: "exit\n"})
	if in.Status != task.Failed || !strings.Contains(in.Err, "exit status 3") {
		t.Fatalf("got %+v", in)
	}
}

// runToEnd spawns l, and returns its snapshot once it has ended.
func runToEnd(t *testing.T, rt task.Runner, l task.Launch) task.Instance {
	t.Helper()
	rt.Spawn(l)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if in, err := rt.(task.Lister).TaskSnapshot(l.ID); err == nil && in.Status.Terminal() {
			return in
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s didn't end", l.ID)
	return task.Instance{}
}

func waitGone(t *testing.T, path string) {
	t.Helper()
	for range 500 {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s is still there after its task ended", path)
}

func TestSandboxNamed(t *testing.T) {
	if sb, err := runnerd.SandboxNamed("none"); err != nil || sb.Name() != "none" {
		t.Fatalf("none: %v %v", sb, err)
	}
	if _, err := runnerd.SandboxNamed("docker"); err == nil {
		t.Fatal("an unknown sandbox was accepted")
	}
}
