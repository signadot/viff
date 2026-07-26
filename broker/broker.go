// Package broker is a controlPolicy whose runtime is an external DIAL-IN worker — a
// laptop agent, a pod in a private cluster, or a human. verse drives it through the
// process.ControlPolicy seam exactly as it drives a local or remote-processd policy; a
// worker connects OUT to the broker's worker-facing HTTP (claim / inbox / report) and
// the broker glues the two legs: a worker's reports become the events verse observes, and
// verse's control becomes items the worker pulls. Both parties are clients; only the
// broker is reachable — so a firewalled worker or a human works without verse inverting.
// See docs/control-policy.md "The worker-facing dial-in protocol".
package broker

import (
	"net/http"
	"strconv"
	"sync"

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

// Ref identifies this policy on the process:instance entity for resume-or-orphan. An
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
func (b *Broker) Spawn(id, task, driver string, _ process.ContentReader) process.ProcHandle {
	b.mu.Lock()
	b.procs[id] = &procState{
		id: id, task: task, driver: driver,
		subs: map[int]chan process.Frame{}, pending: map[string]string{},
	}
	b.mu.Unlock()
	return &handle{b: b, id: id}
}

// Attach re-attaches to an existing process (the resume path).
func (b *Broker) Attach(id string) process.ProcHandle {
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

	mu       sync.Mutex
	claimBy  string
	epoch    int64                      // claim generation; the fencing token (0 = unclaimed)
	log      []process.Frame            // merged event log (worker reports + control-effects)
	inbox    []inboxItem                // control directed at the worker
	subs     map[int]chan process.Frame // verse-side ProcHandle subscribers
	nextSub  int
	logCur   int64
	inboxCur int64
	pending  map[string]string // open gate decisions raised by the worker: id -> gate
	result   string
	status   process.Status // latched terminal status
	terminal bool
}

// inboxItem is one control instruction directed at the worker, cursored so a reconnecting
// worker resumes with ?since=<cursor>.
type inboxItem struct {
	Cursor   int64           `tony:"field=cursor"`
	Kind     string          `tony:"field=kind"` // spec | say | cancel | pause | resume | vector | resolve
	Task     string          `tony:"field=task,omitzero"`
	Driver   string          `tony:"field=driver,omitzero"`
	Text     string          `tony:"field=text,omitzero"`
	By       string          `tony:"field=by,omitzero"`
	Decision string          `tony:"field=decision,omitzero"` // resolve: the decision id being answered
	Proceed  bool            `tony:"field=proceed,omitzero"`  // resolve: approve (true) or deny (false)
	Vector   *process.Vector `tony:"field=vector,omitzero"`   // vector: the new gate vector to slide to
}

// emit appends an event to the merged log (stamping the next cursor) and broadcasts it to
// verse-side subscribers — used both by worker reports and by operator-authored control
// effects (a say-to). The terminal latch is separate (latchTerminal); emit only records.
func (ps *procState) emit(e process.Frame) {
	ps.mu.Lock()
	ps.logCur++
	e.Cursor, e.Process = ps.logCur, ps.id
	ps.log = append(ps.log, e)
	switch e.Kind { // track open gate decisions so `process get`/resolve see them
	case process.EvPending:
		ps.pending[e.ID] = e.Gate
	case process.EvDecision:
		delete(ps.pending, e.ID)
	}
	subs := make([]chan process.Frame, 0, len(ps.subs))
	for _, c := range ps.subs {
		subs = append(subs, c)
	}
	ps.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- e:
		default: // slow verse-side subscriber drops; it resyncs from the snapshot/log
		}
	}
}

func (ps *procState) enqueue(it inboxItem) {
	ps.mu.Lock()
	ps.inboxCur++
	it.Cursor = ps.inboxCur
	ps.inbox = append(ps.inbox, it)
	ps.mu.Unlock()
}

// latchTerminal records the terminal status once — first writer wins (terminal-once). It
// returns true iff this call performed the latch; the caller emits the status event only on
// a true return, so a losing writer (e.g. a late worker `completed` after a verse `cancel`)
// leaves no second terminal on the log. An optional result is frozen alongside.
func (ps *procState) latchTerminal(status process.Status, result string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.terminal {
		return false
	}
	ps.terminal = true
	ps.status = status
	if result != "" {
		ps.result = result
	}
	return true
}

// terminalState reports the frozen terminal status/result, if any.
func (ps *procState) terminalState() (status process.Status, result string, ok bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.status, ps.result, ps.terminal
}

// fenced reports whether epoch is NOT the current claim generation. A stale generation (a
// displaced or never-claimed worker) is fenced: its writes are rejected so it stops.
func (ps *procState) fenced(epoch int64) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return epoch == 0 || epoch != ps.epoch
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
	pending := make([]process.PendingGate, 0, len(ps.pending))
	for id, gate := range ps.pending {
		pending = append(pending, process.PendingGate{ID: id, Gate: gate})
	}
	return process.Instance{
		ID: ps.id, Task: ps.task, Driver: ps.driver, Status: status,
		Result: ps.result, Cursor: ps.logCur, Pending: pending,
	}
}

// ListProcesses / ProcessSnapshot enumerate the broker's processes for versed's unified
// process list. Each brokered process carries its dialURL so its worker face is discoverable
// from `process list`/`get` (the same base for all of a broker's processes).
func (b *Broker) ListProcesses() []process.Instance {
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
	return out
}

func (b *Broker) ProcessSnapshot(id string) (process.Instance, bool) {
	ps := b.get(id)
	if ps == nil {
		return process.Instance{}, false
	}
	m := ps.snapshot()
	if base := b.dialURL(); base != "" {
		m.DialURL = base
	}
	return m, true
}

// ProcessInbox returns the control items queued for a process's worker, WITHOUT the epoch
// fence — an operator observability peek ("what control is waiting for this worker?"), not
// the worker's own consuming pull. It does not disturb a live worker's cursor.
func (b *Broker) ProcessInbox(id string) ([]map[string]any, bool) {
	ps := b.get(id)
	if ps == nil {
		return nil, false
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]map[string]any, 0, len(ps.inbox))
	for _, it := range ps.inbox {
		m := map[string]any{"cursor": it.Cursor, "kind": it.Kind, "text": it.Text, "by": it.By}
		if it.Kind == "resolve" {
			m["decision"], m["proceed"] = it.Decision, it.Proceed
		}
		if it.Kind == "vector" {
			m["vector"] = it.Vector
		}
		out = append(out, m)
	}
	return out, true
}

// handle is the verse-side ProcHandle: the read plane replays+streams the merged log; the
// control plane enqueues inbox items for the worker (and records operator-authored effects
// on the log, per the interlock).
type handle struct {
	b  *Broker
	id string
}

func (h *handle) Subscribe() (<-chan process.Frame, func()) {
	ps := h.b.get(h.id)
	if ps == nil {
		ch := make(chan process.Frame)
		close(ch)
		return ch, func() {}
	}
	ch := make(chan process.Frame, 256)
	ps.mu.Lock()
	for _, e := range ps.log { // replay the log so a (re)attach re-derives state
		select {
		case ch <- e:
		default:
		}
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

func (h *handle) enqueue(it inboxItem) {
	if ps := h.b.get(h.id); ps != nil {
		ps.enqueue(it)
	}
}

// DialURL is where a worker dials in to run this process (the broker's hosted base). mirror
// type-asserts for this and publishes it as `dialURL`; handles without it (local, remote
// processd) have no separate worker-facing address.
func (h *handle) DialURL() string { return h.b.dialURL() }

func (h *handle) Pause()  { h.enqueue(inboxItem{Kind: "pause"}) }
func (h *handle) Resume() { h.enqueue(inboxItem{Kind: "resume"}) }

// Cancel is verse-authoritative (Fork-1(b)): verse latches the process terminal
// immediately — a dial-in worker may be unresponsive, which is often why we cancel — and
// still enqueues a cancel item so a live worker winds down. A late worker `completed`
// loses to the latch. The reason rides the inbox item and the terminal status event.
func (h *handle) Cancel(reason string) {
	ps := h.b.get(h.id)
	if ps == nil {
		return
	}
	ps.enqueue(inboxItem{Kind: "cancel", Text: reason}) // relay to a live worker
	if ps.latchTerminal(process.Cancelled, "") {
		ps.emit(process.Frame{Kind: process.EvStatus, Text: string(process.Cancelled)})
	}
}
func (h *handle) Steer(instr, by string) {
	h.enqueue(inboxItem{Kind: "say", Text: instr, By: by}) // relay to the worker
	if ps := h.b.get(h.id); ps != nil {                    // operator-authored: verse observes it too
		ps.emit(process.Frame{Kind: process.EvSay, Text: instr, By: by})
	}
}

// SetVector relays an autonomy slide to the worker: the new vector rides the inbox item so
// the worker applies it at its next boundary. Like a say-to, it is operator-authored, so verse
// records an EvVector on the read plane immediately (the new default mode) rather than waiting
// on the worker to echo it back.
func (h *handle) SetVector(v *process.Vector, by string) {
	h.enqueue(inboxItem{Kind: "vector", Vector: v, By: by})
	if ps := h.b.get(h.id); ps != nil {
		ps.emit(process.Frame{Kind: process.EvVector, Mode: v.Default.String(), By: by})
	}
}

// Resolve answers a gate the worker raised (via do gate). It relays the outcome to the
// worker's inbox (a resolve item it pulls to unblock) and records the decision on the log,
// so verse observes the resolution and the entity's pending clears — the return leg of the
// gate loop. Governance (who may resolve) is versed's concern, upstream of this.
// Resolve answers a gate the worker raised. It reports whether the decision was a
// genuine open gate: an unknown id (typo, wrong process) or one already answered finds
// nothing in pending and returns false, so verse can surface a 404 rather than silently
// enqueueing a resolve for a decision the worker will never recognize.
func (h *handle) Resolve(decision string, proceed bool, say string) bool {
	ps := h.b.get(h.id)
	if ps == nil {
		return false
	}
	ps.mu.Lock()
	_, open := ps.pending[decision]
	ps.mu.Unlock()
	if !open {
		return false
	}
	ps.enqueue(inboxItem{Kind: "resolve", Decision: decision, Proceed: proceed, Text: say})
	ps.emit(process.Frame{Kind: process.EvDecision, ID: decision, Proceed: proceed})
	return true
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

var _ process.ControlPolicy = (*Broker)(nil)

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
	if _, _, ok := ps.terminalState(); ok {
		http.Error(w, "process is terminal", http.StatusGone)
		return
	}
	var body struct {
		Identity string `tony:"field=identity"`
	}
	_ = codec.Read(r, &body)
	ps.mu.Lock()
	if ps.claimBy != "" && ps.claimBy != body.Identity {
		ps.mu.Unlock()
		http.Error(w, "already claimed", http.StatusConflict)
		return
	}
	firstClaim := ps.claimBy == "" // unclaimed → running transition (below, off-lock)
	ps.epoch++                     // mint a new generation; fences any prior holder (including a same-id zombie)
	ps.claimBy = body.Identity
	resp := map[string]any{
		"spec":    map[string]string{"task": ps.task, "driver": ps.driver},
		"epoch":   ps.epoch,    // carry this on every report/inbox
		"cCursor": ps.inboxCur, // resume control from here
		"rCursor": ps.logCur,   // resume reporting from here
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
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	ps.mu.Lock()
	out := make([]inboxItem, 0)
	for _, it := range ps.inbox {
		if it.Cursor > since {
			out = append(out, it)
		}
	}
	status, terminal := ps.status, ps.terminal
	ps.mu.Unlock()
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
	if status, result, ok := ps.terminalState(); ok {
		writeOut(w, r, map[string]any{"rCursor": ps.cursorNow(), "status": status, "result": result, "terminal": true})
		return
	}
	var body struct {
		Events []process.Frame `tony:"field=events"`
		Result string          `tony:"field=result,omitzero"`
		Epoch  int64           `tony:"field=epoch,omitzero"`
	}
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ps.fenced(body.Epoch) {
		http.Error(w, "fenced: stale claim", http.StatusConflict)
		return
	}
	latched := false
	for _, e := range body.Events {
		if e.Kind == process.EvStatus && process.Status(e.Text).Terminal() {
			latched = true
			if ps.latchTerminal(process.Status(e.Text), body.Result) { // worker-authoritative done/fail
				ps.emit(e)
			}
			break // nothing follows a terminal
		}
		ps.emit(e)
	}
	if !latched && body.Result != "" { // a non-terminal report may carry running result state
		ps.mu.Lock()
		ps.result = body.Result
		ps.mu.Unlock()
	}
	writeOut(w, r, map[string]any{"rCursor": ps.cursorNow()})
}

func writeOut(w http.ResponseWriter, r *http.Request, v any) {
	codec.Write(w, r, http.StatusOK, v)
}
