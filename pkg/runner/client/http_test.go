package client_test

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/systems/systemstest"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/client"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/task"
)

// workEnv is an environment giving the shell and file writes, the steps these tests' drivers
// take: a step outside its environment is refused before any row is read.
var workEnv = systems.Environment{Systems: systems.Systems{
	"commands": {{Verbs: map[string][]systems.Shape{"run": {{}}}}},
	"files":    {{Verbs: map[string][]systems.Shape{"write": {{}}}}},
}}

// openNet is an environment of the open network alone: what a launch on a handler with no offers
// names, since no network block is the network none, which that handler can't build.
var openNet = systems.Environment{Systems: systems.Systems{"network": {{Attributes: map[string]string{"mode": runner.NetworkOpen}}}}}

// a driver that takes `steps` no-op actions then completes.
// doneDriver proposes steps actions, of tool (absent: one that is no step), and is done.
type doneDriver struct {
	steps int
	tool  string
}

func (d *doneDriver) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	if d.steps <= 0 {
		return task.ToolUse{}, false, nil // complete
	}
	d.steps--
	return task.ToolUse{Name: cmp.Or(d.tool, "noop")}, true, nil
}

type okExec struct{}

func (okExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu}, nil, nil
}

// The engine drives a REMOTE runnerd entirely over HTTP: httpRunner.Launch issues the
// launch, remoteHandle.Subscribe streams the SSE read plane (cursored), and the task
// runs to a terminal status — no in-process handle. This is the Phase-3 proof of the
// base-URL decoupling.
func TestHTTPRunnerDrivesRemoteTaskd(t *testing.T) {
	build := func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(workEnv, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), workEnv)
		return task.NewTask(l.ID, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(runnerd.NewInProcessHandler(build).HTTPHandler())
	defer srv.Close()

	pol := client.HTTP(srv.URL)
	h := pol.Launch(runner.Launch{ID: "t1", Agent: &runner.Agent{Name: "test"}, Prompt: "do a thing", Environment: openNet})

	ch, unsub := h.Subscribe()
	defer unsub()

	var sawCursor, sawExec, completed bool
	timeout := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break loop
			}
			if ev.Cursor > 0 {
				sawCursor = true
			}
			if ev.Kind == task.EventExec {
				sawExec = true
			}
			if ev.Kind == task.EventStatus && ev.Text == "done" {
				completed = true
				break loop
			}
		case <-timeout:
			t.Fatal("timed out waiting for the remote task to complete")
		}
	}
	if !completed {
		t.Fatal("never saw a completed status over the wire")
	}
	if !sawCursor {
		t.Fatal("events carried no cursor")
	}
	if !sawExec {
		t.Fatal("never saw the action execute remotely")
	}
}

// A remote runner enumerates its tasks so versed can union them into `task list`/
// `get`: ListTasks/TaskSnapshot are a thin client over the remote's GET /tasks.
func TestHTTPRunnerListsRemoteTasks(t *testing.T) {
	build := func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(workEnv, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), workEnv)
		return task.NewTask(l.ID, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(runnerd.NewInProcessHandler(build).HTTPHandler())
	defer srv.Close()

	pol := client.HTTP(srv.URL)
	pol.Launch(runner.Launch{ID: "r1", Agent: &runner.Agent{Name: "test"}, Prompt: "remote task", Environment: openNet})

	// task.Lister by NAME, not a structural assertion written out here. Spelling the
	// signature inline is what let a widened one report false instead of failing to build.
	lister, ok := pol.(task.Lister)
	if !ok {
		t.Fatal("httpRunner should implement task.Lister")
	}

	insts, err := lister.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks against a live runner = %v, want no error", err)
	}
	var found bool
	for _, m := range insts {
		if m.ID == "r1" {
			found = true
		}
	}
	if !found {
		t.Fatal("remote task r1 not in the runner's ListTasks")
	}

	snap, err := lister.TaskSnapshot("r1")
	if err != nil || snap.Prompt != "remote task" {
		t.Fatalf("TaskSnapshot(r1) = %+v, %v; want the remote task", snap, err)
	}
	// An id the runner has never heard of is ErrNoSuchTask — the one error that means
	// "look for your typo", and the only one `task get` may answer 404 to.
	if _, err := lister.TaskSnapshot("nope"); !errors.Is(err, task.ErrNoSuchTask) {
		t.Fatalf("TaskSnapshot of an unknown id = %v, want ErrNoSuchTask", err)
	}
}

// A runner that REFUSES a launch must not read as one that took it. The status used to be
// discarded, so 400/403/501 all came back as a live remote handle: versed mirrored a
// task:instance for work that was never started and the flow waited on it forever, so the
// refusal surfaced as a hang. A runner declining a launch it cannot host is the whole
// mechanism by which a runner gets to refuse, and it is inert unless this reads it.
func TestHTTPRunnerLaunchReadsARefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"bad request", http.StatusBadRequest, "unknown driver \"llm\""},
		{"not implemented", http.StatusNotImplemented, "this runner does not host relayed work"},
		{"no body", http.StatusForbidden, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, tc.body, tc.code)
			}))
			defer srv.Close()

			h := client.HTTP(srv.URL).Launch(runner.Launch{ID: "x", Agent: &runner.Agent{Name: "test"}, Prompt: "t"})

			// The refusal has to arrive as a terminal on the plane the flow already reads,
			// which is what FailedHandle is for.
			ch, _ := h.Subscribe()
			ev, open := <-ch
			if !open {
				t.Fatal("a refused launch produced no frame at all; the flow waits forever")
			}
			if ev.Kind != task.EventStatus || ev.Text != string(task.Failed) {
				t.Fatalf("frame = %+v, want a failed status — the refusal read as an accepted launch", ev)
			}
			if ev.Err == "" {
				t.Fatal("the failure carries no reason, so the operator is told only that it failed")
			}
			if tc.body != "" && !strings.Contains(ev.Err, tc.body) {
				t.Fatalf("reason = %q, want the runner's own words (%q)", ev.Err, tc.body)
			}
		})
	}
}

// And the read plane: a list that could not be taken must not read as an empty one. nil could
// not carry the difference, so an unreachable runner's tasks were absent from the unified
// list and the response was a clean 200 that was simply short.
func TestHTTPRunnerListReportsWhyItIsEmpty(t *testing.T) {
	// Unreachable: a server that is listening at bind time and gone at call time — the
	// runner that was torn down, which is the ordinary case, not an exotic one.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	if _, err := client.HTTP(deadURL).(task.Lister).ListTasks(); !errors.Is(err, task.ErrUnreachable) {
		t.Fatalf("a dead runner's list = %v, want ErrUnreachable", err)
	}

	// Answering, but not as a runner: a face that serves no
	// GET /tasks, so a url pointed at one 404s. That is a different fact from unreachable
	// — something is there, and it is not this contract.
	worker := httptest.NewServer(http.NewServeMux())
	defer worker.Close()
	if _, err := client.HTTP(worker.URL).(task.Lister).ListTasks(); !errors.Is(err, task.ErrUnsupported) {
		t.Fatalf("a non-runner url's list = %v, want ErrUnsupported", err)
	}

	// And the single-task read tells the same three apart. This one always DID check its
	// status, so no refusal ever passed for an answer — but every failure collapsed into one
	// `false`, which the caller could only report as "no such task".
	if _, err := client.HTTP(deadURL).(task.Lister).TaskSnapshot("p"); !errors.Is(err, task.ErrUnreachable) {
		t.Fatalf("a dead runner's snapshot = %v, want ErrUnreachable — a task on an "+
			"unreachable runner is not a task that does not exist", err)
	}

	// Answering with something undecodable is also not an empty verse: discarding this is how
	// a format drift on either end reads as a runner with nothing running.
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not":"a list"`))
	}))
	defer junk.Close()
	if _, err := client.HTTP(junk.URL).(task.Lister).ListTasks(); err == nil {
		t.Fatal("an undecodable body listed as no tasks at all")
	}
}

// A task that finished while verse was away is re-attached on restart, and the re-attached
// handle delivers how it ended: verse resumes the task rather than orphaning it when the work
// outlived it (versed.reconcileTasks).
func TestResumeReAttachesToRemoteTask(t *testing.T) {
	build := func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(workEnv, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), workEnv)
		return task.NewTask(l.ID, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(runnerd.NewInProcessHandler(build).HTTPHandler())
	defer srv.Close()

	pol := client.HTTP(srv.URL)
	pol.Launch(runner.Launch{ID: "t1", Agent: &runner.Agent{Name: "test"}, Prompt: "task", Environment: openNet}) // runs on the remote to completion
	time.Sleep(300 * time.Millisecond)                                                                            // let it finish (retained in the ring)

	// "verse restart": attach afresh, and read the stream until it says how the task ended.
	events, stop := pol.Attach("t1").Subscribe()
	defer stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("the stream ended without the task's terminal")
			}
			if e.Kind == task.EventStatus && task.Status(e.Text) == task.Done {
				return // resume recovered the terminal from the remote
			}
		case <-deadline:
			t.Fatal("the re-attached handle never delivered the task's terminal")
		}
	}
}

// A gate on a REMOTE task is answerable. Without remoteHandle.Resolve this handle was not
// a gateResolver at all, so every route into it refused with a message about the question —
// a task on another host could be blocked on a decision that no operator could give.
//
// It also pins the vocabulary: the handle sends the contract's runner.ResolveRequest
// (`id`/`choice`), and the remote runtime reads the same type. A body it cannot read as one is
// refused, not answered.
func TestHTTPRunnerAnswersARemoteGate(t *testing.T) {
	build := func(l runner.Launch, reg *task.Registry) *task.Task {
		// Its one command asks, resolved by the daemon's own registry — so the task really
		// blocks and really waits for an answer to arrive over HTTP.
		sup := systemstest.Opened(task.NewSupervisor(workEnv, task.Autonomy{{Match: &task.Match{System: "commands"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAsk}}}, reg), workEnv)
		return task.NewTask(l.ID, sup, &doneDriver{steps: 1, tool: "run_bash"}, okExec{})
	}
	srv := httptest.NewServer(runnerd.NewInProcessHandler(build).HTTPHandler())
	defer srv.Close()

	pol := client.HTTP(srv.URL)
	h := pol.Launch(runner.Launch{ID: "t-gate", Agent: &runner.Agent{Name: "test"}, Prompt: "do a thing", Environment: openNet})
	ch, unsub := h.Subscribe()
	defer unsub()

	rh, ok := h.(interface {
		Resolve(decision, choice, say, by string) error
	})
	if !ok {
		t.Fatal("a remote handle cannot answer a gate, so a remote task can block forever")
	}

	var decision string
	timeout := time.After(5 * time.Second)
	for decision == "" {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended before the task gated")
			}
			if ev.Kind == task.EventPending {
				decision = ev.ID
			}
		case <-timeout:
			t.Fatal("the remote task never raised a gate")
		}
	}

	if err := rh.Resolve(decision, "proceed", "go ahead", "human:walter"); err != nil {
		t.Fatalf("answering the remote gate %s = %v", decision, err)
	}
	// Reported success is not delivery. The task has to actually go on, and the answer has
	// to arrive attributed — an answer that lost its `by` on the wire is an unattributable
	// decision recorded as though it were attributed.
	timeout = time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended without a decision")
			}
			if ev.Kind == task.EventDecision && ev.ID == decision {
				if !ev.Proceed {
					t.Fatalf("the remote read a deny from an approve: %+v", ev)
				}
				if ev.By != "human:walter" {
					t.Fatalf("by = %q, want human:walter — attribution was dropped on the wire", ev.By)
				}
				return
			}
		case <-timeout:
			t.Fatal("the remote task never acted on the answer")
		}
	}
}

// A base url that cannot be parsed is an ERROR, not a panic. `http.NewRequest` answers an
// unparseable url with a nil request, and `Do(nil)` dereferences it — so discarding that error
// turned a bad url into a panic serving HTTP, three call sites deep in whoever was asking.
func TestHTTPRunnerRefusesAnUnparseableURL(t *testing.T) {
	// Unparseable on its own terms — a bad percent-escape — rather than by holding characters
	// some other grammar spends. An address inside a URL is not that case —
	// `http://task.review@demo.pr.1` is a perfectly good URL — so the bad escape is the whole
	// of what this asks about, and stays so however addresses are spelled.
	bad := client.HTTP("http://%zz")
	if _, err := bad.(task.Lister).ListTasks(); !errors.Is(err, task.ErrUnreachable) {
		t.Fatalf("list on an unparseable url = %v, want ErrUnreachable", err)
	}
	if _, err := bad.(task.Lister).TaskSnapshot("x"); !errors.Is(err, task.ErrUnreachable) {
		t.Fatalf("snapshot on an unparseable url = %v, want ErrUnreachable", err)
	}
	// The read plane has no error to return, so it says so where the caller is already
	// looking — the same place a rejected subscribe reports.
	frames, _ := bad.Attach("x").Subscribe()
	select {
	case f := <-frames:
		if f.Err == "" {
			t.Fatalf("subscribe on an unparseable url reported no error: %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscribe on an unparseable url said nothing at all")
	}
}
