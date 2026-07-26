package process

// Frame is one structured entry on a Process's read plane. It is a plain document
// so a daemon can stream it over SSE. The loop emits loop-level events; a driver's
// own narration is a separate concern (wired at the edge, not here).
//
//tony:schemagen=verse-frame,notag
type Frame struct {
	Kind    string `json:"kind" tony:"field=kind"`
	Process string `json:"process" tony:"field=process"`
	Cursor  int64  `json:"cursor,omitempty" tony:"field=cursor,omitzero"` // per-process monotonic; resume with ?since=<cursor>

	ID      string `json:"id,omitempty" tony:"field=id,omitzero"` // decision id, for resolve-by-id
	Seq     int    `json:"seq,omitempty" tony:"field=seq,omitzero"`
	Tool    string `json:"tool,omitempty" tony:"field=tool,omitzero"`
	Gate    string `json:"gate,omitempty" tony:"field=gate,omitzero"`
	Mode    string `json:"mode,omitempty" tony:"field=mode,omitzero"`
	By      string `json:"by,omitempty" tony:"field=by,omitzero"`
	Proceed bool   `json:"proceed,omitempty" tony:"field=proceed,omitzero"`
	Text    string `json:"text,omitempty" tony:"field=text,omitzero"` // reason / detail / result
	Err     string `json:"err,omitempty" tony:"field=err,omitzero"`
	Dropped int    `json:"dropped,omitempty" tony:"field=dropped,omitzero"` // gap marker: events a slow subscriber missed
}

// Event kinds.
const (
	EvBoundary = "boundary" // cursor reached a new boundary
	EvDecision = "decision" // a gate was evaluated + resolved
	EvExec     = "exec"     // an action executed (result summary)
	EvChild    = "child"    // a sub-process was spawned or completed
	EvStatus   = "status"   // run lifecycle (completed | cancelled | paused | resumed | …)
	EvPending  = "pending"  // a gate is blocked awaiting resolution (carries the decision id)
	// EvSay is an utterance in the process's conversation. By empty = the process's own
	// narration (the agent's words); By set = a say-to folded in from an operator or a
	// resolver (what was "steer"). One kind, speaker in By — "you say to it, it says back".
	EvSay = "say"
	// EvVector records an autonomy slide (SetVector): the new default mode rides Mode and
	// the operator rides By. Observable so `entity get`/attach show the dial change mid-run,
	// the same way a say-to is observed — an operator-authored control effect on the plane.
	EvVector = "vector"
	EvGap    = "gap" // a slow subscriber missed N events
)

type subscriber struct {
	ch      chan Frame
	dropped int
}

// ringCap bounds the retained per-process event window served for ?since replay. A
// consumer reconnecting within this many events resumes losslessly; older than it,
// resync from the snapshot.
const ringCap = 256

// Subscribe returns a channel of events (live from now) and an unsubscribe func.
// Subscribers that can't keep up drop events (never blocking the loop); the next
// delivered event is preceded by an EvGap marker reporting how many were missed.
func (p *Process) Subscribe() (<-chan Frame, func()) {
	return p.subscribe(nil)
}

// SubscribeSince returns a channel that first replays retained events with cursor >
// since (in order) then streams live. If since is below the retained window's
// low-watermark, the first delivered event is an EvGap resync marker carrying the
// current cursor — the consumer should re-read the snapshot and resume from there. This
// is the resumable read plane: a reconnecting consumer passes its last cursor.
func (p *Process) SubscribeSince(since int64) (<-chan Frame, func()) {
	return p.subscribe(func(s *subscriber) {
		if len(p.ring) > 0 && since+1 < p.ring[0].Cursor { // gap below the window
			trySend(s, Frame{Kind: EvGap, Process: p.ID, Cursor: p.cursor})
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
func (p *Process) subscribe(prime func(*subscriber)) (<-chan Frame, func()) {
	p.busMu.Lock()
	defer p.busMu.Unlock()
	if p.subs == nil {
		p.subs = map[int]*subscriber{}
	}
	id := p.subID
	p.subID++
	s := &subscriber{ch: make(chan Frame, 256)}
	if prime != nil {
		prime(s)
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

// trySend delivers to a subscriber non-blocking, accruing a drop on a full buffer.
func trySend(s *subscriber, e Frame) {
	select {
	case s.ch <- e:
	default:
		s.dropped++
	}
}

// Cursor is the current per-process high-water event cursor (0 before any event).
func (p *Process) Cursor() int64 {
	p.busMu.Lock()
	defer p.busMu.Unlock()
	return p.cursor
}

// emit broadcasts an event to all subscribers, non-blocking. A subscriber whose
// buffer is full drops the event (favor liveness); its next delivery is prefixed
// with a gap marker. Each event is stamped with the next per-process cursor and
// retained in the bounded ring for ?since replay.
func (p *Process) emit(e Frame) {
	e.Process = p.ID
	p.busMu.Lock()
	defer p.busMu.Unlock()
	p.cursor++
	e.Cursor = p.cursor
	p.ring = append(p.ring, e)
	if len(p.ring) > ringCap {
		p.ring = p.ring[len(p.ring)-ringCap:]
	}
	for _, s := range p.subs {
		if s.dropped > 0 { // try to flush the gap marker before the real event
			select {
			case s.ch <- Frame{Kind: EvGap, Process: p.ID, Dropped: s.dropped}:
				s.dropped = 0
			default:
				s.dropped++
				continue
			}
		}
		select {
		case s.ch <- e:
		default:
			s.dropped++
		}
	}
}

// Emit publishes an external event onto this process's read plane — the seam a
// daemon uses to surface pending decisions (from a Registry OnPost hook) on the
// same stream as the loop's own events.
func (p *Process) Emit(e Frame) { p.emit(e) }

func evTrunc(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "…"
}
