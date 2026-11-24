package trigger

import (
	"context"
	"errors"
	"sort"

	"github.com/signadot/verse/pkg/task"
)

// Running is a task an engine tracked when it was stopped (Stop): its id, the commit its firing
// was caused by (0 where none was), and the handle it is cancelled through.
type Running struct {
	ID         string
	Occurrence int64
	Handle     task.Handle
}

// ErrClosing is why a launch, or a change to the charter, is refused in an engine whose charter
// is being taken down (Close).
var ErrClosing = errors.New("the charter package is being uninstalled")

// TAKING A CHARTER DOWN is two steps, the host's to drive.
//
// Close locks it: from then nothing is launched and the charter is not changed, while the ends
// of the tasks that are running are still taken, each with its hook. The host cancels those
// tasks and waits until the engine is not Busy: every task has ended, and every end is taken.
// Then the charter's records are removed and Stop ends what is left.
//
// WORK IS ASKED FOR BEFORE IT BEGINS, under the lock the closing and the stop are set under,
// and counted until it returns. What may launch asks begin: a firing (fireOne), an action that
// spawns or carries a sequence (Action.apply), the steps a sequence runs off an element's end
// (resumeSeq), the start of what a suspension parked (releaseLaunches), and the settling of
// what the last verse left (open). The end of a task
// asks beginHook (applyHook). So a close waits for a launch that began and refuses one that
// asks later, and a stop does the same for the ends.

// Close locks the engine for its charter to be taken down. When it returns nothing is being
// launched and nothing will be: a firing waiting on a gate has ended, its question off the
// docket, and the launches a suspension parked are dropped, each ended as cancelled. A hook
// still runs when a task ends, unless it would launch: an action that spawns or carries a
// sequence is refused whole, its write too (ErrClosing), with nothing recorded against it, and
// a sequence whose element ends is ended there, cancelled; one whose steps are running is
// waited for. The charter takes no rule and no level, and loses no rule but by its host
// (ErrClosing: RemoveTriggerAs).
func (e *Engine) Close() {
	e.liveMu.Lock()
	e.closing = true
	e.closeContextLocked()
	closeNow := e.closeNow
	e.liveMu.Unlock()
	closeNow()
	e.work.Wait()

	for _, l := range e.takeParked() {
		e.forgetParked(l.id(e))
		// What the launch held is given back as a task of it ending cancelled would: the
		// claim a hooked spawn took, and the run of a sequence it was an element of.
		if l.a.hooked() {
			e.releaseRun(l.chain, l.subj)
		}
		e.stopSeq(l.chain, l.subj, task.Cancelled)
	}
}

// Reopen undoes Close, for a host whose taking down of the charter failed: the rules that stand
// are armed again, as after a lift, so each sees the world as it stands and fires about what it
// let pass while the engine was closed.
func (e *Engine) Reopen() {
	e.liveMu.Lock()
	e.closing = false
	e.closeCtx, e.closeNow = nil, nil
	e.liveMu.Unlock()
	e.reArm(func(string) bool { return true })
}

// Busy reports whether a task of the engine is running, or the end of one is still being taken.
func (e *Engine) Busy() bool {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	return len(e.live) > 0 || e.ends > 0
}

// Running is the tasks the engine tracks.
func (e *Engine) Running() []Running {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	return running(e.live)
}

func running(live map[string]flowTask) []Running {
	out := make([]Running, 0, len(live))
	for id, fp := range live {
		out = append(out, Running{ID: id, Occurrence: fp.occurrence, Handle: fp.h})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Stop ends this engine's part in its work, and answers the tasks it tracked. The host cancels
// the context the engine observes under first.
//
// When it returns nothing of the engine's is in progress, and nothing starts: no launch, and no
// end of a task. A task something started while this waited is tracked, and is among the tasks
// answered.
//
// The tasks are answered and not cancelled: stopping each, and what it holds until it has
// ended, is the host's, which outlives the engine. No hook runs for them. The launches a
// suspension parked are dropped, and their records with them. A task tracked after this is
// cancelled as it is tracked, as after CancelLive.
func (e *Engine) Stop(why string) []Running {
	e.liveMu.Lock()
	e.closing, e.halted = true, true
	e.closeContextLocked()
	closeNow := e.closeNow
	e.liveMu.Unlock()
	closeNow()
	e.work.Wait()
	e.hooks.Wait()

	e.liveMu.Lock()
	live := e.live
	e.live = map[string]flowTask{}
	e.dropped = why
	e.liveMu.Unlock()

	for _, l := range e.takeParked() {
		e.forgetParked(l.id(e))
	}
	return running(live)
}

// Stopped reports whether the engine was stopped (Stop).
func (e *Engine) Stopped() bool {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	return e.halted
}

// takeParked takes every launch a suspension parked.
func (e *Engine) takeParked() []parkedLaunch {
	e.parkMu.Lock()
	defer e.parkMu.Unlock()
	parked := e.parked
	e.parked = nil
	return parked
}

// begin counts a piece of work that may launch as in progress, and answers false where the
// engine is closing or was stopped: the work is then not done. The caller says when it is over
// (e.work.Done).
func (e *Engine) begin() bool {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if e.closing || e.halted {
		return false
	}
	e.work.Add(1)
	return true
}

// beginHook counts the taking of a task's end as in progress, and answers false where the engine
// was stopped: the end is then not taken here. The end was owed (retire, owe), and hookDone
// says it is taken.
func (e *Engine) beginHook() bool {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if e.halted {
		e.ends--
		return false
	}
	e.hooks.Add(1)
	return true
}

func (e *Engine) hookDone() {
	e.hooks.Done()
	e.liveMu.Lock()
	e.ends--
	e.liveMu.Unlock()
}

// owe counts the end of a task that was never live as owed, for a caller about to take it
// (applyHook): retire does it for one that was.
func (e *Engine) owe() {
	e.liveMu.Lock()
	e.ends++
	e.liveMu.Unlock()
}

// isClosing reports whether the engine's charter is being taken down.
func (e *Engine) isClosing() bool {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	return e.closing
}

// Closing reports whether the engine's charter is being taken down (Close).
func (e *Engine) Closing() bool { return e.isClosing() }

// closeContext is the context that ends when the engine closes.
func (e *Engine) closeContext() context.Context {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	e.closeContextLocked()
	return e.closeCtx
}

func (e *Engine) closeContextLocked() {
	if e.closeCtx == nil {
		e.closeCtx, e.closeNow = context.WithCancel(context.Background())
	}
}

// OwnerAt is the Owner of the engine of the charter package at an address in baseline.
func OwnerAt(at Subtree) string {
	var path []string
	if at.confined() {
		path = at.Path()
	}
	return ownerOf("", path)
}
