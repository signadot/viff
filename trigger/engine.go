package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"sync"
	"time"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/example/infra/verse/entity"
	"github.com/example/infra/verse/launch"
	"github.com/example/infra/verse/mirror"
	"github.com/example/infra/verse/process"
)

// Spawner turns a (bound) launch.Spec into a running, hosted process and returns
// it. It owns the full lifecycle — resolve the spec's references (driver, policy,
// resolver, scope, environment), build, mirror into the verse, and run/host — so
// the engine stays agnostic to how processes are hosted (a processd runtime here,
// an RPC to a remote runtime when distributed).
type Spawner func(id string, spec launch.Spec) process.ProcHandle

// flowProc is one process tracked under a flow: its id (for store-driven reap by ref)
// and its governing handle (the control target — a *Process locally, a remote handle
// behind a controlPolicy URL later). The engine holds handles, not concrete processes.
type flowProc struct {
	id string
	h  process.ProcHandle
}

// Engine runs the ECA loop over an entity-verse.
type Engine struct {
	store    entity.Store
	resolver process.Resolver // answers trigger ASK/PARK gates
	spawn    Spawner          // optional: how actions spawn processes
	triggers []Trigger
	log      io.Writer
	clock    func() time.Time // the system clock (injectable for tests); nil = time.Now

	mu       sync.Mutex
	fired    int
	maxFires int // 0 = unbounded (a runaway backstop, not a design limit)
	dc       int // decision counter for gate ids

	// Control plane — steering, serviced with strict priority over observation
	// (see docs/deployment-and-steering.md). Closures run on the loop goroutine, so
	// they serialize with dispatch and never race it; `paused`/`cursor` are
	// loop-goroutine-only.
	control chan func()
	stopped chan struct{} // closed when the loop goroutine exits (so do() can't hang)
	paused  bool
	cursor  int64 // highest processed store revision (the catch-up cursor)

	// flow tracking: a flow's processes by subject (an entity ref), so a flow can be
	// steered or cancelled as a unit. Guarded independently of the loop.
	flowsMu sync.Mutex
	flows   map[string][]flowProc

	// triggers + their declarative defs (the charter). Guarded because they can be
	// added/removed at runtime (over the wire) while the loop dispatches.
	trigMu sync.Mutex
	defs   []Def

	// goals: named goal aggregates, each owning a bundle of triggers + a done/fail
	// lifecycle monitor (see goal.go). Guarded independently of the charter.
	goalsMu sync.Mutex
	goals   map[string]*Goal

	// Per-trigger (subtree) observation mode. A scoped engine (a verse connect
	// federation) opens NO root watch; instead each Def added gets a watch on exactly
	// the (system,kind) subtree it matches. bgctx bounds those watchers.
	subtree bool
	bgctx   context.Context
}

// New builds an engine over store, using resolver for trigger gates.
func New(store entity.Store, resolver process.Resolver) *Engine {
	return &Engine{
		store: store, resolver: resolver, log: io.Discard,
		control: make(chan func(), 64),
		stopped: make(chan struct{}),
		flows:   map[string][]flowProc{},
		goals:   map[string]*Goal{},
	}
}

func (e *Engine) SetSpawner(s Spawner) { e.spawn = s }
func (e *Engine) SetMaxFires(n int)    { e.maxFires = n }
func (e *Engine) SetLog(w io.Writer)   { e.log = w }
func (e *Engine) Store() entity.Store  { return e.store }

// Add installs a trigger (closure-backed). Safe to call while the engine runs.
func (e *Engine) Add(t Trigger) {
	e.trigMu.Lock()
	e.triggers = append(e.triggers, t)
	e.trigMu.Unlock()
}

// Fired reports how many times a trigger has fired (the trace/budget counter).
func (e *Engine) Fired() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fired
}

// Start subscribes to the verse (synchronously, before returning) and runs the
// dispatch loop in the background until ctx is cancelled. Subscribing before Start
// returns means changes produced right after Start are never missed.
//
// The loop services the control plane with strict priority over observation
// (steering outranks observation): all pending control is drained before any change
// is dispatched, and while paused no change is dispatched at all — so control can
// never queue behind an observation backlog. See docs/deployment-and-steering.md.
func (e *Engine) Start(ctx context.Context) {
	e.bgctx = ctx            // also bounds action-time monitors (provider selection) — cancelled on stop
	e.cursor = e.store.Rev() // start live from now; Observe() delivers rev>cursor
	ch, unsub := e.store.Observe()
	go e.loop(ctx, ch, unsub)
}

// ObserveScoped puts the engine in per-trigger (subtree) observation mode: it opens
// NO root watch. Each Def added afterward gets its own watch on exactly the
// (system,kind) subtree it matches — the docd-aligned model (watch what you match,
// not the whole verse). Used for a scoped engine (a verse connect federation); ctx
// bounds every subtree watcher, so cancelling it stops them all. Baseline engines use
// Start instead. Actions still run on this engine's (scoped) store and spawner.
func (e *Engine) ObserveScoped(ctx context.Context) {
	e.bgctx = ctx
	e.subtree = true
	// Install a subtree watch for any trigger already added before we entered subtree
	// mode (e.g. the baseline charter seeded at startup), so entering subtree mode is
	// independent of whether triggers were added before or after this call.
	e.trigMu.Lock()
	type pending struct {
		t Trigger
		d Def
	}
	var ps []pending
	for _, d := range e.defs {
		for _, t := range e.triggers {
			if t.Name == d.Name {
				ps = append(ps, pending{t, d})
				break
			}
		}
	}
	e.trigMu.Unlock()
	for _, p := range ps {
		if err := e.watchDefSubtree(p.t, p.d); err != nil {
			e.tracef("subtree watch for %q: %v", p.d.Name, err)
		}
	}
}

// watchDefSubtree opens a subtree watch for a subtree-mode trigger and fires it on
// each matching change. The store must support SubtreeWatcher (the logd store does).
func (e *Engine) watchDefSubtree(t Trigger, d Def) error {
	if len(d.Matches) != 1 || d.Matches[0].System == "" {
		return fmt.Errorf("subtree trigger %q needs exactly one match with a system (kind optional)", d.Name)
	}
	sw, ok := e.store.(entity.SubtreeWatcher)
	if !ok {
		return fmt.Errorf("scoped trigger %q: store has no subtree watch", d.Name)
	}
	m := d.Matches[0]
	ch, unsub, err := sw.WatchSubtree(m.System, m.Kind)
	if err != nil {
		return fmt.Errorf("scoped trigger %q: watch %s:%s: %w", d.Name, m.System, m.Kind, err)
	}
	ctx := e.bgctx
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if !t.matchesEvent(ev) || e.overBudget() {
					continue
				}
				go e.fire(ctx, t, ev) // off the watcher goroutine: a gate must not stall it
			}
		}
	}()
	return nil
}

func (e *Engine) loop(ctx context.Context, ch <-chan entity.Event, unsub func()) {
	defer close(e.stopped) // unblock any do() waiting once the loop is gone
	defer unsub()
	for {
		e.drainControl() // steering outranks observation: all pending control first
		if e.paused {
			// Paused: service control only. No observation is consumed and nothing
			// is buffered here — on resume we replay from the cursor (catchUp).
			select {
			case <-ctx.Done():
				return
			case fn := <-e.control:
				fn()
			}
			continue
		}
		// Reconcile the cursor to current (level-triggered): after a pause, a flow
		// parked at a long gate, or dropped live events, replay the gap from the
		// store rather than relying on a held buffer.
		if e.cursor < e.store.Rev() {
			e.catchUp(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case fn := <-e.control:
			fn()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			e.consume(ctx, ev)
		}
	}
}

// consume treats a live change as a low-latency doorbell, not the source of truth:
// if it is anywhere ahead of the cursor, reconcile the whole gap from the store via
// catchUp (Since). This keeps the engine correct without trusting live contiguity
// or assuming the backend's revisions are dense — logd commits are monotonic but
// sparse; MemStore's happen to be +1. Both are just "greater than the cursor."
func (e *Engine) consume(ctx context.Context, ev entity.Event) {
	if ev.Rev > e.cursor {
		e.catchUp(ctx)
	}
}

// catchUp replays changes since the cursor from the store (a revision query), in
// order — the cursor-based catch-up that makes a hold cost a cursor, not a buffer.
// If the cursor is older than the store's retained window, it resyncs to current.
func (e *Engine) catchUp(ctx context.Context) {
	evs, ok := e.store.Since(e.cursor)
	if !ok {
		e.tracef("cursor %d older than store window; resync to current", e.cursor)
		e.cursor = e.store.Rev()
		return
	}
	for _, ev := range evs {
		if ev.Rev <= e.cursor {
			continue // Since is ordered and monotonic; skip anything not ahead
		}
		e.cursor = ev.Rev
		e.dispatch(ctx, ev)
	}
}

// drainControl runs every pending control closure (non-blocking) — the priority
// step that makes steering outrank observation.
func (e *Engine) drainControl() {
	for {
		select {
		case fn := <-e.control:
			fn()
		default:
			return
		}
	}
}

// do enqueues fn on the loop goroutine and waits until it has run — so control is
// serialized with dispatch and applied ahead of observation. Requires a started
// engine (the loop is what drains control).
func (e *Engine) do(fn func()) {
	done := make(chan struct{})
	select {
	case e.control <- func() { fn(); close(done) }:
	case <-e.stopped:
		return // loop has exited (ctx cancelled); nothing will run the closure
	}
	select {
	case <-done:
	case <-e.stopped: // loop exited before running it — don't hang
	}
}

// Pause holds the loop: no further changes dispatch until Resume. Synchronous —
// returns once applied. Steering, out-of-band from the observation stream.
func (e *Engine) Pause() { e.do(func() { e.paused = true; e.tracef("paused (control)") }) }

// Resume lifts a Pause; buffered changes then dispatch.
func (e *Engine) Resume() { e.do(func() { e.paused = false; e.tracef("resumed (control)") }) }

// Do runs fn on the control plane — serialized with dispatch, serviced ahead of
// observation. The generic steering primitive (rebind a trigger, cancel a flow,
// inject a steer) until those get typed methods. Synchronous.
func (e *Engine) Do(fn func()) { e.do(fn) }

// dispatch matches one change against every trigger and fires the matches. Firing
// is off the observe goroutine so a blocking gate (a human) never stalls the loop.
func (e *Engine) dispatch(ctx context.Context, ev entity.Event) {
	e.reap(ev) // retire completed processes from their flow (store-driven bookkeeping)
	e.trigMu.Lock()
	trigs := make([]Trigger, len(e.triggers))
	copy(trigs, e.triggers)
	e.trigMu.Unlock()
	for i := range trigs {
		t := trigs[i]
		// Ignition guard: reflected process:* state is not in the default match set;
		// a trigger must opt in (a Match with System == mirror.System) to react to it,
		// so the loop cannot ignite on its own machinery.
		if ev.Ref.System == mirror.System && !t.opensTo(mirror.System) {
			continue
		}
		if !t.matchesEvent(ev) {
			continue
		}
		if e.overBudget() { // termination backstop: don't even pose gates once capped
			e.tracef("budget exhausted (%d); not firing %s on %s", e.maxFires, t.Name, ev.Ref)
			continue
		}
		go e.fire(ctx, t, ev)
	}
}

// overBudget reports whether the fire budget is exhausted — a non-consuming check,
// so a firing that never proceeds (a denied gate) does not burn a slot.
func (e *Engine) overBudget() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.maxFires > 0 && e.fired >= e.maxFires
}

// reserve consumes a fire from the budget; false when exhausted. Called only once a
// firing has passed its gates, so the backstop bounds actions taken, not attempts.
func (e *Engine) reserve() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.maxFires > 0 && e.fired >= e.maxFires {
		return false
	}
	e.fired++
	return true
}

// fire resolves a matched trigger's gates (conjunctively) and runs its action if
// they all proceed. All blocking gates are posed *concurrently* — so a trigger's
// gates appear together and approvers can act in parallel — then fire waits for
// every one; any denial stops the firing.
func (e *Engine) fire(ctx context.Context, t Trigger, ev entity.Event) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	allProceed := true

	for _, gate := range t.Gates {
		if gate.Formula != nil { // temporal gate: monitored on the store, not posed
			gname := gate.Name
			if gname == "" {
				gname = t.Name
			}
			wg.Add(1)
			go func(g Gate, gname string) {
				defer wg.Done()
				if !e.monitorGate(ctx, t, g, gname, ev) {
					mu.Lock()
					allProceed = false
					mu.Unlock()
				}
			}(gate, gname)
			continue
		}
		if !gate.Mode.Blocks() { // auto: passes without a resolver
			continue
		}
		gname := gate.Name
		if gname == "" {
			gname = t.Name
		}
		e.mu.Lock()
		e.dc++
		id := fmt.Sprintf("trigger:%s:%s#%d", t.Name, gname, e.dc)
		e.mu.Unlock()
		d := process.GateQuestion{
			ID: id, Origin: t.Name, Mode: gate.Mode, Gate: gname,
			Reason: fmt.Sprintf("trigger %s gate %s on %s", t.Name, gname, ev.Ref),
		}
		wg.Add(1)
		go func(d process.GateQuestion, gname string) {
			defer wg.Done()
			res, err := e.resolver.Resolve(ctx, d, process.ToolUse{Name: "trigger:" + t.Name})
			e.tracef("gate %s/%s on %s → %s by %s", t.Name, gname, ev.Ref, proceedWord(res.Proceed), res.By)
			if err != nil || !res.Proceed {
				mu.Lock()
				allProceed = false
				mu.Unlock()
			}
		}(d, gname)
	}
	wg.Wait()

	if !allProceed {
		return // some gate denied — no budget consumed
	}
	if !e.reserve() { // consume a slot only for a firing that actually proceeds
		e.tracef("budget exhausted (%d); not firing %s on %s", e.maxFires, t.Name, ev.Ref)
		return
	}
	e.tracef("fire %s on %s", t.Name, ev.Ref)
	if t.Action != nil {
		t.Action(ev, e)
	}
}

// monitorGate blocks until a trigger gate's temporal formula resolves, monitored on the
// store's watch stream, then latches the verdict via a CAS predicated on the witness
// (revocable-until-commit + terminal-once). It returns whether the action should proceed
// (false = ctx ended, or the gate committed a deny). If a revoke lands between
// satisfaction and the commit, the CAS fails and it re-monitors.
func (e *Engine) monitorGate(ctx context.Context, t Trigger, g Gate, gname string, ev entity.Event) bool {
	did := decisionID(t, gname, ev)
	channelRef := entity.Ref{System: "decision", Kind: "channel", ID: did}
	// Bind the formula to THIS decision so a quorum atom tallies vote:<did>:* — its own
	// vote log — and open a discoverable channel entity so voters can find and observe it.
	f := g.Formula.bind(did)
	e.openDecision(channelRef, t, gname, ev, f.quorumNeed())
	commitRef := channelRef
	for {
		var proceed, ok bool
		var witness []entity.Ref
		var recheck *Formula // the satisfy-formula the commit is predicated on (nil = none)
		if f != nil && f.Op == OpUntil && len(f.Args) == 2 {
			proceed, witness, ok = e.awaitUntil(ctx, channelRef, f.Args[0], f.Args[1])
			if proceed {
				recheck = f.Args[0] // predicate a proceed on the satisfy side still holding
			}
		} else {
			witness, ok = NewMonitor(f, e.store).Await(ctx)
			proceed = ok
			recheck = f
		}
		if !ok {
			e.tracef("temporal gate %s/%s: monitor ended before commit", t.Name, gname)
			return false
		}
		outcome := "deny"
		if proceed {
			outcome = "proceed"
		}
		eff, revoked := e.commitDecision(commitRef, recheck, outcome, witness)
		if revoked {
			e.tracef("temporal gate %s/%s: revoked before commit → re-monitoring", t.Name, gname)
			continue
		}
		e.tracef("temporal gate %s/%s committed %q (witness %d facts)", t.Name, gname, eff, len(witness))
		return eff == "proceed"
	}
}

// awaitUntil evaluates `left until right`: proceed when the left formula holds, or default
// (deny) when the right side fires first. The right side is one of two things — a store
// condition (a fact/quorum, watched and Eval'd like left), or a `deadline` (a duration
// resolved against the system clock, armed as a timer; NOT a store fact, so nothing
// produces it). Left wins a tie. Returns (proceed, witness, ok); ok is false if ctx ends
// first.
func (e *Engine) awaitUntil(ctx context.Context, channelRef entity.Ref, left, right *Formula) (bool, []entity.Ref, bool) {
	// The right side is either a store condition (a fact/quorum — watch its subtree and
	// Eval it) or a deadline (a duration resolved against the system clock — a timer). A
	// deadline is a derivation from time, not a store fact, so it is handled here, not via
	// Eval. Left (proceed) wins a tie.
	dur, isDeadline := deadlineDuration(right)
	var deadlineC <-chan time.Time
	if isDeadline {
		at := e.deadlineAt(channelRef, dur)     // stamp/read deadline_at on the channel (restart-safe)
		timer := time.NewTimer(at.Sub(e.now())) // fires at the deadline (immediately if already past)
		defer timer.Stop()                      // don't leak the timer when the store side wins first
		deadlineC = timer.C
	}

	// check evaluates the store side(s); the deadline is signalled by deadlineC, not here.
	check := func() (proceed, decided bool, witness []entity.Ref) {
		if ok, w := left.Eval(e.store); ok {
			return true, true, w
		}
		if !isDeadline {
			if ok, w := right.Eval(e.store); ok {
				return false, true, w
			}
		}
		return false, false, nil
	}
	if proceed, decided, w := check(); decided {
		return proceed, w, true
	}

	// One watch per DISTINCT subtree (left always; right too, unless it is a deadline):
	// left and right frequently read the SAME subtree (approve vs reject over one vote
	// log), and docd permits only one watch per path per session.
	sw, ok := e.store.(entity.SubtreeWatcher)
	if !ok {
		return false, nil, false
	}
	sub, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan entity.Event, 64)
	subtrees := left.Subtrees()
	if !isDeadline {
		subtrees = append(subtrees, right.Subtrees()...)
	}
	seen := map[[2]string]bool{}
	for _, st := range subtrees {
		if seen[st] {
			continue
		}
		seen[st] = true
		ch, unsub, err := sw.WatchSubtree(st[0], st[1])
		if err != nil {
			continue
		}
		defer unsub()
		go drain(sub, ch, events)
	}
	for {
		select {
		case <-ctx.Done():
			return false, nil, false
		case <-deadlineC: // the clock struck — default (deny), unless the left side just made it
			if okL, w := left.Eval(e.store); okL {
				return true, w, true
			}
			return false, nil, true
		case <-events:
			if proceed, decided, w := check(); decided {
				return proceed, w, true
			}
		}
	}
}

// now is the engine's clock (injectable for tests; defaults to the wall clock).
func (e *Engine) now() time.Time {
	if e.clock != nil {
		return e.clock()
	}
	return time.Now()
}

// SetClock overrides the engine's clock — for tests that need a deadline to arrive
// deterministically rather than in wall-clock time.
func (e *Engine) SetClock(fn func() time.Time) { e.clock = fn }

// deadlineDuration reports a formula's deadline duration, if its right side is a deadline
// requirement (`{"deadline":"4h"}`).
func deadlineDuration(f *Formula) (time.Duration, bool) {
	if f == nil || f.Op != OpAtom || f.Req == nil || f.Req.Kind != ReqDeadline {
		return 0, false
	}
	d, err := time.ParseDuration(f.Req.Within)
	if err != nil {
		return 0, false
	}
	return d, true
}

// deadlineAt returns the absolute instant a decision closes on timeout, stamping it on the
// channel the first time (so `verse decisions` shows when it closes, and a restart re-arms
// against the same absolute time — elapsed wall time is honored). It is terminal-once like
// commitDecision/commitSelection: it returns the EFFECTIVE stamped value, so a re-fire — or
// a concurrent arming of the same channel — reads the durable instant rather than sliding
// the deadline forward or arming a timer against a value that never landed.
func (e *Engine) deadlineAt(ref entity.Ref, dur time.Duration) time.Time {
	// Fast path: already stamped — honor it without a write.
	if cur, ok := e.store.Get(ref); ok {
		if at, ok := parseDeadlineAt(cur.Payload); ok {
			return at
		}
	}
	want := e.now().Add(dur).Format(time.RFC3339)
	var eff string
	e.store.Modify(ref, func(cur entity.Payload) entity.Payload {
		if s, done := cur["deadline_at"].(string); done && s != "" {
			eff = s // a concurrent arming won the CAS — honor its instant, not ours
			return cur
		}
		eff = want
		q := clonePayload(cur)
		q["deadline_at"] = want
		return q
	})
	if at, err := time.Parse(time.RFC3339, eff); err == nil {
		return at
	}
	return e.now().Add(dur) // unparseable stamp (shouldn't happen) — arm a fresh timer
}

// parseDeadlineAt reads a channel payload's stamped RFC3339 deadline_at, if present.
func parseDeadlineAt(p entity.Payload) (time.Time, bool) {
	s, _ := p["deadline_at"].(string)
	if s == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, s)
	return at, err == nil
}

// commitDecision latches a gate's verdict onto the decision entity: terminal-once (the
// first commit wins; a later change cannot un-commit) and — for a proceed predicated on a
// quorum — revocable, so a witness ballot retracted before the commit aborts it. It returns
// the effective (possibly pre-existing) outcome and whether it was revoked. The witness is
// recorded on the entity for audit.
//
// When the store is a ConditionalCommitter and the recheck is a quorum, both guards ride one
// ATOMIC conditional write (terminal-once via the channel's status, revoke via the witness
// ballots) — no window between the check and the commit. Otherwise it falls back to a
// best-effort check-then-Modify, which leaves a small non-atomic window.
func (e *Engine) commitDecision(ref entity.Ref, recheck *Formula, outcome string, witness []entity.Ref) (eff string, revoked bool) {
	cc, canCommitIf := e.store.(entity.ConditionalCommitter)
	if wpre, ok := witnessPreconditions(recheck, witness); canCommitIf && ok {
		return e.commitDecisionAtomic(cc, ref, outcome, witness, wpre)
	}
	// Fallback: a store without conditional commit, or a deny with no satisfy side to
	// predicate on. Best-effort recheck, then a terminal-once Modify.
	if recheck != nil {
		if ok, _ := recheck.Eval(e.store); !ok {
			return "", true // the justifying facts no longer hold — do not commit
		}
	}
	e.store.Modify(ref, func(cur entity.Payload) entity.Payload {
		if o, done := cur["outcome"].(string); done {
			eff = o // already latched — terminal-once, honor it
			return cur
		}
		eff = outcome
		return closedDecision(cur, outcome, witness)
	})
	return eff, false
}

// commitDecisionAtomic commits the verdict via one atomic conditional write: terminal-once
// (the channel is still "open") AND the revoke guard (wpre — the witness ballots still
// hold), both checked with the write in a single step. A refused commit is either an
// already-latched decision (read it back and honor it — terminal-once) or a revoke (the
// witness moved — re-monitor).
func (e *Engine) commitDecisionAtomic(cc entity.ConditionalCommitter, ref entity.Ref, outcome string, witness []entity.Ref, wpre []entity.Precondition) (eff string, revoked bool) {
	cur, _ := e.store.Get(ref)
	if o, done := cur.Payload["outcome"].(string); done {
		return o, false // already committed — terminal-once
	}
	pre := append([]entity.Precondition{{Ref: ref, Pattern: map[string]any{"status": "open"}}}, wpre...)
	committed, err := cc.CommitIf(ref, closedDecision(cur.Payload, outcome, witness), pre...)
	if err != nil {
		e.tracef("commit %s: conditional commit error: %v", ref.ID, err)
		return "", true // treat as a revoke; monitorGate re-monitors
	}
	if !committed {
		if got, ok := e.store.Get(ref); ok {
			if o, done := got.Payload["outcome"].(string); done {
				return o, false // a concurrent commit latched first — honor it
			}
		}
		return "", true // the witness no longer holds — revoked
	}
	return outcome, false
}

// closedDecision builds the closed channel payload from the open one, preserving its open
// fields (need/gate/subject) and recording the outcome, tally, and witness.
func closedDecision(cur entity.Payload, outcome string, witness []entity.Ref) entity.Payload {
	p := clonePayload(cur)
	p["status"] = "closed"
	p["outcome"] = outcome
	p["tally"] = len(witness)
	if ids := refIDs(witness); len(ids) > 0 {
		p["witness"] = ids
	}
	return p
}

// witnessPreconditions builds the revoke-guard precondition for an atomic commit: it is
// applicable only when the satisfy-formula is a quorum atom, in which case it requires each
// witness ballot to still be present with the counted choice, over the decision's vote
// subtree. Returns ok=false when there is no quorum recheck or no witness (the deny path, or
// a non-quorum formula) — the caller then uses the best-effort fallback.
func witnessPreconditions(recheck *Formula, witness []entity.Ref) ([]entity.Precondition, bool) {
	if recheck == nil || recheck.Op != OpAtom || recheck.Req == nil || recheck.Req.Kind != ReqQuorum {
		return nil, false
	}
	fs := recheck.Req.Facts
	if fs.System == "" || fs.Kind == "" {
		return nil, false
	}
	choice := fs.Where["choice"]
	sub := map[string]any{}
	for _, w := range witness {
		if w.System != fs.System || w.Kind != fs.Kind {
			continue
		}
		ballot := map[string]any{}
		if choice != "" {
			ballot["choice"] = choice
		}
		sub[w.ID] = ballot
	}
	if len(sub) == 0 {
		return nil, false
	}
	return []entity.Precondition{{Ref: entity.Ref{System: fs.System, Kind: fs.Kind}, Pattern: sub}}, true
}

// decisionID is a short, stable id for a gate's decision instance — deterministic in
// (trigger, gate, event) so a re-fire is idempotent (same channel, terminal-once).
func decisionID(t Trigger, gname string, ev entity.Event) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.Name + "/" + gname + "/" + ev.Ref.String()))
	return fmt.Sprintf("%08x", h.Sum32())
}

// openDecision writes the discoverable channel entity for a decision — what is being
// decided and the threshold — so a voter can find it (`verse decisions`) and observe the
// tally. Idempotent: it never reopens a decision that has already closed.
func (e *Engine) openDecision(ref entity.Ref, t Trigger, gname string, ev entity.Event, need int) {
	e.store.Modify(ref, func(cur entity.Payload) entity.Payload {
		if _, done := cur["outcome"]; done {
			return cur // already closed — leave it
		}
		p := clonePayload(cur)
		p["status"] = "open"
		p["need"] = need
		p["trigger"] = t.Name
		p["gate"] = gname
		p["subject"] = ev.Ref.String()
		return p
	})
}

// clonePayload returns a shallow copy of p (nil → empty), so a Modify can extend the
// current value without mutating the store's clone.
func clonePayload(p entity.Payload) entity.Payload {
	out := entity.Payload{}
	for k, v := range p {
		out[k] = v
	}
	return out
}

// refIDs renders a witness as its entity ref strings (audit trail on the decision).
func refIDs(refs []entity.Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.String())
	}
	return out
}

// ── the who-axis: governed provider selection ───────────────────────────────

// selectProvider resolves the who-axis for a spawn whose Spec carries a Provider: it
// reifies a `select` decision channel, resolves ONE of the eligible doers via the Select
// surface, and returns the chosen driver (which the caller binds into Spec.Driver before
// spawn — no live swap). The selection is CAS-latched terminal-once, so a re-fire is
// idempotent (same channel, same choice). It is orthogonal to the controlPolicy that then
// transports the chosen driver.
func (e *Engine) selectProvider(p *launch.Provider, subject entity.Ref) (string, bool) {
	options, drivers := e.eligibleProviders(p)
	if len(options) == 0 {
		return "", false
	}
	did := selectionID(subject, p)
	channelRef := entity.Ref{System: "decision", Kind: "channel", ID: did}
	e.openSelect(channelRef, subject, p, options)

	var chosen string
	switch p.Select {
	case launch.SurfaceVote, launch.SurfaceAsk:
		// Both resolve a pick over the vote log; ask is a vote where a single valid pick
		// decides (the delegated-authority case), vote awaits the plurality threshold.
		need := p.Need
		if p.Select == launch.SurfaceAsk {
			need = 1
		}
		sel, ok := e.awaitPlurality(channelRef, did, options, need, p.Within)
		if !ok {
			e.tracef("select %s: %s ended before a decision", did, p.Select)
			return "", false
		}
		chosen = sel
	default: // auto (and empty): the first eligible doer
		chosen = options[0]
	}
	selected := e.commitSelection(channelRef, chosen) // terminal-once (honors a prior fire)
	driver, ok := drivers[selected]
	if !ok {
		driver = selected // a prior selection no longer in the registry — use it as the driver
	}
	return driver, true
}

// eligibleProviders resolves a Provider's selectable options to {option → driver}, in a
// deterministic order. Inline Eligible: the option IS the driver. Registry Capability:
// list provider:<capability>:* whose attributes satisfy Match (a tony merge match); the
// option is the provider id, the driver is its `driver` attribute (falling back to the id).
func (e *Engine) eligibleProviders(p *launch.Provider) ([]string, map[string]string) {
	if p == nil {
		return nil, nil
	}
	var options []string
	drivers := map[string]string{}
	add := func(opt, driver string) {
		if _, seen := drivers[opt]; seen {
			return
		}
		options = append(options, opt)
		drivers[opt] = driver
	}
	for _, d := range p.Eligible {
		add(d, d)
	}
	if p.Capability != "" {
		if lister, ok := e.store.(entity.Lister); ok {
			var pattern *ir.Node
			if len(p.Match) > 0 {
				pattern, _ = gomap.ToTonyIR(p.Match)
			}
			ents := lister.List(func(r entity.Ref) bool { return r.System == "provider" && r.Kind == p.Capability })
			sort.Slice(ents, func(i, j int) bool { return ents[i].Ref.ID < ents[j].Ref.ID })
			for _, en := range ents {
				if pattern != nil && !providerMatches(en.Payload, pattern) {
					continue
				}
				driver, _ := en.Payload["driver"].(string)
				if driver == "" {
					driver = en.Ref.ID
				}
				add(en.Ref.ID, driver)
			}
		}
	}
	return options, drivers
}

// providerMatches reports whether a provider's attributes satisfy an eligibility pattern,
// evaluated as a tony (RFC-7396 merge) match — the attributes are converted to a tony
// node and matched against the pattern. Full tag-ops (!and/!or/!glob/…) arrive when the
// pattern is sourced as tony text; for now a JSON pattern is a structural merge match.
func providerMatches(attrs entity.Payload, pattern *ir.Node) bool {
	node, err := gomap.ToTonyIR(map[string]any(attrs))
	if err != nil {
		return false
	}
	ok, err := tony.Match(node, pattern)
	return err == nil && ok
}

// selectionID is a short, stable id for a provider selection — deterministic in the
// subject and the eligibility spec (inline set, capability, match), so a re-fire reuses
// the same channel (idempotent). json.Marshal sorts map keys, so the hash is stable.
func selectionID(subject entity.Ref, p *launch.Provider) string {
	h := fnv.New32a()
	spec, _ := json.Marshal(struct {
		E []string       `json:"e"`
		C string         `json:"c"`
		M map[string]any `json:"m"`
	}{p.Eligible, p.Capability, p.Match})
	_, _ = h.Write([]byte("select/" + subject.String() + "/"))
	_, _ = h.Write(spec)
	return fmt.Sprintf("%08x", h.Sum32())
}

// openSelect writes the discoverable `select` decision channel — a categorical (1-of-N)
// decision — so the choice is reified and observable (`verse decisions`). Idempotent: it
// never reopens an already-resolved selection.
func (e *Engine) openSelect(ref, subject entity.Ref, p *launch.Provider, options []string) {
	e.store.Modify(ref, func(cur entity.Payload) entity.Payload {
		if _, done := cur["selected"]; done {
			return cur // already resolved — leave it
		}
		q := clonePayload(cur)
		q["kind"] = "select"
		q["status"] = "open"
		q["options"] = append([]string(nil), options...)
		q["surface"] = string(p.Select)
		q["subject"] = subject.String()
		if p.Capability != "" {
			q["capability"] = p.Capability
		}
		if p.Need > 0 {
			q["need"] = p.Need
		}
		return q
	})
}

// commitSelection CAS-latches the chosen option onto the select channel: terminal-once
// (the first commit wins — a later change cannot re-select). Returns the effective
// (possibly pre-existing) selection.
func (e *Engine) commitSelection(ref entity.Ref, selected string) (eff string) {
	e.store.Modify(ref, func(cur entity.Payload) entity.Payload {
		if s, done := cur["selected"].(string); done {
			eff = s // already latched — terminal-once, honor it
			return cur
		}
		eff = selected
		q := clonePayload(cur)
		q["status"] = "closed"
		q["selected"] = selected
		return q
	})
	return eff
}

// awaitPlurality resolves a vote-selected provider: it tallies the decision's vote log
// (vote:<did>:<voter>{choice}) by option, restricted to eligible options, and returns the
// first option to reach the threshold (Need, default 1) — threshold-plurality. Monitored
// reactively on the vote subtree, and bounded two ways: the engine's lifetime ctx (so a
// pending selection is released on stop, not leaked), and — when within is a duration — a
// deadline armed off channelRef, the who-axis analog of an `until` gate's deadline. A
// selection that reaches neither the threshold nor the deadline returns ("", false): no
// pick, so the caller does not spawn.
func (e *Engine) awaitPlurality(channelRef entity.Ref, did string, options []string, need int, within string) (string, bool) {
	if need < 1 {
		need = 1
	}
	eligible := map[string]bool{}
	for _, o := range options {
		eligible[o] = true
	}
	leader := func() (string, int) {
		lister, ok := e.store.(entity.Lister)
		if !ok {
			return "", 0
		}
		counts := map[string]int{}
		for _, en := range lister.List(func(r entity.Ref) bool { return r.System == "vote" && r.Kind == did }) {
			if choice, _ := en.Payload["choice"].(string); eligible[choice] {
				counts[choice]++
			}
		}
		best, bestN := "", 0
		for opt, n := range counts {
			if n > bestN {
				best, bestN = opt, n
			}
		}
		return best, bestN
	}

	if opt, n := leader(); n >= need {
		return opt, true
	}
	sw, ok := e.store.(entity.SubtreeWatcher)
	if !ok {
		return "", false
	}
	ch, unsub, err := sw.WatchSubtree("vote", did)
	if err != nil {
		return "", false
	}
	defer unsub()
	bg := e.bgctx
	if bg == nil {
		bg = context.Background()
	}

	// Optional deadline arm (the who-axis analog of an `until` gate's deadline): a duration
	// resolved against the system clock, stamped on the channel so it is restart-safe.
	var deadlineC <-chan time.Time
	if dur, err := time.ParseDuration(within); within != "" && err == nil {
		at := e.deadlineAt(channelRef, dur)
		timer := time.NewTimer(at.Sub(e.now())) // fires at the deadline (immediately if past)
		defer timer.Stop()
		deadlineC = timer.C
	}

	for {
		select {
		case <-bg.Done():
			return "", false
		case <-deadlineC: // the clock struck before any option reached the threshold — no pick
			return "", false
		case _, ok := <-ch:
			if !ok {
				return "", false
			}
			if opt, n := leader(); n >= need {
				return opt, true
			}
		}
	}
}

// Spawn asks the Spawner to launch a process from a launch.Spec — non-blocking.
// The Spawner owns build/mirror/run; the process's state appears in the verse and
// its acts write entities, closing the loop. Requires a Spawner.
func (e *Engine) Spawn(id string, spec launch.Spec) process.ProcHandle {
	if e.spawn == nil {
		e.tracef("no spawner configured; cannot spawn %q", id)
		return nil
	}
	e.tracef("spawn process %s (driver=%s): %s", id, spec.Driver, spec.Task)
	h := e.spawn(id, spec)
	if h != nil && spec.Subject != "" { // record the process under its flow
		fp := flowProc{id: id, h: h}
		e.flowsMu.Lock()
		e.flows[spec.Subject] = append(e.flows[spec.Subject], fp)
		e.flowsMu.Unlock()
		go e.reapOnDone(spec.Subject, fp) // retire it from flow tracking when it ends
	}
	return h
}

// Adopt registers an already-running process (id + governing handle) under a flow — the
// resume path on restart, when a remote-hosted process outlived verse. It re-tracks it
// exactly as Spawn does a fresh one, so the resumed process is steerable (flow
// cancel/say/pause) and reaped from the flow when it ends — not merely observed. subject
// "" is a no-op.
func (e *Engine) Adopt(subject, id string, h process.ProcHandle) {
	if h == nil || subject == "" {
		return
	}
	fp := flowProc{id: id, h: h}
	e.flowsMu.Lock()
	e.flows[subject] = append(e.flows[subject], fp)
	e.flowsMu.Unlock()
	go e.reapOnDone(subject, fp)
}

// reapOnDone retires a process from its flow's tracking once it reaches a terminal
// status — the lifecycle-driven reaper for scoped (subtree) mode, where no root dispatch
// loop runs so the store-driven reap never fires. Reaping is purely process bookkeeping,
// so it hangs off the process's own completion (its handle's bus). NOTE: it can race a
// fast process that finishes before this subscribes (Subscribe is live-from-now, no ring
// replay) — in baseline mode reap (store-driven, via the root watch) is the race-free
// path; this is the backstop and the scoped-mode path. The range also exits if the
// process closes its bus, so this cannot outlive p.
func (e *Engine) reapOnDone(subject string, fp flowProc) {
	ch, unsub := fp.h.Subscribe()
	defer unsub()
	for ev := range ch {
		if ev.Kind == process.EvStatus && mirror.Terminal(ev.Text) {
			break
		}
	}
	e.flowsMu.Lock()
	defer e.flowsMu.Unlock()
	ps := e.flows[subject]
	kept := make([]flowProc, 0, len(ps))
	for _, q := range ps {
		if q.h != fp.h {
			kept = append(kept, q)
		}
	}
	if len(kept) == 0 {
		delete(e.flows, subject)
	} else {
		e.flows[subject] = kept
	}
}

// Flows lists the subjects with in-flight processes. Terminal processes are reaped
// (see reap), so a subject drops off once its work finishes; the handles here are
// this engine's live processes (control targets), not a durable membership view.
func (e *Engine) Flows() []string {
	e.flowsMu.Lock()
	defer e.flowsMu.Unlock()
	out := make([]string, 0, len(e.flows))
	for s := range e.flows {
		out = append(out, s)
	}
	return out
}

// CancelFlow cancels every process of a flow (out-of-band control, honored at each
// process's next boundary — never a store write). Returns how many were signalled.
func (e *Engine) CancelFlow(subject string) int {
	e.flowsMu.Lock()
	ps := e.flows[subject]
	e.flowsMu.Unlock()
	for _, fp := range ps {
		fp.h.Cancel("flow cancel: " + subject)
	}
	return len(ps)
}

// SteerFlow says an instruction to every process of a flow, attributed to by. Returns
// how many were said-to.
func (e *Engine) SteerFlow(subject, text, by string) int {
	e.flowsMu.Lock()
	ps := e.flows[subject]
	e.flowsMu.Unlock()
	for _, fp := range ps {
		fp.h.Steer(text, by)
	}
	return len(ps)
}

// PauseFlow holds every process of a flow at its next boundary — an operator's
// ad-hoc freeze of one subject's live work (distinct from cancel, which is
// destructive, and from a park gate, which is a charter-declared hold). Out-of-band
// control, honored at the boundary, never a store write. Lift it with ResumeFlow.
// Returns how many were signalled. (This is per-flow; it does not pause the engine's
// dispatch loop — other flows keep reacting.)
func (e *Engine) PauseFlow(subject string) int {
	e.flowsMu.Lock()
	ps := e.flows[subject]
	e.flowsMu.Unlock()
	for _, fp := range ps {
		fp.h.Pause()
	}
	return len(ps)
}

// ResumeFlow lifts a PauseFlow, releasing each process to run from its held
// boundary. Returns how many were signalled.
func (e *Engine) ResumeFlow(subject string) int {
	e.flowsMu.Lock()
	ps := e.flows[subject]
	e.flowsMu.Unlock()
	for _, fp := range ps {
		fp.h.Resume()
	}
	return len(ps)
}

// reap retires a process from its flow when its reflected entity reaches a terminal
// status, so Flows()/CancelFlow/… see only in-flight work — no lingering completed
// handles. It is store-driven: the same process:instance change a completion trigger
// reacts to also releases the flow handle. A subject with no live processes is
// dropped entirely.
func (e *Engine) reap(ev entity.Event) {
	if ev.Ref.System != mirror.System {
		return
	}
	status, _ := ev.To["status"].(string)
	if !mirror.Terminal(status) {
		return
	}
	e.flowsMu.Lock()
	defer e.flowsMu.Unlock()
	for subj, ps := range e.flows {
		// Build a fresh slice — never filter ps in place. CancelFlow/SteerFlow/… copy
		// only the slice header under the lock and then iterate it unlocked, so an
		// in-place compaction here would corrupt the array they are reading.
		kept := make([]flowProc, 0, len(ps))
		for _, fp := range ps {
			if fp.id != ev.Ref.ID {
				kept = append(kept, fp)
			}
		}
		if len(kept) == 0 {
			delete(e.flows, subj)
		} else {
			e.flows[subj] = kept
		}
	}
}

// ReconcileOrphans retires process:instance entities that a previous run left
// non-terminal: a fresh runtime holds no handle for them (processd is in-memory, so
// a restart kills every process), so their last durable state is marked orphaned
// rather than left claiming to run. Returns how many were reconciled; a no-op on a
// store that cannot be listed (or an empty one).
//
// Call it once at monolith startup, before Start. Do NOT call it in a split
// deployment where the process runtime outlives the engine — there the processes are
// real and reconnectable, not orphans.
func (e *Engine) ReconcileOrphans() int {
	l, ok := e.store.(entity.Lister)
	if !ok {
		return 0
	}
	n := 0
	for _, ent := range l.List(func(r entity.Ref) bool { return r.System == mirror.System }) {
		status, _ := ent.Payload["status"].(string)
		if status == "" || mirror.Terminal(status) {
			continue
		}
		p := clonePayload(ent.Payload)
		p["status"] = mirror.StatusOrphaned
		e.store.Put(ent.Ref, p)
		n++
	}
	return n
}

func (e *Engine) tracef(format string, a ...any) {
	fmt.Fprintf(e.log, format+"\n", a...)
}

func proceedWord(ok bool) string {
	if ok {
		return "proceed"
	}
	return "deny"
}
