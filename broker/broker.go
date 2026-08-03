// Package broker is a runner whose runtime is an external DIAL-IN worker — a
// laptop agent, a pod in a private cluster, or a human. verse drives it through the
// process.Runner seam exactly as it drives a local or remote-processd runner; a
// worker connects OUT to the broker's worker-facing HTTP (claim / inbox / report) and
// the broker glues the two legs: a worker's reports become the events verse observes, and
// verse's control becomes items the worker pulls. Both parties are clients; only the
// broker is reachable — so a firewalled worker or a human works without verse inverting.
// See docs/process-control.md "The worker-facing dial-in protocol".
package broker

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/process"
)

// Broker hosts many processes, each awaiting a dial-in worker.
type Broker struct {
	mu    sync.Mutex
	base  string // worker-facing dial base (e.g. http://host:port/broker/<nonce>), if hosted
	procs map[string]*procState
}

func New() *Broker { return &Broker{procs: map[string]*procState{}} }

// SetBase records the externally-reachable worker-facing base this broker is hosted under
// (verse mints it as <broker-face>/broker/<nonce> at HostBroker time). It is published on
// each process's entity as `dialURL`, so a worker can be told where to dial in rather than
// being pre-configured with the URL. Empty until hosted.
func (b *Broker) SetBase(url string) {
	b.mu.Lock()
	b.base = url
	b.mu.Unlock()
}

func (b *Broker) dialURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.base
}

// Face is the worker-facing base this broker is hosted under, for discovery on /status
// (parallel to how docd addresses are published) — where a worker dials in.
func (b *Broker) Face() string { return b.dialURL() }

// Ref identifies this runner on the process:instance entity for resume-or-orphan. An
// in-process broker's state is in memory, so like the local supervisor it is ephemeral
// across a verse restart — the label, not a control URL (the worker-facing dial base is
// published separately as `dialURL`; see SetBase).
func (b *Broker) Ref() string { return "broker" }

func (b *Broker) get(id string) *procState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.procs[id]
}

// Spawn registers a process for a worker to claim and returns the verse-side handle.
//
// The frame, tool set, vector and grant are deliberately not recorded here. A brokered
// process is performed by a dial-in worker that builds its own agent — or is a human — so
// the broker has no construction to shape and no boundary to enforce at: it observes
// reports and relays control. Storing an envelope it cannot check would be a claim verse
// is unable to keep, which is the exact failure this change exists to remove. Bounding a
// remote worker is a real question, and it belongs to the claim protocol rather than to
// this registration. See docs/external-processes.md.
func (b *Broker) Spawn(l process.Launch, _ process.ContentReader) process.Handle {
	b.mu.Lock()
	b.procs[l.ID] = &procState{
		id: l.ID, task: l.Task, driver: l.Driver, retain: l.Retain,
		subs: map[int]chan process.Frame{}, pending: map[string]process.Frame{},
	}
	b.mu.Unlock()
	return &handle{b: b, id: l.ID}
}

// Attach re-attaches to an existing process (the resume path).
func (b *Broker) Attach(id string) process.Handle {
	if b.get(id) == nil {
		return nil
	}
	return &handle{b: b, id: id}
}

// Revoke releases id's claim and fences the current holder (bumps the epoch), so a
// different identity may then claim it (Fork-2(a): a takeover to a new worker is
// operator-authorized, never race-driven). This is verse-side authority — the eventual
// health-check calls it when a worker is judged gone, typically alongside a Cancel.
func (b *Broker) Revoke(id string) {
	ps := b.get(id)
	if ps == nil {
		return
	}
	ps.mu.Lock()
	ps.claimBy = ""
	ps.epoch++
	ps.mu.Unlock()
}

// procState is one process's two-legged state: the merged event log verse reads, the
// control inbox the worker pulls, and the verse-side subscribers.
type procState struct {
	id, task, driver string

	mu      sync.Mutex
	claimBy string
	epoch   int64 // claim generation; the fencing token (0 = unclaimed)
	// renewed is when this claim was last heard from. A worker renews by working: every
	// inbox poll and every report is a renewal, so the lease needs no heartbeat of its own
	// and a worker that already talks to the broker is already keeping it.
	renewed time.Time
	log     []process.Frame            // merged event log (worker reports + control-effects)
	inbox   []api.InboxItem            // control directed at the worker
	subs    map[int]chan process.Frame // verse-side Handle subscribers
	nextSub int
	logCur  int64
	// retain is this process's log window in events (0 = the package default). Set from the
	// Launch, because how much of the past is worth keeping is a property of the work.
	retain   int
	inboxCur int64
	// ackedCursor is how far the worker has read its inbox. A poll for `since=N` IS the
	// acknowledgement of everything at or below N — the worker asked for what came after,
	// which it could only do having taken what came before.
	ackedCursor int64
	// pending holds the open gate decisions raised by the worker, keyed by decision id.
	// It keeps the whole raised frame rather than just the gate name: `do gate --reason`
	// rides in on Text, and a resolver reading `verse gates` needs it — that reason is the
	// entire point of an executor escalating instead of deciding. Narrowing this to
	// id -> gate silently blanked it on every brokered gate.
	pending map[string]process.Frame
	// answered remembers the gates that HAVE been decided, so a second answer can be told
	// apart from an answer to nothing. Without it both are "not open", and the two mean
	// opposite things to whoever asked.
	answered map[string]bool
	// dropped counts what each subscriber missed, so the next frame it does receive can say
	// so. The local bus has always done this; the broker discarded silently, and a reader
	// had no way to know its view was partial.
	dropped map[int]int
	// applied remembers recent report keys and what each was answered with, so a retry of a
	// report whose response the worker never saw is answered rather than re-applied. Bounded:
	// a retry follows its original closely, and remembering forever would be a leak with the
	// shape of a guarantee.
	applied   map[string]reportAck
	appliedIn []string // insertion order, for eviction
	// undelivered holds answers queued for the worker but not yet acknowledged, keyed by the
	// inbox cursor that carries them. An answer is a decision only once the worker has taken
	// it; until then the question is answered-and-undelivered, which is a state a docket can
	// show and a person can act on.
	undelivered map[int64]api.InboxItem
	// abandoned records that the terminal found gates still open, so a late answer can be
	// told why it missed rather than being sent to look for a typo.
	abandoned bool
	result    string
	err       string         // terminal failure reason (Failed only); see latchTerminal
	status    process.Status // latched terminal status
	terminal  bool
}

// emit appends an event to the merged log (stamping the next cursor) and broadcasts it to
// verse-side subscribers — used both by worker reports and by operator-authored control
// effects (a say-to). The terminal latch is separate (latchTerminal); emit only records.
func (ps *procState) emit(e process.Frame) {
	ps.mu.Lock()
	ps.logCur++
	e.Cursor, e.Process = ps.logCur, ps.id
	ps.log = append(ps.log, e)
	// Bounded, so the log does not grow for the life of the run — and so that the
	// below-watermark resync frame subscribeFrom already writes can ever fire. It could not
	// before: an unbounded log has no watermark to be below.
	if cap := ps.retainCap(); len(ps.log) > cap {
		ps.log = ps.log[len(ps.log)-cap:]
	}
	switch e.Kind { // track open gate decisions so `process get`/resolve see them
	case process.EvPending:
		ps.pending[e.ID] = e
	case process.EvDecision:
		delete(ps.pending, e.ID)
		if ps.answered == nil {
			ps.answered = map[string]bool{}
		}
		ps.answered[e.ID] = true
	}
	// Broadcast under the lock, as the local bus does. The sends are non-blocking, so it
	// costs nothing to hold — and it is what makes closing below safe: a copy of the channel
	// list dropped the lock between collecting and sending, so an unsubscribe landing in
	// that window closed a channel this loop was about to send on.
	terminal := e.Kind == process.EvStatus && process.TerminalStatus(e.Text)
	for id, c := range ps.subs {
		// A slow verse-side subscriber drops and resyncs from the snapshot/log — except for
		// the terminal, which nothing follows and no resync is prompted by. The last slot is
		// kept free for it, so it is always deliverable.
		if !terminal && len(c) >= cap(c)-1 {
			ps.drop(id)
			continue
		}
		// Anything missed is announced before the next frame that fits, the way the local
		// bus does. Discarding in silence let a reader believe it had the whole story.
		if n := ps.dropped[id]; n > 0 {
			select {
			case c <- process.Frame{Kind: process.EvGap, Process: ps.id, Dropped: n, Cursor: ps.logCur}:
				delete(ps.dropped, id)
			default:
				ps.drop(id)
				continue
			}
		}
		select {
		case c <- e:
		default:
			ps.drop(id)
		}
		if terminal {
			// Nothing will follow: end the stream rather than leave a reader listening to a
			// process that has stopped speaking.
			delete(ps.subs, id)
			close(c)
		}
	}
	ps.mu.Unlock()
}

// enqueue appends one control item and returns the cursor it was stamped with, so a caller
// that must know where its item landed does not have to re-read a counter another enqueue may
// already have moved.
func (ps *procState) enqueue(it api.InboxItem) int64 {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.inboxCur++
	it.Cursor = ps.inboxCur
	ps.inbox = append(ps.inbox, it)
	return it.Cursor
}

// LogRetain is the DEFAULT number of events a brokered process's merged log keeps. A charter
// sets its own per spawn (launch.Spec.Retain); this is what a spawn that says nothing gets.
//
// It matches the local bus's window on purpose: the same `?since=` contract is served from
// both, and a reader should not have to know which kind of runner it is talking to in order
// to know how far back it may resume.
const LogRetain = 256

// retainCap is the window in force, called under ps.mu.
func (ps *procState) retainCap() int {
	if ps.retain > 0 {
		return ps.retain
	}
	return LogRetain
}

// drop records that a subscriber missed one, called under ps.mu.
func (ps *procState) drop(id int) {
	if ps.dropped == nil {
		ps.dropped = map[int]int{}
	}
	ps.dropped[id]++
}

// reportAck is what a report was answered with, kept so an identical retry gets an identical
// answer instead of a second application.
type reportAck struct {
	rCursor int64
	applied int
}

// appliedKeep bounds how many report keys are remembered. A retry chases its original within
// a request timeout, so a short memory covers the case this exists for; a long one would grow
// with the run.
const appliedKeep = 64

// rememberReport records a key's answer, evicting the oldest when full.
func (ps *procState) rememberReport(key string, ack reportAck) {
	if key == "" {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.applied == nil {
		ps.applied = map[string]reportAck{}
	}
	if _, seen := ps.applied[key]; !seen {
		ps.appliedIn = append(ps.appliedIn, key)
		for len(ps.appliedIn) > appliedKeep {
			delete(ps.applied, ps.appliedIn[0])
			ps.appliedIn = ps.appliedIn[1:]
		}
	}
	ps.applied[key] = ack
}

// reportSeen reports what a key was answered with, if it has been.
func (ps *procState) reportSeen(key string) (reportAck, bool) {
	if key == "" {
		return reportAck{}, false
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ack, ok := ps.applied[key]
	return ack, ok
}

// latchTerminal records the terminal status once — first writer wins (terminal-once). It
// returns true iff this call performed the latch; the caller emits the status event only on
// a true return, so a losing writer (e.g. a late worker `completed` after a verse `cancel`)
// leaves no second terminal on the log.
//
// The terminal text is filed by STATUS, not by which field it arrived in. `do complete <x>`
// and `do fail <why>` both ride the report's `result`, so filing both as a result made a
// failed brokered process report `result: "the migration is not reversible"` while an
// identical local one reported `err: "exit status 3"` — the same question answered from a
// different field depending on where the work ran. Worse downstream: the mirror runs
// ParseResult over `result` looking for structured data, so a failure reason was being mined
// for a payload it was never going to have.
func (ps *procState) latchTerminal(status process.Status, text string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.terminal {
		return false
	}
	ps.terminal = true
	ps.status = status
	// The gates it never answered go with it. A terminal does not decide them — nobody did —
	// but it does settle the question of whether they can still be acted on, and leaving
	// them open let a dead process's gates be answered, with the decision written onto the
	// log of a run that had already ended.
	ps.abandoned = len(ps.pending) > 0
	clear(ps.pending)
	if text != "" {
		// A running result already reported stays a result — only the terminal text is
		// filed by status, and a failure does not retroactively turn it into an error.
		if status == process.Failed {
			ps.err = text
		} else {
			ps.result = text
		}
	}
	return true
}

// terminalState reports the frozen terminal status/result/err, if any. All three ride the
// idempotent re-ack to a worker retrying a terminal report, so a lost ack does not cost the
// worker the reason it just gave.
func (ps *procState) terminalState() (status process.Status, result, errText string, ok bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.status, ps.result, ps.err, ps.terminal
}

// fenced reports whether epoch is NOT the current claim generation. A stale generation (a
// displaced or never-claimed worker) is fenced: its writes are rejected so it stops.
func (ps *procState) fenced(epoch int64) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return epoch == 0 || epoch != ps.epoch
}

// AgeClaimForTest backdates a claim's last renewal, so a test can reach the far side of the
// lease without waiting out a real one. Exported for tests in the broker_test package; the
// lease is a duration and there is nothing to observe about it except what happens across it.
func (b *Broker) AgeClaimForTest(id string, by time.Duration) {
	ps := b.get(id)
	if ps == nil {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if !ps.renewed.IsZero() {
		ps.renewed = ps.renewed.Add(-by)
	}
}

// ClaimLease is how long a claim survives without the worker being heard from. A worker that
// vanished — closed a laptop, lost a network, died — used to hold its claim, its gate and its
// process forever, and the only release was an operator calling Revoke, which nothing calls.
//
// Generous, because it is a liveness bound and not a latency budget: expiring it takes the
// work away from somebody who may simply be on a slow link, and the cost of waiting is a
// process that stays claimed a little longer than it had to.
//
// This is the FIRST thing in these seams to depend on a clock for correctness. One clock —
// the broker's — comparing two readings of its own, never a timestamp written elsewhere. That
// is a deliberate line: comparing clocks across machines is a different problem and this does
// not need it.
const ClaimLease = 2 * time.Minute

// renew records that the claim's holder is alive, which every inbox poll and report says.
func (ps *procState) renew(epoch int64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if epoch != 0 && epoch == ps.epoch {
		ps.renewed = time.Now()
	}
}

// expireClaim releases a claim nobody has renewed, so another worker can take the work over.
// It reports whether it released one.
//
// The process is NOT made terminal: the work may well still be running somewhere, and a
// broker that cannot hear a worker knows nothing about whether it is working. What expires is
// the exclusive right to report on it, which is the thing that was blocking a takeover.
func (ps *procState) expireClaim(now time.Time) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.claimBy == "" || ps.terminal || ps.renewed.IsZero() || now.Sub(ps.renewed) < ClaimLease {
		return false
	}
	ps.claimBy = ""
	ps.epoch++ // fence the silent holder: if it comes back, it comes back as a stranger
	return true
}

func (ps *procState) cursorNow() int64 {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.logCur
}

// snapshot renders this process in the shape the process API returns (id/task/status/
// result/cursor/pending), so a brokered process appears in the unified `process list`
// alongside local ones. driver is included for context; status is unclaimed → running → terminal.
func (ps *procState) snapshot() process.Instance {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	status := process.Unclaimed // spawned, hosted here, no executor yet
	if ps.claimBy != "" {
		status = process.Running // an executor claimed it and is working
	}
	if ps.terminal {
		status = ps.status
	}
	// Mode and Origin stay empty on purpose: a worker raises a gate, it does not decide
	// who answers it or under which dial — that is versed's to say, upstream of here.
	pending := make([]process.PendingGate, 0, len(ps.pending))
	for id, ev := range ps.pending {
		pending = append(pending, process.PendingGate{
			ID: id, Gate: ev.Gate, Tool: ev.Tool, Reason: ev.Text,
		})
	}
	return process.Instance{
		ID: ps.id, Task: ps.task, Driver: ps.driver, Status: status,
		Result: ps.result, Err: ps.err, Cursor: ps.logCur, Pending: pending,
	}
}

// ListProcesses / ProcessSnapshot enumerate the broker's processes for versed's unified
// process list. Each brokered process carries its dialURL so its worker face is discoverable
// from `process list`/`get` (the same base for all of a broker's processes).
// The error is always nil: this registry is in memory, so there is no unreachable case to
// report. It is on the signature because the other implementation has one (a runner behind a
// URL), and process.Lister is what both are held to.
func (b *Broker) ListProcesses() ([]process.Instance, error) {
	b.mu.Lock()
	base := b.base
	pss := make([]*procState, 0, len(b.procs))
	for _, ps := range b.procs {
		pss = append(pss, ps)
	}
	b.mu.Unlock()
	out := make([]process.Instance, 0, len(pss))
	for _, ps := range pss {
		m := ps.snapshot()
		if base != "" {
			m.DialURL = base
		}
		out = append(out, m)
	}
	return out, nil
}

// ErrNoSuchProcess rather than an ok, and never ErrUnreachable: this registry is in memory,
// so "not here" is the whole of what can go wrong and it is always the truth.
func (b *Broker) ProcessSnapshot(id string) (process.Instance, error) {
	ps := b.get(id)
	if ps == nil {
		return process.Instance{}, fmt.Errorf("%w: %s", process.ErrNoSuchProcess, id)
	}
	m := ps.snapshot()
	if base := b.dialURL(); base != "" {
		m.DialURL = base
	}
	return m, nil
}

// ProcessInbox returns the control items queued for a process's worker, WITHOUT the epoch
// fence — an operator observability peek ("what control is waiting for this worker?"), not
// the worker's own consuming pull. It does not disturb a live worker's cursor.
func (b *Broker) ProcessInbox(id string) ([]api.InboxItem, bool) {
	ps := b.get(id)
	if ps == nil {
		return nil, false
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	// Returned as the items they are. This used to rebuild each one as a map, field by
	// field and kind by kind — a third spelling of a type the broker itself declares, which
	// quietly decided which fields an operator could see.
	return append(make([]api.InboxItem, 0, len(ps.inbox)), ps.inbox...), true
}

// handle is the verse-side Handle: the read plane replays+streams the merged log; the
// control plane enqueues inbox items for the worker (and records operator-authored effects
// on the log, per the interlock).
type handle struct {
	b  *Broker
	id string
}

func (h *handle) Subscribe() (<-chan process.Frame, func()) {
	// The whole log, so a (re)attach re-derives state from nothing.
	return h.subscribeFrom(0, false)
}

// SubscribeSince resumes from a cursor: the log after it, then live. It is the same contract
// process.SubscribeSince has locally, and it exists so `?since=` means ONE thing wherever the
// work runs — a client that resumed against a brokered process used to be handed the log from
// the beginning and had to recognise what it had already seen.
//
// A reader that finds its cursor below the retained window gets an EvGap first, so "you are
// resuming" and "you missed some" are distinguishable. Today a broker retains its whole log
// and never emits one; the check is here because the semantics belong to the contract, not to
// how much this implementation happens to keep.
func (h *handle) SubscribeSince(since int64) (<-chan process.Frame, func()) {
	return h.subscribeFrom(since, true)
}

func (h *handle) subscribeFrom(since int64, resume bool) (<-chan process.Frame, func()) {
	ps := h.b.get(h.id)
	if ps == nil {
		ch := make(chan process.Frame)
		close(ch)
		return ch, func() {}
	}
	ch := make(chan process.Frame, 256)
	ps.mu.Lock()
	// Primed under the lock, as the local bus does, so no live event can interleave ahead of
	// the replay and arrive out of order.
	if resume && len(ps.log) > 0 && since+1 < ps.log[0].Cursor {
		select {
		case ch <- process.Frame{Kind: process.EvGap, Process: ps.id, Cursor: ps.logCur}:
		default:
		}
	}
	for _, e := range ps.log {
		if resume && e.Cursor <= since {
			continue
		}
		select {
		case ch <- e:
		default:
		}
	}
	// A process that already ended is a finite record: hand over what it said and close,
	// rather than registering a subscriber for events that will never come.
	if ps.terminal {
		ps.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	id := ps.nextSub
	ps.nextSub++
	ps.subs[id] = ch
	ps.mu.Unlock()
	return ch, func() {
		ps.mu.Lock()
		if _, ok := ps.subs[id]; ok {
			delete(ps.subs, id)
			close(ch)
		}
		ps.mu.Unlock()
	}
}

func (h *handle) enqueue(it api.InboxItem) error {
	ps := h.b.get(h.id)
	if ps == nil {
		return process.ErrNoSuchProcess
	}
	ps.enqueue(it)
	return nil
}

// RelaysControl says that an answer accepted here is not an answer delivered: it is queued
// for a worker that must dial in and take it. Verse asks so it can record a decision as
// in-flight rather than as done — the difference between "approved" and "the thing you
// approved has been told".
//
// It is a statement rather than an inference. A worker-facing dial URL happens to imply the
// same thing today, and a runner that relayed without one would break that silently.
func (h *handle) RelaysControl() bool { return true }

// DialURL is where a worker dials in to run this process (the broker's hosted base). mirror
// type-asserts for this and publishes it as `dialURL`; handles without it (local, remote
// processd) have no separate worker-facing address.
func (h *handle) DialURL() string { return h.b.dialURL() }

// The broker is in-process and its inbox is in memory, so a control call cannot fail to be
// QUEUED. What it can fail to be is queued for a process that exists — and beyond that, the
// broker cannot speak for the worker: an item on the inbox is delivered when the worker pulls
// it, which is a separate fact this signature does not claim.
func (h *handle) Pause() error  { return h.enqueue(api.InboxItem{Kind: "pause"}) }
func (h *handle) Resume() error { return h.enqueue(api.InboxItem{Kind: "resume"}) }

// Cancel is verse-authoritative (Fork-1(b)): verse latches the process terminal
// immediately — a dial-in worker may be unresponsive, which is often why we cancel — and
// still enqueues a cancel item so a live worker winds down. A late worker `completed`
// loses to the latch. The reason rides the inbox item and the terminal status event.
func (h *handle) Cancel(reason string) error {
	ps := h.b.get(h.id)
	if ps == nil {
		return process.ErrNoSuchProcess
	}
	ps.enqueue(api.InboxItem{Kind: "cancel", Text: reason}) // relay to a live worker
	if ps.latchTerminal(process.Cancelled, "") {
		ps.emit(process.Frame{Kind: process.EvStatus, Text: string(process.Cancelled)})
	}
	return nil
}

func (h *handle) Steer(instr, by string) error {
	if err := h.enqueue(api.InboxItem{Kind: "say", Text: instr, By: by}); err != nil {
		return err
	}
	if ps := h.b.get(h.id); ps != nil { // operator-authored: verse observes it too
		ps.emit(process.Frame{Kind: process.EvSay, Text: instr, By: by})
	}
	return nil
}

// SetVector relays an autonomy slide to the worker: the new vector rides the inbox item so
// the worker applies it at its next boundary. Like a say-to, it is operator-authored, so verse
// records an EvVector on the read plane immediately (the new default mode) rather than waiting
// on the worker to echo it back.
func (h *handle) SetVector(v *process.Vector, by string) error {
	if err := h.enqueue(api.InboxItem{Kind: "vector", Vector: v, By: by}); err != nil {
		return err
	}
	if ps := h.b.get(h.id); ps != nil {
		ps.emit(process.Frame{Kind: process.EvVector, Mode: v.Default.String(), By: by})
	}
	return nil
}

// Resolve answers a gate the worker raised (via do gate). It relays the outcome to the
// worker's inbox (a resolve item it pulls to unblock) and records the decision on the log,
// so verse observes the resolution and the entity's pending clears — the return leg of the
// gate loop. Governance (who may resolve) is versed's concern, upstream of this.
// Resolve answers a gate the worker raised. It reports whether the decision was a
// genuine open gate: an unknown id (typo, wrong process) or one already answered finds
// nothing in pending and returns false, so verse can surface a 404 rather than silently
// enqueueing a resolve for a decision the worker will never recognize.
func (h *handle) Resolve(decision string, proceed bool, say, by string) error {
	ps := h.b.get(h.id)
	if ps == nil {
		return process.ErrNoSuchProcess
	}
	ps.mu.Lock()
	_, open := ps.pending[decision]
	answered := ps.answered[decision]
	abandoned := ps.abandoned
	ps.mu.Unlock()
	if !open {
		// Which it is matters to whoever asked: look for your typo, somebody was here
		// first, or the process ended underneath you.
		switch {
		case answered:
			return process.ErrAlreadyAnswered
		case abandoned:
			return process.ErrProcessEnded
		}
		return process.ErrNoSuchDecision
	}
	// by rides both legs: the worker sees who answered on its inbox item, and the read
	// plane records it on the decision — the same By a locally-resolved gate carries.
	//
	// The decision is NOT emitted here. Queuing an answer is not delivering one, and this
	// used to do both at once: the gate cleared and every surface reported the question
	// settled while the worker that must act on it had heard nothing. If it never came back
	// the answer evaporated with no trace that it was undelivered.
	//
	// It is emitted when the worker's inbox poll passes this item — its acknowledgement,
	// which the lease already reads for liveness and the cursor already reads for position.
	it := api.InboxItem{Kind: "resolve", Decision: decision, Proceed: proceed, Text: say, By: by}
	at := ps.enqueue(it)
	ps.mu.Lock()
	// Two different moments, and conflating them is what this separates. The RIGHT TO ANSWER
	// is taken now — the gate closes, and a second answerer is told somebody was here first,
	// because two answers to one question is a race with one winner however slowly the
	// answer travels. The DECISION is recorded later, when the worker takes it.
	delete(ps.pending, decision)
	if ps.answered == nil {
		ps.answered = map[string]bool{}
	}
	ps.answered[decision] = true
	if ps.undelivered == nil {
		ps.undelivered = map[int64]api.InboxItem{}
	}
	ps.undelivered[at] = it
	ps.mu.Unlock()
	return nil
}

// Err is the reason a brokered process failed. It needed no change to the worker protocol,
// contrary to what stood here: the protocol already carries the reason, because
// `verse process do fail <id> <why>` reports it on the terminal event's result. What was
// missing was filing it as a failure rather than as an answer (see latchTerminal), which is
// why this used to return "" and the mirror recorded a failure reason under `result`.
func (h *handle) Err() string {
	ps := h.b.get(h.id)
	if ps == nil {
		return ""
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.err
}

func (h *handle) Result() string {
	ps := h.b.get(h.id)
	if ps == nil {
		return ""
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.result
}

var (
	_ process.Runner = (*Broker)(nil)
	// Asserted so a change to Lister fails to compile here, rather than making versed's
	// type assertion report false and silently drop every brokered process from the list.
	_ process.Lister = (*Broker)(nil)
)

// WorkerHandler serves the worker-facing dial-in protocol (claim / inbox / report). It is
// intended for verse's sibling broker port, mounted under /broker/<nonce>. Both verse and
// the worker are clients of the broker; this is the worker's leg.
func (b *Broker) WorkerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /processes/{id}/claim", b.claim)
	mux.HandleFunc("GET /processes/{id}/inbox", b.inbox)
	mux.HandleFunc("POST /processes/{id}/report", b.report)
	return mux
}

// claim: a worker takes the process and gets the spec, resume cursors, and an epoch (the
// fencing token to carry on every subsequent report/inbox). Same-identity re-claim is
// always allowed (a reconnect — it bumps the epoch, fencing its own zombie). A different
// identity is rejected with 409 unless the claim was released (Revoke); a terminal process
// is 410 Gone. Every successful claim mints a fresh epoch (Fork-2(a)).
func (b *Broker) claim(w http.ResponseWriter, r *http.Request) {
	ps := b.get(r.PathValue("id"))
	if ps == nil {
		http.NotFound(w, r)
		return
	}
	if _, _, _, ok := ps.terminalState(); ok {
		http.Error(w, "process is terminal", http.StatusGone)
		return
	}
	var body struct {
		Identity string `tony:"field=identity"`
	}
	_ = codec.Read(r, &body)
	// A claim nobody has renewed within the lease is released here, at the moment somebody
	// asks for the work — which is the only moment it matters. No sweeper, no timer, and no
	// clock read except this one, taken against the broker's own earlier reading.
	if ps.expireClaim(time.Now()) {
		ps.emit(process.Frame{
			Kind: process.EvStatus, Text: string(process.Unclaimed),
			Err: "claim expired: the worker was not heard from within " + ClaimLease.String(),
		})
	}
	ps.mu.Lock()
	if ps.claimBy != "" && ps.claimBy != body.Identity {
		ps.mu.Unlock()
		http.Error(w, "already claimed", http.StatusConflict)
		return
	}
	firstClaim := ps.claimBy == "" // unclaimed → running transition (below, off-lock)
	if ps.claimBy != body.Identity {
		// A takeover, not a reconnect. What the previous holder acknowledged is not
		// something this worker has done, and an acknowledgement was never a promise that
		// the work was carried out — so the new holder starts from the beginning of what
		// is still queued rather than inheriting a position it never reached.
		ps.ackedCursor = 0
	}
	ps.epoch++ // mint a new generation; fences any prior holder (including a same-id zombie)
	ps.claimBy = body.Identity
	ps.renewed = time.Now() // the lease starts now
	resp := map[string]any{
		"spec":  map[string]string{"task": ps.task, "driver": ps.driver},
		"epoch": ps.epoch, // carry this on every report/inbox
		// Resume control from what this worker ACKNOWLEDGED, not from where the broker has
		// got to. Handing back the current cursor told a reconnecting worker to skip
		// everything queued while it was away — which is the outage, and so exactly the
		// window in which control is most likely to have been issued. An answer given
		// during it was never shown to the worker, while every other surface reported the
		// gate settled.
		"cCursor": ps.ackedCursor,
		"rCursor": ps.logCur, // resume reporting from here
	}
	ps.mu.Unlock()
	// An executor picked it up: leave the pre-running `unclaimed` state. emit takes ps.mu,
	// so it must run off-lock. A re-claim by the same worker (resume) does not re-announce.
	if firstClaim {
		ps.emit(process.Frame{Kind: process.EvStatus, Text: string(process.Running)})
	}
	writeOut(w, r, resp)
}

// inbox: control directed at the worker, with cursor > since. The caller's epoch must be
// the current generation, else it is fenced (409) — a displaced or revoked worker learns to
// stop here. The response carries the terminal status when set, giving the worker a clean
// stop condition (e.g. a verse-authoritative cancel it must wind down to). Immediate return
// (the worker polls at its own cadence); a production broker would long-poll.
func (b *Broker) inbox(w http.ResponseWriter, r *http.Request) {
	ps := b.get(r.PathValue("id"))
	if ps == nil {
		http.NotFound(w, r)
		return
	}
	epoch, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
	if ps.fenced(epoch) {
		http.Error(w, "fenced: stale claim", http.StatusConflict)
		return
	}
	ps.renew(epoch) // a worker that is polling is a worker that is alive
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	ps.mu.Lock()
	// A worker that asks to go BACKWARDS is served from where it actually got to. It has
	// already taken everything at or below ackedCursor, and handing it back would re-apply
	// a pause, a say or a vector slide it has already acted on — the broker knows what this
	// claim acknowledged, so it does not need the worker to remember.
	//
	// Only within one claim. A takeover resets the mark (see claim), because what a
	// DIFFERENT worker acknowledged says nothing about what this one has done.
	if since < ps.ackedCursor {
		since = ps.ackedCursor
	}

	// Read the poll as the acknowledgement it is: asking for what comes after N is something
	// a worker could only do having taken what came before, so N is the high-water mark a
	// re-claim should hand back.
	if since > ps.ackedCursor {
		ps.ackedCursor = since
	}
	// Everything at or below the acknowledged cursor has been TAKEN, so any answer riding
	// there is delivered and is now a decision. Collected under the lock, emitted off it.
	//
	// Note which poll settles it: the one that asks for what comes AFTER the item, not the
	// one that returns it. Receiving is not taking — that response could be the one that got
	// lost — and the conservative reading is the one that composes with the lease: a worker
	// that dies holding an answer it never acknowledged leaves it undelivered, which is
	// exactly what it is.
	var landed []api.InboxItem
	for cur, it := range ps.undelivered {
		if cur <= ps.ackedCursor {
			landed = append(landed, it)
			delete(ps.undelivered, cur)
		}
	}
	out := make([]api.InboxItem, 0)
	for _, it := range ps.inbox {
		if it.Cursor > since {
			out = append(out, it)
		}
	}
	status, terminal := ps.status, ps.terminal
	ps.mu.Unlock()
	for _, it := range landed {
		ps.emit(process.Frame{Kind: process.EvDecision, ID: it.Decision, Proceed: it.Proceed, By: it.By})
	}
	writeOut(w, r, map[string]any{"items": out, "status": status, "terminal": terminal})
}

// report: the worker appends events (say / exec / status …); each is sequenced onto the
// merged log and broadcast to verse-side subscribers. Two guards, in order:
//   - terminal-first: once terminal, the log is frozen — acknowledge idempotently with the
//     final status so a lost-ack retry of the same terminal reads as success, not an error;
//   - fence: only the current claim generation may write (a stale epoch → 409).
//
// A reported terminal status (completed | failed | worker-confirmed cancelled) latches once;
// a loser (verse already latched cancelled) is dropped so the log carries one terminal.
func (b *Broker) report(w http.ResponseWriter, r *http.Request) {
	ps := b.get(r.PathValue("id"))
	if ps == nil {
		http.NotFound(w, r)
		return
	}
	if status, result, errText, ok := ps.terminalState(); ok {
		writeOut(w, r, map[string]any{
			"rCursor": ps.cursorNow(), "status": status,
			"result": result, "err": errText, "terminal": true,
		})
		return
	}
	var body struct {
		Events []process.Frame `tony:"field=events"`
		Result string          `tony:"field=result,omitzero"`
		Epoch  int64           `tony:"field=epoch,omitzero"`
		// Key makes a report retryable. A worker whose response was lost cannot know
		// whether its events landed, and without this its only choices were to drop what it
		// observed or to report it twice — which is one deploy becoming two events with two
		// cursors, indistinguishable afterwards from the worker having done it twice.
		//
		// Optional: a worker that sends none gets exactly today's behaviour, which is that
		// every report is a new one.
		Key string `tony:"field=key,omitzero"`
	}
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ps.fenced(body.Epoch) {
		http.Error(w, "fenced: stale claim", http.StatusConflict)
		return
	}
	ps.renew(body.Epoch) // reporting is working, and working is being alive
	// A repeat of a key already applied is ANSWERED, not re-applied — and answered with what
	// the first one was answered with, so a worker retrying into a lost response gets the
	// same rCursor and the same count it would have got had the first response arrived.
	if ack, seen := ps.reportSeen(body.Key); seen {
		writeOut(w, r, map[string]any{"rCursor": ack.rCursor, "applied": ack.applied})
		return
	}
	latched, applied := false, 0
	for _, e := range body.Events {
		if e.Kind == process.EvStatus && process.Status(e.Text).Terminal() {
			latched = true
			if ps.latchTerminal(process.Status(e.Text), body.Result) { // worker-authoritative done/fail
				ps.emit(e)
				applied++
			}
			break // nothing follows a terminal
		}
		ps.emit(e)
		applied++
	}
	if !latched && body.Result != "" { // a non-terminal report may carry running result state
		ps.mu.Lock()
		ps.result = body.Result
		ps.mu.Unlock()
	}
	// applied says how many of the reported events were taken. It is not always len(events):
	// a batch whose terminal is not its last entry has everything after it dropped, since
	// nothing follows a terminal — and answering 200 without saying so told a worker its
	// whole wind-down had landed when part of it was on the floor. A retry can be computed
	// from this; it could not be computed from silence.
	ack := reportAck{rCursor: ps.cursorNow(), applied: applied}
	ps.rememberReport(body.Key, ack)
	writeOut(w, r, map[string]any{"rCursor": ack.rCursor, "applied": ack.applied})
}

func writeOut(w http.ResponseWriter, r *http.Request, v any) {
	codec.Write(w, r, http.StatusOK, v)
}
