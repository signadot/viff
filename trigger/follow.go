package trigger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger/charter"
)

// THE RECORD IS THE RULE. An install is a write of a rule's record and a removal a delete of
// it, and the engine keeps its charter in step with the records through one watch over them
// (charter.Follow), opened when the engine starts: a record that appears or changes is placed,
// one that goes is unplaced, and the opening picture is the restore. The engine is derived
// from its records, so there is nothing for the two to disagree about — a rule that stands in
// the store stands in the engine, armed or pending with why — and a caller is answered once
// the engine holds what it wrote (reached).
//
// One carrier in both modes: the baseline engine's root loop sees every commit and could carry
// the follow, but that is two code paths for one thing. The follow is a subtree watch
// everywhere, and the baseline engine holds one more watch for it.
//
// BEFORE THE FOLLOW RUNS, AN INSTALL PLACES DIRECTLY. An engine nobody has started has nothing
// to wait on — a check's engine, a test's — so with no follow running an install writes the
// record and places the rule itself, as the follow would, and the follow's picture reconciles
// by name when it starts: a record whose value the placed rule already has is left as it
// stands, one whose value differs replaces it.
//
// THE WRITE IS THE INSTALL; THE WAIT IS FOR THE ENGINE TO SHOW IT. A caller whose wait ends
// before the follow has delivered its write — the bound ran out, its context ended, the follow
// is dead — is answered with ErrNotReflected: the record stands as written and the engine will
// hold it when the follow reaches it, and what failed is the showing. A caller that unwinds on
// an error puts such a write back deliberately, not by leaving it out of the unwind.

// followWait bounds how long a caller waits for the follow to deliver what it wrote, beyond its
// own context: a charter the engine is not following is a failure, not a lag, and the caller is
// told rather than held. The bound covers the ask for a mark too, since the store's request is
// bounded by nothing but its context. An engine's own is followWait unless set (tests).
const followWait = 10 * time.Second

// followRetry is how long a refused follow waits before it is asked for again.
const followRetry = time.Second

// ErrNotReflected is the error an install, a restore or a removal is answered with when its
// write landed and the wait for the engine to show it did not end with it shown: the record is
// as the caller wrote it, and the engine is not yet. Wrapped in ErrCharter.
var ErrNotReflected = errors.New("the record is written and the engine has not yet reflected it")

// follower is the engine's watch over its records and how far it has read them.
type follower struct {
	mu sync.Mutex
	// w is the watch once the store has answered it; refused is why it has not, while the
	// follow asks again (a refusal is a phase, as the store's own are).
	w       *entity.Watch
	refused error
	// rev is the revision the charter is current through: every record set or deleted at or
	// before it has been placed or unplaced. changed is closed and replaced as it advances.
	rev     int64
	changed chan struct{}
	// need is the revision the opening picture is current through, once the watch has said it
	// (Progress; marked), and restored is closed once rev has reached it: the restore is in.
	need     int64
	marked   bool
	restored chan struct{}
	done     bool
	// dead is why the follow ended, once it has: the watch could not be asked for, its stream
	// closed, or the engine's context ended. A wait on a dead follow is answered with it.
	dead error
}

// follow opens the engine's watch over its records and keeps the charter in step with them
// until ctx ends. Once: an engine started twice follows once.
func (e *Engine) follow(ctx context.Context) {
	e.followMu.Lock()
	defer e.followMu.Unlock()
	if e.following != nil {
		return
	}
	f := &follower{changed: make(chan struct{}), restored: make(chan struct{})}
	e.following = f
	go e.followLoop(ctx, f)
}

// follower is the engine's follow, nil before it starts.
func (e *Engine) follower() *follower {
	e.followMu.Lock()
	defer e.followMu.Unlock()
	return e.following
}

// followLoop asks the store for the watch, again after a refusal until ctx ends, then applies
// each delivery of it to the charter, advancing rev, and marks the restore once the opening
// picture is in.
func (e *Engine) followLoop(ctx context.Context, f *follower) {
	w, err := charter.Follow(e.store)
	for err != nil {
		f.refuse(err)
		e.logger.Warn("the charter's watch was refused; asked again", "in", e.followRetry, "err", err)
		select {
		case <-ctx.Done():
			f.end(ctx.Err())
			return
		case <-time.After(e.followRetry):
		}
		w, err = charter.Follow(e.store)
	}
	f.open(w)
	defer w.Close()
	ready := w.Ready
	for {
		select {
		case <-ctx.Done():
			f.end(ctx.Err())
			return
		case <-ready:
			// The picture is in the pump, handed to this stream under the same lock, so it is
			// at most a delivery behind; how far current the stream is, the watch says as a mark
			// in the stream (Progress), in order with every commit before it. An empty charter
			// pictures nothing, so the mark is what says the restore is in.
			ready = nil
			go f.mark(ctx, w, e.followWait)
		case c, ok := <-w.Commits:
			if !ok {
				f.end(errors.New("the charter's watch ended"))
				return
			}
			if ctx.Err() != nil {
				f.end(ctx.Err())
				return
			}
			e.applyCharter(c)
			f.advance(c.Rev)
		}
	}
}

// refuse records that the store refused the watch, and open that it answered it; each wakes
// every waiter.
func (f *follower) refuse(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused = err
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *follower) open(w *entity.Watch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.w, f.refused = w, nil
	close(f.changed)
	f.changed = make(chan struct{})
}

// mark asks the watch how far current it is, and again until the mark has come through the
// stream: the watch may not be standing at the instant the picture arrived, and a mark it
// answered is in the stream of the watch that answered it — one that drops before the mark
// reaches the stream comes back on a picture that may carry nothing, so the mark is asked
// for again, bounded as a caller's ask is.
func (f *follower) mark(ctx context.Context, w *entity.Watch, bound time.Duration) {
	for {
		f.mu.Lock()
		done, dead := f.done, f.dead
		f.mu.Unlock()
		if done || dead != nil || ctx.Err() != nil {
			return
		}
		ask, cancel := context.WithTimeout(ctx, bound)
		rev, err := w.Progress(ask)
		cancel()
		again := 50 * time.Millisecond
		if err == nil {
			f.mu.Lock()
			f.need, f.marked = rev, true
			f.mu.Unlock()
			f.advance(0)
			again = bound
		}
		select {
		case <-f.restored:
			return
		case <-ctx.Done():
			return
		case <-time.After(again):
		}
	}
}

// advance records that the charter is current through rev, and wakes every waiter; the restore
// is marked once rev has reached the picture's.
func (f *follower) advance(rev int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rev > f.rev {
		f.rev = rev
		close(f.changed)
		f.changed = make(chan struct{})
	}
	if !f.done && f.marked && f.rev >= f.need {
		f.done = true
		close(f.restored)
	}
}

// end marks the follow dead with why, waking every waiter.
func (f *follower) end(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead == nil {
		f.dead = err
	}
	close(f.changed)
	f.changed = make(chan struct{})
}

// Restored is closed once the engine's charter has been read from its records: the follow's
// opening picture is in, and every rule and stratum standing in the store stands in the engine,
// armed or pending with why. What a start waits on before it says the charter is restored. Nil
// for an engine nobody started, which restores nothing.
func (e *Engine) Restored() <-chan struct{} {
	f := e.follower()
	if f == nil {
		return nil
	}
	return f.restored
}

// FollowState is what the store is doing for the engine's watch over its records, for a start
// to say while it waits (Restored): the handle's phase, or why the follow is not running.
func (e *Engine) FollowState() entity.WatchState {
	f := e.follower()
	if f == nil {
		return entity.WatchState{Phase: entity.WatchFailed, Err: errors.New("the engine is not started")}
	}
	f.mu.Lock()
	dead, w, refused := f.dead, f.w, f.refused
	f.mu.Unlock()
	switch {
	case dead != nil:
		return entity.WatchState{Phase: entity.WatchFailed, Err: dead}
	case w == nil:
		return entity.WatchState{Phase: entity.WatchRefused, Err: refused}
	}
	st, _ := w.State()
	return st
}

// followed is how an install, a removal or a restore of a rule ends: with the engine holding
// what the caller wrote at rev. With the follow running, that is once it has delivered the
// write (reached); before it runs, the caller does to the engine what the follow would (direct).
func (e *Engine) followed(ctx context.Context, rev int64, direct func()) error {
	if e.follower() == nil {
		direct()
		return nil
	}
	return e.reached(ctx, rev)
}

// reached waits until the follow has delivered every record through rev — the one a caller
// just wrote — so that the engine holds what the caller wrote when the call returns. Bounded by
// ctx and by the engine's followWait, the ask for a mark included; a wait that ends any other
// way than delivered is ErrNotReflected, with why: the follow is dead, its watch Failed or not
// yet answered, the bound ran out, the caller's context ended. Nothing to wait for before the
// follow runs.
func (e *Engine) reached(ctx context.Context, rev int64) error {
	f := e.follower()
	if f == nil {
		return nil
	}
	bound, cancel := context.WithTimeout(ctx, e.followWait)
	defer cancel()
	for {
		f.mu.Lock()
		cur, changed, dead, w, refused := f.rev, f.changed, f.dead, f.w, f.refused
		f.mu.Unlock()
		if cur >= rev {
			return nil
		}
		if dead != nil {
			return fmt.Errorf("%w: %w: the engine is not following its charter: %w", ErrCharter, ErrNotReflected, dead)
		}
		st, moved := entity.WatchState{Phase: entity.WatchRefused, Err: refused}, (<-chan struct{})(nil)
		if w != nil {
			st, moved = w.State()
			if st.Phase == entity.WatchFailed {
				return fmt.Errorf("%w: %w: the charter's watch: %s", ErrCharter, ErrNotReflected, st)
			}
			if st.Phase == entity.WatchOpen {
				// The stream carries only what changed, and a commit that changed nothing at
				// the records — a record written back as it stands, a delete of nothing — is
				// one it says nothing of; asked, the watch marks in the stream how far current
				// it is, in order with every commit before the mark.
				w.Progress(bound) // an ask that did not answer in the bound ends below
			}
		}
		select {
		case <-changed:
		case <-moved:
		case <-bound.Done():
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%w: %w: %w", ErrCharter, ErrNotReflected, err)
			}
			return fmt.Errorf("%w: %w: the charter's watch has not delivered revision %d in %s (%s)", ErrCharter, ErrNotReflected, rev, e.followWait, st)
		}
	}
}

// applyCharter keeps the charter in step with one delivery of the records: each of this
// engine's rules and strata set or deleted at the commit. Whose a record is, is read off it —
// the scope and the address stamped on it (charter.Payload) — and a deletion, which carries no
// record, is this engine's where it holds the name at that address.
func (e *Engine) applyCharter(c entity.Commit) {
	for _, ch := range charter.Changes(c) {
		switch {
		case ch.Deleted:
			e.unplaceRecord(ch)
		case ch.Scope != e.scope || ch.At != e.confine.stamp():
			// Another charter's: baseline's seen through a scope, a package's beside baseline.
		case ch.Kind == charter.RuleKind:
			e.placeRecord(ch.Record)
		case ch.Kind == charter.StratumKind:
			e.placeStratumRecord(ch.Record)
		}
	}
}

// placeRecord keeps the rule a record holds in step with the engine: a record whose value the
// standing rule already has is left as it stands — its words swapped in where they moved, so
// the engine's copy is the record — and one whose value differs, or whose name nothing holds,
// is placed in the old one's stead, as whose the record is. A record that does not decode is
// said once and left standing, with whatever stood under its name: a record verse could not
// read is not one it should quietly delete, and a rewrite of it is placed.
func (e *Engine) placeRecord(rec charter.Record) {
	t, err := ruleOf(rec)
	if err != nil {
		e.badRecord(rec, err)
		return
	}
	if old, stands := e.Rule(t.Name); stands && SameValue(old, t) {
		if !SameText(old, t) {
			e.reword(t)
		}
		return
	}
	e.place(t, e.OwnerOf(t.Name))
}

// place vets a rule as whose it is and admits it: pending with the refusal where vet refuses
// — its record stands, and the world that refuses it may change (reconcile.go) — since what
// reaches here refused is a record written some other way than an install, which refuses
// where the author is present, or one whose world has moved. A spawn no runner offers is held
// for a runner (RunnersToCome): the record was accepted, and its runner may be still to come.
func (e *Engine) place(t Trigger, as string) {
	hold, err := e.vet(t, as, RunnersToCome)
	if err != nil {
		hold = &cause{causeRefused, err.Error()}
	}
	e.admit(t, hold)
}

// unplaceRecord takes out what a deleted record held, where this engine held it: a deletion at
// the engine's own address, under a name it holds. The stream is the engine's view of the
// records — a scope's is the scope's, baseline read through — so a deletion of baseline's record
// under a name the scope's own charter holds changes nothing the scope sees, and is not
// delivered to it.
func (e *Engine) unplaceRecord(ch charter.Change) {
	switch ch.Kind {
	case charter.RuleKind:
		if ch.Ref == e.RuleRef(ch.Name) {
			e.unplace(ch.Name)
		}
	case charter.StratumKind:
		if ch.Ref == e.StratumRef(ch.Name) {
			e.removeStratum(ch.Name)
		}
	}
}

// placeStratumRecord keeps the level a record holds in step with the ordering: a record the
// standing level already says is left; one that differs replaces it; one under a name nothing
// holds joins the ordering. Each faces the properties of the set (checkStrataSetLocked), and a
// record that does not fit is said once and left standing, as a rule's is. The rules held for
// the level are armed by the pass it asks for.
func (e *Engine) placeStratumRecord(rec charter.Record) {
	s, err := stratumOf(rec)
	if err != nil {
		e.badRecord(rec, err)
		return
	}
	e.trigMu.Lock()
	if old, stands := e.stratumLocked(s.Name); stands {
		if old.Level == s.Level && matchesText(old.Match) == matchesText(s.Match) {
			e.trigMu.Unlock()
			return
		}
		err = e.checkStrataSetLocked(s, s.Name)
		if err == nil {
			e.swapStratumLocked(s)
		}
	} else if err = e.checkStrataSetLocked(s, ""); err == nil {
		e.strata = append(e.strata, s)
	}
	e.trigMu.Unlock()
	if err != nil {
		e.badRecord(rec, err)
		return
	}
	e.promote(s.Name)
}

// reword swaps a rule's words in where it stands, armed or pending, with no install: the record
// says what it said and no longer the same way — the comments its author left above it moved —
// and the engine's copy follows.
func (e *Engine) reword(t Trigger) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for i := range e.triggers {
		if e.triggers[i].Name == t.Name {
			e.triggers[i] = t
		}
	}
	for i := range e.pending {
		if e.pending[i].Name == t.Name {
			e.pending[i].Trigger = t
		}
	}
}

// badRecord says once, per record and revision, that a record could not be placed: the rest
// of the charter is still a program worth running.
func (e *Engine) badRecord(rec charter.Record, err error) {
	key := rec.Ref.String()
	e.trigMu.Lock()
	seen := e.badRecords[key] == rec.Rev
	e.badRecords[key] = rec.Rev
	e.trigMu.Unlock()
	if !seen {
		e.logger.Warn("a charter record could not be placed; what stood under its name stands, and a rewrite of it is placed",
			"at", key, "rev", rec.Rev, "err", err)
	}
}
