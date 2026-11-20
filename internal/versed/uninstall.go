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
// The lock is the package's own (trigger.Engine.Close). An install at the address meanwhile is
// refused as a conflict, and so is a change to one rule or level of it. A hook that would
// launch is refused whole, its write too: nothing of the package starts once it is locked.
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

// stopAndTakeDown is one uninstall, start to end.
func (s *Server) stopAndTakeDown(store entity.Store, at string) error {
	ctx := s.ctxOrBackground()
	eng, err := s.packageEngine(at)
	if err != nil {
		return err
	}
	if err := s.cancelWork(ctx, eng, at, 0); err != nil {
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
	if err := s.takeDown(store, at); err != nil {
		eng.Reopen() // what was not removed stands, and runs
		return err
	}
	return nil
}

// cancelWork locks the engine of the package at an address and cancels its tasks, and answers
// once every one has ended and its end is taken (trigger.Engine.Busy). A cancel that fails is
// asked again. within bounds the wait where it is not 0, and the engine stays locked past it.
func (s *Server) cancelWork(ctx context.Context, eng *trigger.Engine, at string, within time.Duration) error {
	// Open first: an engine takes no end until it is, and its tasks are all in by then.
	if opened := eng.Opened(); opened != nil {
		select {
		case <-opened:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	eng.Close()
	why := "the charter package at " + at + " is being uninstalled"
	var bound <-chan time.Time
	if within > 0 {
		bound = time.After(within)
	}
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
		case <-bound:
			return fmt.Errorf("%s: its tasks had not ended within %s", at, within)
		case <-time.After(wait):
		}
	}
}

// takeDownStopping is takeDown for a caller that holds the charter lock and cannot let go of it:
// a prune, inside the install it is part of. The package's tasks are cancelled and their ends
// taken for as long as stopWait, and what has not ended by then is stopped with no hook run
// (stopPackage).
func (s *Server) takeDownStopping(store entity.Store, at string) error {
	eng := s.packageEngineIfRunning(at)
	if eng == nil {
		return s.takeDown(store, at)
	}
	if err := s.cancelWork(s.ctxOrBackground(), eng, at, s.stopWait); err != nil {
		fmt.Fprintf(os.Stderr, "verse: uninstall %s: %v; they are stopped with no hook run\n", at, err)
	}
	if err := s.takeDown(store, at); err != nil {
		eng.Reopen()
		return err
	}
	return nil
}
