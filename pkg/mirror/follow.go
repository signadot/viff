package mirror

import (
	"sync"
	"time"

	"github.com/signadot/verse/pkg/task"
)

// FOLLOWING A TASK is keeping a subscription to its stream until its terminal frame, on three
// clocks worked out from the task's lease (runner.DefaultLease):
//
//	resubscribe  lease / 4   the stream is closed and asked for again from its cursor
//	lease        as launched the runner's: it ends a task no subscribe has renewed for that long
//	wait         lease + 1m  the mirror stops waiting on a runner that has accepted none for that long
//
// All three count subscribes the runner accepted, and none counts an open connection, so a
// stream that is open and dead renews nothing: it is replaced within a quarter of the lease by a
// subscribe the runner must answer. The runner's count starts no later than the mirror's, since
// the runner accepts a subscribe before the mirror hears that it did, so the lease runs out
// before the wait does: by the time the mirror ends a task for a runner it cannot reach, the
// runner has ended it too.
//
// The clocks are a runner's. A handle that does not say when its subscribes are accepted
// (task.Accepting) is of a task with no runner between it and the mirror: its stream is kept as
// long as it stays open, and one that closes is asked for again, each frame heard on it being
// what says it is working.

// waitMargin is how much longer than a task's lease the mirror waits on its runner.
var waitMargin = time.Minute

// What follow tells record beside the task's frames.
const (
	markFrame   = iota // a frame of the task's
	markCurrent        // the stream is working: a subscribe was accepted
	markLost           // the stream ended, and not by this mirror's clock
	markGaveUp         // the wait ran out: the mirror has stopped waiting on the runner
)

// item is one thing follow hands record: a frame, or a mark with none.
type item struct {
	ev   task.Event
	mark int
}

// frames is what follow has read and record has not yet written, in order. It has no bound: a
// store that is slow to take a write holds record up and never follow, whose next subscribe is
// owed by a clock.
type frames struct {
	mu    sync.Mutex
	items []item
	done  bool
	more  chan struct{}
}

func newFrames() *frames { return &frames{more: make(chan struct{}, 1)} }

func (q *frames) put(it item) {
	q.mu.Lock()
	q.items = append(q.items, it)
	q.mu.Unlock()
	q.wake()
}

// close says nothing more is coming.
func (q *frames) close() {
	q.mu.Lock()
	q.done = true
	q.mu.Unlock()
	q.wake()
}

func (q *frames) wake() {
	select {
	case q.more <- struct{}{}:
	default:
	}
}

// take is the next item, waiting for one; false once there is none and none is coming.
func (q *frames) take() (item, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			it := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return it, true
		}
		done := q.done
		q.mu.Unlock()
		if done {
			return item{}, false
		}
		<-q.more
	}
}

// subscribe asks for the task's stream, saying when the runner accepted where the handle can:
// the first subscribe of a task, or one after it, from the cursor the last one reached.
func subscribe(h task.Handle, first bool, since int64) (<-chan task.Event, func()) {
	if acc, ok := h.(task.Accepting); ok {
		return acc.SubscribeAccepted(since)
	}
	if first {
		return h.Subscribe()
	}
	return h.SubscribeSince(since)
}

// follow hands q the frames of a task, from the subscription given (or one it asks for, where
// none is) and each one after it, and returns at the task's terminal frame, or when the wait
// has run out (markGaveUp).
func follow(h task.Handle, q *frames, ch <-chan task.Event, unsub func(), lease time.Duration) {
	defer q.close()
	_, leased := h.(task.Accepting)
	wait := lease + waitMargin
	// The launch, or the attach, stands for the first accepted subscribe: the runner's own
	// count started no later.
	accepted := time.Now()
	var cursor int64
	if ch == nil {
		ch, unsub = subscribe(h, true, 0)
	}
	for attempt := 0; ; {
		var again <-chan time.Time
		var clock *time.Timer
		if leased {
			clock = time.NewTimer(lease / 4)
			again = clock.C
		}
		heard := false
		// take hands one frame on, and says whether it was the task's last.
		take := func(ev task.Event) (ended bool) {
			switch {
			case ev.Kind == task.EventAccepted:
				accepted = time.Now()
				q.put(item{mark: markCurrent})
				return false
			case !leased:
				// No runner says a subscribe was accepted: a frame is what says the stream works.
				accepted = time.Now()
				if !heard {
					q.put(item{mark: markCurrent})
				}
			}
			// A frame of the runner's, and so a stream that works: one that is accepted and
			// closes with nothing on it is asked for again no sooner each time.
			heard, attempt = true, 0
			if ev.Cursor > cursor {
				cursor = ev.Cursor
			}
			q.put(item{ev: ev})
			return ev.Kind == task.EventStatus && task.Status(ev.Text).Terminal()
		}
		byClock, ended := false, false
	stream:
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					break stream
				}
				if ended = take(ev); ended {
					break stream
				}
			case <-again:
				byClock = true
				// What the stream has already delivered is taken before it is closed: the
				// task's last frame may be there, and a runner asked again may no longer have
				// the task to say it.
				for n := len(ch); n > 0 && !ended; n-- {
					ev, ok := <-ch
					if !ok {
						break
					}
					ended = take(ev)
				}
				break stream
			}
		}
		unsub()
		if clock != nil {
			clock.Stop()
		}
		if ended {
			return
		}
		if !byClock {
			left := wait - time.Since(accepted)
			if left <= 0 {
				q.put(item{mark: markGaveUp})
				return
			}
			q.put(item{mark: markLost})
			time.Sleep(min(backoff(attempt), left))
			attempt++
		}
		ch, unsub = subscribe(h, false, cursor)
	}
}
