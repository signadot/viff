package runnerd_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/client"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/systems/systemstest"
	"github.com/signadot/verse/pkg/task"
)

// leased is each handler with every task's lease set to lease, and a launch of a task that
// runs until it is stopped.
func leased(t *testing.T, lease time.Duration) map[string]func(id string) client.Launcher {
	t.Helper()
	return map[string]func(id string) client.Launcher{
		"in the runner's process": func(id string) client.Launcher {
			h := runnerd.NewInProcessHandler(func(l runner.Launch, reg *task.Registry) *task.Task {
				sup := systemstest.Opened(task.NewSupervisor(workEnv, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, reg), workEnv)
				return task.NewTask(l.ID, sup, waitingDriver{}, noopExec{})
			})
			h.SetLease(lease)
			srv := httptest.NewServer(h.HTTPHandler())
			t.Cleanup(srv.Close)
			rt := client.HTTP(srv.URL)
			launchOK(t, rt, runner.Launch{ID: id, Agent: &runner.Agent{Name: "test"}, Environment: openNet, Prompt: "wait"})
			return rt
		},
		"in a process of its own": func(id string) client.Launcher {
			h, rt := newSubprocess(t)
			h.SetLease(lease)
			launchOK(t, rt, runner.Launch{ID: id, Agent: &runner.Agent{Name: "test"}, Environment: openNet, Prompt: "spec wait\n"})
			return rt
		},
	}
}

func launchOK(t *testing.T, rt client.Launcher, l runner.Launch) {
	t.Helper()
	if why, never := task.NeverStarted(rt.Launch(l)); never {
		t.Fatalf("launch %s: %s", l.ID, why)
	}
}

// snapshotOf is the task as its runner holds it.
func snapshotOf(t *testing.T, rt client.Launcher, id string) task.Snapshot {
	t.Helper()
	snap, err := rt.(task.Lister).TaskSnapshot(id)
	if err != nil {
		t.Fatalf("snapshot of %s: %v", id, err)
	}
	return snap
}

// endOf reads a task's stream to its terminal frame.
func endOf(t *testing.T, rt client.Launcher, id string) task.Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		frames, stop := rt.Attach(id).SubscribeSince(0)
		for f := range frames {
			if f.Kind == task.EventStatus && task.Status(f.Text).Terminal() {
				stop()
				return f
			}
		}
		stop()
		select {
		case <-deadline:
			t.Fatalf("no terminal frame on the stream of %s", id)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A task nobody subscribes to for its lease is ended by its runner, failed and orphaned, with
// the lease for its reason, on its snapshot and on its stream. A subscribe that comes after is
// answered with that end and renews nothing, and a launch of the id then runs it again.
func TestATaskNobodyReadsIsEndedAtItsLease(t *testing.T) {
	const lease = 400 * time.Millisecond
	for name, launch := range leased(t, lease) {
		t.Run(name, func(t *testing.T) {
			t0 := time.Now()
			rt := launch("unread")
			var snap task.Snapshot
			for deadline := time.Now().Add(10 * time.Second); ; {
				if snap = snapshotOf(t, rt, "unread"); snap.Status.Terminal() {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the task still runs %s after its launch, with a %s lease and no reader", time.Since(t0), lease)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if took := time.Since(t0); took < lease {
				t.Fatalf("the task ended %s after its launch, before its %s lease ran out", took, lease)
			}
			if snap.Status != task.Failed || !snap.Orphaned || snap.Err != "no reader for 400ms" {
				t.Fatalf("snapshot = %s orphaned=%v err=%q, want it failed, orphaned, for want of a reader", snap.Status, snap.Orphaned, snap.Err)
			}
			end := endOf(t, rt, "unread")
			if end.Text != string(task.Failed) || !end.Orphaned || end.Err != "no reader for 400ms" {
				t.Fatalf("terminal frame = %+v, want it failed, orphaned, for want of a reader", end)
			}
			if snap := snapshotOf(t, rt, "unread"); snap.Status != task.Failed {
				t.Fatalf("after a subscribe past the lease the task is %s, want it still ended", snap.Status)
			}
			launchOK(t, rt, runner.Launch{ID: "unread", Agent: &runner.Agent{Name: "test"}, Environment: openNet, Prompt: "spec wait\n"})
			if snap := snapshotOf(t, rt, "unread"); snap.Status.Terminal() {
				t.Fatalf("a launch of the id after its orphaned end is %s, want it running again", snap.Status)
			}
		})
	}
}

// A subscribe inside the lease keeps the task: it runs on for as long as it is asked after.
func TestASubscribeInsideTheLeaseKeepsTheTask(t *testing.T) {
	const lease = 400 * time.Millisecond
	for name, launch := range leased(t, lease) {
		t.Run(name, func(t *testing.T) {
			rt := launch("read")
			for until := time.Now().Add(3 * lease); time.Now().Before(until); {
				_, stop := rt.Attach("read").SubscribeSince(0)
				time.Sleep(lease / 4)
				stop()
			}
			if snap := snapshotOf(t, rt, "read"); snap.Status.Terminal() {
				t.Fatalf("the task ended (%s: %s) while it was subscribed to four times a lease", snap.Status, snap.Err)
			}
		})
	}
}

// A subscribe for a task its process does not host yet is answered at once, and keeps its lease:
// the task is still there, running, once its process has it.
func TestASubscribeBeforeTheTaskIsHostedIsAnsweredAtOnce(t *testing.T) {
	h, rt := newSubprocess(t)
	h.SetLease(600 * time.Millisecond)
	launchOK(t, rt, runner.Launch{ID: "starting", Agent: &runner.Agent{Name: "test"}, Environment: openNet, Prompt: "spec slow\n"})

	t0 := time.Now()
	frames, stop := rt.Attach("starting").(task.Accepting).SubscribeAccepted(0)
	select {
	case f, ok := <-frames:
		if !ok || f.Kind != task.EventAccepted {
			t.Fatalf("first frame = %+v (open %v), want the subscribe accepted", f, ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the subscribe was not answered")
	}
	if took := time.Since(t0); took > 500*time.Millisecond {
		t.Fatalf("the subscribe was answered after %s, and the task's process takes a second to host it", took)
	}
	defer stop()
	// Asked after four times a lease until well past the second its process takes.
	for until := t0.Add(1800 * time.Millisecond); time.Now().Before(until); {
		_, stop := rt.Attach("starting").SubscribeSince(0)
		time.Sleep(150 * time.Millisecond)
		stop()
	}
	if snap := snapshotOf(t, rt, "starting"); snap.Status != task.Running {
		t.Fatalf("the task is %s (%s) after its process hosted it, want it running", snap.Status, snap.Err)
	}
	// And the stream opened before it was hosted carries its frames now that it is.
	select {
	case _, ok := <-frames:
		if !ok {
			t.Fatal("the stream opened before the task was hosted closed with no frame of the task's")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame of the task's on the stream opened before it was hosted")
	}
}

// A partition between verse and a runner: the runner ends the task at its lease, while verse is
// still waiting on the runner, and when the runner can be reached again verse hears that end
// and writes it on the task's record.
func TestTheRunnerEndsATaskBeforeVerseStopsWaitingOnIt(t *testing.T) {
	const lease = 400 * time.Millisecond
	h := runnerd.NewInProcessHandler(func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(workEnv, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, reg), workEnv)
		return task.NewTask(l.ID, sup, waitingDriver{}, noopExec{})
	})
	h.SetLease(lease)
	direct := httptest.NewServer(h.HTTPHandler())
	t.Cleanup(direct.Close)
	// What verse reaches the runner through, which the test cuts.
	var cut atomic.Bool
	target, _ := url.Parse(direct.URL)
	through := httputil.NewSingleHostReverseProxy(target)
	through.FlushInterval = -1
	link := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cut.Load() {
			http.Error(w, "partitioned", http.StatusBadGateway)
			return
		}
		ctx, stop := context.WithCancel(r.Context())
		defer stop()
		go func() {
			for !cut.Load() && ctx.Err() == nil {
				time.Sleep(10 * time.Millisecond)
			}
			stop() // an open stream is cut with the link
		}()
		through.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(link.Close)

	launchOK(t, client.HTTP(direct.URL), runner.Launch{ID: "p", Agent: &runner.Agent{Name: "test"}, Environment: openNet, Prompt: "wait"})
	store := entitytest.Store(t)
	mirror.Reflect(store, "p", client.HTTP(link.URL).Attach("p"), mirror.Meta{Runner: link.URL, Lease: lease.String()})
	record := func() map[string]any {
		e, _, _ := store.Get(mirror.Ref("p"))
		return entity.ToObj(e.Payload)
	}

	// Watched: the task outlives its lease while verse subscribes.
	time.Sleep(2 * lease)
	if snap := snapshotOf(t, client.HTTP(direct.URL), "p"); snap.Status.Terminal() {
		t.Fatalf("the task ended (%s: %s) while verse was subscribed to it", snap.Status, snap.Err)
	}

	cut.Store(true)
	for deadline := time.Now().Add(10 * time.Second); !snapshotOf(t, client.HTTP(direct.URL), "p").Status.Terminal(); {
		if time.Now().After(deadline) {
			t.Fatal("the runner did not end a task verse could not reach")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := record(); got["status"] != string(task.Running) {
		t.Fatalf("record = %v when the runner ended the task, want verse still waiting on the runner", got)
	}

	cut.Store(false)
	for deadline := time.Now().Add(15 * time.Second); record()["status"] == string(task.Running); {
		if time.Now().After(deadline) {
			t.Fatalf("verse did not hear the task's end once the runner could be reached: %v", record())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := record(); got["status"] != string(task.Failed) || got[mirror.OrphanedField] != true || got["err"] != "no reader for 400ms" {
		t.Fatalf("record = %v, want the runner's end: failed, orphaned, for want of a reader", got)
	}
}

// A task's process is stopped with the commands it started: no sandbox ends them with it here,
// and one left running would go on with the work of a task whose record says it ended.
func TestStoppingATaskStopsTheCommandsItStarted(t *testing.T) {
	h, rt := newSubprocess(t)
	h.SetLease(400 * time.Millisecond)
	said := filepath.Join(t.TempDir(), "pid")
	launchOK(t, rt, runner.Launch{ID: "c", Agent: &runner.Agent{Name: "test"}, Environment: openNet, Prompt: "spec child\n" + said})
	var pid int
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(said); err == nil && len(b) > 0 {
			pid = must(strconv.Atoi(string(b)))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the task did not start its command: %s", snapshotOf(t, rt, "c").Err)
		}
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })

	if end := endOf(t, rt, "c"); !end.Orphaned {
		t.Fatalf("the task ended %s (%s), not orphaned at its lease", end.Text, end.Err)
	}
	for deadline := time.Now().Add(3 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the command the task started (pid %d) is still running after the task was stopped", pid)
		}
	}
}
