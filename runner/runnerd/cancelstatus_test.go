package runnerd_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/signadot/verse/task"
)

// A CANCELLED TASK IS CANCELLED, not failed.
//
// `Run` returns the run's CAUSE alongside its outcome — that is how a cancel's reason reaches a
// caller at all — so `err != nil` is true for a task somebody stopped on purpose. The handle
// folded that as `status = Failed`, throwing away what the machine had just decided and emitted
// as its terminal frame.
//
// It is not cosmetic. verse splits the two everywhere it matters: a charter's `onCancelled` and
// `onFailed` are different actions, and verse-dev's merge train says why in its own words
// — a cancel decided nothing, so the gate is asked again, where a failed run must not
// re-ask anybody anything. With the fold, a cancelled run fired onFailed and the question was
// never re-posed.
//
// Driven by hand first: `verse task run --driver scripted "sleep 5"`, `verse task cancel <id>`,
// and the entity read `status: failed` with `err: "cancelled: cancelled via daemon"` — the
// record disagreeing with itself in two adjacent fields.
func TestACancelledTaskIsNotReportedAsFailed(t *testing.T) {
	d, srv := hosting(t)
	if code := spawnOne(t, srv.URL, "run-cancel"); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("spawn: %d", code)
	}

	tk := d.Lookup("run-cancel")
	if tk == nil {
		t.Fatal("the task is not hosted")
	}
	if err := tk.Cancel("by hand"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	var got task.Snapshot
	for i := 0; i < 200; i++ {
		in, ok := d.SnapshotOf("run-cancel")
		if ok && in.Status.Terminal() {
			got = in
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Status != task.Cancelled {
		t.Errorf("a cancelled task ended %q: an error is a CAUSE, not a status, and the machine "+
			"had already decided this one", got.Status)
	}
	// The cause is still recorded — the point is that it does not overwrite how the run ended.
	if got.Err == "" {
		t.Error("the cancel's reason was dropped along with the wrong status")
	}
}
