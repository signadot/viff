package versed

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// watchedReads records the prefix every listing is asked at, so a test can pin WHAT WAS READ
// rather than how long it took — which is the property, and does not flake on a loaded machine.
type watchedReads struct {
	entity.Store
	at [][]string
}

func (w *watchedReads) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	w.at = append(w.at, append([]string{}, at...))
	return w.Store.ListAt(at, depth, pred)
}

// LISTING TASKS DOES NOT READ THE RUN ARCHIVE. The rows are instances; asked one level up, the
// read also pulls every `task:run:<id>@<rev>` and every claim across the wire and throws them
// away in Go. The archive is unbounded — a run record per firing, and nothing prunes them — so
// the cost grows with the verse's whole history rather than with the number of tasks it lists.
// Measured before this narrowing: 100 instances with 40 runs each answered GET /tasks in 2.8s,
// for 12.9KB of answer; after, 8ms. git-issue w2j58rp2h12ksgdzhhn0.
func TestListingTasksDoesNotReadTheRunArchive(t *testing.T) {
	st := &watchedReads{Store: entitytest.Store(t)}
	payload := entity.Obj{"status": "done", "prompt": "review"}.Payload()
	entitytest.Set(t, st, entity.VerseRef(mirror.System, mirror.InstanceKind, "review-1"), payload)
	for rev := int64(100); rev < 140; rev++ {
		entitytest.Set(t, st, mirror.RunRef("review-1", rev), payload)
	}

	rows := taskRows(st)
	if len(rows) != 1 {
		t.Fatalf("taskRows listed %d rows; want the one instance", len(rows))
	}
	if len(st.at) != 1 {
		t.Fatalf("taskRows made %d listings; want one", len(st.at))
	}
	got := strings.Join(st.at[0], ".")
	want := strings.Join([]string{entity.VerseRoot, mirror.System, mirror.InstanceKind}, ".")
	if got != want {
		t.Fatalf("tasks were listed at %q, so the read carries every run record and claim "+
			"under %q with it; want %q", got, strings.Join(st.at[0], "."), want)
	}
}
