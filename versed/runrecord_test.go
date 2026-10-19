package versed

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// A failed run followed by a successful one leaves BOTH.
//
// That is the whole of what a run record buys, and it is what a task:instance cannot do: one
// entity per id, so the second run overwrites the first and the failure is gone — no record
// that it happened, no reason, nothing to compare the retry against. An operator asking "did
// this ever work" gets the last answer and cannot tell whether it is the first.
//
// The run also writes itself at CREATION rather than only at the terminal, which matters for
// the case that prompted this: a run that never finishes. Staging held one wedged for
// three-quarters of an hour, and a terminal-only record would have lost precisely the run
// worth keeping the moment the next one started (git-issue gjv063rrh12ksvgnfsn0).
func TestEachRunLeavesItsOwnRecord(t *testing.T) {
	store := entitytest.Store(t)
	// A driver that fails the first time and succeeds the second, so the two runs differ in
	// the field an operator would actually be reading.
	var runs int
	drive := func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := task.NewSupervisor(task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, reg)
		runs++
		if runs == 1 {
			return task.NewTask(l.ID, sup, &failingDriver{why: "first attempt broke"}, noopExecutor{})
		}
		return task.NewTask(l.ID, sup, &countingDriver{n: 1}, noopExecutor{})
	}
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"),
		entity.Obj{"stage": "open"}.Payload())

	// TWO RUNTIMES over one store, because that is how an id comes to run twice. Within one
	// process a finished task keeps its id — a re-spawn returns the task that already ran —
	// so the second run of an id follows a restart, which is exactly what staging did between
	// its 18:15 and 19:05 runs. A test that spawned twice against one server would be
	// asserting about one run wearing two names.
	srv := NewWithStore(drive, store)
	ts := httptest.NewServer(srv.Handler())
	spawnScoped(t, ts, "run-1", "")
	waitTerminal(t, store, "run-1")
	first := occurrenceOf(t, store, "run-1")
	ts.Close()

	entitytest.Set(t, store, entity.RefAt("demo", "pr", "2"),
		entity.Obj{"stage": "open"}.Payload())

	srv2 := NewWithStore(drive, store)
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	spawnScoped(t, ts2, "run-1", "")
	// Not waitTerminal: the instance ALREADY says terminal, from the run before. What tells
	// the second run apart is its occurrence, which is the point of the whole change.
	second := waitRunAfter(t, store, "run-1", first)

	if first == second {
		t.Fatalf("both runs report occurrence %d", first)
	}

	// Both runs are on the record, at their own addresses, saying different things.
	a := runRecord(t, store, "run-1", first)
	b := runRecord(t, store, "run-1", second)
	if a["err"] == nil {
		t.Fatalf("the failed run's record carries no reason: %v", a)
	}
	if b["err"] != nil {
		t.Fatalf("the successful run's record carries the failure: %v", b)
	}

	// And the instance describes ONE run, not two merged. This is the shape that started all
	// of it: a record reading `ended` before `started`, because a write is a patch and the new
	// run's fields landed on top of the old run's without displacing them. Somebody reading it
	// cannot tell which half is about which run — and the earlier fix, which stamped the
	// occurrence, made that detectable without making it stop.
	inst := entity.ToObj(mustGet(t, store, mirror.Ref("run-1")))
	if inst["err"] != nil {
		t.Fatalf("the second run's record carries the first's failure: %v", inst)
	}
	if started, ended := inst["started"], inst["ended"]; started != nil && ended != nil {
		if started.(string) > ended.(string) {
			t.Fatalf("the record started (%v) after it ended (%v) — two runs in one document",
				started, ended)
		}
	}

	// And the instance is still the live view — the latest run, as every reader of it expects.
	e, ok, err := store.Get(mirror.Ref("run-1"))
	if err != nil || !ok {
		t.Fatalf("no instance record: ok=%v err=%v", ok, err)
	}
	if got := entity.ToObj(e.Payload)["occurrence"]; got != second {
		t.Fatalf("the instance reports occurrence %v, want the latest run's %d", got, second)
	}
}

func runRecord(t *testing.T, store entity.Store, id string, occurrence int64) entity.Obj {
	t.Helper()
	e, ok, err := store.Get(mirror.RunRef(id, occurrence))
	if err != nil || !ok {
		t.Fatalf("no run record for %s@%d: ok=%v err=%v", id, occurrence, ok, err)
	}
	return entity.ToObj(e.Payload)
}

// failingDriver ends the run with a reason, which is the field the two records must differ in.
type failingDriver struct{ why string }

func (d *failingDriver) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	return task.ToolUse{}, false, errors.New(d.why)
}

// waitRunAfter waits until the instance describes a LATER run than prev, terminally, and
// answers its occurrence.
func waitRunAfter(t *testing.T, store entity.Store, id string, prev int64) int64 {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := store.Get(mirror.Ref(id)); ok {
			p := entity.ToObj(e.Payload)
			occ, _ := p["occurrence"].(int64)
			st, _ := p["status"].(string)
			if occ != prev && occ != 0 && task.Status(st).Terminal() {
				return occ
			}
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(mirror.Ref(id))
			t.Fatalf("no run after %d on %s: %v", prev, id, entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A field the mirror clears is ABSENT, not present-and-null.
//
// The mirror cleared `observed` by writing nil, with a comment saying "nil, not delete: a
// write MERGES" — true while a bare null meant removal, and wrong since baa0c459 made a bare
// null data. It was writing `observed: null` where it meant to write nothing, so a charter
// asking `{observed: null}` — tony's present-with-any-value idiom — matched a task the mirror
// had just told it was fine.
func TestAClearedFieldIsAbsent(t *testing.T) {
	store := entitytest.Store(t)
	drive := func(l runner.Launch, reg *task.Registry) *task.Task {
		sup := task.NewSupervisor(task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}, reg)
		return task.NewTask(l.ID, sup, &countingDriver{n: 1}, noopExecutor{})
	}
	srv := NewWithStore(drive, store)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	spawnScoped(t, ts, "run-1", "")
	waitTerminal(t, store, "run-1")

	rec := entity.ToObj(mustGet(t, store, mirror.Ref("run-1")))
	for _, k := range []string{"observed", "pending"} {
		if v, present := rec[k]; present && v == nil {
			t.Fatalf("%q is present holding null; a cleared field must be absent: %v", k, rec)
		}
	}
}

func mustGet(t *testing.T, store entity.Store, ref entity.Ref) entity.Payload {
	t.Helper()
	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		t.Fatalf("get %s: ok=%v err=%v", ref, ok, err)
	}
	return e.Payload
}
