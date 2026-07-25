package trigger

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
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

	// the charter (e.triggers, declared above). Guarded because triggers can be
	// added/removed at runtime (over the wire) while the loop dispatches.
	trigMu sync.Mutex

	// goals: named goal aggregates, each owning a bundle of triggers + a done/fail
	// lifecycle monitor (see goal.go). Guarded independently of the charter.
	goalsMu sync.Mutex
	goals   map[string]*Goal

	// Per-trigger (subtree) observation mode. A scoped engine (a verse connect
	// federation) opens NO root watch; instead each trigger added gets a watch on exactly
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
// NO root watch. Each trigger added afterward gets its own watch on exactly the
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
	trigs := make([]Trigger, len(e.triggers))
	copy(trigs, e.triggers)
	e.trigMu.Unlock()
	for _, t := range trigs {
		if err := e.watchTriggerSubtree(t); err != nil {
			e.tracef("subtree watch for %q: %v", t.Name, err)
		}
	}
}

// watchTriggerSubtree opens a subtree watch for a subtree-mode trigger and fires it on
// each matching change. The store must support SubtreeWatcher (the logd store does).
func (e *Engine) watchTriggerSubtree(t Trigger) error {
	if len(t.Matches) != 1 || t.Matches[0].System == "" {
		return fmt.Errorf("subtree trigger %q needs exactly one match with a system (kind optional)", t.Name)
	}
	sw, ok := e.store.(entity.SubtreeWatcher)
	if !ok {
		return fmt.Errorf("scoped trigger %q: store has no subtree watch", t.Name)
	}
	m := t.Matches[0]
	ch, unsub, err := sw.WatchSubtree(m.System, m.Kind)
	if err != nil {
		return fmt.Errorf("scoped trigger %q: watch %s:%s: %w", t.Name, m.System, m.Kind, err)
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
		m, _ := gate.mode() // validated at AddTrigger
		if !m.Blocks() {    // auto: passes without a resolver
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
			ID: id, Origin: t.Name, Mode: m, Gate: gname,
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
	t.Action.apply(t.Name, ev, e)
}

// monitorGate blocks until a trigger gate's temporal formula resolves, monitored on the
// store's watch stream, then latches the verdict via a CAS predicated on the witness
// (revocable-until-commit + terminal-once). It returns whether the action should proceed
// (false = ctx ended, or the gate committed a deny). If a revoke lands between
// satisfaction and the commit, the CAS fails and it re-monitors.
func (e *Engine) monitorGate(ctx context.Context, t Trigger, g Gate, gname string, ev entity.Event) bool {
	channelRef := entity.Ref{System: "decision", Kind: "channel", ID: decisionChannelID(t.Name + "/" + gname + "/" + ev.Ref.String())}
	// Compile the gate's Formula into a decision over {proceed, deny}, bound to THIS
	// channel's vote log, and open a discoverable channel entity voters can find/observe.
	d := e.compileFormula(g.Formula, channelRef, t, gname, ev)
	e.openChannel(d)
	for {
		winner, witness, ok := e.awaitDecision(ctx, d)
		if !ok {
			e.tracef("temporal gate %s/%s: monitor ended before commit", t.Name, gname)
			return false
		}
		// The winning option's formula is the revoke guard (nil for a deadline-default deny).
		eff, revoked := e.commitDecision(channelRef, d.formulaOf(winner), winner, witness)
		if revoked {
			e.tracef("temporal gate %s/%s: revoked before commit → re-monitoring", t.Name, gname)
			continue
		}
		e.tracef("temporal gate %s/%s committed %q (witness %d facts)", t.Name, gname, eff, len(witness))
		return eff == "proceed"
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
// commitDecision: it returns the EFFECTIVE stamped value, so a re-fire — or
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
	system, kind := fs.System(), fs.Kind()
	if system == "" || kind == "" {
		return nil, false
	}
	choice, _ := fs.Fields()["choice"].(string)
	sub := map[string]any{}
	for _, w := range witness {
		if w.System != system || w.Kind != kind {
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
	return []entity.Precondition{{Ref: entity.Ref{System: system, Kind: kind}, Pattern: sub}}, true
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
// reifies a `select` decision — a decision over the eligible doers as options — resolves it
// through the SAME machinery as a whether-gate (openChannel/awaitDecision/commitDecision),
// and returns the chosen driver (bound into Spec.Driver before spawn — no live swap). The
// commit is CAS terminal-once with the revoke guard, so a ballot retracted between threshold
// and commit re-monitors; a re-fire is idempotent (same channel). Orthogonal to the
// controlPolicy that then transports the chosen driver.
func (e *Engine) selectProvider(p *launch.Provider, subject entity.Ref) (string, bool) {
	options, drivers := e.eligibleProviders(p)
	if len(options) == 0 {
		return "", false
	}
	channelRef := entity.Ref{System: "decision", Kind: "channel", ID: decisionChannelID(providerSpecSeed(subject, p))}
	d := e.compileProvider(p, subject, channelRef, options)
	e.openChannel(d)

	// auto: a rule picks the first eligible doer immediately — no channel await, no votes.
	if p.Select == "" || p.Select == "auto" {
		eff, _ := e.commitDecision(channelRef, nil, options[0], nil)
		return driverFor(drivers, eff), true
	}

	// vote/ask: resolve over the vote log, bounded by the engine lifetime, with the revoke
	// guard active — a ballot retracted between threshold and commit re-monitors.
	ctx := e.bgctx
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		winner, witness, ok := e.awaitDecision(ctx, d)
		if !ok {
			e.tracef("select %s: %s ended before a decision", channelRef.ID, p.Select)
			return "", false
		}
		if winner == "" { // no pick (deadline default) — record it, do not spawn
			e.commitDecision(channelRef, nil, "no-pick", nil)
			return "", false
		}
		eff, revoked := e.commitDecision(channelRef, d.formulaOf(winner), winner, witness)
		if revoked {
			e.tracef("select %s: revoked before commit → re-monitoring", channelRef.ID)
			continue
		}
		return driverFor(drivers, eff), true
	}
}

// driverFor maps a selected option to its driver; a prior selection no longer in the
// registry falls back to the option id itself as the driver.
func driverFor(drivers map[string]string, selected string) string {
	if dr, ok := drivers[selected]; ok {
		return dr
	}
	return selected
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
		if ev.Kind == process.EvStatus && process.TerminalStatus(ev.Text) {
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
	if !process.TerminalStatus(status) {
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
		if status == "" || process.TerminalStatus(status) {
			continue
		}
		p := clonePayload(ent.Payload)
		p["status"] = string(process.Orphaned)
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
