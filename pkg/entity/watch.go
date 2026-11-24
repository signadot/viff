package entity

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	logdapi "github.com/signadot/tony-format/go-tony/system/logd/api"
)

// WatchPhase is what the store is doing for a watch now. A watch is a thing the store works on
// over time — it waits for a slot, asks the substrate, is answered or refused, drops and is
// asked again — and a consumer that holds one is told where it stands rather than made to wait
// on it (Store.Watch).
type WatchPhase int

const (
	WatchInLine    WatchPhase = iota // waiting for a slot in the line (watchGate)
	WatchOpening                     // the establish is sent and the opening picture not in
	WatchOpen                        // the picture is in; streaming
	WatchReopening                   // it ended, or was found dead; in line or establishing again
	WatchRefused                     // the last establish failed; asked again after a backoff
	WatchFailed                      // refused for a reason asking again cannot cure; final
)

// String is the phase as status and an install's answer say it; the open phase says nothing.
func (p WatchPhase) String() string {
	switch p {
	case WatchInLine:
		return "in line at the store"
	case WatchOpening:
		return "its watch is being opened"
	case WatchReopening:
		return "its watch is being opened again after a drop"
	case WatchRefused:
		return "the store refused its watch, and is asked again"
	case WatchFailed:
		return "the store cannot watch it"
	}
	return ""
}

// WatchState is a watch's phase, with the error of a refusal and when the phase was entered.
type WatchState struct {
	Phase WatchPhase
	Err   error // the establish's error while Refused or Failed
	Since time.Time
	// Opened is whether the watch has been Open before: a Refused after that is a watch that
	// stood being asked for again, which a consumer that wants its stream waits through.
	Opened bool
}

// String is the state as a wait is reported: the phase's words, with the refusal's.
func (s WatchState) String() string {
	if s.Err != nil {
		return s.Phase.String() + ": " + s.Err.Error()
	}
	return s.Phase.String()
}

// A Watch is a subscriber to the store's watch on a prefix (pump), answered at once: what the
// store has to do to open it — the line, the establish, its retries — is the store's, and the
// handle says where that stands.
type Watch struct {
	// Commits is the stream: one commit per commit the watched slice changed at the pattern,
	// opened with what already stands there unless NoInit asked otherwise (Store.WatchSubtree).
	Commits <-chan Commit
	// State is the phase now, and a channel closed when it next changes. The two are read
	// together, so a reader that finds the channel closed asks again and misses nothing.
	State func() (WatchState, <-chan struct{})
	// Ready is closed at the watch's first Open, and never reopened: what a consumer waits on
	// that wants the watch standing before it goes on.
	Ready <-chan struct{}
	// Progress asks the standing watch how far it is current, for a handle opened with
	// Progress (WatchSubtreeProgress); nil otherwise.
	Progress func(ctx context.Context) (int64, error)
	// Close ends the subscription: the stream closes, and a watch in line is out of it.
	Close func()
}

// WatchOptions are a watch's manners.
type WatchOptions struct {
	// WaitIfAbsent opens on a path holding nothing and waits for it; without it, such a path
	// Fails the watch with what the picture showed — a person naming a path at a command line
	// would rather be told than wait.
	WaitIfAbsent bool
	// NoInit joins at the next change: the opening picture is taken in and not sent.
	NoInit bool
	// Progress asks the watch's progress (Watch.Progress); such a watch opens ahead of the line.
	Progress bool
}

// await is what the conveniences do with a handle: wait for the watch to be Open, and answer
// the stream, or a refusal's error with the handle closed — what a caller that asked for a
// channel expects, as the store answered before it said phases. Open rather than merely past
// the line, because what a refusal of the subscriber's own turns on (WaitIfAbsent) is in the
// picture. A refusal of a watch that has stood (WatchState.Opened) is the store asking again
// for it, behind the handle, and is waited through: a joiner that arrives between two asks is
// not told no for it.
func await(w *Watch, err error) (<-chan Commit, func(), error) {
	if err != nil {
		return nil, nil, err
	}
	for {
		st, changed := w.State()
		switch {
		case st.Phase == WatchOpen:
			return w.Commits, w.Close, nil
		case st.Phase == WatchFailed, st.Phase == WatchRefused && !st.Opened:
			w.Close()
			return nil, nil, st.Err
		default:
			<-changed
		}
	}
}

// uncurable is a refusal that asking again will not change: a path the substrate cannot
// address. Such a watch Fails. A path that disagrees with the shape of what stands there
// (path_conflict) is not one: a write can change the shape, so it is Refused and asked again.
func uncurable(err error) bool {
	var e *logdapi.Error
	return errors.As(err, &e) && e.Code == logdapi.ErrCodeInvalidPath
}

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
// OFF is a line with no slots (LogdConfig.WatchSlots below zero; viffd reads it from
// VIFF_WATCH_SLOTS): take and give do nothing, and watches open as they are asked for.
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
