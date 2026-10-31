package entity

import (
	"context"
	"sync/atomic"
)

// DefaultWatchSlots is how many watches a store opens at once where its config says nothing
// (LogdConfig.WatchSlots is the knob). Under logd's eight reads per session
// (maxConcurrentReads), so that pings, writes and the reads of the moment keep flowing beside
// a round of opens.
const DefaultWatchSlots = 4

// watchGate is the line a watch waits in to open: a slot is taken before the establish and
// given back when the opening State (or Absent) has arrived, since that picture is a read the
// store serves and a session has a few reads at a time. Asked for every watch at once — the
// restore arming every rule at start, a drop re-establishing every watch it ended — they take
// every read slot, the ping queues behind them, the heartbeat tears the session down, and the
// round begins again: on staging one timeout became twenty minutes of them (git-issue
// wz2pp5j0). In line, a round is a few at a time, and the session answers between. The line
// is the client's side of it: that a session's ping waits behind its reads at all is logd's
// (tony-format x32pp5j0), and the line's width is set against logd's count as it stands.
//
// THE LINE PACES PUMPS, NOT SUBSCRIBERS: a watch is one per prefix (pump), and the subscribers
// that join one open take no slot.
//
// FIFO, and one line: a re-establish and a new watch wait together. Two kinds of watch are not
// in it. The root watch (Observe) is one watch, opened only while something observes. A watch
// asked for its progress (WatchSubtreeProgress) is the watermark's: every attributed read above
// the watermark waits on it, and is refused while it has no standing watch (see watermark.go,
// "it fails closed"), so one in line would hold every such read for the length of the round
// behind it. They are few — the loaded view's two, over the perms mirror — and their pictures
// are small; they open ahead of the line, and the line is sized with room for them.
//
// OFF is a line with no slots (LogdConfig.WatchSlots below zero; versed reads it from
// VERSE_WATCH_SLOTS): take and give do nothing, and watches open as they are asked for.
type watchGate struct {
	slots    chan struct{} // nil when off
	waiting  atomic.Int64  // watches in line
	inFlight atomic.Int64  // watches between their establish and their opening picture
	high     atomic.Int64  // the most in flight at once: what a test checks
}

func newWatchGate(n int) *watchGate {
	if n < 0 {
		return &watchGate{}
	}
	if n == 0 {
		n = DefaultWatchSlots
	}
	return &watchGate{slots: make(chan struct{}, n)}
}

// take waits for a slot, or for ctx.
func (g *watchGate) take(ctx context.Context) error {
	if g.slots == nil {
		return nil
	}
	g.waiting.Add(1)
	defer g.waiting.Add(-1)
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	n := g.inFlight.Add(1)
	for {
		h := g.high.Load()
		if n <= h || g.high.CompareAndSwap(h, n) {
			return nil
		}
	}
}

// give returns a slot.
func (g *watchGate) give() {
	if g.slots == nil {
		return
	}
	g.inFlight.Add(-1)
	<-g.slots
}
