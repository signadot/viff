package versed

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// waitTerminal blocks until the record says the run is over — an id is reusable only then.
func waitTerminal(t *testing.T, store entity.Store, id string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := store.Get(mirror.Ref(id)); ok {
			if s, _ := entity.ToObj(e.Payload)["status"].(string); task.Status(s).Terminal() {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s never reached a terminal status", id)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A task record says WHICH RUN it describes.
//
// An id names the work — chain plus subject, derived so a re-fire is idempotent and a hook is
// scoped to the run that raised it — and nothing named the run. One entity per id therefore
// meant a second run overwrote the first, field by field, keeping whatever the newer run had
// not yet written. Staging produced this, which is the shape being ruled out:
//
//	started: "2026-08-13T19:05:16Z"     ← the run that was going
//	ended:   "2026-08-13T18:15:14Z"     ← the run before it, fifty minutes earlier
//	status:  running
//	result:  "…"                        ← and its answer
//
// A reader who trusts `ended` concludes the task is over; one who trusts `status` concludes it
// is running. Both are right about different runs, and nothing in the record says so
// (git-issue gjv063rrh12ksvgnfsn0).
func TestATaskRecordSaysWhichRunItIs(t *testing.T) {
	// A driver that finishes, because the case is a RE-FIRE: an id is reused only once the
	// run holding it is over. A second spawn of a LIVE id is deduped by the runtime, which is
	// the property the id exists for and not the one under test here.
	drive := func(l task.Launch, reg *task.Registry) *task.Task {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, reg)
		return task.NewTask(l.ID, sup, &countingDriver{n: 1}, noopExecutor{})
	}
	store := entitytest.Store(t)
	srv := NewWithStore(drive, store)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Something has happened in this verse before the first run — which is not staging for
	// the test's benefit but the ordinary case: a store at revision 0 has had no commits, so
	// a run in it has no revision to name, and the moment anything is written that stops
	// being true.
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"),
		entity.Obj{"stage": "open"}.Payload())

	spawnScoped(t, ts, "run-1", "")
	first := occurrenceOf(t, store, "run-1")
	waitTerminal(t, store, "run-1")
	if first == 0 {
		t.Fatal("a run with no occurrence: the record cannot say which run it is")
	}

	// A second run of the SAME id, as a re-fire produces. The verse has moved on, so this
	// run is about a later revision — which is the whole of what makes the two tellable
	// apart.
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "2"),
		entity.Obj{"stage": "open"}.Payload())
	spawnScoped(t, ts, "run-1", "")

	second := occurrenceOf(t, store, "run-1")
	if second <= first {
		t.Fatalf("the second run's occurrence is %d, not later than the first's %d — "+
			"two runs of one id are still indistinguishable", second, first)
	}
}

// occurrenceOf waits for the record and answers its occurrence. It waits because the mirror
// writes on the task's own goroutine and a spawn returns before the record lands.
func occurrenceOf(t *testing.T, store entity.Store, id string) int64 {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := store.Get(mirror.Ref(id)); ok {
			if v, isInt := entity.ToObj(e.Payload)["occurrence"].(int64); isInt {
				return v
			}
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(mirror.Ref(id))
			t.Fatalf("no occurrence on %s after waiting: %v", id, entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}
