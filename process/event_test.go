package process

import (
	"context"
	"testing"
	"time"
)

// A run emits the loop-level events to a subscriber.
func TestEventBusRun(t *testing.T) {
	d := &scriptDriver{steps: []ToolUse{tool("a"), tool("b")}}
	p := NewProcess("t", NewSupervisor(autoVec(), AutoResolver{}), d, &recExec{})
	ch, unsub := p.Subscribe()
	defer unsub()

	seenCh := make(chan map[string]int, 1)
	go func() {
		seen := map[string]int{}
		for ev := range ch {
			seen[ev.Kind]++
			if ev.Kind == EvStatus { // run ended
				break
			}
		}
		seenCh <- seen
	}()

	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	select {
	case seen := <-seenCh:
		if seen[EvBoundary] == 0 || seen[EvExec] == 0 || seen[EvStatus] == 0 {
			t.Fatalf("missing expected events: %v", seen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events")
	}
}

// A subscriber that can't keep up drops events and gets a gap marker rather than
// blocking the loop.
func TestEventBusGapMarker(t *testing.T) {
	p := NewProcess("t", NewSupervisor(autoVec(), AutoResolver{}), &scriptDriver{}, &recExec{})
	ch, unsub := p.Subscribe()
	defer unsub()

	for i := 0; i < 300; i++ { // overflow the 256 buffer without draining → drops accrue
		p.emit(Frame{Kind: EvBoundary, Seq: i})
	}
	for i := 0; i < 10; i++ { // free some slots, then emit → gap marker flushes
		<-ch
	}
	p.emit(Frame{Kind: EvBoundary, Seq: 999})

	sawGap, dropped := false, 0
	for {
		select {
		case ev := <-ch:
			if ev.Kind == EvGap {
				sawGap, dropped = true, ev.Dropped
			}
		default:
			if !sawGap || dropped == 0 {
				t.Fatalf("expected a gap marker with dropped>0, got gap=%v dropped=%d", sawGap, dropped)
			}
			return
		}
	}
}
