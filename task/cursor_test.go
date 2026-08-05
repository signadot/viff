package task

import (
	"fmt"
	"testing"
)

// SubscribeSince replays the retained window after a cursor, then streams live — the
// resumable read plane a reconnecting consumer uses.
func TestSubscribeSinceReplaysAfterCursor(t *testing.T) {
	p := &Task{ID: "t"}
	for i := 1; i <= 5; i++ {
		p.Emit(Frame{Kind: EvSay, Text: fmt.Sprintf("m%d", i)})
	}
	if p.Cursor() != 5 {
		t.Fatalf("cursor=%d, want 5", p.Cursor())
	}

	ch, unsub := p.SubscribeSince(2) // resume after cursor 2 → replay 3,4,5
	defer unsub()
	var got []int64
	for i := 0; i < 3; i++ {
		got = append(got, (<-ch).Cursor)
	}
	if len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Fatalf("replay cursors=%v, want [3 4 5]", got)
	}

	p.Emit(Frame{Kind: EvSay, Text: "m6"}) // live event after the replay
	if ev := <-ch; ev.Cursor != 6 {
		t.Fatalf("live cursor=%d, want 6", ev.Cursor)
	}
}

// A cursor below the retained window's low-watermark gets a resync marker (EvGap
// carrying the current cursor), telling the consumer to re-read the snapshot.
func TestSubscribeSinceBelowWatermarkResyncs(t *testing.T) {
	p := &Task{ID: "t"}
	for i := 0; i < ringCap+10; i++ { // overflow the ring; low-watermark rises past 1
		p.Emit(Frame{Kind: EvSay})
	}
	ch, unsub := p.SubscribeSince(1) // 1 is now below the retained window
	defer unsub()
	ev := <-ch
	if ev.Kind != EvGap {
		t.Fatalf("first event kind=%q, want a %q resync marker", ev.Kind, EvGap)
	}
	if ev.Cursor != p.Cursor() {
		t.Fatalf("resync cursor=%d, want current %d", ev.Cursor, p.Cursor())
	}
}
