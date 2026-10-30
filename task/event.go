package task

// Event is one event on `GET /tasks/{id}/events`: one structured entry on a task's read
// plane, streamed over SSE. The loop emits loop-level events; a driver's own narration is a
// separate concern (wired at the edge, not here).
//
//tony:schemagen=verse-task-event,notag
type Event struct {
	Kind   string `tony:"field=kind"`
	Task   string `tony:"field=task"`
	Cursor int64  `tony:"field=cursor,omitzero"` // per-task monotonic; resume with ?since=<cursor>

	ID   string `tony:"field=id,omitzero"` // decision id, for resolve-by-id
	Seq  int    `tony:"field=seq,omitzero"`
	Tool string `tony:"field=tool,omitzero"`
	// Action is the action in one line (ToolUse.Describe), on a pending or a decision event:
	// what the gate is about, for whoever reads it on the docket or at a terminal.
	Action string `tony:"field=action,omitzero"`
	Gate   string `tony:"field=gate,omitzero"`
	// Step and Fields are the system call a pending, a decision or an exec event is about
	// (task.Step), when it is one: `<system>.<verb>` and the call's params, what an autonomy row
	// matches. A decision names the row that decided it as Gate; the exec that follows it names
	// the same call, with its outcome.
	Step    string            `tony:"field=step,omitzero"`
	Fields  map[string]string `tony:"field=fields,omitzero"`
	By      string            `tony:"field=by,omitzero"`
	Proceed bool              `tony:"field=proceed,omitzero"`
	// Choice is the option a decision settled on; Proceed is the same fact as yes or no, kept
	// for every reader of a two-option question. A pending event offers Options and a
	// Deadline the way a posed question does, so a worker that raises a gate with outcomes
	// of its own reports the same record a charter's gate would.
	Choice   string    `tony:"field=choice,omitzero"`
	Options  []Option  `tony:"field=options,omitzero"`
	Deadline *Deadline `tony:"field=deadline,omitzero"`
	// Answerers is who a worker raising a gate wants it held out to — a worker asking somebody.
	// The launch's own gate of the same name, when it has answerers, stands
	// over this: that gate is the charter's autonomy.
	Answerers []string `tony:"field=answerers,omitzero"`
	Text      string   `tony:"field=text,omitzero"` // reason / detail / result
	Err       string   `tony:"field=err,omitzero"`
	Dropped   int      `tony:"field=dropped,omitzero"` // gap marker: events a slow subscriber missed
}

// The kinds of Event, its `kind`.
const (
	EventBoundary = "boundary" // cursor reached a new boundary
	EventDecision = "decision" // a gate was evaluated + resolved
	EventExec     = "exec"     // an action executed (result summary)
	EventChild    = "child"    // a sub-agent started or ended
	EventStatus   = "status"   // the task's status changed: its Text is a Status (running, paused, done, …)
	EventPending  = "pending"  // a gate is blocked awaiting resolution (carries the decision id)
	// EventWithdrawn is a question the task posed withdrawn before it was answered, when its last
	// waiter stopped waiting (a say, a pause, a cancel, a deadline): ID is the question's, Text why.
	EventWithdrawn = "withdrawn"
	// EventSay is an utterance in the task's conversation. By empty = the task's own
	// narration (the agent's words); By set = a say-to folded in from an operator or a
	// resolver (what was "steer"). One kind, speaker in By — "you say to it, it says back".
	EventSay = "say"
	EventGap = "gap" // a slow subscriber missed N events
	// EventAutonomy is the task's autonomy replaced while it runs: By is who replaced it, and Text
	// the autonomy in one line (Autonomy.Summary).
	EventAutonomy = "autonomy"
)

type subscriber struct {
	ch      chan Event
	dropped int
}

// RingCap is the retained per-task event window served for ?since replay. A consumer
// reconnecting within this many events resumes losslessly; older than it, resync from the
// snapshot. Modest on purpose: the window is paid for in memory per live task.
const RingCap = 256

// Subscribe returns a channel of events (live from now) and an unsubscribe func.
// Subscribers that can't keep up drop events (never blocking the loop); the next
// delivered event is preceded by an EventGap marker reporting how many were missed.
//
// On a task that has already ENDED, "live from now" is its terminal frame and then the
// end of the stream. It used to be nothing, forever — which strands a mirror on a task
// that finished, the same way FailedHandle exists to avoid.
func (p *Task) Subscribe() (<-chan Event, func()) {
	return p.subscribe(func(s *subscriber) {
		if p.ended && len(p.ring) > 0 {
			trySend(s, p.ring[len(p.ring)-1])
		}
	})
}

// SubscribeSince returns a channel that first replays retained events with cursor >
// since (in order) then streams live. If since is below the retained window's
// low-watermark, the first delivered event is an EventGap resync marker carrying the
// current cursor — the consumer should re-read the snapshot and resume from there. This
// is the resumable read plane: a reconnecting consumer passes its last cursor.
func (p *Task) SubscribeSince(since int64) (<-chan Event, func()) {
	return p.subscribe(func(s *subscriber) {
		if len(p.ring) > 0 && since+1 < p.ring[0].Cursor { // gap below the window
			trySend(s, Event{Kind: EventGap, Task: p.ID, Cursor: p.cursor})
			// A reader that fell below the window is told it missed some. Telling it WHAT
			// is the part that matters: the live path replays open gates and this one did
			// not, on the reasoning that the ring carries them — which stops being true at
			// exactly the moment the gap fires. A resolver is a client of this call and
			// nothing else, so it reconnected, learned it was behind, and could not see the
			// question it exists to answer.
			//
			// Only in the gap case. Without one the ring covers everything since the
			// cursor, so a gate raised after it is already in the replay and a gate raised
			// before it is one the reader has already seen.
			for _, g := range p.open {
				if g.Cursor < p.ring[0].Cursor { // below the window, so the replay misses it
					trySend(s, g)
				}
			}
		}
		for _, e := range p.ring {
			if e.Cursor > since {
				trySend(s, e)
			}
		}
	})
}

// subscribe registers a subscriber, optionally priming its channel (replay) under the
// bus lock so no live event can interleave ahead of the replay.
func (p *Task) subscribe(prime func(*subscriber)) (<-chan Event, func()) {
	p.busMu.Lock()
	defer p.busMu.Unlock()
	if p.subs == nil {
		p.subs = map[int]*subscriber{}
	}
	id := p.subID
	p.subID++
	s := &subscriber{ch: make(chan Event, 256)}
	if prime != nil {
		prime(s)
	}
	// An ENDED task is a finite record. Registering a subscriber on one would leave it
	// listening to something that has stopped speaking, which is not waiting — it is a leak
	// with the shape of patience. Close it: a closed buffered channel still yields what was
	// primed into it, so the reader gets the record and then the end of it.
	if p.ended {
		close(s.ch)
		return s.ch, func() {}
	}
	p.subs[id] = s
	return s.ch, func() {
		p.busMu.Lock()
		defer p.busMu.Unlock()
		if _, ok := p.subs[id]; ok {
			delete(p.subs, id)
			close(s.ch)
		}
	}
}

// terminalFrame reports whether e is the one frame whose loss cannot be recovered from.
// Every other event is replaceable: the reader is told it missed some and resyncs from the
// snapshot. Nothing follows this one, so nothing can carry that news.
func terminalFrame(e Event) bool {
	return e.Kind == EventStatus && Status(e.Text).Terminal()
}

// trySend delivers to a subscriber non-blocking, accruing a drop on a full buffer — except
// that the last slot is RESERVED for the terminal.
//
// The reservation is what makes delivery of the terminal certain rather than likely: an
// ordinary frame is only sent when the buffer is below cap-1, so occupancy never reaches
// cap, so a slot is always free when the terminal arrives. One slot is enough because a
// task ends once.
//
// It costs one slot of buffer and no blocking, which is the point: the loop must not stall
// because an observer is slow (see emit), and the terminal is retained in the ring either
// way — what was lost was never the fact, only one subscriber's delivery of it.
func trySend(s *subscriber, e Event) {
	if !terminalFrame(e) && len(s.ch) >= cap(s.ch)-1 {
		s.dropped++
		return
	}
	select {
	case s.ch <- e:
	default:
		s.dropped++
	}
}

// Cursor is the current per-task high-water event cursor (0 before any event).
func (p *Task) Cursor() int64 {
	p.busMu.Lock()
	defer p.busMu.Unlock()
	return p.cursor
}

// emit broadcasts an event to all subscribers, non-blocking. A subscriber whose
// buffer is full drops the event (favor liveness); its next delivery is prefixed
// with a gap marker. Each event is stamped with the next per-task cursor and
// retained in the bounded ring for ?since replay.
func (p *Task) emit(e Event) {
	p.busMu.Lock()
	defer p.busMu.Unlock()
	p.emitLocked(e)
}

// emitLive emits e unless the task has ended, and reports whether it did. The check and the emit
// are one step under the bus lock, so nothing it emits can follow the terminal frame.
func (p *Task) emitLive(e Event) bool {
	p.busMu.Lock()
	defer p.busMu.Unlock()
	if p.ended {
		return false
	}
	p.emitLocked(e)
	return true
}

// emitLocked is emit, with the bus lock held.
func (p *Task) emitLocked(e Event) {
	e.Task = p.ID
	p.cursor++
	e.Cursor = p.cursor
	p.ring = append(p.ring, e)
	if len(p.ring) > RingCap {
		p.ring = p.ring[len(p.ring)-RingCap:]
	}
	switch e.Kind { // what is still being asked, for a reader that resumes past the window
	case EventPending:
		if p.open == nil {
			p.open = map[string]Event{}
		}
		p.open[e.ID] = e
	case EventDecision:
		delete(p.open, e.ID)
	}
	terminal := terminalFrame(e)
	for _, s := range p.subs {
		// The terminal is never withheld behind a gap marker that would not fit. For every
		// other frame that withholding is right — the next one carries the marker — and for
		// this one it is fatal, because a subscriber that is behind is exactly the one in
		// the dropped state, and nothing follows to try again.
		if terminal {
			trySend(s, e)
			continue
		}
		if s.dropped > 0 { // try to flush the gap marker before the real event
			was := s.dropped
			s.dropped = 0
			trySend(s, Event{Kind: EventGap, Task: p.ID, Dropped: was})
			if s.dropped > 0 { // the marker itself did not fit; keep the tally
				s.dropped += was
				continue
			}
		}
		trySend(s, e)
	}
	if terminal {
		// Nothing will follow. Ending the stream says so, and says it in the one way that
		// cannot be dropped for want of buffer.
		p.ended = true
		for id, s := range p.subs {
			delete(p.subs, id)
			close(s.ch)
		}
	}
}

// Emit publishes an external event onto this task's read plane — the seam a
// daemon uses to surface pending decisions (from a Registry OnPost hook) on the
// same stream as the loop's own events.
func (p *Task) Emit(e Event) { p.emit(e) }

// emitTerminal says how the task ended, unless it has already said. It is the panic path's
// entry to the read plane: property 1 is that a terminal is reached at most once and nothing
// follows it, and a panic can arrive after teardown has already emitted one.
func (p *Task) emitTerminal(why Status) {
	p.busMu.Lock()
	already := p.ended
	p.busMu.Unlock()
	if !already {
		p.emit(Event{Kind: EventStatus, Text: string(why), Err: p.Err()})
	}
}

func evTrunc(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "…"
}
