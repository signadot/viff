package trigger

import (
	"context"

	"github.com/signadot/verse/entity"
)

// Monitor watches the facts a Formula reads and reports when the formula's truth
// changes, carrying the witness — the leaf refs whose presence satisfied it, which
// becomes the commit CAS precondition (so a commit only lands if the justifying facts
// still hold). It is the reusable heart of a temporal gate: `fire` blocks on it until
// the formula holds. Because it is reactive (re-evaluates on every relevant change), the
// gate stays live — safety/revocable — rather than latching on a one-shot read.
type Monitor struct {
	formula *Formula
	store   entity.Store
}

// NewMonitor builds a Monitor for a formula over a store.
func NewMonitor(f *Formula, store entity.Store) *Monitor {
	return &Monitor{formula: f, store: store}
}

// State is a formula's current truth and its witness (nil when false).
type State struct {
	True    bool
	Witness []entity.Ref
}

// Watch streams the formula's State — the initial state, then a State whenever the
// evaluation changes (truth flips, or the witness changes while true) — until ctx is
// cancelled. It subscribes to exactly the fact subtrees the formula reads and
// re-evaluates on each change. A subtree that does not exist yet is fine: a base-logd
// watch registers against future commits and delivers a fact's birth when it is first
// written (the "path not found on init" the server logs is benign — the watch is not
// failed). If the store can't watch subtrees at all, it emits the initial state and
// holds it. The returned channel is closed when ctx ends.
func (m *Monitor) Watch(ctx context.Context) <-chan State {
	out := make(chan State, 1)
	go func() {
		defer close(out)

		eval := func() State {
			ok, w := m.formula.Eval(m.store)
			return State{True: ok, Witness: w}
		}
		last := eval()
		if !send(ctx, out, last) {
			return
		}
		emit := func() bool {
			cur := eval()
			if cur.True != last.True || (cur.True && !sameRefs(cur.Witness, last.Witness)) {
				last = cur
				return send(ctx, out, cur)
			}
			return true
		}

		sw, ok := m.store.(entity.SubtreeWatcher)
		if !ok {
			<-ctx.Done() // no reactive watch available; hold the initial state
			return
		}
		events := make(chan entity.Event, 64)
		for _, st := range m.formula.Subtrees() {
			ch, unsub, err := sw.WatchSubtree(st[0], st[1])
			if err != nil {
				// A base-logd subtree watch does not fail on a missing path; an error here
				// is a real one (e.g. a controller-owned/mount subtree that can't watch on
				// appearance). Skip it — a future decision-channel surface would own that.
				continue
			}
			defer unsub()
			go drain(ctx, ch, events)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-events:
				if !emit() {
					return
				}
			}
		}
	}()
	return out
}

// Await blocks until the formula holds, returning the witness at that moment (ok=false
// if ctx ends first). Reachability: it returns on the first True. A caller wanting the
// airtight commit re-checks/latches via CAS on the returned witness (step 4).
//
// The watch is scoped to this call: returning (or ctx ending) cancels it, tearing down the
// underlying subtree watch and its goroutine. Without this, a caller that re-invokes Await
// in a loop — e.g. monitorGate re-monitoring after a revoked commit — leaks a watch (and, on
// a logd store, a server-side watcher) every iteration, flooding the store.
func (m *Monitor) Await(ctx context.Context) (witness []entity.Ref, ok bool) {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for st := range m.Watch(wctx) {
		if st.True {
			return st.Witness, true
		}
	}
	return nil, false
}

func drain(ctx context.Context, in <-chan entity.Event, out chan<- entity.Event) {
	for {
		select {
		case ev, ok := <-in:
			if !ok {
				return
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func send(ctx context.Context, out chan<- State, s State) bool {
	select {
	case out <- s:
		return true
	case <-ctx.Done():
		return false
	}
}

func sameRefs(a, b []entity.Ref) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[entity.Ref]bool, len(a))
	for _, r := range a {
		seen[r] = true
	}
	for _, r := range b {
		if !seen[r] {
			return false
		}
	}
	return true
}
