package task

import (
	"testing"
	"time"
)

// burst emits n frames and then a terminal, without anybody draining in between — the shape
// of a real task that outruns its observer.
func burst(t *testing.T, p *Task, n int) {
	t.Helper()
	for range n {
		p.emit(Event{Kind: EventExec, Tool: "noop"})
	}
	p.emit(Event{Kind: EventStatus, Text: string(Done)})
}

// drain reads a channel to its end (or until it stalls) and reports what arrived.
func drain(t *testing.T, ch <-chan Event) (kinds []string, closed bool) {
	t.Helper()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return kinds, true
			}
			kinds = append(kinds, ev.Kind+":"+ev.Text)
		case <-time.After(time.Second):
			return kinds, false
		}
	}
}

func sawTerminal(kinds []string) bool {
	for _, k := range kinds {
		if k == EventStatus+":"+string(Done) {
			return true
		}
	}
	return false
}

// A subscriber that falls behind still learns the task ended.
//
// It did not. Two things conspired: the buffer filled and the terminal was dropped like any
// other frame, and — because a subscriber that is behind is exactly the one in the dropped
// state — emit was busy failing to fit a gap marker and skipped the event entirely. Both had
// to be fixed; either alone leaves the terminal lost.
//
// What made it silent is that the gap marker rides the NEXT delivered event, and nothing
// follows a terminal. So the loss announced itself to nobody: the entity said `running`
// forever, mirror.Reflect's goroutine never returned, and onDone never fired.
func TestABehindSubscriberStillGetsTheTerminal(t *testing.T) {
	for _, n := range []int{10, 300, 5000} {
		p := NewTask("p", toolSup(allowAll(), AutoResolver{}), nil, nil)
		ch, unsub := p.Subscribe()
		burst(t, p, n) // nothing is draining ch while this runs
		unsub()

		kinds, _ := drain(t, ch)
		if !sawTerminal(kinds) {
			t.Fatalf("n=%d: %d frames delivered and none was the terminal", n, len(kinds))
		}
	}
}

// The reservation is what makes that certain rather than lucky: an ordinary frame is refused
// at cap-1, so a slot is always free when the terminal arrives. Without it the terminal
// competes with 256 replaceable frames for the same space.
func TestAnOrdinaryFrameCannotTakeTheLastSlot(t *testing.T) {
	s := &subscriber{ch: make(chan Event, 4)}
	for range 10 {
		trySend(s, Event{Kind: EventExec})
	}
	if got := len(s.ch); got != 3 {
		t.Fatalf("buffer holds %d of 4, want 3 — the last slot is not reserved", got)
	}
	trySend(s, Event{Kind: EventStatus, Text: string(Done)})
	if got := len(s.ch); got != 4 {
		t.Fatalf("buffer holds %d of 4 after the terminal, want 4 — it did not fit", got)
	}
}

// After the terminal the stream ENDS. Nothing will follow it, and saying so is the one
// signal that cannot be lost for want of buffer — a closed channel needs no room.
//
// A reader left listening to a task that has stopped speaking is not waiting, it is
// leaking: that is how a mirror goroutine and, behind it, an HTTP connection to a remote
// runner survived their task indefinitely.
func TestTheStreamEndsAfterTheTerminal(t *testing.T) {
	p := NewTask("p", toolSup(allowAll(), AutoResolver{}), nil, nil)
	ch, unsub := p.Subscribe()
	defer unsub()
	burst(t, p, 3)

	kinds, closed := drain(t, ch)
	if !closed {
		t.Fatalf("the stream stayed open after the terminal: %v", kinds)
	}
	if !sawTerminal(kinds) {
		t.Fatalf("the stream ended without delivering the terminal: %v", kinds)
	}
}

// And subscribing to a task that has ALREADY ended gives its outcome, not silence.
//
// `Subscribe` means live-from-now, and for a finished task what is true now is that it
// finished. Answering with nothing at all is what strands a mirror on a completed task —
// the failure FailedHandle exists to avoid, reachable by a second route.
func TestSubscribingToAFinishedTask(t *testing.T) {
	p := NewTask("p", toolSup(allowAll(), AutoResolver{}), nil, nil)
	burst(t, p, 3)

	ch, unsub := p.Subscribe()
	defer unsub()
	kinds, closed := drain(t, ch)
	if !sawTerminal(kinds) || !closed {
		t.Fatalf("a late subscriber got %v (closed=%v), want the terminal and the end", kinds, closed)
	}

	// The cursored form answers with the record it retains, which already ends in the
	// terminal, and then ends too.
	since, unsub2 := p.SubscribeSince(0)
	defer unsub2()
	kinds, closed = drain(t, since)
	if !sawTerminal(kinds) || !closed {
		t.Fatalf("a late resume got %v (closed=%v), want the record and the end", kinds, closed)
	}
}

// A task that never ran has no outcome to report, and must not invent one by handing a
// late subscriber the last thing it happened to say.
func TestSubscribingToALiveTaskStaysOpen(t *testing.T) {
	p := NewTask("p", toolSup(allowAll(), AutoResolver{}), nil, nil)
	p.emit(Event{Kind: EventExec, Tool: "noop"})

	ch, unsub := p.Subscribe()
	defer unsub()
	select {
	case ev, ok := <-ch:
		t.Fatalf("a live task handed a new subscriber %+v (open=%v)", ev, ok)
	case <-time.After(100 * time.Millisecond):
	}
}

// A reader that fell below the retained window is told WHAT it missed, not merely that it
// missed something.
//
// The live path replays open gates; the resume path did not, on the reasoning that the ring
// carries them — which stops being true at exactly the moment the gap fires. A resolver is a
// client of this call and nothing else, so it reconnected, learned it was behind, and could
// not see the open question it exists to answer.
func TestAResumeBelowTheWindowReplaysOpenGates(t *testing.T) {
	p := NewTask("p", toolSup(allowAll(), AutoResolver{}), nil, nil)
	p.emit(Event{Kind: EventPending, ID: "p~d1", Gate: "write"})
	gateAt := p.Cursor()
	for range RingCap + 10 { // it keeps talking while the gate stands
		p.emit(Event{Kind: EventSay, Text: "thinking"})
	}

	ch, unsub := p.SubscribeSince(gateAt - 1)
	defer unsub()
	kinds, _ := drain(t, ch)
	if len(kinds) == 0 || kinds[0] != EventGap+":" {
		t.Fatalf("first frame = %v, want the gap marker", kinds[:min(2, len(kinds))])
	}
	var sawGate bool
	for _, k := range kinds {
		if k == EventPending+":" {
			sawGate = true
		}
	}
	if !sawGate {
		t.Fatalf("the resume said %d frames and none was the standing gate", len(kinds))
	}
}

// And a gate that has been ANSWERED is not replayed: it is not open, and re-announcing it
// would put a settled question back on a reader's docket.
func TestAResumeDoesNotReplayAnsweredGates(t *testing.T) {
	p := NewTask("p", toolSup(allowAll(), AutoResolver{}), nil, nil)
	p.emit(Event{Kind: EventPending, ID: "p~d1", Gate: "write"})
	gateAt := p.Cursor()
	p.emit(Event{Kind: EventDecision, ID: "p~d1", Proceed: true, By: "human:scott"})
	for range RingCap + 10 {
		p.emit(Event{Kind: EventSay, Text: "thinking"})
	}

	ch, unsub := p.SubscribeSince(gateAt - 1)
	defer unsub()
	kinds, _ := drain(t, ch)
	for _, k := range kinds {
		if k == EventPending+":" {
			t.Fatal("an answered gate was replayed as though it were still open")
		}
	}
}

// Without a gap there is nothing to re-announce: the ring covers everything since the cursor,
// so replaying open gates as well would show the reader the same question twice.
func TestAResumeInsideTheWindowDoesNotDuplicate(t *testing.T) {
	p := NewTask("p", toolSup(allowAll(), AutoResolver{}), nil, nil)
	p.emit(Event{Kind: EventSay, Text: "starting"})
	from := p.Cursor()
	p.emit(Event{Kind: EventPending, ID: "p~d1", Gate: "write"})

	ch, unsub := p.SubscribeSince(from)
	defer unsub()
	kinds, _ := drain(t, ch)
	var gates int
	for _, k := range kinds {
		if k == EventPending+":" {
			gates++
		}
	}
	if gates != 1 {
		t.Fatalf("the standing gate appeared %d times in a resume inside the window", gates)
	}
}
