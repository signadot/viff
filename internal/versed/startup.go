package versed

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/signadot/verse/internal/trigger"
	"github.com/signadot/verse/pkg/entity"
)

// The pause before a phase whose read did not happen is asked again, and its ceiling; and how
// long a phase waits for an engine's charter to be read from its records before it says so and
// is asked again (charterIn).
const (
	retryWait    = 2 * time.Second
	retryWaitMax = 30 * time.Second
	restoreWait  = 2 * time.Second
)

// The phases of Start, by the name each reports under. Named once, because a phase that
// follows another names it (awaiting), and status lists them.
const (
	phasePerms       = "loaded the perms mirror"
	phaseSuspensions = "loaded the suspensions"
	phaseTasks       = "reconciled tasks"
	phaseCharter     = "restored the charter"
	phasePackages    = "restored packages"
	phaseSettle      = "settled tasks and claims"
	phaseSystem      = "installed the system charter"
	phaseSystems     = "installed the systems' documents"
	phaseDeadlines   = "armed deadlines"
	phaseSchedules   = "armed schedules"
	phaseAsks        = "armed ask expiries"
	phaseCharters    = "armed charter expiries"
	phaseAsksWait    = "waited on asks"
	phaseSources     = "restored sources"
	// A runner installed without its offers (--no-probe, or a pod that was not up) is asked
	// what it offers, so a spawn naming it is checked rather than refused for want of an answer.
	phaseRunners = "probed runners"
	// After the sources: a charter verse ships at the system level sends to places the iods
	// read, and an outbox is checked against the registered iods, which the restore registers.
	phaseSystemTexts = "installed the system charters"
)

// phase runs one phase of Start and says how long it took, on stderr with the rest of the
// startup: startup is several store-shaped phases in a row, and nothing between them said which
// was slow until each had a line.
//
// A READ THAT DID NOT HAPPEN IS NOT AN EMPTY ANSWER. Each phase lists a slice of the store and
// acts on what it finds, and a listing that hit its deadline finds nothing — a verse that took
// that as the answer came back with its sources registered and none running, and stayed that
// way until the next restart. So a phase that reports its read did not happen (entity.IsUnread)
// is asked again, with a growing pause, until it answers or the server stops. It is asked in
// the background, because the phases after it do not depend on its answer and the server is
// serving meanwhile; while it waits it is in Restoring, which is what status shows.
//
// Any other error the phase reports is what it was before: printed, and the phase is over.
func (s *Server) phase(name string, fn func() error) {
	t0 := time.Now()
	err := fn()
	if !entity.IsUnread(err) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "verse: %s: %v\n", name, err)
		}
		fmt.Fprintf(os.Stderr, "verse up: %s in %s\n", name, time.Since(t0).Round(time.Millisecond))
		return
	}
	fmt.Fprintf(os.Stderr, "verse up: %s: asked again until it answers: %v\n", name, err)
	s.waitMu.Lock()
	if s.waiting == nil {
		s.waiting = map[string]bool{}
	}
	s.waiting[name] = true
	s.waitMu.Unlock()
	go s.again(name, fn, t0)
}

// again asks a phase for its read until the read happens.
func (s *Server) again(name string, fn func() error, t0 time.Time) {
	defer func() {
		s.waitMu.Lock()
		delete(s.waiting, name)
		s.waitMu.Unlock()
	}()
	ctx := s.ctxOrBackground()
	wait := s.retryWait
	if wait <= 0 {
		wait = retryWait
	}
	for tries := 2; ; tries++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		err := fn()
		if entity.IsUnread(err) {
			if ctx.Err() != nil {
				return
			}
			wait = min(wait*2, retryWaitMax)
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "verse: %s: %v\n", name, err)
		}
		fmt.Fprintf(os.Stderr, "verse up: %s on try %d, %s after the start\n", name, tries,
			time.Since(t0).Round(time.Millisecond))
		return
	}
}

// Restoring is the phases of Start still being asked again, sorted: each read the store and
// the store did not answer, so what it would have restored is not running yet. Empty on a
// verse that is up.
func (s *Server) Restoring() []string {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if len(s.waiting) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.waiting))
	for name := range s.waiting {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// awaiting reports, as a read that did not happen, that a phase this one follows is still being
// asked again. The order between phases is load-bearing — a selection resumed before the
// charter is back is settled as asked by a rule that is gone, a source's first pass before the
// rules are armed is an edge nothing saw — and Start runs them in order once; a phase asked
// again keeps the order by waiting for what it follows.
func (s *Server) awaiting(phases ...string) error {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	for _, p := range phases {
		if s.waiting[p] {
			return entity.Unread(fmt.Errorf("it follows %q, which is still being restored", p))
		}
	}
	return nil
}

// charterIn waits for an engine's charter to be read from its records (trigger.Engine.Restored):
// the follow's opening picture, every rule and level standing in the store placed, armed or
// pending with why. A charter not in by restoreWait is reported as a read that did not happen,
// with what the store is doing for the watch, so the phase is asked again until it is and
// status says so meanwhile (Restoring). what names the charter in that report.
func (s *Server) charterIn(ctx context.Context, eng *trigger.Engine, what string) error {
	wait := s.restoreWait
	if wait <= 0 {
		wait = restoreWait
	}
	select {
	case <-eng.Restored():
		return nil
	case <-ctx.Done():
		return nil
	case <-time.After(wait):
		return entity.Unread(fmt.Errorf("%s is being read from its records: %s", what, eng.FollowState()))
	}
}

// restoreScopeCharter waits for the scope's own charter to be read from its records, for the
// reason Start waits for baseline's: a scope's engine is made on demand, by a request that then
// reads or writes its charter. A charter not in by then is logged, as Start's are, and arrives
// when the store answers: the engine follows its records whether or not anybody waited.
func (s *Server) restoreScopeCharter(ctx context.Context, eng *trigger.Engine, scope string) bool {
	if err := s.charterIn(ctx, eng, "the charter of scope "+scope); err != nil {
		fmt.Fprintf(os.Stderr, "verse: charter (scope %s): %v\n", scope, err)
		return false
	}
	return ctx.Err() == nil
}

// SeedError is what the permission seed refused with, nil when it landed. A server holding one
// has no autonomy and must not serve; `verse up` says so and stops.
func (s *Server) SeedError() error { return s.seedErr }

// StartServing is Start for a server that is about to serve: it takes the lifetime context
// synchronously — scoped engines need it, and a request may arrive immediately — and runs the
// restore behind it.
//
// It exists because `verse up` served nothing until Start returned. The restore is several
// store-shaped phases and there is no reason the API should be absent for them: /healthz
// answers `started: false` with a 503 until they finish, so readiness gates exactly as it did
// while an operator (and the probe) gets an answer instead of a refused connection.
func (s *Server) StartServing(ctx context.Context) {
	s.ctx = ctx
	s.susp.Unload()        // before the first request can be served, which Start's goroutine is not
	s.resuming.Store(true) // likewise: an engine a request makes waits for its tasks
	s.Eng.AwaitTasks()     // and the root package's is shut before a request can hand it one
	go s.Start(ctx)
}

// Start begins the engine's reactive loop (observing the store). It returns
// immediately; the loop runs until ctx is cancelled. It first reconciles orphans —
// task:instance entities a previous run left running — since this is the monolith
// (the task runtime restarts with the engine, so those tasks are gone).
func (s *Server) Start(ctx context.Context) {
	s.ctx = ctx // scoped engines (created later, per scope) start under this
	// Nothing is suspended that the records do not say, and nothing runs until they have been
	// read: the table holds everything from here until phaseSuspensions loads it.
	if s.susp.Loaded() {
		s.susp.Unload()
	}
	// WHERE THE TIME GOES, said out loud, and A READ THAT DID NOT HAPPEN IS ASKED AGAIN: each
	// phase reads the store, and one whose read hit its deadline says so and is retried until
	// it answers, rather than restoring nothing for good (startup.go).
	phase := s.phase
	defer func() { s.restored.Store(true) }()
	// The perms mirror, loaded and watched, so the check every write and read makes costs no
	// round trip: on a busy store the per-write read hit its deadline and refused the write.
	phase(phasePerms, func() error { return entity.Track(ctx, s.Store) })
	// The suspensions before anything they could hold is re-attached or restored: until they
	// have loaded they hold everything, and a store that cannot serve them holds every rule and
	// task, since a suspension that failed open on a restart would resume what somebody stopped.
	phase(phaseSuspensions, s.restoreSuspensions)
	// The root package's engine is shut until the tasks the last verse left are handed to it
	// (reconcileTasks), however long that takes: no rule of it arms ahead of them.
	s.Eng.AwaitTasks()
	s.resuming.Store(true)
	phase(phaseTasks, s.reconcileTasks) // resume remote-hosted tasks; orphan local-dead ones
	// Per-trigger subtree observation — no root watch. The baseline engine watches
	// exactly the (system[,kind]) subtree each trigger matches, the same docd-aligned
	// model the scoped engines use; a mount under verse no longer collides with (and
	// force-ends) a whole-verse root watch. The old root-watch dispatch loop (Start)
	// and its cursor/steering control plane remain in the engine, dormant, for
	// reintroduction when engine-wide steering is wired.
	s.Eng.ObserveScoped(ctx)

	// The durable charter: the engine follows its records from the moment it observes
	// (internal/trigger/follow.go), so the program a restart comes back to is the one it went down with,
	// and this phase is the wait for the records to be in. Each rule opens its own watch as it
	// arms and REPLAYS what already matches it — which is why this follows reconcile: a restored
	// trigger should see a task entity that tells the truth about the restart rather than the
	// `running` a dead runtime left behind.
	//
	// And why it follows ObserveScoped, which is what binds ctx to the engine: anything
	// restored before that runs under a context nothing cancels, so it would outlive the
	// shutdown that was supposed to stop it.
	//
	// A record that could not be placed is reported and left standing, so one bad entity does
	// not cost the rest of the charter. The levels and the rules arrive in one picture, in no
	// order: a rule placed before its level is held for it, and armed when the level is in.
	//
	// This is BASELINE's charter. A scope's own is restored when its engine is built, which
	// happens on demand — the first scoped request, i.e. the dev reconnecting (see
	// scopeEngine). It cannot happen here, because a restart cannot enumerate the scopes that
	// have one: a scope's layer is invisible from baseline and nothing registers a scope's
	// existence. So a scope nobody has come back to sits inert, its tasks and claims with it
	// (resumeScope).
	phase(phaseCharter, func() error {
		if err := s.awaiting(phaseSuspensions); err != nil {
			return err
		}
		return s.charterIn(ctx, s.Eng, "the charter")
	})
	// The charter PACKAGES, each into an engine of its own: a package is a stamp on records
	// anyone can list, so unlike a scope a restart can bring every one of them back rather than
	// waiting for somebody to reconnect. It follows the baseline load because the two split the
	// same listing between them — baseline takes the unstamped records, a package its own.
	phase(phasePackages, func() error {
		err := s.restorePackages()
		s.packagesRestored.Store(err == nil)
		return err
	})
	// What the last verse left to a package that is not there is stopped, and that package's
	// claims removed: which packages there are is the restore above's to say. Every other task
	// and claim is its own engine's, handed over and settled as the engine opens.
	phase(phaseSettle, s.settleTasks)
	// And the charter the deployment itself stands behind, as the system. After the restore,
	// so that it is installed over what stood rather than under it, and every time, since an
	// install of what already stands is a no-op with a revision attached and the file is the
	// truth about what the system's charter is.
	phase(phaseSystem, func() error {
		// Over what stood, so it waits for the restore to have answered; a restore still
		// being asked again is a charter this must not install under.
		if err := s.awaiting(phaseCharter, phasePackages); err != nil {
			return err
		}
		s.installSystemCharter()
		return nil
	})
	// And the systems' documents, which say what a task may do (package systems): kept where
	// a charter's gates can be read against them, over what stood, every time, since the
	// binary's documents are the truth about what the systems are.
	phase(phaseSystems, s.installSystems)

	// And the clocks. A question's deadline is durable and the timer that fires it is not.
	phase(phaseDeadlines, s.armDeadlines)
	// And the schedules, for the same reason: the record holds `next`, this process holds the
	// timer.
	phase(phaseSchedules, s.armSchedules)
	// And the asks' expiries, for the same reason: the ask holds `expires`, this process the timer.
	phase(phaseAsks, s.armAsks)
	// And the charters', the same again: a package's declaration holds its `expires`.
	phase(phaseCharters, s.armCharters)
	// And a person's asks: each that waits is waited on again, since the wait was the last
	// process's, and a yes whose `then` had not started starts.
	phase(phaseAsksWait, s.resumeAsks)
	// And the sources, last: each one's first pass is a write the restored charter should be
	// standing to react to. A level-triggered pass does not repeat itself, so what a rule
	// missed by being late it would miss for good.
	phase(phaseSources, s.restoreSources)
	phase(phaseRunners, s.probeRunners)
	// And the charters verse ships at the system level, rendered by `verse up` from the config
	// (Server.SystemCharters). After the sources: such a charter declares outboxes to the places
	// the iods read, and the declaration is checked against the iods the restore registered. Over
	// what stood, as the system charter file is, and every time, for the same reason.
	phase(phaseSystemTexts, func() error {
		if err := s.awaiting(phaseSystem, phaseSources); err != nil {
			return err
		}
		s.installSystemCharters()
		// And what the system installed that the config no longer names goes, which is how
		// dropping a charter from the config turns it off.
		s.pruneSystemCharters()
		return nil
	})
	s.reportActingAsNobody()
}
