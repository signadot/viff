package versed

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/signadot/verse/internal/trigger"
	"github.com/signadot/verse/pkg/entity"
)

// UNINSTALLING A CHARTER PACKAGE, in order:
//
//	1. the package is locked: nothing of it is launched, and its charter is not changed
//	2. its tasks are cancelled, each asked until it takes the cancel, and each one's end is
//	   taken as any end is: its onCancelled runs, its claim comes off, its sequence ends
//	3. its rules, levels, schedules and records are removed, and its engine
//	4. the lock comes off
//
// The lock is the package's own, and stands from the moment the uninstall is asked for until it
// is over (lockedAt). An install at the address meanwhile is refused, to be asked again (429),
// and so is the removal of one rule, level or schedule of it: every change to a package asks. Its engine
// is closed under it (trigger.Engine.Close): a hook that would launch is refused whole, its
// write too, and nothing of the package starts.
//
// STEP 2 TAKES AS LONG AS THE TASKS DO, and is not done under the lock every charter operation
// shares (charterlock.go): that one is taken for step 3 alone. A task whose runner does not
// answer the cancel holds the uninstall there, the package still installed and locked, until it
// does or the task ends. The uninstall runs on the server's account and not the request's: a
// request that goes away leaves it running, and one that asks again joins it. A verse that
// restarts meanwhile comes back with the package installed and not locked, its tasks attached
// again, for the uninstall to be asked again.

// uninstalling is one package's uninstall under way: done closes when it is over, and err is
// how it ended.
type uninstalling struct {
	done chan struct{}
	err  error
}

// uninstall takes the package at an address down, joining an uninstall of it that is under way.
// It answers when the uninstall is over, or when ctx ends, which leaves the uninstall running.
func (s *Server) uninstall(ctx context.Context, store entity.Store, at string) error {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return err
	}
	key := "at:" + confine.Stamp()
	s.scopeMu.Lock()
	u := s.uninstalls[key]
	if u == nil {
		u = &uninstalling{done: make(chan struct{})}
		s.uninstalls[key] = u
		go func() {
			err := s.stopAndTakeDown(store, at)
			s.scopeMu.Lock()
			delete(s.uninstalls, key)
			s.scopeMu.Unlock()
			u.err = err
			close(u.done)
		}()
	}
	s.scopeMu.Unlock()
	select {
	case <-u.done:
		return u.err
	case <-ctx.Done():
		return fmt.Errorf("%s: its tasks have not all ended, and it is removed once they have: %w", at, ctx.Err())
	}
}

// lockedAt answers the refusal a change to the charter at an address is refused with while the
// package there is being uninstalled, and nil otherwise. Only a package is ever locked: a
// scope's charter and baseline's are not uninstalled.
func (s *Server) lockedAt(scope, at string) error {
	if scope != "" || at == "" {
		return nil
	}
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return nil // no address: whoever resolves it says so
	}
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if s.uninstalls["at:"+confine.Stamp()] == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", confine.Stamp(), trigger.ErrClosing)
}

// stopAndTakeDown is one uninstall, start to end.
func (s *Server) stopAndTakeDown(store entity.Store, at string) error {
	ctx := s.ctxOrBackground()
	eng, err := s.packageEngine(at)
	if err != nil {
		return err
	}
	if err := s.cancelWork(ctx, eng, at); err != nil {
		return err // verse is stopping
	}
	var release func()
	for {
		if release, err = s.lockCharter(ctx); err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	defer release()
	if err := s.takeDown(store, at, eng); err != nil {
		eng.Reopen() // what was not removed stands, and runs
		return err
	}
	return nil
}

// cancelWork closes the engine of the package at an address and cancels its tasks, and answers
// once every one has ended and its end is taken (trigger.Engine.Busy). A cancel that fails is
// asked again.
func (s *Server) cancelWork(ctx context.Context, eng *trigger.Engine, at string) error {
	// Open first: an engine takes no end until it is, and its tasks are all in by then.
	// One that was stopped before it opened never opens: an install at an address that had no
	// charter made it, and was refused whole (stopPackage). Nothing is installed there then.
	for opened, isOpen := eng.Opened(), false; opened != nil && !isOpen; {
		select {
		case <-opened:
			isOpen = true
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
			if eng.Stopped() {
				return fmt.Errorf("%s: the charter that was being installed there was refused, and nothing is installed", at)
			}
		}
	}
	eng.Close()
	why := "the charter package at " + at + " is being uninstalled"
	asked, said := map[string]bool{}, map[string]bool{}
	for wait := 50 * time.Millisecond; ; wait = min(2*wait, 2*time.Second) {
		for _, r := range eng.Running() {
			if asked[r.ID] {
				continue
			}
			if err := r.Handle.Cancel(why, "system:verse"); err == nil {
				asked[r.ID] = true
			} else if !said[r.ID] {
				said[r.ID] = true
				fmt.Fprintf(os.Stderr, "verse: uninstall %s: task %s did not take its cancel, and is asked again: %v\n", at, r.ID, err)
			}
		}
		if !eng.Busy() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
