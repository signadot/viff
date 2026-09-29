package entity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
)

// THE CHECK IS ORDERED WITH THE STORE. What is served from revision R — a read, a listing, a
// watch event — is checked against a view of the perms mirror that has applied every change to
// the mirror at or before R, and never against one that has not: a record committed with its
// node at R holds from R, and a revoke at R holds for everything served after it.
//
// The loaded view (Track) knows how far it has got: the revision each of its two watches has
// applied through, the records' and the root's (nodesAt, rootAt), and the lower of the two is
// the view's watermark. A watch delivers only the commits that change its path, so what it
// delivers cannot carry the watermark past a commit that changed nothing there — on a verse
// whose perms stand still, past nothing a read is ever served at. So a read served at R above
// the watermark ASKS THE WATCHES: a progress request each (WatchSubtreeProgress), which the
// store acknowledges with its head and answers in the watch's own stream, after every commit up
// to that head. The view applies it in order with the rest (follow), and once it has applied
// one at or above R on both halves it holds the mirror as of R — whichever read's request
// prompted it.
//
// A request is shared. Every read waiting while one is in flight waits for it, and a read
// whose R its head does not cover — it was served after the request was answered — asks the
// next. A read at an R the view already covers pays nothing: a lock and a comparison.
//
// IT FAILS CLOSED. A request the store refuses (a watch it cannot answer for, one being
// re-established), and an answer that does not arrive within its budget, leave the read
// unchecked, and an unchecked read is refused, as a check whose mirror did not answer is
// (Unread): it is never checked against the view it already had instead.
//
// THE MIRROR IS READ WHOLE only to load the view, and again where the stream cannot be applied
// to it exactly — a watch re-established, whose changes are the net of commits the view never
// saw one by one (refresh). A view over a store whose watches cannot be asked is brought up by
// that read for every read above its watermark, which is right and costs a mirror read each.
//
// A view that is not loaded — a scope whose mirror has not loaded yet, a view nobody tracked —
// reads each record it asks about, and a record it read before R is read again for a check at R
// (recordAt): the same rule, one key at a time.

// maxBarriers bounds how many requests one read waits through. The first may have been
// answered before the read was served; the second was asked after, and its head is at or past
// the read's. A third is the store answering with a head below one it has already served at,
// which is not a thing to wait out.
const maxBarriers = 3

// progressBudget is how long a read waits for the watches to say they are current through it:
// the budget of one call on the session.
var progressBudget = sessionBudget

// barrier is one request that the view be brought up — a progress request of each watch, or
// a fresh read of the mirror — shared by everyone waiting on it. head is what the store
// acknowledged: the view is current through it once the answers arrive.
type barrier struct {
	done chan struct{}
	head int64
	err  error
}

// throughLocked is the watermark: the revision the loaded view has applied every change to the
// mirror through. Called with the lock held.
func (a *Attribution) throughLocked() int64 { return min(a.nodesAt, a.rootAt) }

// advancedLocked wakes every read waiting on the watermark. Called with the lock held, after it
// moved.
func (a *Attribution) advancedLocked() {
	if a.moved != nil {
		close(a.moved)
		a.moved = nil
	}
}

// ensure brings the loaded view up to rev: at once when it is there already, and otherwise by
// asking the watches (or, where they cannot be asked or the stream was lost, by reading the
// mirror) and waiting for the answer to be applied. s is the store the view is over. An error
// is the mirror not answering, and the read at rev must not be answered from this view.
func (a *Attribution) ensure(s Store, rev int64) error {
	deadline := time.Now().Add(progressBudget)
	for range maxBarriers {
		a.mu.Lock()
		if !a.loaded || rev <= 0 || a.throughLocked() >= rev {
			a.mu.Unlock()
			return nil
		}
		b, lead := a.pending, a.pending == nil
		if lead {
			b = &barrier{done: make(chan struct{})}
			a.pending = b
		}
		asks := a.progress
		if a.lost {
			asks = nil
		}
		a.mu.Unlock()
		if lead {
			if asks == nil {
				b.err = a.refresh(s)
			} else {
				b.head, b.err = ask(asks, deadline)
			}
			a.mu.Lock()
			a.pending = nil
			a.mu.Unlock()
			close(b.done)
		} else {
			<-b.done
		}
		if b.err != nil {
			return b.err
		}
		if b.head >= rev {
			return a.await(rev, deadline)
		}
	}
	a.mu.Lock()
	through := a.throughLocked()
	a.mu.Unlock()
	if through >= rev {
		return nil
	}
	return fmt.Errorf("the perms mirror is current through %d, still below revision %d", through, rev)
}

// ask sends each watch its progress request at once, and answers the lowest head acknowledged:
// the view is current through it once every watch's answer has been applied.
func ask(asks []func(context.Context) (int64, error), deadline time.Time) (int64, error) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	heads := make([]int64, len(asks))
	errs := make([]error, len(asks))
	var wg sync.WaitGroup
	for i, progress := range asks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			heads[i], errs[i] = progress(ctx)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return 0, fmt.Errorf("asking the perms watches whether they are current: %w", err)
	}
	return slices.Min(heads), nil
}

// await waits for the watermark to reach rev: for the progress answers, applied in order.
func (a *Attribution) await(rev int64, deadline time.Time) error {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		a.mu.Lock()
		through := a.throughLocked()
		if through >= rev {
			a.mu.Unlock()
			return nil
		}
		if a.moved == nil {
			a.moved = make(chan struct{})
		}
		moved := a.moved
		a.mu.Unlock()
		select {
		case <-moved:
		case <-timer.C:
			return fmt.Errorf("the perms watches did not answer through revision %d within %v; "+
				"current through %d", rev, progressBudget, through)
		}
	}
}

// refresh is a barrier: the mirror read whole, records and root's together, at one commit, and
// applied to the view as of that commit (replace). It is also how Track loads the view.
func (a *Attribution) refresh(s Store) error {
	node, ok, rev, err := servedMirror(s)
	if err != nil {
		return err
	}
	a.mu.Lock()
	root := a.rootChildGrants
	a.mu.Unlock()
	nodes := map[string]*permsRecord{}
	var rootRec *permsRecord
	if ok {
		if obj := ir.Uncomment(childByKey(node, PermsKind)); obj != nil && obj.Type == ir.ObjectType {
			for i, f := range obj.Fields {
				key := fieldName(f)
				nodes[key] = parseRecord(key, obj.Values[i], root)
			}
		}
		if r := childByKey(node, PermsRootKind); r != nil {
			rootRec = parseRecord("", r, root)
		}
	}
	a.mu.Lock()
	moved := a.replace(rev, nodes, rootRec)
	a.mu.Unlock()
	for _, key := range moved {
		a.dropInScopes(s, key)
	}
	return nil
}

// replace puts the mirror as it stood at rev into the view — the records' half, the root's, or
// both — where the view has not already applied past rev on that half, and reports the keys whose
// records moved. Called with the lock held.
func (a *Attribution) replace(rev int64, nodes map[string]*permsRecord, root *permsRecord) []string {
	if a.records == nil {
		a.records = map[string]*permsRecord{}
	}
	var moved []string
	if rev > a.nodesAt || !a.loaded {
		for key, old := range a.records {
			if rec, ok := nodes[key]; key != "" && (!ok || !reflect.DeepEqual(old, rec)) {
				moved = append(moved, key)
			}
		}
		for key, rec := range nodes {
			if _, ok := a.records[key]; !ok {
				moved = append(moved, key)
			}
			a.records[key] = rec
		}
		for key := range a.records {
			if _, ok := nodes[key]; key != "" && !ok {
				delete(a.records, key)
			}
		}
		a.nodesAt = rev
	}
	if rev > a.rootAt || !a.loaded {
		old, had := a.records[""]
		switch {
		case root == nil && had:
			delete(a.records, "")
			moved = append(moved, "")
		case root != nil && (!had || !reflect.DeepEqual(old, root)):
			a.records[""] = root
			moved = append(moved, "")
		}
		a.rootAt = rev
	}
	if !a.loaded || len(moved) > 0 {
		a.dropTaken()
		a.owners = nil
		for _, key := range moved {
			if ref, err := ParseAddr(key); err == nil {
				delete(a.decided, ref)
			}
		}
	}
	a.loaded = true
	a.advancedLocked()
	return moved
}

// follow applies one commit from a watch on the mirror — the records' (root false) or the
// root's — to the view, IN ORDER with the reads of the whole mirror. A commit at or below what
// the view has applied on that half is old news. One taken against a copy the view has applied
// through is applied, and the half's watermark moves to it: the watch delivers every commit
// that changes its path, so nothing on that half stood between — and a progress answer says
// exactly that, up to its commit. Anything else — a re-established watch, whose changes are the
// net of commits the view may have seen part of, or a copy newer than the view — cannot be
// applied to it exactly: the view has LOST the stream, and the mirror is read whole to find it
// again. Until that read lands, what the view is asked to be current through it answers by
// reading the mirror too, since no progress answer can speak for a copy it no longer follows.
func (a *Attribution) follow(s Store, root bool, c Commit) {
	a.mu.Lock()
	at := &a.nodesAt
	if root {
		at = &a.rootAt
	}
	if c.Rev <= *at {
		a.mu.Unlock()
		return
	}
	if c.resync || c.since > *at {
		a.lost = true
		a.mu.Unlock()
		if err := a.refresh(s); err == nil {
			a.mu.Lock()
			if a.throughLocked() >= c.Rev {
				a.lost = false
			}
			a.mu.Unlock()
		}
		return
	}
	var moved []string
	for _, ev := range c.Changes {
		key, ok := mirrorKey(ev.Ref)
		if !ok {
			continue
		}
		a.applyLocked(key, ev)
		moved = append(moved, key)
	}
	*at = c.Rev
	a.advancedLocked()
	a.mu.Unlock()
	for _, key := range moved {
		a.dropInScopes(s, key)
	}
}

// servedAt is the view for what was served at rev: this one, once the loaded mirror is known to
// have applied every change at or before rev, carrying rev for the records a view that is not
// loaded reads one at a time (recordAt). Its error is the mirror not answering, and marked so.
func (a *attributed) servedAt(rev int64) (*attributed, error) {
	if err := a.attr.ensure(a.Store, rev); err != nil {
		a.attr.noteUnreadable()
		return nil, Unread(fmt.Errorf("the perms mirror did not reach revision %d: %w", rev, err))
	}
	v := *a
	v.rev = rev
	return &v, nil
}

// The reads below are the store's, with the commit each was served at. A store that says
// (LogdStore) is asked; one that does not — a wrapper, in a test — answers with the latest
// commit this process has seen, read AFTER the read for what it served, since the answer raised
// it, and BEFORE the read for a copy of the mirror, since what the read found is at least that
// new. Each is the side that cannot claim more than was read.

// servedGet is Get, and the commit it was served at.
func servedGet(s Store, ref Ref) (Entity, bool, int64, error) {
	if h, ok := s.(interface {
		GetCommit(Ref) (Entity, bool, int64, error)
	}); ok {
		e, found, rev, err := h.GetCommit(ref)
		if err == nil && rev == 0 {
			rev = s.Head()
		}
		return e, found, rev, err
	}
	e, found, err := s.Get(ref)
	return e, found, s.Head(), err
}

// servedReadAt is ReadAt, and the commit it was served at.
func servedReadAt(s Store, at []string) (*ir.Node, bool, int64, error) {
	if h, ok := s.(interface {
		ReadAtCommit([]string) (*ir.Node, bool, int64, error)
	}); ok {
		n, found, rev, err := h.ReadAtCommit(at)
		if err == nil && rev == 0 {
			rev = s.Head()
		}
		return n, found, rev, err
	}
	n, found, err := s.ReadAt(at)
	return n, found, s.Head(), err
}

// servedList is ListErr, and the commit it was served at.
func servedList(s Store, pred func(Ref) bool) ([]Entity, int64, error) {
	if h, ok := s.(interface {
		ListErrCommit(func(Ref) bool) ([]Entity, int64, error)
	}); ok {
		out, rev, err := h.ListErrCommit(pred)
		if err == nil && rev == 0 {
			rev = s.Head()
		}
		return out, rev, err
	}
	out, err := ListErr(s, pred)
	return out, s.Head(), err
}

// servedListAt is ListAtErr, and the commit it was served at.
func servedListAt(s Store, at []string, depth int, pred func(Ref) bool) ([]Entity, int64, error) {
	if h, ok := s.(interface {
		ListAtErrCommit([]string, int, func(Ref) bool) ([]Entity, int64, error)
	}); ok {
		out, rev, err := h.ListAtErrCommit(at, depth, pred)
		if err == nil && rev == 0 {
			rev = s.Head()
		}
		return out, rev, err
	}
	out, err := ListAtErr(s, at, depth, pred)
	return out, s.Head(), err
}

// servedPaths is PathsAt, and the commit it was served at.
func servedPaths(s Store, at []string, depth int) ([]Path, int64, error) {
	if h, ok := s.(interface {
		PathsAtCommit([]string, int) ([]Path, int64, error)
	}); ok {
		out, rev, err := h.PathsAtCommit(at, depth)
		if err == nil && rev == 0 {
			rev = s.Head()
		}
		return out, rev, err
	}
	out, err := s.PathsAt(at, depth)
	return out, s.Head(), err
}

// knownBefore is a commit a read about to be made will be served at or after: the latest this
// process has seen, or — where it has seen none yet — the store's own, asked.
func knownBefore(s Store) int64 {
	if h := s.Head(); h > 0 {
		return h
	}
	return s.Rev()
}

// mirrorGet is one record read, and a commit its answer is at least as new as.
func mirrorGet(s Store, ref Ref) (Entity, bool, int64, error) {
	before := s.Head()
	if h, ok := s.(interface {
		GetCommit(Ref) (Entity, bool, int64, error)
	}); ok {
		e, found, rev, err := h.GetCommit(ref)
		return e, found, max(rev, before), err
	}
	e, found, err := s.Get(ref)
	return e, found, before, err
}

// servedMirror is the perms subtree — the records and the root's — in one read, and a commit its
// answer is at least as new as.
func servedMirror(s Store) (*ir.Node, bool, int64, error) {
	at := []string{VerseRoot, PermsSystem}
	before := knownBefore(s)
	if h, ok := s.(interface {
		ReadAtCommit([]string) (*ir.Node, bool, int64, error)
	}); ok {
		n, found, rev, err := h.ReadAtCommit(at)
		return n, found, max(rev, before), err
	}
	n, found, err := s.ReadAt(at)
	return n, found, before, err
}
