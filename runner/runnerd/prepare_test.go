package runnerd_test

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// preparer is a Preparer for the tests: it refuses what refuse says, blocks in Prepare while
// gate is open, readies what prepared returns, and records how each task ended.
type preparer struct {
	refuse   func(*task.SpawnRequest) error
	gate     chan struct{}
	prepared func(spawn task.SpawnRequest) (*runnerd.Prepared, error)

	mu    sync.Mutex
	ended []task.Snapshot
}

func (p *preparer) Admit(spawn *task.SpawnRequest) error {
	if p.refuse != nil {
		return p.refuse(spawn)
	}
	return nil
}

func (p *preparer) Prepare(ctx context.Context, spawn task.SpawnRequest) (*runnerd.Prepared, error) {
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var prep *runnerd.Prepared
	if p.prepared != nil {
		var err error
		if prep, err = p.prepared(spawn); err != nil {
			return nil, err
		}
	}
	if prep == nil {
		prep = &runnerd.Prepared{}
	}
	prep.Ended = func(final task.Snapshot) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.ended = append(p.ended, final)
	}
	return prep, nil
}

func (p *preparer) endings() []task.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]task.Snapshot(nil), p.ended...)
}

// newPrepared is a SubprocessHandler with a Preparer, its task processes this test binary.
func newPrepared(t *testing.T, pr *preparer) (*runnerd.SubprocessHandler, task.Runner) {
	t.Helper()
	return newPreparedIn(t, pr, runnerd.Unconfined{})
}

// newPreparedIn is newPrepared with the sandbox the processes run in.
func newPreparedIn(t *testing.T, pr *preparer, sb runnerd.Sandbox) (*runnerd.SubprocessHandler, task.Runner) {
	t.Helper()
	h := runnerd.NewSubprocessHandler(runnerd.SubprocessConfig{
		Command: []string{os.Args[0], "-test.run=^$", taskArg},
		Sandbox: sb, Work: resolvedTempDir(t), Control: shortTempDir(t),
		Log: io.Discard, Preparer: pr, LingerAfterEnd: 50 * time.Millisecond,
	})
	srv := httptest.NewServer(h.HTTPHandler())
	t.Cleanup(func() { srv.Close(); h.Shutdown() })
	return h, client.HTTP(srv.URL)
}

// With a Preparer, a spawn is answered before the task is prepared: the caller's deadline bounds
// the answer and not the work. Meanwhile the task runs, to a reader and on the list, and a
// second spawn of it is the same answer.
func TestASpawnIsAnsweredBeforeTheTaskIsPrepared(t *testing.T) {
	pr := &preparer{gate: make(chan struct{})}
	_, rt := newPrepared(t, pr)

	start := time.Now()
	h := rt.Spawn(task.Launch{ID: "t1", Prompt: "done\nok", Driver: "d"})
	if h.Err() != "" {
		t.Fatalf("spawn: %s", h.Err())
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the spawn waited %s for a preparation that has not begun", took)
	}
	snap, err := rt.(task.Lister).TaskSnapshot("t1")
	if err != nil || snap.Status != task.Running || snap.Prompt != "done\nok" || snap.Driver != "d" {
		t.Fatalf("while preparing: %+v (%v), want the task as spawned, running", snap, err)
	}
	if list, err := rt.(task.Lister).ListTasks(); err != nil || len(list) != 1 || list[0].ID != "t1" {
		t.Errorf("the list while preparing = %v (%v), want t1 alone", list, err)
	}
	if again := rt.Spawn(task.Launch{ID: "t1", Prompt: "done\nok"}); again.Err() != "" {
		t.Errorf("a second spawn of a task being prepared was refused: %s", again.Err())
	}

	close(pr.gate)
	in := awaitEnd(t, rt, "t1")
	if in.Status != task.Done || in.Result != "ok" {
		t.Fatalf("ended %+v", in)
	}
	waitForEndings(t, pr, 1)
	if got := pr.endings()[0]; got.ID != "t1" || got.Status != task.Done {
		t.Errorf("the Preparer was told %+v, want the task's end", got)
	}
}

// What Admit refuses is refused on the wire, in its words, and no task is made.
func TestAdmitRefusesOnTheWire(t *testing.T) {
	pr := &preparer{refuse: func(s *task.SpawnRequest) error {
		if s.Driver == "nope" {
			return errors.New("no driver nope here")
		}
		return nil
	}}
	h, rt := newPrepared(t, pr)
	if err := rt.Spawn(task.Launch{ID: "t1", Prompt: "x", Driver: "nope"}).Err(); !strings.Contains(err, "no driver nope here") {
		t.Errorf("a refused spawn reported %q", err)
	}
	if got := h.Snapshots(); len(got) != 0 {
		t.Errorf("a refused spawn is listed: %+v", got)
	}
}

// A preparation that fails is a task that failed, with the reason, where the caller reads it.
func TestAPreparationThatFailsIsAFailedTask(t *testing.T) {
	pr := &preparer{prepared: func(task.SpawnRequest) (*runnerd.Prepared, error) {
		return nil, errors.New("the repository svc could not be cloned")
	}}
	_, rt := newPrepared(t, pr)
	// Not refused: the spawn is answered, and the failure is the task's.
	if h := rt.Spawn(task.Launch{ID: "t2", Prompt: "done\nnever"}); h == nil {
		t.Fatal("no handle")
	}
	in := awaitEnd(t, rt, "t2")
	if in.Status != task.Failed || !strings.Contains(in.Err, "svc could not be cloned") {
		t.Fatalf("ended %+v, want failed with the reason", in)
	}
	if list, _ := rt.(task.Lister).ListTasks(); len(list) != 1 || list[0].Status != task.Failed {
		t.Errorf("list after the failure = %v", list)
	}
	if len(pr.endings()) != 0 {
		t.Error("the Preparer was told of an end for a task it never readied")
	}
}

// A cancel while preparing ends the preparation: the task is cancelled, and no process starts.
func TestACancelWhilePreparingIsCancelled(t *testing.T) {
	pr := &preparer{gate: make(chan struct{})}
	h, rt := newPrepared(t, pr)
	hd := rt.Spawn(task.Launch{ID: "t3", Prompt: "done\nlate"})
	if err := hd.Cancel("changed my mind"); err != nil {
		t.Fatalf("cancel while preparing: %v", err)
	}
	in := awaitEnd(t, rt, "t3")
	if in.Status != task.Cancelled {
		t.Fatalf("ended %+v, want cancelled", in)
	}
	if dirs, _ := filepath.Glob(filepath.Join(h.Work, "task-*")); len(dirs) != 0 {
		t.Errorf("a process directory was made for a cancelled preparation: %v", dirs)
	}
}

// A cancel that arrives while the task's process is starting ends the task all the same: it
// was answered, so the caller believes it took. The process is stopped, the task is cancelled,
// and its directory goes.
func TestACancelDuringProcessStartIsCancelled(t *testing.T) {
	sb := &slowSandbox{starting: make(chan struct{}), release: make(chan struct{})}
	h, rt := newPreparedIn(t, &preparer{}, sb)
	hd := rt.Spawn(task.Launch{ID: "t4", Prompt: "done\nlate"})
	<-sb.starting
	if err := hd.Cancel("changed my mind"); err != nil {
		t.Fatalf("cancel while the process starts: %v", err)
	}
	close(sb.release)
	in := awaitEnd(t, rt, "t4")
	if in.Status != task.Cancelled {
		t.Fatalf("ended %+v, want cancelled", in)
	}
	if dirs, _ := filepath.Glob(filepath.Join(h.Work, "task-*")); len(dirs) != 0 {
		t.Errorf("the process directory of a cancelled task remains: %v", dirs)
	}
}

// slowSandbox is Unconfined, and holds each process's start until the test lets it go.
type slowSandbox struct {
	runnerd.Unconfined
	starting, release chan struct{}
}

func (s *slowSandbox) Wrap(cmd *exec.Cmd, dir string, net runnerd.Network) error {
	close(s.starting)
	<-s.release
	return s.Unconfined.Wrap(cmd, dir, net)
}

// What a Preparer readies is what the process gets: its environment as given, its working
// directory, and its extra descriptors from fd 5.
func TestAPreparedProcessGetsWhatWasReadied(t *testing.T) {
	dir := resolvedTempDir(t)
	pr := &preparer{prepared: func(task.SpawnRequest) (*runnerd.Prepared, error) {
		// A pipe per task: the handler closes its copy of the read end once the process has it.
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		return &runnerd.Prepared{
			Dir: dir, Env: []string{"PATH=" + os.Getenv("PATH"), "PREPARED=yes"}, Extra: []*os.File{r},
			Serve: func() { io.WriteString(w, "from the runner"); w.Close() },
		}, nil
	}}
	_, rt := newPrepared(t, pr)

	rt.Spawn(task.Launch{ID: "e1", Prompt: "env\n"})
	in := awaitEnd(t, rt, "e1")
	if in.Status != task.Done || !strings.Contains(in.Result, "PREPARED=yes") {
		t.Fatalf("the process's environment is not the one readied: %+v", in)
	}
	for _, kv := range strings.Split(in.Result, "\n") {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "TMPDIR=") {
			t.Errorf("the handler added %q to an environment given whole", kv)
		}
	}
	rt.Spawn(task.Launch{ID: "p1", Prompt: "pwd\n"})
	in = awaitEnd(t, rt, "p1")
	if in.Status != task.Done || in.Result != dir {
		t.Fatalf("ran in %q, want the readied directory %s", in.Result, dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the readied directory was removed when its task ended, and it was not the handler's to remove: %v", err)
	}
	rt.Spawn(task.Launch{ID: "f1", Prompt: "fd5\n"})
	in = awaitEnd(t, rt, "f1")
	if in.Status != task.Done || in.Result != "from the runner" {
		t.Fatalf("fd 5 read %q, want what Serve wrote", in.Result)
	}
}

func awaitEnd(t *testing.T, rt task.Runner, id string) task.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if in, err := rt.(task.Lister).TaskSnapshot(id); err == nil && in.Status.Terminal() {
			return in
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s didn't end", id)
	return task.Snapshot{}
}

func waitForEndings(t *testing.T, pr *preparer, n int) {
	t.Helper()
	for range 500 {
		if len(pr.endings()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the Preparer was told of %d ending(s), want %d", len(pr.endings()), n)
}
