package runnerd

import (
	"testing"

	"github.com/signadot/verse/pkg/task"
)

// A TASK HAS ONE END, on its stream and its snapshot alike, when its own end and its lease's
// arrive together. Whichever the runner takes first stands: a lease that runs out after the
// task's terminal frame was followed ends nothing, and a terminal frame followed after the
// lease ended the task is not kept.
func TestATaskWhoseLeaseRunsOutAsItEndsHasOneEnd(t *testing.T) {
	done := task.Event{Kind: task.EventStatus, Text: string(task.Done), Cursor: 2}
	working := task.Event{Kind: task.EventSay, Text: "working", Cursor: 1}

	t.Run("the end followed first", func(t *testing.T) {
		p := &proc{id: "t", known: task.Snapshot{ID: "t"}}
		p.record(working)
		p.record(done)
		if p.endOrphaned("no reader for 4m") {
			t.Fatal("the lease ended a task whose end was already followed")
		}
		if _, ended := p.snapshot(); ended {
			t.Fatal("a last word was set by a lease that ended nothing")
		}
	})
	t.Run("the lease first", func(t *testing.T) {
		p := &proc{id: "t", known: task.Snapshot{ID: "t"}}
		p.record(working)
		if !p.endOrphaned("no reader for 4m") {
			t.Fatal("the lease did not end a task with no end")
		}
		p.record(done)
		final, _ := p.snapshot()
		events := p.eventsSince(0)
		last := events[len(events)-1]
		if last.Kind == task.EventStatus && task.Status(last.Text).Terminal() && last.Text != string(final.Status) {
			t.Fatalf("the stream ends %s and the snapshot %s", last.Text, final.Status)
		}
		if final.Status != task.Failed || !final.Orphaned {
			t.Fatalf("snapshot = %s orphaned=%v, want failed and orphaned", final.Status, final.Orphaned)
		}
	})
}
