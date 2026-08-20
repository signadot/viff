package taskd_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/taskd"
)

// steady takes n steps at a pace, so a task is still running when a second spawn arrives.
type steady struct{ n int }

func (d *steady) Next(ctx context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	if d.n <= 0 {
		return task.ToolUse{}, false, nil
	}
	d.n--
	select {
	case <-time.After(20 * time.Millisecond):
	case <-ctx.Done():
	}
	return task.ToolUse{Name: "noop"}, true, nil
}

type echoExec struct{}

func (echoExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu}, nil, nil
}

func hosting(t *testing.T) (*taskd.Daemon, *httptest.Server) {
	t.Helper()
	d := taskd.New(func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, reg)
		return task.NewTask(l.ID, sup, &steady{n: 50}, echoExec{})
	})
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	return d, srv
}

func spawnOne(t *testing.T, url, id string) int {
	t.Helper()
	b, err := codec.Marshal(map[string]any{"id": id, "prompt": "work"}, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url+"/tasks", codec.MediaType(codec.Tony), bytes.NewReader(b))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// An id is spawned once. A second spawn of a live id returns the task that is already
// running, and does not start another.
//
// Without this the second spawn REPLACED the first in the daemon's map. The original kept
// running — its goroutine untouched — with nothing able to reach it, address it or stop it,
// while every control, event and gate for that id went to the newcomer.
//
// It is what makes a spawn safe to retry, and a spawn that cannot be retried is one a client
// must abandon on a timeout it cannot interpret.
func TestSpawningOneIDTwiceKeepsTheFirstTask(t *testing.T) {
	d, srv := hosting(t)

	if got := spawnOne(t, srv.URL, "p"); got != http.StatusCreated {
		t.Fatalf("first spawn = %d, want 201", got)
	}
	first, ok := d.SnapshotOf("p")
	if !ok {
		t.Fatal("the task was not hosted")
	}
	time.Sleep(60 * time.Millisecond) // let it get somewhere

	if got := spawnOne(t, srv.URL, "p"); got != http.StatusOK {
		t.Fatalf("second spawn = %d, want 200 — a repeat is the same task, not a new one", got)
	}
	if n := d.Count(); n != 1 {
		t.Fatalf("hosted count = %d, want 1", n)
	}

	// The SAME task, still going: its cursor has advanced past where the first spawn
	// left it rather than restarting. A replacement would read as a task that began now.
	second, _ := d.SnapshotOf("p")
	if second.Cursor <= first.Cursor {
		t.Fatalf("cursor went %d → %d; the id was taken over by a fresh task",
			first.Cursor, second.Cursor)
	}
}

// Two spawns of one id at the same moment: one builds, one joins, and no second loop starts.
func TestConcurrentSpawnsOfOneIDDoNotRace(t *testing.T) {
	d, srv := hosting(t)

	codes := make(chan int, 2)
	for range 2 {
		go func() { codes <- spawnOne(t, srv.URL, "p") }()
	}
	a, b := <-codes, <-codes
	if n := d.Count(); n != 1 {
		t.Fatalf("hosted count = %d after two concurrent spawns, want 1", n)
	}
	if (a == http.StatusCreated) == (b == http.StatusCreated) {
		t.Fatalf("both spawns answered %d and %d; exactly one should have built it", a, b)
	}
}

// quick finishes immediately, and reports what it was asked so a second run is distinguishable
// from a replay of the first.
type quick struct{ answer string }

func (d *quick) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	return task.ToolUse{}, false, nil
}
func (d *quick) Result() string { return d.answer }

// A SPAWN OF AN ID THAT ALREADY COMPLETED RUNS, where it was answered with the old run.
//
// Nothing reaps the hosted map, so a finished task stayed in it and every later spawn of the
// same id came back INSTANTLY carrying the first run's answer — with the second run's prompt on
// the record beside it. The model was never called, and a completion hook then ran on the
// replayed answer and advanced the flow as though the work had been done
// (git-issue 6j911yhzh12kswgjgsn0).
//
// The id is stable by design: it is what makes a task addressable and what stops a re-fire
// spawning twice. So any rule whose subject OUTLIVES one task — a cursor, a queue head,
// anything that walks — got one real run and then unbounded silent no-ops that each looked like
// success. The dedupe is against a task that is still RUNNING, which is what it means everywhere
// else.
func TestSpawningAnIDThatAlreadyCompletedRunsAgain(t *testing.T) {
	var built int
	answers := []string{"first run", "second run"}
	d := taskd.New(func(l task.Launch, reg *task.Registry, _ task.ContentReader) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, reg)
		drv := &quick{answer: answers[min(built, len(answers)-1)]}
		built++
		return task.NewTask(l.ID, sup, drv, echoExec{})
	})
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	if got := spawnOne(t, srv.URL, "walker"); got != http.StatusCreated {
		t.Fatalf("first spawn = %d, want 201", got)
	}
	waitDone(t, d, "walker")
	if got := snapOf(t, d, "walker").Result; got != "first run" {
		t.Fatalf("first result = %q", got)
	}

	// The same id again, now that the first has finished.
	if got := spawnOne(t, srv.URL, "walker"); got != http.StatusCreated {
		t.Fatalf("second spawn = %d, want 201 — a completed id is a previous run, not this one", got)
	}
	if built != 2 {
		t.Fatalf("the driver was built %d time(s): the second spawn replayed instead of running", built)
	}
	waitDone(t, d, "walker")
	if got := snapOf(t, d, "walker").Result; got != "second run" {
		t.Fatalf("second result = %q, want the SECOND run's answer — this is the replay", got)
	}
}

func snapOf(t *testing.T, d *taskd.Daemon, id string) task.Instance {
	t.Helper()
	s, ok := d.SnapshotOf(id)
	if !ok {
		t.Fatalf("%s is not hosted", id)
	}
	return s
}

func waitDone(t *testing.T, d *taskd.Daemon, id string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if s, ok := d.SnapshotOf(id); ok && s.Status.Terminal() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s never finished", id)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
