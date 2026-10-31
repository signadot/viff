package trigger

//go:generate go tool tony-codegen -dir .

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// Spawner turns a (bound) launch.Spec into a running, hosted task and returns
// it. It owns the full lifecycle — resolve the spec's references (driver, runner,
// resolver, scope, environment), build, mirror into the verse, and run/host — so
// the engine stays agnostic to how tasks are hosted (a runnerd runtime here,
// an RPC to a remote runtime when distributed).
type Spawner func(id string, spec launch.Spec) task.Handle

// Engine runs the ECA loop over an entity-verse.
//
// ONE BASELINE ENGINE PER STORE. Everything the engine knows about what it has already done is
// per-engine and in memory — the commits each trigger was evaluated at (seen), the in-flight firings (inflight),
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
//     commit, once in each engine.
//
// Nothing in the tree runs two peers on one baseline today. This is the constraint that breaks
// if something starts to, and the causal-depth throttle designed in
// docs/cycle-throttle.md inherits it exactly: a chain that runs between two peer engines
// is throttled by neither, because each sees only its own half of it.
type Engine struct {
	store    entity.Store
	resolver task.Resolver // answers trigger ASK gates
	// elsewhere answers whether a rule name is installed in another engine of this verse.
	spawn Spawner // optional: how actions spawn tasks
	// moveDeadline moves a standing question's deadline (SetDeadlineMover); nil leaves a date a
	// question reads from its subject fixed at the instant it was posed with.
	moveDeadline func(scope, id string, at time.Time) error
	offerCheck   func(l runner.Launch) error // optional: whether some runner offers what the spawn asks
	triggers     []Trigger
	// logger carries both the running dispatch commentary (Debug) and anything an
	// operator has to act on (Warn). One channel with levels, rather than a verbose
	// io.Writer that defaulted to io.Discard and that nothing ever wired up — which meant
	// a charter that could not do what it said reported itself into a void. slog.Default
	// puts Warn on stderr, alongside the logd/docd records already landing there.
	logger *slog.Logger
	clock  func() time.Time // the system clock (injectable for tests); nil = time.Now

	mu    sync.Mutex
	fired int // entities acted on, for the trace and for tests; see countFire
	// troubles is what went wrong at each rule's firings, the latest of each kind (trouble.go).
	troubles map[string]Trouble

	// seen is the commits each trigger has been evaluated at, which makes one verdict per
	// (trigger, commit). See firstSight.
	seenMu sync.Mutex
	seen   map[string]*sight

	// causes places the commits this engine wrote in their causal chains: revision → how far
	// it stands from the external write that started it, and what wrote it. A commit that is
	// not here was caused by somebody else, which is the answer for a person, a task, a
	// mirror, a peer, and a restart alike. Written by a firing's store (see causal.go), read
	// per commit on the dispatch path — so it is guarded on its own rather than behind the
	// loop, which firings do not run on.
	// confine is the subtree this engine's charter may write in — zero for a baseline engine,
	// which may write anywhere. See confine.go.
	confine Subtree
	// reads is what this engine's charter declares it READS outside its confinement. Empty on
	// a package means it reads only inside its own address; meaningless on baseline, which is
	// unconfined. See reads.go.
	reads   []Subtree
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
	//
	// Each carries what the firing is asking (flight), so a suspension can withdraw a question
	// from in front of people while the firing keeps its place and waits to ask it again.
	flightMu sync.Mutex
	inflight map[string]*flight

	// parked is the launches a suspension stopped at launchSpawn, in the order they arrived —
	// a hook's or a sequence step's spawn about suspended work — each started when nothing
	// holds it. It does not survive a restart. Guarded by parkMu.
	parkMu sync.Mutex
	parked []parkedLaunch

	// lifted is closed and replaced whenever a suspension lifts, so a firing waiting on one
	// wakes and asks whether it is still held (awaitLift). Guarded by liftMu.
	liftMu sync.Mutex
	lifted chan struct{}

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
	// dropped is why this engine's live work was cancelled whole (CancelLive), "" until it is:
	// a task tracked after that is cancelled as it arrives rather than run with nothing to own it.
	dropped string

	// the charter (e.triggers, declared above). Guarded because triggers can be
	// added/removed at runtime (over the wire) while the loop dispatches.
	trigMu sync.Mutex

	// strata is the declared level ordering — few, changing rarely, where rules are many and
	// change often. Guarded by trigMu with the charter it orders, because every level check
	// reads both and a check split across two locks would be answering about two different
	// charters. See stratum.go.
	strata []Stratum

	// Per-trigger (subtree) observation mode. A scoped engine opens NO root watch; instead each trigger added gets a watch on exactly
	// the (system,kind) subtree it matches. bgctx bounds those watchers.
	subtree bool
	bgctx   context.Context
	// scope is the scope this engine acts in — "" for baseline. It is written onto
	// every rule this engine persists, and read back to tell the scope's OWN charter from
	// baseline's, which a scoped store reads through and cannot otherwise distinguish. See
	// SetScope.
	scope string
	// watches holds, per armed rule, the handles of its watches and the cancel that stops
	// the goroutines firing on them (watches.go). In subtree mode a rule's observation is its
	// own, so the charter alone no longer says what fires: removing a rule has to stop them,
	// or a removed rule keeps firing off a watch nobody can see. Guarded by trigMu, with the
	// charter it mirrors.
	watches map[string]*ruleWatches
	// pending is every rule of the charter that is not armed, with why (reconcile.go). Guarded
	// by trigMu.
	pending []held
	// placed counts the rules ever placed among the pending ones; each carries its number (held.gen).
	placed uint64
	// kicks asks the reconciler for a pass; reconciling says one is running to ask; every is
	// the backstop between passes (reconcileEvery; short in tests).
	kicks       chan struct{}
	reconciling atomic.Bool
	every       time.Duration

	// susp is the scope's suspension table (suspend.go), asked wherever work would make
	// progress; nil suspends nothing. Set once, before anything is installed.
	susp *Suspensions

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
		watches:  map[string]*ruleWatches{},
		kicks:    make(chan struct{}, 1),
		every:    reconcileEvery,
	}
}

func (e *Engine) SetSpawner(s Spawner) { e.spawn = s }

// SetDeadlineMover installs what moves a standing question's deadline, which is versed's: the
// instant is stamped on the question's record and a timer settles it, and both are kept where
// the record is. A gate reading its date from the subject calls it when the date is edited.
func (e *Engine) SetDeadlineMover(m func(scope, id string, at time.Time) error) { e.moveDeadline = m }

// SetOfferCheck sets what an install checks a spawn against: whether some runner offers its doer
// and holds its environment, as the runners described themselves. The launch is the spawn as
// written, with its templated values standing in. Optional: with none set, the firing says.
func (e *Engine) SetOfferCheck(f func(l runner.Launch) error) { e.offerCheck = f }

// SetScope names the scope this engine acts in — a named scope, or "" for
// baseline. Set before anything is installed, because it is stamped on what this engine
// persists.
//
// It exists because a scoped store READS BASELINE THROUGH: listing `trigger:rule:*` in a
// scope returns baseline's rules and the scope's own, with nothing on the record to tell
// them apart, so a restore either resurrects nothing or installs the whole baseline charter
// into one scope. Neither is what `verse c -a --scope me` promised. The
// scope's name on the record answers it exactly, at the one moment the answer is known: the
// install itself.
//
// A record carrying `scope: me` also answers the same question for a reader — `verse data
// get trigger:rule:x --scope me` says which scope the rule belongs to rather than
// leaving it to be inferred from which listing it turned up in.
func (e *Engine) SetScope(name string) { e.scope = name }

// Scope is the scope this engine acts in, "" for baseline.
func (e *Engine) Scope() string { return e.scope }

// SetLogger redirects engine logging. Debug carries dispatch detail (off by default),
// Warn carries misconfiguration an operator must act on.
func (e *Engine) SetLogger(l *slog.Logger) { e.logger = l }
func (e *Engine) Store() entity.Store      { return e.store }

// Fired reports how many entities triggers have acted on — the trace counter. It only ever
// rises: countFire increments per firing that proceeds and nothing decrements or persists it,
// so this is cumulative for the life of THIS engine, and a test reading 0 is asserting that
// nothing has fired at all rather than that nothing is in flight. What is in flight is Flows.
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
	e.bgctx = ctx            // bounds what the engine starts in the background — cancelled on stop
	e.cursor = e.store.Rev() // start live from now; Observe() delivers rev>cursor
	// NO PATTERNS: this loop asks each commit whether it touched what a rule reads
	// (Trigger.touches), which the commit answers from its own delta. The task records it acts
	// on per entity are watched where they stand, below.
	ch, unsub := e.store.Observe()
	go e.loop(ctx, ch, unsub)
	e.watchTaskRecords(ctx)
	e.startReconciler(ctx)
}

// startReconciler runs the reconciler under ctx, once, and asks for a pass: the caller has just
// placed rules for it, whether or not it was running already.
func (e *Engine) startReconciler(ctx context.Context) {
	if !e.reconciling.Swap(true) {
		go e.reconcile(ctx)
	}
	e.kick()
}

// ObserveScoped puts the engine in per-trigger (subtree) observation mode: it opens
// NO root watch. Each trigger added afterward gets its own watch on exactly the
// (system,kind) subtree it matches — the docd-aligned model (watch what you match,
// not the whole verse). Used for a scoped engine; ctx
// bounds every subtree watcher, so cancelling it stops them all. Baseline engines use
// Start instead. Actions still run on this engine's (scoped) store and spawner.
func (e *Engine) ObserveScoped(ctx context.Context) {
	e.bgctx = ctx
	e.subtree = true
	// Any trigger added before we entered subtree mode (e.g. the baseline charter seeded at
	// startup) has no watch: it goes back to pending, for the reconciler to arm, so entering
	// subtree mode is independent of whether triggers were added before or after this call.
	e.trigMu.Lock()
	for _, t := range e.triggers {
		e.hold(t, cause{causePass, ""})
	}
	e.triggers = nil
	e.trigMu.Unlock()
	e.startReconciler(ctx)
	// Watch the task:instance mirror subtree directly, independent of any charter trigger:
	// this drives store-driven reaping AND completion-hook (onX) application in scoped mode,
	// where no root dispatch loop runs. Store-driven means race-free for a fast task (unlike
	// the reapOnDone bus); reapOnDone remains the backstop, and the atomic retire dedupes.
	// Where the records are, and at their own length: a run's record is `mirror.Ref(id)`, so the
	// watch is on the place above it and reports each record whole.
	e.watchTaskRecords(ctx)
}

// watchTaskRecords opens the watch the completion path reads: the task records, at their own
// length, wherever the engine runs. Store-driven means race-free for a fast task — the property
// reapOnDone's live-from-now bus lacks — and the atomic retire makes the two exactly-once.
func (e *Engine) watchTaskRecords(ctx context.Context) {
	at := mirror.Ref("").Segs() // verse.task.instance.<id>, without the id
	at = at[:len(at)-1]         // …and without the kind: a run record is taskish too
	if ch, unsub, err := e.store.WatchSubtree(len(at)+2, at...); err == nil {
		go e.reapLoop(ctx, ch, unsub)
	} else {
		e.tracef("task record watch: %v", err)
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
//
// ONLY AN EXPIRED CURSOR RESYNCS. Skipping to current is how work gets DROPPED — every commit
// between the cursor and now goes undispatched, so a rule that should have fired never does —
// and it is the right answer for exactly one failure: the history is gone and no amount of
// asking will bring it back. Every other failure (the store unreachable, the replay cut off at
// its budget, a watch ended under it) leaves the cursor where it is, so the next pass asks
// again for the same gap. Reported as one bit, all of them resynced (git-issue
// 3qf2z59dh12ksdazg9n0).
//
// A partial replay is dispatched before either decision: those commits happened, and advancing
// over them is what makes the next attempt smaller.
func (e *Engine) catchUp(ctx context.Context) {
	commits, err := e.store.Since(e.cursor)
	for _, c := range commits {
		if c.Rev <= e.cursor {
			continue // Since is ordered and monotonic; skip anything not ahead
		}
		e.cursor = c.Rev
		e.dispatch(ctx, c)
	}
	switch {
	case err == nil:
	case errors.Is(err, entity.ErrReplayTooOld):
		e.tracef("cursor %d is older than the store retains; resync to current: %v", e.cursor, err)
		e.cursor = e.store.Rev()
	default:
		e.tracef("catch-up from cursor %d did not complete, and the cursor stands: %v", e.cursor, err)
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
// only consulted to decide whether asking is worth it (Trigger.touches). Reaping runs off its
// own watch now (watchTaskRecords), so this asks every trigger and nothing else.
func (e *Engine) dispatch(ctx context.Context, c entity.Commit) {
	e.trigMu.Lock()
	trigs := make([]Trigger, len(e.triggers))
	copy(trigs, e.triggers)
	e.trigMu.Unlock()
	e.subjectsMoved(c) // a paced firing about an entity this commit wrote is stale; let it go
	for i := range trigs {
		// AS THE RULE'S OWNER SEES IT: a change to what the owner may not read is not a
		// change this rule is woken by, and the skip is asked of the narrowed commit so a
		// rule whose only visible change was withheld does not evaluate on it either.
		seen := e.visibleTo(trigs[i], c)
		if !trigs[i].touches(seen) {
			continue // nothing it reads changed; the verdict cannot have (see touches)
		}
		e.evaluate(ctx, trigs[i], seen)
	}
}

// reader is the store a rule READS through: the engine's store acting as the rule, so that a
// charter reads as its owner and matches only what its owner can read. A store that
// attributes nothing — a test's raw store — is read as it is.
func (e *Engine) reader(name string) entity.Store {
	if entity.PolicyOf(e.store) == nil {
		return e.store
	}
	return entity.As(e.store, e.Principal(name))
}

// visibleTo is a commit as the rule's owner may see it.
func (e *Engine) visibleTo(t Trigger, c entity.Commit) entity.Commit {
	r := e.reader(t.Name)
	if r == e.store {
		return c
	}
	return c.Only(func(ref entity.Ref) bool {
		ok, err := entity.Holds(r, entity.PrincipalOf(r), entity.Read, ref)
		return err == nil && ok
	})
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
	// READ AS WHOEVER WRITES. A firing's store reads as the engine, which is the system and sees
	// everything, and writes as the rule; a patch reads the node, changes it and writes the
	// whole of it back — so read as the system, what it writes back carries every place inside
	// the node, somebody else's included, and the write is refused for reaching into them: no
	// rule patches a row that holds another party's place. Read as the rule, the node comes
	// back without what the rule may not read, and what is written is the rule's.
	if c, ok := s.(causal); ok && entity.PolicyOf(c.Store) != nil {
		s = readsAs{Store: c, reader: entity.As(c.Store, c.principal)}
	}
	return entity.Modify(ctx, s, ref, fn)
}

// readsAs is a store whose Get is made through another view of it: the firing's store, reading
// as the rule it writes as.
type readsAs struct {
	entity.Store
	reader entity.Store
}

func (r readsAs) Get(ref entity.Ref) (entity.Entity, bool, error) { return r.reader.Get(ref) }

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
func (e *Engine) patchEntity(chain string, parent int64, subj, ref entity.Ref, patch entity.Payload) error {
	// THE OTHER BACKSTOP: every charter-authored write passes here, so a firing's write about a
	// suspended subject is refused even when the firing began before the suspension landed. A
	// completion hook's writes and a sequence step's are not: they record what a task did, and
	// recording is not progress — what they wake is itself held.
	if !recordsCompletion(chain) {
		if held := e.holding(mirror.TriggerOf(chain), subj); len(held) > 0 {
			err := fmt.Errorf("%s is suspended (%s)", subj, strings.Join(held, ", "))
			e.tracef("%s: write to %s refused: %v", chain, ref, err)
			return err
		}
	}
	// THE CONFINEMENT, at the moment of the write. A static check at install cannot be the
	// whole answer while a derive's address can be templated, and this is the one place every
	// charter-authored write passes through — which is what lets it be a promise rather than a
	// convention each action keeps.
	if err := e.mayWrite(ref); err != nil {
		e.logger.Warn("a charter write was refused: it is outside what this charter may write",
			"chain", chain, "ref", ref.String(), "err", err)
		return err
	}
	// AND VERSE'S OWN HALF, for the same reason at the same moment (reservedWrite).
	if err := e.reservedWrite(chain, ref); err != nil {
		e.logger.Warn("a charter write was refused: verse's half is not a rule's to write",
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
		// WHERE THE PATCH WOULD LAND ON A VALUE, refuse instead. A nested object in a patch
		// merges into an object and REPLACES anything else, so a rule patching a field under a
		// subject whose field holds an array took the array away and reported success. The
		// question is asked here because this is where the current payload is in hand — see
		// entity.PatchLands, and git-issue mbjpdr6sh12ksa5zgsn0.
		if err := entity.PatchLands(cur, patch); err != nil {
			perr = err
			return cur // leave it exactly as it was
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
