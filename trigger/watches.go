package trigger

import (
	"context"
	"fmt"
	"strings"

	"github.com/signadot/verse/entity"
)

// A rule's watches are asked of the store by the reconciler as it arms the rule (reconcile.go):
// one subtree watch per (system,kind) the trigger's condition READS — its When, or its Subject
// when there is no When — fired on each matching change. Asking the formula which subtrees it
// reads — the same question a Monitor asks — means a When over several subtrees is watched
// correctly for free, and there is no "exactly one match" restriction to enforce.
//
// THE STORE OPENS THEM BEHIND THE ARM. A watch is a handle (entity.Watch), answered at once, and
// what the store is doing for it — in line, opening, open, opening again after a drop, refused
// and asked again — is read off the handle. The rule is armed from the moment it has its
// handles, fires on what they deliver, and `arming:` names it until every one is open.
//
// The goroutines firing on the handles run under a context of this rule's own, derived from
// bgctx, and it is kept with the handles under the rule's name (Engine.watches) — so
// RemoveTrigger can stop exactly these while every other rule keeps observing.

// ruleWatches is what an armed rule holds in subtree mode: the handles of its watches, the
// cancel that stops the goroutines firing on them, and ready, closed once every handle is or
// the rule is stopped.
type ruleWatches struct {
	handles []*entity.Watch
	cancel  context.CancelFunc
	ready   chan struct{}
}

// stop ends the rule's observation: the goroutines, then the subscriptions. A nil rw is a
// rule with none (baseline mode).
func (rw *ruleWatches) stop() {
	if rw == nil {
		return
	}
	rw.cancel()
	for _, w := range rw.handles {
		w.Close()
	}
}

// watchedSubtrees is what a trigger's watch has to cover, or why it cannot be watched at all —
// the part of opening a watch that does not depend on the store answering.
func (e *Engine) watchedSubtrees(t Trigger) ([]Subtree, error) {
	// Everything the trigger READS, not just what dispatches it: a verdict is a function of
	// its cause, its condition and its subject together, so a change to any of them can change
	// it. This is the same list the commit skip is asked about (Trigger.reads), which is what
	// keeps "what it watches" and "what could change its answer" from drifting apart.
	subtrees := t.reads()
	if len(subtrees) == 0 {
		return nil, fmt.Errorf("subtree trigger %q: it reads no subtree to watch", t.Name)
	}
	for _, st := range subtrees {
		if st.Named() == 0 { // a root alone is the whole of a half, which is not watchable
			return nil, fmt.Errorf("subtree trigger %q needs a system in its `on`, its `when` or its "+
				"`subject` (kind optional); a scoped engine opens no root watch, so there is "+
				"nothing to watch without one", t.Name)
		}
	}
	return subtrees, nil
}

// openWatches asks the store for one watch per subtree and starts evaluating the trigger on
// what each delivers, answering the handles without waiting on the store. The error is a
// request the store cannot make at all; the handles already asked for are closed on it.
func (e *Engine) openWatches(t Trigger, subtrees []Subtree) (*ruleWatches, error) {
	ctx, cancel := context.WithCancel(e.bgctx)
	rw := &ruleWatches{cancel: cancel, ready: make(chan struct{})}
	for _, st := range subtrees {
		// AT THE SUBTREE'S OWN DEPTH — the depth the match that named it says its subjects
		// stand at. Reported at the fixed arity instead, a change to anything deeper arrived
		// as a change to its three-segment ancestor, which is not the entity this rule is
		// about (see entity.Store.WatchSubtree).
		w, err := e.store.Watch(st.Path(), st.Pattern(), entity.WatchOptions{WaitIfAbsent: true})
		if err != nil {
			rw.stop()
			return nil, fmt.Errorf("scoped trigger %q: watch %s: %w", t.Name, strings.Join(st.Path(), ":"), err)
		}
		rw.handles = append(rw.handles, w)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case c, ok := <-w.Commits:
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
					// so arriving here IS the skip's answer. firstSight inside evaluate is
					// what keeps several watched subtrees from evaluating one commit several
					// times, and what tells a commit one watch delivers late from a replay.
					e.evaluate(e.bgctx, t, e.visibleTo(t, c))
				}
			}
		}()
	}
	// ready closes once every handle's Ready has, or one of them Failed, or the rule is
	// stopped: either way there is nothing more to wait for, and a waiter left on the channel
	// would wait forever (Ready).
	go func() {
		defer close(rw.ready)
		for _, w := range rw.handles {
			for {
				st, changed := w.State()
				if st.Phase == entity.WatchFailed {
					return
				}
				select {
				case <-w.Ready:
				case <-changed:
					continue
				case <-ctx.Done():
					return
				}
				break
			}
		}
	}()
	return rw, nil
}

// stopWatchers stops a rule's observation, if it had any (nothing in baseline mode). Called
// with trigMu HELD: nothing in it waits on the store, so this cannot deadlock against the
// charter lock.
func (e *Engine) stopWatchers(name string) {
	if rw := e.watches[name]; rw != nil {
		delete(e.watches, name)
		rw.stop()
	}
}

// watchingLocked is whether every watch of an armed rule is open, and otherwise what the store
// is doing for the first that is not, in the words `arming:` and an install's answer use. A
// rule with no watches (baseline mode) is watching. Called with trigMu HELD.
func (e *Engine) watchingLocked(name string) (bool, string) {
	rw := e.watches[name]
	if rw == nil {
		return true, ""
	}
	for _, w := range rw.handles {
		if st, _ := w.State(); st.Phase != entity.WatchOpen {
			return false, st.String()
		}
	}
	return true, ""
}

// Ready is a channel closed once there is nothing more to wait for on the rule of this name:
// every watch of the armed rule is open, or one the store cannot open at all (Failed), or the
// rule is out of the charter — what an install's `wait` waits on. A rule still pending for the
// pass that will arm it (cause.passing) is waited on through that pass, and a rule that moves
// while it is waited on — a lift holds it again, for the pass — is followed through the move.
// Nil for a rule held for something waiting does not cure, or not in the charter. A rule armed
// with no watches (baseline mode) is ready.
//
// A replacement under the name (InstallTriggerAs) takes the old rule out and puts the new one
// in as two steps, and a wait that reads the engine between them is answered as on a removal.
func (e *Engine) Ready(name string) <-chan struct{} {
	wait, done := e.readyOn(name)
	if wait == nil || done {
		return wait
	}
	out := make(chan struct{})
	go func() {
		defer close(out)
		for wait != nil && !done {
			<-wait
			wait, done = e.readyOn(name)
		}
	}()
	return out
}

// readyOn is what a waiter on the rule waits on next, and whether that is the end of the
// waiting: readyNow and true for a rule with nothing to wait for, nil for one not waited on.
// Asked again after each step, since a step's end may be a move rather than the rule being
// ready: its watches stopped by a lift or a replacement, the pass that held it answered.
func (e *Engine) readyOn(name string) (<-chan struct{}, bool) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	if rw := e.watches[name]; rw != nil {
		select {
		case <-rw.ready:
			return readyNow, true // open, or failed; stopped, it is out of the map by now
		default:
			return rw.ready, false
		}
	}
	for _, t := range e.triggers {
		if t.Name == name {
			return readyNow, true
		}
	}
	if i := e.pendingIndex(name); i >= 0 && e.pending[i].cause.passing() {
		return e.pending[i].settled, false
	}
	return nil, true
}

// readyNow is a channel already closed: the answer for a rule with nothing to wait on.
var readyNow = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()
