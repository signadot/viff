package runnerd

import (
	"testing"

	"github.com/signadot/verse/pkg/task"
)

// A replay that would read across a hole, or begin before what is kept, starts with a gap
// marker, so the reader resyncs from the snapshot rather than trusting a stream with a hole in it.
func TestAReplayAcrossAHoleSaysSo(t *testing.T) {
	p := &proc{id: "t1"}
	for _, c := range []int64{1, 2, 5, 6} { // 3 and 4 were missed
		p.record(task.Event{Kind: task.EventExec, Task: "t1", Cursor: c})
	}
	p.record(task.Event{Kind: task.EventGap, Cursor: 9}) // a gap marker is not an event of the task
	if got := p.eventsSince(0); len(got) != 5 || got[0].Kind != task.EventGap || got[0].Dropped != 2 {
		t.Fatalf("across the hole: %+v", got)
	}
	if got := p.eventsSince(4); len(got) != 2 || got[0].Kind == task.EventGap {
		t.Fatalf("after the hole: %+v", got)
	}
	for c := int64(7); c < 7+task.RingCap; c++ {
		p.record(task.Event{Kind: task.EventExec, Task: "t1", Cursor: c})
	}
	if n := len(p.events); n != task.RingCap {
		t.Fatalf("kept %d events, want %d", n, task.RingCap)
	}
	if got := p.eventsSince(0); got[0].Kind != task.EventGap {
		t.Fatalf("before the window: %+v", got[0])
	}
}
