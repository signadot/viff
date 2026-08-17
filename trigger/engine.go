package trigger

//go:generate go tool tony-codegen -dir .

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// Spawner turns a (bound) launch.Spec into a running, hosted task and returns
// it. It owns the full lifecycle — resolve the spec's references (driver, runner,
// resolver, scope, environment), build, mirror into the verse, and run/host — so
// the engine stays agnostic to how tasks are hosted (a taskd runtime here,
// an RPC to a remote runtime when distributed).
type Spawner func(id string, spec launch.Spec) task.Handle

// flowTask is one LIVE task: its id (for store-driven reap by ref), the subject it is
// about, and its governing handle (the control target — a *Task locally, a remote handle
// behind a runner URL later). The engine holds handles, not concrete tasks.
type flowTask struct {
	id      string
	subject string // the resolved subject this task is about — a flow is a query over these
	h       task.Handle
	// hooks is the spawning action, carried so the engine can fire its completion hook (onX)
	// when THIS task reaches a terminal status — scoped to this task, not every task
	// about the subject. nil for Adopt (a resumed task has no charter action to hook).
	hooks *Action
	// chain is this spawn's position in its trigger's action tree, as a KPATH: the trigger
	// name at the top, then review.onDone, review.onDone.onFailed, … So it is not a private
	// naming scheme but the address of the spawning Action inside the charter — which is
	// what makes a hook's spawn id distinct and deterministic, and what a reader (or, later,
	// a resume) can navigate back to the action that owns it.
	chain string
}

// Engine runs the ECA loop over an entity-verse.
//
// ONE BASELINE ENGINE PER STORE. Everything the engine knows about what it has already done is
// per-engine and in memory — the per-trigger watermark (seen), the in-flight firings (inflight),
// the live task registry, the fire budget — and every one of them reads a write this engine
// did not make as somebody else's business. That is right for a scope and wrong for a peer:
//
//   - A SCOPED engine composes, structurally. Store.Scope's contract is that writes land in the
//     overlay while reads see the baseline layered with the scope, so the causal edge runs one
//     way only: baseline writes are visible to the scope, scope writes are invisible to the
//     baseline. There is nothing for the two to disagree about.
//   - TWO BASELINE engines on one store do not compose. Each governs its own firings and neither
//     can see the other's, so A's write is external to B and B's to A: an A→B→A loop is bounded
//     by neither budget, and a trigger installed on both is evaluated — and fired — twice per
//     commit, once against each watermark.
//
// Nothing in the tree runs two peers on one baseline today. This is the constraint that breaks
// if something starts to, and the causal-depth throttle designed in
// docs/working/cycle-throttle.md inherits it exactly: a chain that runs between two peer engines
// is throttled by neither, because each sees only its own half of it.
type Engine struct {
	store    entity.Store
	resolver task.Resolver // answers trigger ASK/PARK gates
	spawn    Spawner       // optional: how actions spawn tasks
	triggers []Trigger
	// logger carries both the running dispatch commentary (Debug) and anything an
	// operator has to act on (Warn). One channel with levels, rather than a verbose
	// io.Writer that defaulted to io.Discard and that nothing ever wired up — which meant
	// a charter that could not do what it said reported itself into a void. slog.Default
	// puts Warn on stderr, alongside the logd/docd records already landing there.
	logger *slog.Logger
	clock  func() time.Time // the system clock (injectable for tests); nil = time.Now

	mu    sync.Mutex
	fired int // entities acted on, for the trace and for tests; see countFire

	// seen is the highest commit each trigger has been evaluated at — the watermark that
	// makes one verdict per (trigger, commit). See firstSight.
	seenMu sync.Mutex
	seen   map[string]int64

	// causes places the commits this engine wrote in their causal chains: revision → how far
	// it stands from the external write that started it, and what wrote it. A commit that is
	// not here was caused by somebody else, which is the answer for a person, a task, a
	// mirror, a peer, and a restart alike. Written by a firing's store (see causal.go), read
	// per commit on the dispatch path — so it is guarded on its own rather than behind the
	// loop, which firings do not run on.
	// confine is the subtree this engine's charter may write in — zero for a baseline engine,
	// which may write anywhere. See confine.go.
	confine Subtree
	// imports is what this engine's charter declares it READS outside its confinement. Empty on
	// a package means it reads only inside its own address; meaningless on baseline, which is
	// unconfined. See imports.go.
	imports []Subtree
	chainMu sync.Mutex
	causes  map[int64]link
	trips   int // firings refused for running past the depth bound (see throttled)
	// reported holds the chain roots already named in a report, so a runaway that keeps being
	// re-ignited from the same origin is described once rather than at the rate the store
	// accepts writes. Keyed by root and not by trigger: it is the CHAIN that has been
	// explained, and the same pair of rules looping from a different origin is news again.
	reported map[int64]bool
	// tripPace is D: how long a tripped firing waits before proceeding. 0 refuses it outright.
	// Set once at startup (SetTripPace) and read on the firing path.
	tripPace time.Duration

	// pacing holds the subjects with a tripped firing waiting out D, so a write to one of them
	// can tell it the chain broke. It holds only what is WAITING — entries are removed as each
	// firing gives up or proceeds — so it does not grow with the verse.
	pacingMu sync.Mutex
	pacing   map[entity.Ref][]chan struct{}

	// inflight holds the (trigger, subject) firings currently under way. A firing takes time
	// — a gate is answered by a person, a spawn runs — and every commit meanwhile re-evaluates
	// the trigger, which selects the same subject because the write that would take it out of
	// the selection is exactly what has not happened yet. Without this, a firing waiting on a
	// gate is joined by another at every commit that touches its subtree.
	//
	// It is the same identity as everything else a firing owns — its spawn id, its gate
	// question, its motion channel are all functions of (trigger, subject) — which is what
	// makes "a second firing about a subject already firing" the SAME firing rather than a new
	// one to be deduplicated afterwards.
	flightMu sync.Mutex
	inflight map[string]bool

	// Control plane — steering, serviced with strict priority over observation
	// (see docs/deployment-and-steering.md). Closures run on the loop goroutine, so
	// they serialize with dispatch and never race it; `paused`/`cursor` are
	// loop-goroutine-only.
	control chan func()
	stopped chan struct{} // closed when the loop goroutine exits (so do() can't hang)
	paused  bool
	cursor  int64 // highest processed store revision (the catch-up cursor)

	// Live tasks, keyed by task id. A FLOW IS NOT STORED HERE: it is a QUERY over
	// these tasks' subjects (see matchFlow), so "the tasks of demo:pr:1" and "…of
	// every demo:pr" are the same operation at two settings — relational steering falls out
	// of the selector rather than needing a second index. Keying by id also makes reap a
	// delete and spawn-dedupe a lookup. Guarded independently of the loop.
	liveMu sync.Mutex
	live   map[string]flowTask

	// the charter (e.triggers, declared above). Guarded because triggers can be
	// added/removed at runtime (over the wire) while the loop dispatches.
	trigMu sync.Mutex

	// strata is the declared level ordering — few, changing rarely, where rules are many and
	// change often. Guarded by trigMu with the charter it orders, because every level check
	// reads both and a check split across two locks would be answering about two different
	// charters. See stratum.go.
	strata []Stratum
	// pending holds rules whose `stratum:` names one that is not installed yet: written down,
	// not firing, waiting. A rule and a stratum are separate entities and either can be
	// installed first, so refusing here would make the ORDER of two independent writes matter
	// — which is the property one-entity-per-rule exists to avoid. Promoted by addStratum.
	pending []Trigger

	// Per-trigger (subtree) observation mode. A scoped engine (a verse connect
	// federation) opens NO root watch; instead each trigger added gets a watch on exactly
	// the (system,kind) subtree it matches. bgctx bounds those watchers.
	subtree bool
	bgctx   context.Context
	// scope is the federation this engine acts in — "" for baseline. It is written onto
	// every rule this engine persists, and read back to tell the scope's OWN charter from
	// baseline's, which a scoped store reads through and cannot otherwise distinguish. See
	// SetScope.
	scope string
	// watchers holds, per trigger name, the cancel that stops THAT trigger's subtree
	// watchers. In subtree mode a trigger's observation is its own goroutines, so the
	// charter alone no longer says what fires: removing a trigger has to stop them, or a
	// removed trigger keeps firing off a watch nobody can see. Guarded by trigMu, with
	// the charter it mirrors.
	watchers map[string]func()

	// shapes fingerprints each installed sequence by its carrier's chain, so a running
	// sequence can tell that the charter moved under it. Recorded at install (see
	// recordShapes) because hashing an action mutates its nodes.
	shapeMu sync.Mutex
	shapes  map[string]string
}

// New builds an engine over store, using resolver for trigger gates.
func New(store entity.Store, resolver task.Resolver) *Engine {
	return &Engine{
		store: store, resolver: resolver, logger: slog.Default(),
		control:  make(chan func(), 64),
		stopped:  make(chan struct{}),
		live:     map[string]flowTask{},
		causes:   map[int64]link{},
		tripPace: DefaultTripPace,
		shapes:   map[string]string{},
		watchers: map[string]func(){},
	}
}

func (e *Engine) SetSpawner(s Spawner) { e.spawn = s }

// SetScope names the federation this engine acts in — a `verse connect` scope, or "" for
// baseline. Set before anything is installed, because it is stamped on what this engine
// persists.
//
// It exists because a scoped store READS BASELINE THROUGH: listing `trigger:rule:*` in a
// scope returns baseline's rules and the scope's own, with nothing on the record to tell
// them apart, so a restore either resurrects nothing or installs the whole baseline charter
// into one dev's federation. Neither is what `verse charter add --scope me` promised. The
// scope's name on the record answers it exactly, at the one moment the answer is known: the
// install itself.
//
// A record carrying `scope: me` also answers the same question for a reader — `verse state
// get trigger:rule:x --scope me` says which federation the rule belongs to rather than
// leaving it to be inferred from which listing it turned up in.
func (e *Engine) SetScope(name string) { e.scope = name }

// Scope is the federation this engine acts in, "" for baseline.
func (e *Engine) Scope() string { return e.scope }

// SetLogger redirects engine logging. Debug carries dispatch detail (off by default),
// Warn carries misconfiguration an operator must act on.
func (e *Engine) SetLogger(l *slog.Logger) { e.logger = l }
func (e *Engine) Store() entity.Store      { return e.store }

// Fired reports how many entities triggers have acted on — the trace counter.
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
	// Watch the task:instance mirror subtree directly, independent of any charter trigger:
	// this drives store-driven reaping AND completion-hook (onX) application in scoped mode,
	// where no root dispatch loop runs. Store-driven means race-free for a fast task (unlike
	// the reapOnDone bus); reapOnDone remains the backstop, and the atomic retire dedupes.
	if ch, unsub, err := e.store.WatchSubtree(mirror.System, "instance"); err == nil {
		go e.reapLoop(ctx, ch, unsub)
	} else {
		e.tracef("mirror subtree watch: %v", err)
	}
}

// watchTriggerSubtree opens a subtree watch per (system,kind) the trigger's condition READS —
// its When, or its Subject when there is no When — and fires it on each matching change. Asking
// the formula which subtrees it reads — the same question a Monitor asks — means a When over
// several subtrees is watched correctly for free, and there is no "exactly one match"
// restriction to enforce.
//
// The watchers run under a context of this trigger's own, derived from bgctx, and its
// cancel is registered under the trigger's name — so RemoveTrigger can stop exactly these
// goroutines while every other trigger keeps observing. A partial failure (the second of
// three subtrees refuses to watch) cancels the ones already opened rather than leaving a
// half-installed trigger firing.
func (e *Engine) watchTriggerSubtree(t Trigger) error {
	// Everything the trigger READS, not just what dispatches it: a verdict is a function of
	// its cause, its condition and its subject together, so a change to any of them can change
	// it. This is the same list the commit skip is asked about (Trigger.reads), which is what
	// keeps "what it watches" and "what could change its answer" from drifting apart.
	subtrees := t.reads()
	if len(subtrees) == 0 {
		return fmt.Errorf("subtree trigger %q: it reads no subtree to watch", t.Name)
	}
	for _, st := range subtrees {
		if st.Named() == 0 { // a root alone is the whole of a half, which is not watchable
			return fmt.Errorf("subtree trigger %q needs a system in its `on`, its `when` or its "+
				"`subject` (kind optional); a scoped engine opens no root watch, so there is "+
				"nothing to watch without one", t.Name)
		}
	}
	ctx, cancel := context.WithCancel(e.bgctx)
	for _, st := range subtrees {
		ch, unsub, err := e.store.WatchSubtree(st.Path()...)
		if err != nil {
			cancel() // stop this trigger's already-opened watchers; none of it is installed
			return fmt.Errorf("scoped trigger %q: watch %s: %w", t.Name, strings.Join(st.Path(), ":"), err)
		}
		go func() {
			defer unsub()
			for {
				select {
				case <-ctx.Done():
					return
				case c, ok := <-ch:
					if !ok {
						return
					}
					// A cancel and a pending commit are both ready: select picks either, so
					// removal has to be checked, not merely selected on, or a removed
					// trigger can still fire once on a commit that raced it.
					if ctx.Err() != nil {
						return
					}
					// Fire on bgctx, not this trigger's ctx: a firing already under way — a
					// posed gate, a spawned task — belongs to the work, not to the charter
					// entry, and removing the trigger stops FURTHER firings rather than
					// cancelling one in flight.
					//
					// No skip check: this watch delivers only the subtrees the trigger reads,
					// so arriving here IS the skip's answer. The per-commit watermark inside
					// evaluate is what keeps several watched subtrees from evaluating one
					// commit several times.
					e.evaluate(e.bgctx, t, c)
				}
			}
		}()
	}
	e.registerWatchers(t.Name, cancel)
	return nil
}

// registerWatchers records the cancel that stops a trigger's subtree watchers. It cancels
// any cancel it replaces — a re-installed name must not leave its predecessor's watchers
// running — and, if the trigger is no longer in the charter, cancels the one just handed to
// it: opening the watches is not instantaneous, so a RemoveTrigger can land in the middle
// of it, and the removal has to win rather than lose to a late registration.
func (e *Engine) registerWatchers(name string, cancel func()) {
	e.trigMu.Lock()
	old := e.watchers[name]
	installed := false
	for _, t := range e.triggers {
		if t.Name == name {
			installed = true
			break
		}
	}
	if installed {
		e.watchers[name] = cancel
	} else {
		delete(e.watchers, name)
	}
	e.trigMu.Unlock()
	if old != nil {
		old()
	}
	if !installed {
		cancel()
	}
}

// stopWatchers stops a trigger's subtree watchers, if it had any (nothing in baseline
// mode). Called with trigMu HELD: the cancel is non-blocking — the watcher goroutines
// notice and unsubscribe on their own — so this cannot deadlock against the charter lock.
func (e *Engine) stopWatchers(name string) {
	if cancel := e.watchers[name]; cancel != nil {
		delete(e.watchers, name)
		cancel()
	}
}

func (e *Engine) loop(ctx context.Context, ch <-chan entity.Commit, unsub func()) {
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
		case c, ok := <-ch:
			if !ok {
				return
			}
			e.consume(ctx, c)
		}
	}
}

// consume treats a live commit as a low-latency doorbell, not the source of truth:
// if it is anywhere ahead of the cursor, reconcile the whole gap from the store via
// catchUp (Since). This keeps the engine correct without trusting live contiguity
// or assuming the backend's revisions are dense — logd commits are monotonic but
// sparse. All the cursor ever asks is "greater than".
func (e *Engine) consume(ctx context.Context, c entity.Commit) {
	if c.Rev > e.cursor {
		e.catchUp(ctx)
	}
}

// catchUp replays commits since the cursor from the store (a revision query), in
// order — the cursor-based catch-up that makes a hold cost a cursor, not a buffer.
// If the cursor is older than the store's retained window, it resyncs to current.
func (e *Engine) catchUp(ctx context.Context) {
	commits, ok := e.store.Since(e.cursor)
	if !ok {
		e.tracef("cursor %d older than store window; resync to current", e.cursor)
		e.cursor = e.store.Rev()
		return
	}
	for _, c := range commits {
		if c.Rev <= e.cursor {
			continue // Since is ordered and monotonic; skip anything not ahead
		}
		e.cursor = c.Rev
		e.dispatch(ctx, c)
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

// dispatch evaluates one COMMIT against every trigger. Firing is off the observe goroutine so
// a blocking gate (a human) never stalls the loop.
//
// The commit is the unit throughout: a trigger's rule is a question about the store at a
// revision, not about one entity, so every trigger is asked once and the commit's changes are
// only consulted to decide whether asking is worth it (Trigger.touches). Reaping still walks
// them, because retiring a finished task IS per-entity bookkeeping.
func (e *Engine) dispatch(ctx context.Context, c entity.Commit) {
	e.trigMu.Lock()
	trigs := make([]Trigger, len(e.triggers))
	copy(trigs, e.triggers)
	e.trigMu.Unlock()
	for _, ev := range c.Changes {
		e.reap(ev)             // retire completed tasks from their flow (store-driven bookkeeping)
		e.subjectMoved(ev.Ref) // a paced firing about this entity is now stale; let it go
	}
	for i := range trigs {
		if !trigs[i].touches(c) {
			continue // nothing it reads changed; the verdict cannot have (see touches)
		}
		e.evaluate(ctx, trigs[i], c)
	}
}

// countFire records that one entity was acted on. It is a counter and nothing more: what
// bounds a runaway is the cycle throttle (causal.go), which recognizes the loop rather than
// running out of a number.
//
// There WAS a budget here — maxFires, engine-wide, cumulative, never reset — and it was the
// only thing standing between a rule soup and a store filling up. It is gone, for two reasons
// beyond redundancy. It bounded an engine's LIFETIME rather than any loop, so one looping rule
// exhausted the allowance for every other rule and the engine then stopped firing everything,
// permanently. And a paced cycle is now a legitimate thing to write — a control loop through a
// gate or a ticker — which burns such a budget every lap, so any finite setting was a timer on
// the charter's death rather than a safety limit.
//
// What it also bounded, and nothing does now, is fan-out WIDTH: one firing over 200 members is
// 200 actions, and depth correctly sees that as width. If that wants bounding it belongs per
// firing, refused loudly, because width is a property of a firing and not of an engine's
// lifetime. Nothing in the tree ever set the budget (it defaulted to unbounded and no server
// configured it), so removing it changes no deployed behaviour.
func (e *Engine) countFire() {
	e.mu.Lock()
	e.fired++
	e.mu.Unlock()
}

// fire names what the firing is about, resolves the trigger's gates (conjunctively), and
// runs its action if they all proceed. All blocking gates are posed *concurrently* — so a
// trigger's gates appear together and approvers can act in parallel — then fire waits for
// every one; any denial stops the firing.
//
// The subject is resolved BEFORE any gate, because it is named at the instant When fires:
// a long approval then cannot slide it (and, under fan-out, cannot change the member set
// between asking and acting), and a firing that resolves to nothing never troubles an
// approver. Gates are per FIRING, not per subject — a broadcast raises one approval.
// spawnLabel names the task a firing WILL start if its gates pass. spawnID is a pure
// function of the trigger name and the subject, both settled before the gate is raised, so
// this is the very id the spawn will use — not a guess and not a second scheme. apply() calls
// the same function, and a test pins the two together so they cannot drift apart.
//
// Empty when the action has no spawn: a set-only trigger starts no task, and inventing an
// id for one would be worse than saying nothing. Note the id is what the task WILL be
// called — the gate may be denied, or a provider selection may fail to resolve a doer, and
// then nothing by that name is ever created.
func spawnLabel(t Trigger, subjects []entity.Ref) string {
	if t.Action.Spawn == nil || len(subjects) == 0 {
		return ""
	}
	return spawnID(t.Name, subjects[0])
}

// fireSubjects starts one firing per selected member, settling first what each one's causal
// chain is and whether it may run at all (see causeFor and throttled — the throttle is asked
// per SUBJECT, because a chain is per subject).
func (e *Engine) fireSubjects(ctx context.Context, t Trigger, c entity.Commit, subjects []entity.Ref) {
	for _, subj := range subjects {
		parent := e.causeFor(t, c, subj)
		// The causing commit rides ALONGSIDE the causal parent rather than replacing it: parent
		// is zeroed when this firing begins a fresh chain, and c is the commit that caused it
		// either way. See occasion.
		oc := occasion{commit: c, parent: parent, snap: &snapCache{}}
		if e.throttled(t, subj, parent) {
			if e.tripPace <= 0 {
				continue // no pace configured: refuse outright, and the chain ends here
			}
			// Pace it: hold the firing for D and then let it through, so a tripped chain
			// runs at a dull steady rate rather than at the speed of the store. The wait is
			// in this goroutine, not on the dispatch path — see pauseFor.
			go func(subj entity.Ref, oc occasion) {
				if !e.pauseFor(ctx, subj, e.tripPace) {
					return
				}
				e.fireOne(ctx, t, oc, subj)
			}(subj, oc)
			continue
		}
		go e.fireOne(ctx, t, oc, subj)
	}
}

// fireOne is one firing: hold the subject, resolve the gates conjunctively, and act if they
// all proceed.
//
// The subject is settled before any gate — selected from the snapshot the rule was evaluated
// against — which is what lets a gate say what it is deciding about, and stops a long approval
// sliding the answer underneath itself.
func (e *Engine) fireOne(ctx context.Context, t Trigger, oc occasion, subj entity.Ref) {
	// One firing at a time per subject, for as long as the WORK lasts — which is two things,
	// because a firing can outlive its own function call:
	//
	//   claimFlight  covers the firing itself: the gates being answered, the action applied.
	//   working      covers what it started: a spawned task still running.
	//
	// Every commit meanwhile re-selects the subject, since the write that would release it is
	// exactly what has not happened yet. Neither check changes what RUNS — spawnHooked already
	// refuses a second task under a live id — but together they stop a firing for work
	// already under way from posing a question and spending budget before being told so.
	if e.working(t, subj) {
		e.tracef("%s: %s already has work running; not firing again", t.Name, subj)
		return
	}
	if !e.claimFlight(t.Name, subj) {
		return
	}
	defer e.releaseFlight(t.Name, subj)

	subjects := []entity.Ref{subj} // the gate and motion identities are over the firing's set
	var wg sync.WaitGroup
	var mu sync.Mutex
	allProceed := true
	// The gates that actually REFUSED, which is not the same set as "the gates that did not
	// proceed": a resolver that errored, or a monitor that ended before its motion committed,
	// said nothing. Applying a refusal's consequence to those would write a rejection nobody
	// made.
	var denied []Gate

	// DESIGN BUG, unfixed: this fork splits one construct on a field that does not mean what
	// the split needs.
	//
	// One `gating.gates` list produces two surfaces. A formula gate becomes a MOTION
	// (motion:channel, `verse docket`, `verse vote`, settled by tally); everything else
	// becomes a QUESTION (gate:question, `verse gate`, `verse gate approve`, settled by an
	// answer). Different entity, different verb, different docket — and a resolver can own
	// only the second, so whether a rule can be supervised depends on which of the two an
	// author wrote under the same key.
	//
	// `Formula != nil` is not that distinction. Gate.mode() resolves a formula gate with no
	// explicit mode to "ask", so a thing literally called ASK goes down the motion arm. And
	// openChannel is unconditional, so `!until [!fact green, !deadline "4h"]` — which asks
	// nobody anything — opens a channel and lands on the docket with no ballot anyone can
	// cast. The result is a docket listing decisions nobody can make, and a `verse gate`
	// omitting decisions people are being asked for.
	//
	// The distinction the split needs is ASKS SOMEBODY versus WAITS FOR SOMETHING, and it is
	// a property of the formula's contents — a quorum asks, a fact and a clock do not — not
	// of whether a formula is present. Fixing it is the prerequisite for a resolver over a
	// motion: git-issue sp27m3g1h12krdgve1n0, docs/working/attached-resolvers-residual.md §1.
	for _, gate := range t.AllGates() {
		if gate.Formula != nil { // → a motion, not a posed question
			gname := gate.Name
			if gname == "" {
				gname = t.Name
			}
			wg.Add(1)
			go func(g Gate, gname string) {
				defer wg.Done()
				proceed, decided := e.monitorGate(ctx, t, g, gname, subjects)
				if proceed {
					return
				}
				mu.Lock()
				allProceed = false
				if decided { // committed a deny, rather than the monitor ending
					denied = append(denied, g)
				}
				mu.Unlock()
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
		d := task.GateQuestion{
			ID: gateID(t.Name, gname, subjects), Origin: t.Name, Mode: m, Gate: gname,
			Reason: fmt.Sprintf("trigger %s gate %s on %s", t.Name, gname, subj),
			// What the approver is deciding about, and what saying yes starts. One subject,
			// named exactly: the question is about this entity and no other.
			Subject: subj.String(),
			Spawns:  spawnLabel(t, subjects),
			// The named layer for a FIRING's gates, which a spawn has had all along and a
			// rule did not. Empty leaves the question to the standing layer, and then to
			// the docket a person reads.
			BoundTo: t.GateResolver(),
		}
		wg.Add(1)
		go func(g Gate, d task.GateQuestion, gname string) {
			defer wg.Done()
			res, err := e.resolver.Resolve(ctx, d, task.ToolUse{Name: "verse:trigger:" + t.Name})
			e.tracef("gate %s/%s on %s → %s by %s", t.Name, gname, subj, proceedWord(res.Proceed), res.By)
			if err == nil && res.Proceed {
				return
			}
			mu.Lock()
			allProceed = false
			if err == nil { // answered, and the answer was no
				denied = append(denied, g)
			}
			mu.Unlock()
		}(gate, d, gname)
	}
	wg.Wait()

	if !allProceed {
		// A no is a decision, and until this it was a decision with no consequence in the
		// store: nothing was written, so the subject stayed in the state that asked the
		// question, and the next commit to touch it for any reason re-fired the rule and
		// re-posed the same deterministic gate id over the answer somebody had given. The
		// latch a well-formed rule carries could not help — it lives in the action, and on
		// a deny the action never runs.
		//
		// So the charter says what a no means, and the engine writes nothing of its own
		// invention. A gate with no onDenied behaves exactly as it did (advise names it at
		// install), which is what keeps this additive.
		for _, g := range denied {
			if g.OnDenied == nil {
				continue
			}
			gname := g.Name
			if gname == "" {
				gname = t.Name
			}
			// Addressed as its place in the charter, like a completion hook's chain: the
			// id it writes or spawns under is where a reader can go and find the words.
			if err := g.OnDenied.apply(t.Name+"."+gname+".onDenied", oc, subjects, e, fromSubject); err != nil {
				e.tracef("gate %s/%s onDenied: %v", t.Name, gname, err)
			}
		}
		return // some gate denied — no budget consumed
	}
	// Counted only for a firing that proceeds, so a denied gate leaves no trace of a firing
	// that did not happen. One firing, one entity acted on.
	e.countFire()
	e.tracef("fire %s on %s", t.Name, subj)
	// ev.Rev is this firing's place in its causal chain, settled by causeFor before the firing
	// started — including whether a gate paces it. Nothing here needs to revisit that.
	//
	// A trigger firing has no privileged cause: the change that dispatched it is a doorbell,
	// so the action's references read the subject (see refSource).
	t.Action.apply(t.Name, oc, subjects, e, fromSubject)
}

// monitorGate blocks until a trigger gate's temporal formula resolves, monitored on the
// store's watch stream, then latches the verdict via a CAS predicated on the witness
// (revocable-until-commit + terminal-once). It returns whether the action should proceed
// It returns (proceed, decided): `decided` separates a committed refusal from a monitor that
// ended before its motion committed, because only the first is an answer — and only an answer
// carries an onDenied. If a revoke lands between satisfaction and the commit, the CAS fails and
// it re-monitors.
func (e *Engine) monitorGate(ctx context.Context, t Trigger, g Gate, gname string, subjects []entity.Ref) (bool, bool) {
	// The channel is keyed by WHAT THE FIRING IS ABOUT, not by the entity whose change
	// happened to dispatch it. Those differ whenever cause and subject come apart — a
	// completion advancing the PR it carried, a broadcast over a selected set — and keying on
	// the cause put two firings about the same subjects behind two separate votes while
	// putting one firing's several subjects behind one. Both are wrong in the same way: a
	// motion is a decision about the work, and the work is the subjects.
	//
	// Deterministic, like the gate id beside it, so a re-firing re-monitors the SAME channel
	// and re-uses the ballots already cast there instead of asking the room again.
	channelRef := entity.VerseRef(motionSystem, motionChannelKind,
		motionChannelID(gateID(t.Name, gname, subjects)))

	// Compile the gate's Formula into a motion over {proceed, deny}, bound to THIS
	// channel's vote log, and open a discoverable channel entity voters can find/observe.
	// It is displayed under the same subject the channel is keyed by, for the same reason.
	var subject entity.Ref
	if len(subjects) > 0 {
		subject = subjects[0]
	}
	m := e.compileFormula(g.Formula, channelRef, t, gname, subject)
	e.openChannel(m)
	for {
		winner, witness, ok := e.awaitMotion(ctx, m)
		if !ok {
			e.tracef("temporal gate %s/%s: monitor ended before commit", t.Name, gname)
			return false, false
		}
		// THE WORLD, alongside the ballots. The revoke guard below rechecks the facts the
		// verdict rests on and says nothing about the state that raised the question — so a
		// motion whose subject left the rule's selection while it stood open still reached
		// quorum, still committed, and still ran the action. That is not hypothetical: a merge
		// proposal was swept to `state: stale`, the gate carried on the ballot already cast,
		// and the branch was merged out from under the sweep that had retired it
		// (docs/working/merge-gate-residual.md).
		//
		// So a gate asks its question of a world, and the answer applies to that world. When
		// the rule would no longer select the subject, the question is moot: the motion is
		// withdrawn rather than committed, and this returns "not decided" — a moot question is
		// not a refusal, so no `onDenied` runs. Nothing is latched, so a rule that selects the
		// subject again reopens the same channel over the same ballots.
		if len(subjects) > 0 && !e.stillSelects(t, subject) {
			e.orphanMotion(channelRef, fmt.Sprintf(
				"%s no longer selects %s, so the question it asked is moot", t.Name, subject))
			e.tracef("temporal gate %s/%s: %s left the selection → withdrawn", t.Name, gname, subject)
			return false, false
		}
		// The winning option's formula is the revoke guard (nil for a deadline-default deny).
		eff, revoked := e.commitMotion(channelRef, m.guardFor(winner, witness), winner, witness)
		if revoked {
			e.tracef("temporal gate %s/%s: revoked before commit → re-monitoring", t.Name, gname)
			continue
		}
		e.tracef("temporal gate %s/%s committed %q (witness %d facts)", t.Name, gname, eff, len(witness))
		return eff == "proceed", true
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

// deadlineAt returns the absolute instant a motion closes on timeout, stamping it on the
// channel the first time (so `verse docket` shows when it closes, and a restart re-arms
// against the same absolute time — elapsed wall time is honored). It is terminal-once like
// commitMotion: it returns the EFFECTIVE stamped value, so a re-fire — or
// a concurrent arming of the same channel — reads the durable instant rather than sliding
// the deadline forward or arming a timer against a value that never landed.
func (e *Engine) deadlineAt(ref entity.Ref, dur time.Duration) time.Time {
	// Fast path: already stamped — honor it without a write. A failed read only costs
	// the fast path; the Modify below does its own read-modify-write under CAS.
	if cur, ok, _ := e.store.Get(ref); ok {
		if at, ok := parseDeadlineAt(cur.Payload); ok {
			return at
		}
	}
	want := e.now().Add(dur).Format(time.RFC3339)
	var eff string
	e.modify(ref, func(cur entity.Payload) entity.Payload {
		if s := entity.Str(cur, "deadline_at"); s != "" {
			eff = s // a concurrent arming won the CAS — honor its instant, not ours
			return cur
		}
		eff = want
		q := clonePayload(cur)
		q["deadline_at"] = want
		return q.Payload()
	})
	if at, err := time.Parse(time.RFC3339, eff); err == nil {
		return at
	}
	return e.now().Add(dur) // unparseable stamp (shouldn't happen) — arm a fresh timer
}

// parseDeadlineAt reads a channel payload's stamped RFC3339 deadline_at, if present.
func parseDeadlineAt(p entity.Payload) (time.Time, bool) {
	s := entity.Str(p, "deadline_at")
	if s == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, s)
	return at, err == nil
}

// commitMotion latches a gate's verdict onto the motion entity: terminal-once (the
// first commit wins; a later change cannot un-commit) and — for a proceed predicated on a
// quorum — revocable, so a witness ballot retracted before the commit aborts it. It returns
// the effective (possibly pre-existing) outcome and whether it was revoked. The witness is
// recorded on the entity for audit.
//
// When the recheck is a quorum, both guards ride one ATOMIC conditional write
// (terminal-once via the channel's status, revoke via the witness ballots) — no window
// between the check and the commit. A deny has no satisfy side to predicate on, so it
// falls back to a best-effort recheck then a terminal-once Modify, which leaves a small
// non-atomic window.
func (e *Engine) commitMotion(ref entity.Ref, recheck *Formula, outcome string, witness []entity.Ref) (eff string, revoked bool) {
	if wpre, ok := witnessPreconditions(recheck, witness); ok {
		return e.commitMotionAtomic(ref, outcome, witness, wpre)
	}
	// A deny: nothing to predicate the revoke guard on. Best-effort recheck, then a
	// terminal-once Modify.
	if recheck != nil {
		if ok, _ := recheck.Eval(e.store); !ok {
			return "", true // the justifying facts no longer hold — do not commit
		}
	}
	e.modify(ref, func(cur entity.Payload) entity.Payload {
		if o := entity.Str(cur, "outcome"); o != "" {
			eff = o // already latched — terminal-once, honor it
			return cur
		}
		eff = outcome
		return closedMotion(cur, outcome, witness)
	})
	return eff, false
}

// commitMotionAtomic commits the verdict via one atomic conditional write: terminal-once
// (the channel is still "open") AND the revoke guard (wpre — the witness ballots still
// hold), both checked with the write in a single step. A refused commit is either an
// already-latched motion (read it back and honor it — terminal-once) or a revoke (the
// witness moved — re-monitor).
func (e *Engine) commitMotionAtomic(ref entity.Ref, outcome string, witness []entity.Ref, wpre []entity.Require) (eff string, revoked bool) {
	// A failed read must NOT fall through to the commit below: the new payload is built
	// FROM cur, so committing on an empty cur would overwrite a motion with a stripped
	// one. Treat it as a revoke — monitorGate re-monitors and we try again with a real read.
	cur, _, err := e.store.Get(ref)
	if err != nil {
		e.tracef("commit %s: read failed: %v", ref.ID(), err)
		return "", true
	}
	if o := entity.Str(cur.Payload, "outcome"); o != "" {
		return o, false // already committed — terminal-once
	}
	when := append([]entity.Require{{Pattern: entity.Obj{"status": "open"}.Payload()}}, wpre...)
	committed, _, err := e.store.Write(entity.Write{
		At:    ref.At(),
		Value: closedMotion(cur.Payload, outcome, witness),
		When:  when,
	})
	if err != nil {
		e.tracef("commit %s: conditional commit error: %v", ref.ID(), err)
		return "", true // treat as a revoke; monitorGate re-monitors
	}
	if !committed {
		// A failed read here is indistinguishable from "no latch yet", and both lead to
		// the same answer: revoked, re-monitor.
		if got, ok, _ := e.store.Get(ref); ok {
			if o := entity.Str(got.Payload, "outcome"); o != "" {
				return o, false // a concurrent commit latched first — honor it
			}
		}
		return "", true // the witness no longer holds — revoked
	}
	return outcome, false
}

// closedMotion builds the closed channel payload from the open one, preserving its open
// fields (need/gate/subject) and recording the outcome, tally, and witness.
//
// AsData because it is built from what was READ and written straight back (the commit is one
// conditional write, not a Modify — see commitMotionAtomic), and a write is a patch: whatever
// the channel carries has to be stored again rather than executed.
func closedMotion(cur entity.Payload, outcome string, witness []entity.Ref) entity.Payload {
	p := clonePayload(cur)
	p["status"] = "closed"
	p["outcome"] = outcome
	p["tally"] = len(witness)
	if ids := refIDs(witness); len(ids) > 0 {
		p["witness"] = ids
	}
	return entity.AsData(p.Payload())
}

// witnessPreconditions builds the revoke-guard precondition for an atomic commit: it is
// applicable only when the satisfy-formula is a quorum atom, in which case it requires each
// witness ballot to still be present with the counted choice, over the motion's vote
// subtree. Returns ok=false when there is no quorum recheck or no witness (the deny path, or
// a non-quorum formula) — the caller then uses the best-effort fallback.
func witnessPreconditions(recheck *Formula, witness []entity.Ref) ([]entity.Require, bool) {
	if recheck == nil || recheck.Op != OpAtom || recheck.Req == nil || recheck.Req.Kind != ReqQuorum {
		return nil, false
	}
	fs := recheck.Req.Facts
	system, kind := fs.System(), fs.Kind()
	if system == "" || kind == "" {
		return nil, false
	}
	choice := fs.FieldString("choice")
	sub := map[string]any{}
	for _, w := range witness {
		if w.System() != system || w.Kind() != kind {
			continue
		}
		ballot := map[string]any{}
		if choice != "" {
			ballot["choice"] = choice
		}
		sub[w.ID()] = ballot
	}
	if len(sub) == 0 {
		return nil, false
	}
	return []entity.Require{{
		At:      entity.Path{Ref: entity.RefAt(system, kind)}, // the whole vote subtree
		Pattern: entity.Obj(sub).Payload(),
	}}, true
}

// clonePayload reads p back as a mutable object builder (nil, or a non-object payload →
// empty), so a Modify can extend the current value without mutating the store's copy. The
// caller converts once, with .Payload(), when it returns.
//
// Field by field as NODES, not through entity.ToObj: a map[string]any has nowhere to put a
// tag, so reading a payload back through Go values silently rewrites what it carries — a
// stored rule losing `!glob "hotfix-*"` down to the string, and its
// `tmp: !delete null` down to a null, which the write would then apply as a tombstone. A
// record that holds a document has to survive being extended by one field. Each node is
// cloned because the builder's result is parented into a fresh document, and the copy it came
// from belongs to the store.
//
// What is built from one of these is data on its way back in. Through Modify that is taken
// care of (it escapes what it writes); a builder written STRAIGHT back has to say so itself,
// with entity.AsData — closedMotion is the one that does.
func clonePayload(p entity.Payload) entity.Obj {
	out := entity.Obj{}
	for k, v := range entity.Fields(p) {
		out[k] = v.Clone()
	}
	return out
}

// refIDs renders a witness as its entity ref strings (audit trail on the motion).
func refIDs(refs []entity.Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.String())
	}
	return out
}

// ── the who-axis: governed provider selection ───────────────────────────────

// selectProvider resolves the who-axis for a spawn whose Spec carries a Provider and returns
// the chosen doer as a launch.Fragment — overlaid onto the spawn before it launches (no live
// swap).
//
// It returned a bare driver string until the fragment landed. That was proportionate when a
// spawn was (id, task, driver); once a spawn could say frame/tools/grant, two doers could
// still differ only by which runtime ran the prompt, which is the smaller half of what
// distinguishes them. See docs/provider-selection.md.
//
// `ask` and `vote` reify a `select` motion — a motion over the eligible doers as options —
// resolved through the SAME machinery as a whether-gate (openChannel/awaitMotion/
// commitMotion). The commit is CAS terminal-once with the revoke guard, so a ballot
// retracted between threshold and commit re-monitors; a re-fire is idempotent (same
// channel). `auto` poses no question and opens no motion — see below.
//
// So the dial has three settings but two surfaces, and which one a setting lands on is not
// arbitrary: the docket carries a question that is NOT settled when it is posed, because it
// needs several contributions or because a clock (`within`) can settle it instead. That is
// why `ask` sits here rather than in `verse gate` alongside a whether-ask — it composes
// with `within`, and naming a doer is a claim that must be attributable, which a ballot is
// by construction and `gate approve` today is not.
//
// Orthogonal to the runner that then transports the chosen driver.
func (e *Engine) selectProvider(spawn launch.Spec, subject entity.Ref) (launch.Fragment, bool) {
	p := spawn.Provider
	options, frags := e.eligibleProviders(spawn)
	if len(options) == 0 {
		// A motion needs options, so there is no question to pose and nothing reaches the
		// docket. That is indistinguishable from "the trigger never matched" unless we say
		// so HERE: the firing matched, its gates passed, and then it produced no task,
		// no motion, and no error. Every other exit from this function traces; this one is
		// the likeliest to be hit (a capability whose registry is empty, or a match that
		// excludes everyone) and was the only one silent.
		e.logger.Warn("provider selection has no eligible doer; no motion opened and nothing spawned",
			"subject", subject.String(), "capability", p.Capability,
			"select", p.Select, "why", e.whyNoEligible(spawn))
		return launch.Fragment{}, false
	}
	// auto: a rule picks the first eligible doer immediately — and poses NOTHING. The docket
	// is the votable, attributable surface; an auto pick has no voter and casts no ballot, so
	// opening a channel for it put `outcome=gpt tally=0` on the docket — a decision reported
	// as the group's that nobody made. This is the rule the whether-axis already follows: a
	// channel is opened only for a gate carrying a Formula, never for `mode: auto`.
	//
	// The pick stays observable on the spawned task (its Driver). What a motion would add
	// is the alternatives, and those are provenance of a spawn rather than a decision anyone
	// took — if they are wanted, they belong on the task entity.
	//
	// Not latching also retires a failure mode: a committed auto pick outlived the registry
	// and went on naming a retired doer (what fragmentFor's fallback exists to absorb).
	// Re-deriving per firing is deterministic given the same registry — eligibleProviders
	// sorts — and follows the registry when it changes.
	if p.Select == "" || p.Select == "auto" {
		return fragmentFor(frags, options[0]), true
	}

	channelRef := entity.VerseRef(motionSystem, motionChannelKind,
		motionChannelID(providerSpecSeed(subject, p)))

	// The chain rides the SPEC (launchSpawn stamps it before the selection), so the motion
	// records which action it is blocking without this needing an argument of its own.
	m := e.compileProvider(p, subject, channelRef, options, frags, spawn.Chain)
	e.openChannel(m)

	// vote/ask: resolve over the vote log, bounded by the engine lifetime, with the revoke
	// guard active — a ballot retracted between threshold and commit re-monitors.
	ctx := e.bgctx
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		winner, witness, ok := e.awaitMotion(ctx, m)
		if !ok {
			e.tracef("select %s: %s ended before the motion closed", channelRef.ID(), p.Select)
			return launch.Fragment{}, false
		}
		if winner == "" { // no pick (deadline default) — record it, do not spawn
			e.commitMotion(channelRef, nil, "no-pick", nil)
			return launch.Fragment{}, false
		}
		eff, revoked := e.commitMotion(channelRef, m.guardFor(winner, witness), winner, witness)
		if revoked {
			e.tracef("select %s: revoked before commit → re-monitoring", channelRef.ID())
			continue
		}
		return fragmentFor(frags, eff), true
	}
}

// fragmentFor maps a selected option to what it contributes to the spawn; a prior selection
// no longer in the registry falls back to the option id as the driver, contributing nothing
// else. That fallback is why a retired doer degrades to today's behaviour rather than to an
// empty envelope: a fragment that silently dropped a grant would widen, which is the one
// direction this may never fail in.
func fragmentFor(frags map[string]launch.Fragment, selected string) launch.Fragment {
	if f, ok := frags[selected]; ok {
		return f
	}
	return launch.Fragment{ID: selected, Driver: selected}
}

// eligibleProviders resolves a spawn's selectable options to {option → fragment}, in a
// deterministic order — the candidates that both match and may run under this spawn.
func (e *Engine) eligibleProviders(spawn launch.Spec) ([]string, map[string]launch.Fragment) {
	var options []string
	frags := map[string]launch.Fragment{}
	for _, c := range e.providerCandidates(spawn) {
		if c.refused != "" {
			continue
		}
		options = append(options, c.id)
		frags[c.id] = c.frag
	}
	return options, frags
}

// providerCandidate is one doer the selection considered, and — when it may not run under
// this spawn — why not. Refusals are carried rather than dropped so whyNoEligible can explain
// an empty ballot without re-deriving the reasoning that produced it.
type providerCandidate struct {
	id      string
	frag    launch.Fragment
	refused string // "" = eligible
}

// providerCandidates lists every doer the spawn's Provider names or matches, each marked
// eligible or refused. Inline Eligible: the option IS the driver, and contributes nothing
// else — an inline list is a list of names, and a name has no attributes to carry. Registry
// Capability: list provider:<capability>:* whose attributes satisfy Match (a tony merge
// match); the option is the provider id and the fragment is read off its payload.
//
// The payload is one namespace holding two kinds of key: SELECTION attributes (`tier`,
// `risk`, `cost` — whatever the charter's Match talks about) and FRAGMENT fields (the four
// launch.Fragment names). They are not separated, and should not be: `driver` was always both
// — matchable and bound — and splitting them would make a provider entity a schema instead of
// the free-form thing that lets a new attribute be invented without a code change.
//
// A refused candidate is INELIGIBLE, not trimmed. It never reaches a ballot, so a vote cannot
// elect a doer that then fails to be seated — a decision with no seat, which no voter could
// have known they were making. The alternative, quietly dropping the offending field and
// running the doer anyway, would put a task under an envelope different from the one its
// own entity states; task/authority.go was written to end exactly that.
func (e *Engine) providerCandidates(spawn launch.Spec) []providerCandidate {
	p := spawn.Provider
	if p == nil {
		return nil
	}
	ceiling, declared, ceilErr := task.NamedGrant(spawn.Grant)
	var out []providerCandidate
	seen := map[string]bool{}
	add := func(id string, f launch.Fragment, payload entity.Payload) {
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, providerCandidate{
			id: id, frag: f,
			refused: refuseProvider(f, payload, spawn, ceiling, declared, ceilErr),
		})
	}
	for _, d := range p.Eligible {
		add(d, launch.Fragment{ID: d, Driver: d}, nil)
	}
	if p.Capability != "" {
		var pattern *ir.Node
		if len(p.Match) > 0 {
			pattern, _ = gomap.ToTonyIR(p.Match)
		}
		ents := e.store.List(func(r entity.Ref) bool { return r.System() == "provider" && r.Kind() == p.Capability })
		sort.Slice(ents, func(i, j int) bool { return ents[i].Ref.ID() < ents[j].Ref.ID() })
		for _, en := range ents {
			if pattern != nil && !providerMatches(en.Payload, pattern) {
				continue
			}
			add(en.Ref.ID(), providerFragment(en.Ref, en.Payload), en.Payload)
		}
	}
	return out
}

// unhonoredProviderFields are the launch-spec fields a provider entity may NOT set. They are
// refused rather than ignored: a doer whose entity says `vector: auto` and which then runs at
// the charter's `ask` is a doer nobody described.
//
// `vector` is here because it is the one axis that can widen without touching authority — it
// says whether a person sees an action at all, and `vector: auto` means "this firing already
// answered its gates" (task/authority.go), which is the charter's claim to make. An
// ordering over auto|ask|park that would let a doer TIGHTEN autonomy is a later question.
// `resolver` and `environment` are here because neither has semantics to resolve to yet
// (versed.launchOf), so honoring them would be a promise nothing keeps.
var unhonoredProviderFields = []string{"vector", "resolver", "environment"}

// refuseProvider reports why a doer may not run under this spawn, "" when it may. The three
// refusals are the three ways a provider entity — an ordinary entity, writable by anyone who
// can write entities — could otherwise obtain authority the charter never granted.
func refuseProvider(f launch.Fragment, payload entity.Payload, spawn launch.Spec,
	ceiling task.Grant, declared bool, ceilErr error) string {

	for _, name := range unhonoredProviderFields {
		if entity.Get(payload, name) != nil {
			return fmt.Sprintf("it sets `%s`, which a provider may not: that field is the charter's", name)
		}
	}
	if f.Grant != "" {
		switch {
		case ceilErr != nil:
			return fmt.Sprintf("it declares a grant and the spawn's own grant %q does not resolve (%v)", spawn.Grant, ceilErr)
		case !declared:
			// Unreachable through AddTrigger, which requires a grant on any spawn carrying a
			// provider. Kept because the reachable-today answer and the safe answer differ: an
			// undeclared ceiling is UNBOUNDED (task.Launch.Grant nil), so taking it as the
			// bound would make every provider grant a "narrowing" of everything.
			return "it declares a grant and the spawn declares none, so there is no ceiling to check it against"
		}
		g, ok, err := task.NamedGrant(f.Grant)
		if err != nil || !ok {
			return fmt.Sprintf("its grant %q does not resolve (%v)", f.Grant, err)
		}
		if !ceiling.Subset(g) {
			return fmt.Sprintf("its grant %q is not within the spawn's %q — a doer may narrow authority, never widen it",
				f.Grant, spawn.Grant)
		}
	}
	// Everything else the overlay itself can refuse — today, tool lists that share nothing.
	// Asking Overlay rather than restating its rules keeps one answer to "may these compose".
	if _, err := spawn.Overlay(f); err != nil {
		return err.Error()
	}
	return ""
}

// providerFragment reads a provider entity's launch fragment off its payload. A provider
// with no `driver` falls back to its own id, which is what let `provider:review:claude` mean
// the claude driver before the field was conventional.
func providerFragment(ref entity.Ref, payload entity.Payload) launch.Fragment {
	f := launch.Fragment{
		ID:     ref.String(),
		Driver: entity.Str(payload, "driver"),
		Frame:  entity.Str(payload, "frame"),
		Grant:  entity.Str(payload, "grant"),
		Tools:  entity.Strs(payload, "tools"),
	}
	if f.Driver == "" {
		f.Driver = ref.ID()
	}
	return f
}

// whyNoEligible explains an empty eligible set in terms of what to go fix. The four
// causes need different answers — register a doer, loosen the match, narrow a doer (or widen
// the spawn), or write a provider into the spec — and "no eligible doer" tells you which one
// to try only by accident.
func (e *Engine) whyNoEligible(spawn launch.Spec) string {
	p := spawn.Provider
	if p == nil {
		return "the spawn declares no provider"
	}
	// Matched, and then refused for what it would run as. This is checked before the registry
	// counts below because it is the only cause whose fix is in TWO places — the doer's entity
	// or the spawn's own grant — so the message has to name both sides of the comparison.
	if refused := refusedProviders(e.providerCandidates(spawn)); len(refused) > 0 {
		return fmt.Sprintf("%d matching doer(s) may not run under this spawn: %s",
			len(refused), strings.Join(refused, "; "))
	}
	if p.Capability == "" {
		return "the spawn's provider lists no eligible doers and names no capability"
	}
	// Registered-but-excluded and not-registered-at-all look the same from the caller and
	// are fixed in opposite places, so count the registry before the match is applied.
	registered := len(e.store.List(func(r entity.Ref) bool {
		return r.System() == "provider" && r.Kind() == p.Capability
	}))
	if registered == 0 {
		return fmt.Sprintf("no provider:%s:* is registered (register one, e.g. "+
			"`verse state put provider:%s:<id> '{driver: llm}'`)", p.Capability, p.Capability)
	}
	return fmt.Sprintf("all %d registered provider:%s:* were excluded by match %s",
		registered, p.Capability, matchText(p.Match))
}

// refusedProviders renders the refused candidates as "<id>: <why>", for the message above.
func refusedProviders(cands []providerCandidate) []string {
	var out []string
	for _, c := range cands {
		if c.refused != "" {
			out = append(out, c.id+": "+c.refused)
		}
	}
	return out
}

// matchText renders a match pattern the way the charter that declared it is written, so
// the operator can compare the message against their own file rather than translate Go's
// map formatting back into tony.
func matchText(m map[string]any) string {
	b, err := codec.Marshal(m, codec.Tony)
	if err != nil {
		return fmt.Sprintf("%v", m)
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// providerMatches reports whether a provider's attributes satisfy an eligibility pattern,
// evaluated as a tony (RFC-7396 merge) match — the attributes are converted to a tony
// node and matched against the pattern. Tag-ops (!and/!or/!glob/…) work whatever dialect
// the pattern was authored in, since JSON input is parsed by the tony parser too; an
// untagged pattern stays a structural merge match.
func providerMatches(attrs entity.Payload, pattern *ir.Node) bool {
	// A payload IS a node, so there is nothing to convert — this used to marshal the map
	// through gomap first. Same call, one fewer representation.
	ok, err := tony.Match(attrs, pattern)
	return err == nil && ok
}

// Spawn asks the Spawner to launch a task from a launch.Spec — non-blocking.
// The Spawner owns build/mirror/run; the task's state appears in the verse and
// its acts write entities, closing the loop. Requires a Spawner.
func (e *Engine) Spawn(id string, spec launch.Spec) task.Handle {
	return e.spawnHooked(id, spec, nil, "")
}

// spawnHooked is Spawn with completion hooks: hooks is the spawning action (its onX fire when
// this task ends), chain is this spawn's position in a hook chain (for deterministic nested
// ids). Public Spawn passes nil/"" — an ordinary spawn with no chaining.
func (e *Engine) spawnHooked(id string, spec launch.Spec, hooks *Action, chain string) task.Handle {
	if e.spawn == nil {
		e.tracef("no spawner configured; cannot spawn %q", id)
		return nil
	}
	// Idempotent per id: spawnID is deterministic per (trigger, subject), so a re-fire about
	// the same subject must not start a second task. The runtime would not stop it — a
	// taskd Spawn overwrites its registry entry while the old task keeps running — so
	// the engine, which owns the live registry, is where this belongs.
	e.liveMu.Lock()
	if fp, running := e.live[id]; running {
		e.liveMu.Unlock()
		e.tracef("task %s already live (subject %s); not respawning", id, fp.subject)
		return fp.h
	}
	e.liveMu.Unlock()

	e.tracef("spawn task %s (driver=%s): %s", id, spec.Driver, spec.Prompt)
	h := e.spawn(id, spec)
	if h != nil && spec.Subject != "" {
		e.track(flowTask{id: id, subject: spec.Subject, h: h, hooks: hooks, chain: chain})
	}
	return h
}

// track registers a live task and arms its lifecycle-driven reaper.
func (e *Engine) track(fp flowTask) {
	e.liveMu.Lock()
	e.live[fp.id] = fp
	e.liveMu.Unlock()
	go e.reapOnDone(fp) // retire it when it ends
}

// Adopt registers an already-running task (id + governing handle) under a flow — the
// resume path on restart, when a remote-hosted task outlived verse. It re-tracks it
// exactly as Spawn does a fresh one, so the resumed task is steerable (flow
// cancel/say/pause) and reaped from the flow when it ends — not merely observed. subject
// "" is a no-op.
func (e *Engine) Adopt(subject, id string, h task.Handle) {
	e.AdoptChain(subject, id, "", h)
}

// AdoptChain is Adopt for a task that came from the charter: chain is the kpath of the
// action that spawned it, read back off its task entity. The engine navigates to that
// action and re-attaches its completion hooks, so a resumed task finishes the way it
// would have — its onDone fires, and a sequence it belongs to advances.
//
// This is what a chain being a LOOKUP KEY into the charter is for. Before it, resume tracked
// a task with no hooks at all, which meant a completion after a restart silently ended a
// chain: docs/trigger-model.md records that as a limitation, and it is why `onOrphaned`
// never fires (the only orphan is written by this very reconcile, on a fresh engine).
//
// Rehydration is best-effort by nature, and the failures are ordinary rather than alarming:
// a trigger removed while its task ran, or a charter edited so the chain no longer
// resolves. Both mean there is no action left to run, which is the correct outcome — the
// task is still adopted, so it stays steerable and is still reaped.
func (e *Engine) AdoptChain(subject, id, chain string, h task.Handle) {
	if h == nil || subject == "" {
		return
	}
	fp := flowTask{id: id, subject: subject, h: h, chain: chain}
	if chain != "" {
		if t, ok := e.triggerNamed(mirror.TriggerOf(chain)); ok {
			if act, ok := navigate(t, chain); ok {
				fp.hooks = act
			} else {
				e.tracef("adopt %s: chain %s no longer resolves; no hooks rehydrated", id, chain)
			}
		} else {
			e.tracef("adopt %s: trigger %s is gone; no hooks rehydrated", id, mirror.TriggerOf(chain))
		}
	}
	e.track(fp)
}

// reapOnDone retires a task from its flow's tracking once it reaches a terminal
// status — the lifecycle-driven reaper for scoped (subtree) mode, where no root dispatch
// loop runs so the store-driven reap never fires. Reaping is purely task bookkeeping,
// so it hangs off the task's own completion (its handle's bus). NOTE: it can race a
// fast task that finishes before this subscribes (Subscribe is live-from-now, no ring
// replay) — in baseline mode reap (store-driven, via the root watch) is the race-free
// path; this is the backstop and the scoped-mode path. The range also exits if the
// task closes its bus, so this cannot outlive p.
func (e *Engine) reapOnDone(fp flowTask) {
	ch, unsub := fp.h.Subscribe()
	defer unsub()
	var status string
	for ev := range ch {
		if ev.Kind == task.EvStatus && task.TerminalStatus(ev.Text) {
			status = ev.Text
			break
		}
	}
	// Retire whether or not we saw a terminal status (a closed bus means the task is gone).
	// Apply the completion hook only when we both observed a terminal status AND won the retire
	// (retireHandle is atomic, so the store-driven reapLoop/reap and this can't double-fire).
	got, ok := e.retireHandle(fp.id, fp.h)
	if ok && status != "" {
		e.applyHook(got, task.Status(status), e.taskEvent(got, task.Status(status)))
	}
}

// retire atomically removes a live task by id, returning its flowTask. Whoever retires it
// first (the store-driven reapLoop/reap, or the bus-driven reapOnDone) is the one that applies
// its completion hook — the once-guard for exactly-once hook application.
func (e *Engine) retire(id string) (flowTask, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	fp, ok := e.live[id]
	if ok {
		delete(e.live, id)
	}
	return fp, ok
}

// retireHandle is retire scoped to a specific handle: it removes the entry only if the tracked
// handle still matches (not a successor that reused the id). The reapOnDone path uses it.
func (e *Engine) retireHandle(id string, h task.Handle) (flowTask, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if fp, ok := e.live[id]; ok && fp.h == h {
		delete(e.live, id)
		return fp, true
	}
	return flowTask{}, false
}

// applyHook fires the completion hook for a terminated task's status (if any), scoped to
// exactly that task: the hook (a full Action) runs about the task's own subject. The chain
// name descends by status, so a hook's own spawn gets a distinct deterministic id. ev carries the
// completed task:instance payload, so the hook's templates can read $status/$flow/$result.
func (e *Engine) applyHook(fp flowTask, status task.Status, ev entity.Event) {
	subject, serr := entity.ParseAddr(fp.subject)
	// A completed SEQUENCE ELEMENT advances its sequence instead of firing a hook: the list
	// already says what follows, which is why an element may not carry its own onDone. Only
	// on `done` — any other terminal status is an off-ramp, and takes the hook path below so
	// the element's own onFailed/onCancelled can run and the sequence stops there (fail-stop).
	if status == task.Done && serr == nil {
		if e.resumeSeq(fp.chain, subject, ev) {
			return
		}
	}
	// An off-ramp ENDS the sequence, so the run ends here and has to be written down as ended.
	// Before the two returns below, because a sequence stops at an off-ramp whether or not the
	// element carries a hook for it — a failure nobody wrote an `onFailed` for is still a
	// failure, and the run behind it is still over.
	if status != task.Done && serr == nil {
		e.stopSeq(fp.chain, subject, status)
	}
	if fp.hooks == nil {
		return
	}
	hook := fp.hooks.hookFor(status)
	if hook == nil {
		return
	}
	if serr != nil {
		return
	}
	subj := subject
	// Descend the chain by the hook's FIELD name, not by the status: `review.onDone` is the
	// kpath of the action that is about to run, so the id it spawns under is its address in
	// the charter. (An empty field cannot happen — hookFor already returned non-nil only for
	// a terminal status — but a chain is a wire-visible id, so do not build a malformed one.)
	seg := hookField(status)
	if seg == "" {
		return
	}
	// Appended directly rather than via kpath.Join, which reads its PREFIX as a single
	// segment and so quotes a multi-segment chain: Join("review.onDone", "onFailed") is
	// `"review.onDone".onFailed`, a valid kpath naming one field with a dotted name — and
	// a quote is not legal unescaped in a URL path. A field segment appends with '.'.
	chain := fp.chain + "." + seg
	// A hook has no causing commit — it is woken by a task finishing rather than by a
	// write, which is exactly why its chain starts again (causal.go). So the occasion carries
	// the task event for its references and nothing for $subject.value/$subject.commit,
	// which then fail by name rather than resolving to a silent zero.
	if err := hook.apply(chain, occasion{ev: ev}, []entity.Ref{subj}, e, fromCause); err != nil {
		e.recordHookFailure(mirror.Ref(fp.id), chain, err)
	}
}

// recordHookFailure persists a completion hook's failure onto the task whose completion
// fired it, under a `hook` record:
//
//	hook: { chain: review.onDone, status: failed, err: "$data.severity is not present …" }
//
// A hook is where an unverifiable claim turns into a state transition, so its failure is the
// one an operator most needs to see — and a trace line is invisible to everything that reads
// the verse. Written here rather than onto the SUBJECT because the failure is a fact about
// this task's completion, not about the PR: the subject deliberately did not advance, and
// putting an error in a field a charter matches on is what would make `value: {stage: X}`
// quietly stop matching.
//
// Writing to the task entity is safe despite the mirror owning that payload, because a
// store write MERGES: a field the mirror never names survives its re-Put (see mirror.go,
// which relies on the same property for `pending`). That also makes it ordering-independent,
// which matters — this can run before the mirror's own terminal write, since the two race.
//
// `hook` is one reserved name on an entity the engine and the mirror already co-own, the way
// `verse` is one reserved root on a subject. status is the mirror's field and is left alone.
func (e *Engine) recordHookFailure(ref entity.Ref, chain string, err error) {
	e.tracef("hook %s on %s: %v", chain, ref, err)
	e.modify(ref, func(cur entity.Payload) entity.Payload {
		p := clonePayload(cur)
		p["hook"] = entity.Obj{"chain": chain, "status": "failed", "err": err.Error()}
		return p.Payload()
	})
}

// taskEvent synthesizes the store event for a completed task — the bus-driven reapOnDone
// path has no entity.Event, so it reads the task:instance payload to give the hook the
// same bindings the store-driven path has.
//
// The mirror entity is NOT authoritative for the terminal facts. reapOnDone and
// mirror.Reflect are two independent subscribers to the same task bus, so when this path
// wins the retire — which for a fast task it reliably does, the engine's reader getting
// there first — the store still holds the PRE-terminal projection. The hook then bound
// $status to "running" and found no $result at all, which is exactly the payload a
// completion hook exists to read. The store-driven path never had this problem: mirror
// writes status and result in one commit (see mirror.go), so the event it dispatches on
// already carries both.
//
// So the facts this path knows first-hand are overlaid on whatever the mirror has managed to
// write: the terminal status this reap just observed, and the handle's final answer. flow and
// chain come from the engine's own record for the same reason — a hook must bind them even
// when the mirror has written nothing about this task yet.
func (e *Engine) taskEvent(fp flowTask, status task.Status) entity.Event {
	ref := mirror.Ref(fp.id)
	ev := entity.Event{Ref: ref}
	st := entity.Obj{}
	if ent, ok, _ := e.store.Get(ref); ok {
		if o := entity.ToObj(ent.Payload); o != nil {
			st = o
		}
	}
	st["status"] = string(status)
	if fp.h != nil {
		if r := fp.h.Result(); r != "" {
			st["result"] = r
			// Derive `data` here too, with the mirror's own function: a hook that derefs
			// $data.x must resolve against the same document the store ends up holding,
			// and this path can run before the mirror has written anything.
			if d := mirror.ParseResult(r); d != nil {
				st["data"] = d
			}
		}
		if er := fp.h.Err(); er != "" {
			st["err"] = er
		}
	}
	if fp.subject != "" {
		st["flow"] = fp.subject
	}
	if fp.chain != "" {
		st["chain"] = fp.chain
	}
	ev.To = st.Payload()
	return ev
}

// Flows lists the distinct subjects with in-flight tasks — a QUERY over the live
// registry, not a maintained index. Terminal tasks are reaped, so a subject drops off
// once its work finishes; the handles behind it are this engine's live tasks (control
// targets), not a durable membership view.
// SubjectOf returns the subject a live task is about, or "" if the engine does not know
// it — a task spawned directly by `task run` genuinely has none, and one that has
// already been reaped is no longer live. A flow is a query over these subjects, so this is
// that query at its narrowest setting: one task.
func (e *Engine) SubjectOf(id string) string {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	return e.live[id].subject
}

func (e *Engine) Flows() []string {
	e.liveMu.Lock()
	seen := make(map[string]bool, len(e.live))
	for _, fp := range e.live {
		seen[fp.subject] = true
	}
	e.liveMu.Unlock()
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out) // deterministic for a CLI listing
	return out
}

// matchFlow selects the live tasks a subject-Match addresses. This IS the flow
// primitive: a flow is not a stored key but the answer to "which live tasks are about
// entities matching this". A payload predicate (`fields`) is applied by reading the subject
// entity, so "pause every open PR's work" is the same selector at a wider setting.
func (e *Engine) matchFlow(m Match) []flowTask {
	e.liveMu.Lock()
	all := make([]flowTask, 0, len(e.live))
	for _, fp := range e.live {
		all = append(all, fp)
	}
	e.liveMu.Unlock()

	hasPredicate := m.HasPredicate()
	out := make([]flowTask, 0, len(all))
	for _, fp := range all {
		ref, err := entity.ParseAddr(fp.subject)
		if err != nil || !m.MatchRef(ref) {
			continue
		}
		if hasPredicate {
			// Unreadable is treated as non-matching: a field predicate that cannot be
			// evaluated must not select the flow. Skipping is the conservative answer.
			ent, ok, _ := e.store.Get(ref)
			if !ok || !m.MatchEvent(entity.Event{Ref: ref, To: ent.Payload}) {
				continue
			}
		}
		out = append(out, fp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// CancelFlow cancels every task the address selects (out-of-band control, honored at
// each task's next boundary — never a store write). Returns how many were signalled.
func (e *Engine) CancelFlow(m Match) int {
	ps := e.matchFlow(m)
	for _, fp := range ps {
		fp.h.Cancel("flow cancel: " + fp.subject)
	}
	return len(ps)
}

// SteerFlow says an instruction to every task the address selects, attributed to by.
// Returns how many were said-to.
func (e *Engine) SteerFlow(m Match, text, by string) int {
	ps := e.matchFlow(m)
	for _, fp := range ps {
		fp.h.Steer(text, by)
	}
	return len(ps)
}

// PauseFlow holds every task of a flow at its next boundary — an operator's
// ad-hoc freeze of one subject's live work (distinct from cancel, which is
// destructive, and from a park gate, which is a charter-declared hold). Out-of-band
// control, honored at the boundary, never a store write. Lift it with ResumeFlow.
// Returns how many were signalled. (This is per-flow; it does not pause the engine's
// dispatch loop — other flows keep reacting.)
func (e *Engine) PauseFlow(m Match) int {
	ps := e.matchFlow(m)
	for _, fp := range ps {
		fp.h.Pause()
	}
	return len(ps)
}

// ResumeFlow lifts a PauseFlow, releasing each task to run from its held
// boundary. Returns how many were signalled.
func (e *Engine) ResumeFlow(m Match) int {
	ps := e.matchFlow(m)
	for _, fp := range ps {
		fp.h.Resume()
	}
	return len(ps)
}

// reap retires a task when its reflected entity reaches a terminal status, so
// Flows()/matchFlow see only in-flight work — no lingering completed handles. It is
// store-driven: the same task:instance change a completion trigger reacts to also
// releases the handle. Keyed by task id, so this is one delete — no scan of subjects, and
// none of the "never filter in place" hazard the per-subject slices used to carry (the
// control ops copied a slice header under the lock and iterated it unlocked).
func (e *Engine) reap(ev entity.Event) {
	if !mirror.IsTaskish(ev.Ref) {
		return
	}
	status := entity.Str(ev.To, "status")
	if !task.TerminalStatus(status) {
		return
	}
	if fp, ok := e.retire(ev.Ref.ID()); ok {
		e.applyHook(fp, task.Status(status), ev)
	}
}

// reapLoop is the store-driven completion path for scoped (subtree) mode: it watches the
// task:instance mirror subtree and, on each terminal status, retires the task and fires its
// completion hook. Opened in ObserveScoped BEFORE any spawn (WatchSubtree replays then edges), so
// it is race-free for a fast task — the property reapOnDone's live-from-now bus lacks. The
// atomic retire makes it and the reapOnDone backstop exactly-once. Replayed old terminals are
// harmless: retire finds no live entry for a task this engine never tracked.
func (e *Engine) reapLoop(ctx context.Context, ch <-chan entity.Commit, unsub func()) {
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-ch:
			if !ok {
				return
			}
			for _, ev := range c.Changes {
				status := entity.Str(ev.To, "status")
				if !task.TerminalStatus(status) {
					continue
				}
				if fp, ok := e.retire(ev.Ref.ID()); ok {
					e.applyHook(fp, task.Status(status), ev)
				}
			}
		}
	}
}

// ReconcileOrphans retires task:instance entities that a previous run left
// non-terminal: a fresh runtime holds no handle for them (taskd is in-memory, so
// a restart kills every task), so their last durable state is marked orphaned
// rather than left claiming to run. Returns how many were reconciled.
//
// Call it once at monolith startup, before Start. Do NOT call it in a split
// deployment where the task runtime outlives the engine — there the tasks are
// real and reconnectable, not orphans.
func (e *Engine) ReconcileOrphans() int {
	n := 0
	// Every kind the task system holds, deliberately — instances AND run records (see
	// mirror.RunKind). A run whose runtime died IS orphaned, and its record is the only place
	// that fact can still be told; leaving it saying `running` forever would make the history
	// less honest than the live view beside it. Claims carry no status and fall through.
	for _, ent := range e.store.List(mirror.IsTaskish) {
		status := entity.Str(ent.Payload, "status")
		if status == "" || task.TerminalStatus(status) {
			continue
		}
		e.write(ent.Ref.At("status"), entity.Val(string(task.Orphaned)))
		n++
	}
	return n
}

// modify read-modify-writes an entity under compare-and-swap, bounded so a losing writer
// cannot spin forever. Like write, a failure is logged rather than returned: the engine is
// reacting, and there is nobody to hand an error to.
func (e *Engine) modify(ref entity.Ref, fn func(entity.Payload) entity.Payload) {
	if err := e.modifyErr(ref, fn); err != nil {
		e.logger.Warn("read-modify-write failed; the reaction did not land", "ref", ref.String(), "err", err)
	}
}

// modifyErr is modify with the failure RETURNED. A reaction has nobody to return one to, so
// modify logs; a request does (an install), and a caller that can refuse the whole operation
// should not have to read the log to find out that it half-happened.
func (e *Engine) modifyErr(ref entity.Ref, fn func(entity.Payload) entity.Payload) error {
	return e.modifyOn(e.store, ref, fn)
}

// modifyOn is modifyErr through a NAMED store rather than the engine's own — which is how an
// action's write reaches the store that stamps its causal chain (see caused). The engine's
// machinery keeps passing e.store and so stays outside every chain, deliberately: a motion
// ledger, a charter record and a reaped status are bookkeeping about a firing, not the state
// transition the firing exists to make, and only the latter can wake the rule that closes a loop.
func (e *Engine) modifyOn(s entity.Store, ref entity.Ref, fn func(entity.Payload) entity.Payload) error {
	parent := e.bgctx
	if parent == nil {
		parent = context.Background() // not started (a direct call in a test)
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	return entity.Modify(ctx, s, ref, fn)
}

// write applies a value at a path. A failed write is LOGGED, at Warn: the engine is
// reacting, not being called, so there is nobody to return an error to — and a reaction
// that did not land is exactly the thing an operator must not have to infer from the
// absence of what should have happened next.
func (e *Engine) write(at entity.Path, v entity.Payload) {
	if _, _, err := e.store.Write(entity.Write{At: at, Value: v}); err != nil {
		e.logger.Warn("write failed; the reaction did not land", "at", at.String(), "err", err)
	}
}

// tracef is the engine's running commentary on what it is doing — every dispatch, gate
// and flow decision. It is Debug, so it costs nothing until someone turns it on.
func (e *Engine) tracef(format string, a ...any) {
	if e.logger == nil || !e.logger.Enabled(context.Background(), slog.LevelDebug) {
		return // the Sprintf is the expensive part; skip it when nobody is listening
	}
	e.logger.Debug(fmt.Sprintf(format, a...))
}

func proceedWord(ok bool) string {
	if ok {
		return "proceed"
	}
	return "deny"
}

// patchEntity applies a tony patch to an entity's payload, atomically.
//
// Via Modify (compare-and-swap) so two triggers advancing the same entity cannot lose each
// other's writes — and now at DEPTH, since a patch merges subtrees where the old field
// overlay replaced whichever top-level key it touched. The error is captured out of the
// closure because Modify's fn cannot fail; fn may run more than once under retry, so the last
// run's verdict is the one that stands.
//
// THIS IS THE ACTION'S WRITE, and so the one place a causal chain is carried forward. chain
// names the firing (its trigger, then any hook or sequence descent) and parent is the revision
// of the commit that woke it — 0 when nothing this engine wrote did, which is how a chain
// begins. Every other write the engine makes goes through e.store and stays outside every
// chain; see modifyOn for why that is the right cut rather than an omission.
func (e *Engine) patchEntity(chain string, parent int64, ref entity.Ref, patch entity.Payload) error {
	// THE CONFINEMENT, at the moment of the write. A static check at install cannot be the
	// whole answer while a derive's address can be templated, and this is the one place every
	// charter-authored write passes through — which is what lets it be a promise rather than a
	// convention each action keeps.
	if err := e.mayWrite(ref); err != nil {
		e.logger.Warn("a charter write was refused: it is outside what this charter may write",
			"chain", chain, "ref", ref.String(), "err", err)
		return err
	}
	var perr error
	if err := e.modifyOn(e.caused(chain, parent), ref, func(cur entity.Payload) entity.Payload {
		// An ABSENT entity is a null document, not a nil one — tony.Patch panics on a nil
		// doc, and null is what "nothing there yet" means in tony anyway. This is what keeps
		// a named subject's patch CREATING the entity, as the old `set` did.
		if cur == nil {
			cur = ir.Null()
		}
		out, err := tony.Patch(cur, patch)
		if err != nil {
			perr = err
			return cur // leave it exactly as it was
		}
		return out
	}); err != nil {
		// The store failing is not the patch failing, and only the second is returned (a firing
		// stops on a patch it could not resolve). Reported here rather than swallowed, and named
		// by chain, because a state transition that did not land is the thing an operator would
		// otherwise have to infer from the absence of whatever it was supposed to wake.
		e.logger.Warn("patch did not land; the firing's state transition was lost",
			"chain", chain, "ref", ref.String(), "err", err)
	}
	return perr
}
