package versed

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/signadot/verse/entity"
)

// The pause before a phase whose read did not happen is asked again, and its ceiling.
const (
	retryWait    = 2 * time.Second
	retryWaitMax = 30 * time.Second
)

// The phases of Start, by the name each reports under. Named once, because a phase that
// follows another names it (awaiting), and status lists them.
const (
	phasePerms       = "loaded the perms mirror"
	phaseSuspensions = "loaded the suspensions"
	phaseTasks       = "reconciled tasks"
	phaseStrata      = "restored the strata"
	phaseCharter     = "restored the charter"
	phasePackages    = "restored packages"
	phaseSystem      = "installed the system charter"
	phaseSystems     = "installed the systems' documents"
	phaseDeadlines   = "armed deadlines"
	phaseSchedules   = "armed schedules"
	phaseAsks        = "armed ask expiries"
	phaseAsksWait    = "waited on asks"
	phaseSources     = "restored sources"
	// A runner installed without its offers (--no-probe, or a pod that was not up) is asked
	// what it offers, so a spawn naming it is checked rather than refused for want of an answer.
	phaseRunners = "probed runners"
	// After the sources: a charter verse ships at the system level sends to places the ios
	// read, and an outbox is checked against the registered ios, which the restore registers.
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

// loaded runs one of the baseline charter's loads under the charter lock — a phase asked again
// runs beside a serving verse, and an operator's install must not race it — and sorts its
// answer: a read that did not happen is the phase's to ask again, anything else is a rule or a
// level that would not restore, reported and skipped so that one bad record does not cost the
// rest. A lock that could not be taken in time is asked again too.
func (s *Server) loaded(load func() error) error {
	release, err := s.lockCharter(s.ctxOrBackground())
	if err != nil {
		return entity.Unread(err)
	}
	defer release()
	err = load()
	if entity.IsUnread(err) {
		return err
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "verse: charter: %v\n", err)
	}
	return nil
}
