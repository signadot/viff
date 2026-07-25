package entity

import (
	"reflect"
	"sync"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
)

// Store is the entity-verse as its consumers (engine, mirror, actions) see it:
// write changes, read state, observe the change stream, and catch up from a
// revision cursor. It is deliberately minimal and transport-agnostic, so an
// in-memory MemStore (dev/test) and a durable/remote backend (e.g. logd/docd) both
// satisfy it without touching consumers.
//
// Observation is cursor-based: every change carries a Rev, and a consumer that
// falls behind (a paused engine, a flow parked at a long gate, a reconnect)
// catches up by asking Since(cursor) rather than holding a growing buffer — so a
// hold costs a cursor, not memory. Observe is a low-latency hint that may drop;
// Since (a query by revision, docd-native) is the source of truth.
//
// Query-by-Go-predicate is intentionally NOT on this interface: a cross-boundary
// query is a match expression, not a closure, so listing stays on the concrete
// MemStore.
type Store interface {
	// Put creates or updates an entity and emits the change. It is an unconditional
	// whole-value write (last write wins); to read-modify-write safely, use Modify.
	Put(ref Ref, p Payload) Event
	// Modify atomically read-modify-writes an entity: fn receives the current payload
	// (nil if absent) and returns the new one, applied atomically with a retry on
	// concurrent modification — so two writers to one ref cannot lose an update
	// (compare-and-swap). fn must be pure (it may run more than once) and must not
	// mutate its argument. Returns the resulting change.
	Modify(ref Ref, fn func(cur Payload) Payload) Event
	// Delete removes an entity, emitting a Delete change; reports whether it existed.
	Delete(ref Ref) (Event, bool)
	// Get returns an entity by ref.
	Get(ref Ref) (Entity, bool)
	// Observe returns a channel of live change events (from now) and an unsubscribe
	// func — a low-latency hint, not the source of truth: it may drop under
	// backpressure. Correctness comes from the revision cursor + Since.
	Observe() (<-chan Event, func())
	// Since returns retained changes with Rev > from, in order. ok is false when
	// `from` is older than the retained/durable window — the caller must resync from
	// current state. This is the cursor-based catch-up (a docd query by revision).
	Since(from int64) (evs []Event, ok bool)
	// Rev returns the current store revision.
	Rev() int64
	// Scope returns a view of the store isolated to the named scope: writes land in
	// the scope, reads and Observe/Since see the baseline layered with the scope.
	// This is the reserved seam for local-dev / sandbox federation — a developer's
	// working tree overlaid on the shared verse (see docs/deployment-and-steering.md).
	// The in-memory MemStore returns a non-isolating passthrough; a docd/logd-backed
	// store implements real copy-on-write scopes.
	Scope(name string) Store
}

// Lister is an optional capability: enumerate entities matching a Go predicate.
// It is deliberately kept off the core Store interface (a cross-boundary query is a
// match expression, not a closure), but both the in-memory and logd-backed stores
// support it for the "list everything in system X" convenience the CLI exposes.
type Lister interface {
	List(pred func(Ref) bool) []Entity
}

// ScopeDeleter is an optional capability: drop a copy-on-write scope and all of
// its data (federation teardown). Only a store with real scopes implements it —
// the logd-backed store does; the in-memory store's scopes are passthrough, so it
// does not.
type ScopeDeleter interface {
	DeleteScope(name string) error
}

// SubtreeWatcher is an optional capability: observe exactly one (system,kind) slice
// as a change stream, rather than the whole store. It is what a scoped trigger uses
// to watch precisely what it matches (no root watch). Only the logd-backed store
// implements it; the in-memory store's Observe already delivers everything cheaply.
type SubtreeWatcher interface {
	WatchSubtree(system, kind string) (<-chan Event, func(), error)
}

// Precondition asserts that the current value at Ref's subtree satisfies Pattern (a tony
// match). ID == "" selects the whole (System, Kind) subtree (e.g. a decision's vote log);
// otherwise it is one entity's payload. It is the building block of a ConditionalCommitter's
// atomic compare-and-swap, evaluated against the store state as of the commit.
type Precondition struct {
	Ref     Ref
	Pattern map[string]any
}

// ConditionalCommitter is an optional capability: write a payload to one ref iff EVERY
// Precondition still holds — atomically, together with the write, in a single step (docd's
// cross-path PatchIf on the server; the in-memory store under its write lock). It is the
// airtight substrate for a revocable, terminal-once decision commit: the verdict lands only
// if the justifying facts still hold AND the decision has not already been decided, with no
// window between the check and the write for a revoke to slip through. Returns (committed,
// err); (false, nil) means a precondition did not hold (a revoke, or an already-committed
// decision). A store lacking this capability commits best-effort (check-then-Modify), which
// leaves a small non-atomic window.
type ConditionalCommitter interface {
	CommitIf(ref Ref, payload Payload, pre ...Precondition) (committed bool, err error)
}

// MemStore is an in-memory, observable Store. It keeps a bounded window of recent
// changes so a consumer can catch up from a cursor (Since); a real backend (docd)
// keeps this durably with compaction. Live observers get drop-with-gap; drops are
// recovered by the consumer via Since, so they are no longer silent.
type MemStore struct {
	mu      sync.RWMutex
	ents    map[string]Entity // keyed by Ref.String()
	rev     int64             // monotonic revision
	history []Event           // retained recent changes (oldest first), bounded by histCap
	histCap int
	subs    map[int]*observer
	nextID  int
}

type observer struct {
	ch      chan Event
	dropped int
}

// NewMemStore returns an empty in-memory Store retaining a default window of recent
// changes for catch-up.
func NewMemStore() *MemStore {
	return &MemStore{ents: map[string]Entity{}, subs: map[int]*observer{}, histCap: 4096}
}

// Put creates or updates an entity, bumps the revision, and emits the change. The
// payload is cloned, so a caller mutating its map afterward does not race the store.
func (s *MemStore) Put(ref Ref, p Payload) Event {
	s.mu.Lock()
	prev, existed := s.ents[ref.String()]
	next := Entity{Ref: ref, Payload: p.clone()}
	s.ents[ref.String()] = next
	s.rev++
	var from Payload
	if existed {
		from = prev.Payload // informational; Op does not distinguish create from update
	}
	ev := Event{Ref: ref, Op: Set, From: from, To: next.Payload, Rev: s.rev}
	s.record(ev)
	s.emit(ev)
	s.mu.Unlock()
	return ev
}

// CommitIf implements ConditionalCommitter: it evaluates every precondition and writes the
// payload only if all still hold — the whole check→write runs under the single store lock,
// so it is one atomic step (no window for a revoke to slip between the check and the write).
func (s *MemStore) CommitIf(ref Ref, payload Payload, pre ...Precondition) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range pre {
		node, err := s.subtreeNodeLocked(p.Ref)
		if err != nil {
			return false, err
		}
		pat, err := gomap.ToTonyIR(p.Pattern)
		if err != nil {
			return false, err
		}
		ok, err := tony.Match(node, pat)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil // a precondition no longer holds — do not write
		}
	}
	prev, existed := s.ents[ref.String()]
	next := Entity{Ref: ref, Payload: payload.clone()}
	s.ents[ref.String()] = next
	s.rev++
	var from Payload
	if existed {
		from = prev.Payload
	}
	ev := Event{Ref: ref, Op: Set, From: from, To: next.Payload, Rev: s.rev}
	s.record(ev)
	s.emit(ev)
	return true, nil
}

// subtreeNodeLocked builds the tony IR for the current value at ref (caller holds s.mu): a
// single entity's payload when ref.ID is set, else the whole (System, Kind) subtree as
// {id: payload, …}. An absent entity/subtree is null, so a concrete pattern simply fails to
// match it — matching MemStore's evaluation to docd's (valueAtPath returns null for an
// absent path).
func (s *MemStore) subtreeNodeLocked(ref Ref) (*ir.Node, error) {
	if ref.ID != "" {
		e, ok := s.ents[ref.String()]
		if !ok {
			return ir.Null(), nil
		}
		return gomap.ToTonyIR(map[string]any(e.Payload))
	}
	sub := map[string]any{}
	for _, e := range s.ents {
		if e.Ref.System == ref.System && e.Ref.Kind == ref.Kind {
			sub[e.Ref.ID] = map[string]any(e.Payload)
		}
	}
	return gomap.ToTonyIR(sub)
}

// Modify atomically read-modify-writes an entity under the store lock, so the whole
// read→apply→write is a single critical section — no retry needed, no lost update.
func (s *MemStore) Modify(ref Ref, fn func(Payload) Payload) Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, existed := s.ents[ref.String()]
	var cur Payload
	if existed {
		cur = prev.Payload.clone() // read-only copy for fn
	}
	next := fn(cur)
	s.ents[ref.String()] = Entity{Ref: ref, Payload: next.clone()}
	s.rev++
	var from Payload
	if existed {
		from = prev.Payload
	}
	ev := Event{Ref: ref, Op: Set, From: from, To: s.ents[ref.String()].Payload, Rev: s.rev}
	s.record(ev)
	s.emit(ev)
	return ev
}

// Delete removes an entity, bumping the revision and emitting a Delete event.
func (s *MemStore) Delete(ref Ref) (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, existed := s.ents[ref.String()]
	if !existed {
		return Event{}, false
	}
	delete(s.ents, ref.String())
	s.rev++
	ev := Event{Ref: ref, Op: Delete, From: prev.Payload, Rev: s.rev}
	s.record(ev)
	s.emit(ev)
	return ev, true
}

// record appends to the retained window, trimming the oldest beyond histCap.
func (s *MemStore) record(ev Event) {
	s.history = append(s.history, ev)
	if len(s.history) > s.histCap {
		s.history = s.history[len(s.history)-s.histCap:]
	}
}

// Get returns an entity by ref.
func (s *MemStore) Get(ref Ref) (Entity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.ents[ref.String()]
	return e, ok
}

// Since returns retained changes with Rev > from, in order. ok is false when `from`
// predates the retained window (the caller must resync from current state).
func (s *MemStore) Since(from int64) ([]Event, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if from >= s.rev {
		return nil, true // nothing new
	}
	if len(s.history) > 0 && from+1 < s.history[0].Rev {
		return nil, false // needed revisions already dropped
	}
	var out []Event
	for _, ev := range s.history {
		if ev.Rev > from {
			out = append(out, ev)
		}
	}
	return out, true
}

// Rev returns the current store revision.
func (s *MemStore) Rev() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rev
}

// Scope is a reserved seam. The in-memory store has no isolation, so it returns
// itself (baseline) — a non-isolating passthrough usable for single-dev work. A
// docd/logd-backed store implements real copy-on-write scopes for federation.
func (s *MemStore) Scope(name string) Store { return s }

// List returns every entity whose ref satisfies pred (nil pred = all). This is
// the query that stands in for a namespace: "everything in system X," "everything
// touching prod," etc. It is a MemStore convenience, not part of the Store
// interface (cross-boundary querying is a match expression, not a closure).
func (s *MemStore) List(pred func(Ref) bool) []Entity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entity, 0, len(s.ents))
	for _, e := range s.ents {
		if pred == nil || pred(e.Ref) {
			out = append(out, e)
		}
	}
	return out
}

// Observe returns a channel of live change events and an unsubscribe func.
func (s *MemStore) Observe() (<-chan Event, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	o := &observer{ch: make(chan Event, 256)}
	s.subs[id] = o
	return o.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(o.ch)
		}
	}
}

// WatchSubtree observes one (system,kind) slice — or a whole system when kind is "" —
// emitting the current matching entities up front (as Set) then each live change. It
// mirrors the logd store's subtree watch so the engine's per-trigger observation is
// uniform across stores. The in-memory store has no docd watch to collide with a
// mount, so this is Observe plus a filter and an initial snapshot; it never force-ends
// and so never needs the logd store's re-establish loop.
func (s *MemStore) WatchSubtree(system, kind string) (<-chan Event, func(), error) {
	match := func(r Ref) bool { return r.System == system && (kind == "" || r.Kind == kind) }
	// Register the live subscriber BEFORE snapshotting, so a change between the two is
	// delivered live rather than lost (it may also appear in the snapshot — a harmless
	// duplicate Set the consumer's resync/latch absorbs).
	src, unsubSrc := s.Observe()
	snap := s.List(match)
	out := make(chan Event, 256)
	done := make(chan struct{})
	go func() {
		defer close(out)
		// prev is the last emitted payload per ref. Like LogdStore.WatchSubtree, this watch is
		// EDGE-ON-REAL-CHANGE: a write that does not change an entity's content emits nothing, so
		// a reactive re-write (a trigger's effect re-touching its own cause) does not loop back
		// into the trigger that caused it — no charter-level latch needed.
		prev := map[Ref]Payload{}
		emit := func(e Event) bool {
			switch e.Op {
			case Set:
				if old, ok := prev[e.Ref]; ok && reflect.DeepEqual(old, e.To) {
					return true // unchanged content — not a reactive edge
				}
				prev[e.Ref] = e.To
			case Delete:
				if _, ok := prev[e.Ref]; !ok {
					return true // a delete of something we never had present — no edge
				}
				delete(prev, e.Ref)
			}
			select {
			case out <- e:
				return true
			case <-done:
				return false
			}
		}
		for _, e := range snap {
			if !emit(Event{Ref: e.Ref, Op: Set, To: e.Payload}) {
				return
			}
		}
		for {
			select {
			case <-done:
				return
			case ev, ok := <-src:
				if !ok {
					return
				}
				if ev.Gap > 0 { // a drop marker: forward as-is (the consumer resyncs), never diffed
					select {
					case out <- ev:
					case <-done:
						return
					}
					continue
				}
				if match(ev.Ref) {
					if !emit(ev) {
						return
					}
				}
			}
		}
	}()
	var once sync.Once
	return out, func() { once.Do(func() { close(done); unsubSrc() }) }, nil
}

// emit broadcasts to observers, non-blocking. Caller holds s.mu. A full observer
// drops the event and accrues a gap count flushed before its next delivery; the
// consumer recovers the drop via Since, so it is not silent.
func (s *MemStore) emit(e Event) {
	for _, o := range s.subs {
		if o.dropped > 0 {
			select {
			case o.ch <- Event{Op: e.Op, Gap: o.dropped}:
				o.dropped = 0
			default:
				o.dropped++
				continue
			}
		}
		select {
		case o.ch <- e:
		default:
			o.dropped++
		}
	}
}

// compile-time check: MemStore satisfies Store and SubtreeWatcher.
var (
	_ Store          = (*MemStore)(nil)
	_ SubtreeWatcher = (*MemStore)(nil)
)
