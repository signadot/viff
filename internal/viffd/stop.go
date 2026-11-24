package viffd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"go.viff.dev/viff/internal/trigger"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

// A CHARTER PACKAGE THAT IS NOT THERE HAS NO WORK. Its tasks are cancelled, with no hook run: its
// rules are gone, and a hook is a rule's. That is a package whose records have been removed
// (stopPackage), and one the last viff left tasks of that this viff does not have
// (settleTasks). An uninstall cancels a package's tasks while its rules stand, so that their
// hooks run, those that start nothing (uninstall.go), and comes here with nothing left running. One stop runs for a
// package at a time (stopWork), and it takes
// every task of the package there is: the ones its engine tracked, and the ones the last viff
// left that no engine took.
//
// A CANCEL IS A REQUEST, AND THE TASK'S RECORD SAYS WHEN IT HAS ENDED. Until then the task may
// still be running: a runner takes time to end one, and a runner that cannot be reached has not
// been asked at all. So the claims of the package stand until the records of its tasks have
// ended, a cancel that failed is asked again until then, and no engine is made for the package
// meanwhile (packageEngineLocked). An engine made sooner would find those claims with no task of
// its own behind them and settle them as work that was lost, and its rule could start a second
// run at the id of one that is still ending. Once every record has ended, the claims the package
// held are removed and the address is free.
//
// The subject of a task stopped this way is left as its rule last wrote it.

// stopTask is one task being stopped.
type stopTask struct {
	id         string
	occurrence int64 // the commit its firing was caused by, 0 where not known: which run's record ends it
	h          task.Handle
	asked      bool // its cancel was taken
	said       bool // a cancel that failed has been logged
}

// stopState is a package being stopped: the tasks it still waits on, and done, closed when
// they have ended and its claims are off.
type stopState struct {
	ids  []string
	done chan struct{}
}

// stopWait is how long taking a package down waits for tasks that are still running to have
// ended, so that the address is free when it returns wherever they end promptly. The stop goes
// on past it.
const stopWait = 5 * time.Second

// stopPackage stops the engine at an address and what is left of its charter's work, and forgets
// the engine: takeDown's last step, and the end of an install that was refused whole at an
// address that had no charter before it. It waits up to stopWait for the work to have ended.
func (s *Server) stopPackage(at string) {
	confine, err := trigger.ParseAt(at)
	if err != nil {
		return
	}
	key, owner := "at:"+confine.Stamp(), trigger.OwnerAt(confine)
	// Out of the registry and marked as stopping in one step, so that no engine is made at the
	// address between the two. An engine stands only at an address that is not stopping
	// (packageEngineLocked), so the mark is this stop's own.
	s.scopeMu.Lock()
	pe, ok := s.scopeEngs[key]
	var st *stopState
	if ok {
		delete(s.scopeEngs, key)
		st = &stopState{done: make(chan struct{})}
		s.stopping[owner] = st
	}
	s.scopeMu.Unlock()
	if !ok {
		return
	}
	why := "the charter package at " + at + " was removed"
	pe.cancel()
	var tasks []stopTask
	for _, r := range pe.eng.Stop(why) {
		tasks = append(tasks, stopTask{id: r.ID, occurrence: r.Occurrence, h: r.Handle})
	}
	s.susp.Detach(pe.eng)
	s.stopWork(owner, st, tasks, why)
	select {
	case <-st.done:
	case <-s.ctxOrBackground().Done():
	case <-time.After(s.stopWait):
		fmt.Fprintf(os.Stderr, "viff: uninstall %s: its tasks have not ended yet (%s); nothing is "+
			"installed at the address until they have\n", at, strings.Join(s.stoppingIDs(st), ", "))
	}
}

// stopWork stops the work of the package whose ids carry owner, which the caller marked as
// stopping (st) under scopeMu: the tasks given, which its engine tracked, and the tasks the last
// viff left that no engine took. Each is asked to stop. Once the record of each has ended, and
// the tasks the last viff left are all in to be seen, the claims the package held are removed,
// the mark comes off, and st.done closes.
func (s *Server) stopWork(owner string, st *stopState, tasks []stopTask, why string) {
	go func() {
		ctx := s.ctxOrBackground()
		for wait := 50 * time.Millisecond; ; wait = min(2*wait, 2*time.Second) {
			in := s.leftAreIn() // ahead of the taking, so that a true is of a list that is whole
			tasks = append(tasks, s.takeLeft(owner)...)
			tasks = s.notEnded(tasks, why)
			if len(tasks) == 0 && in && s.removeClaims(owner) == nil {
				break
			}
			ids := make([]string, 0, len(tasks))
			for _, t := range tasks {
				ids = append(ids, t.id)
			}
			s.scopeMu.Lock()
			st.ids = ids
			s.scopeMu.Unlock()
			select {
			case <-ctx.Done():
				return // viff is stopping: the next start finds what is left (settleTasks)
			case <-time.After(wait):
			}
		}
		s.scopeMu.Lock()
		delete(s.stopping, owner)
		s.scopeMu.Unlock()
		close(st.done)
	}()
}

// leftAreIn reports whether the tasks the last viff left are all re-attached and the root package's handed over (reconcileTasks):
// true for a server nobody started, which re-attaches none.
func (s *Server) leftAreIn() bool {
	if !s.resuming.Load() {
		return true
	}
	select {
	case <-s.leftIn:
		return true
	default:
		return false
	}
}

// takeLeft takes the tasks the last viff left to the package whose ids carry owner that no
// engine took: none is handed to an engine from here (handTasks).
func (s *Server) takeLeft(owner string) []stopTask {
	var tasks []stopTask
	s.leftMu.Lock()
	defer s.leftMu.Unlock()
	for _, r := range s.left {
		if r.eng == nil && !r.stopped && r.chain != "" && trigger.OwnerOf(r.id) == owner {
			r.stopped = true
			tasks = append(tasks, stopTask{id: r.id, h: r.h})
		}
	}
	return tasks
}

// notEnded asks each task that has not taken a cancel to stop, and answers the tasks whose
// record does not say this run has ended. A record that could not be read says nothing.
func (s *Server) notEnded(tasks []stopTask, why string) []stopTask {
	var left []stopTask
	for _, t := range tasks {
		if !t.asked {
			if err := t.h.Cancel(why, "system:viff"); err == nil {
				t.asked = true
			} else if !t.said {
				t.said = true
				fmt.Fprintf(os.Stderr, "viff: task %s is not stopped, and its charter package's claims stand until it has ended: cancelling it failed, and is asked again: %v\n", t.id, err)
			}
		}
		ent, ok, err := s.Store.Get(mirror.Ref(t.id))
		if err == nil && ok && task.TerminalStatus(entity.Str(ent.Payload, "status")) &&
			(t.occurrence == 0 || mirror.OccurrenceOf(ent.Payload) == t.occurrence) {
			continue
		}
		left = append(left, t)
	}
	return left
}

// removeClaims removes every claim whose id carries owner, each as it was read. One that is no
// longer as it was read is an error, for the caller to read them again.
func (s *Server) removeClaims(owner string) error {
	claims, err := entity.BelowErr(s.Store, func(r entity.Ref) bool {
		return r.ChildOf(entity.ViffRoot, mirror.System, mirror.ClaimKind)
	}, 1, entity.ViffRoot, mirror.System, mirror.ClaimKind)
	if err != nil {
		return entity.Unread(err)
	}
	for _, claim := range claims {
		if trigger.OwnerOf(claim.Ref.Name()) != owner {
			continue
		}
		took, _, err := s.Store.Write(entity.Write{At: claim.Ref.At(), When: []entity.Require{{Pattern: entity.Unchanged(claim.Payload)}}})
		if err != nil {
			return err
		}
		if !took {
			return fmt.Errorf("claim %s changed as it was removed", claim.Ref.Name()) // read again
		}
	}
	return nil
}

// stoppingIDs is the tasks a stopping package still waits on.
func (s *Server) stoppingIDs(st *stopState) []string {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	return append([]string(nil), st.ids...)
}
