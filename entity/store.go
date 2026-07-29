package entity

import "github.com/signadot/tony-format/go-tony/ir"

// Store is the entity-verse as its consumers (engine, mirror, actions) see it:
// write changes, read state, observe the change stream, and catch up from a
// revision cursor. It is deliberately minimal and transport-agnostic: the store it is
// satisfied by today is logd/docd-backed, and a different durable backend could take its
// place without touching consumers.
//
// Observation is cursor-based: every change carries a Rev, and a consumer that
// falls behind (a paused engine, a flow parked at a long gate, a reconnect)
// catches up by asking Since(cursor) rather than holding a growing buffer — so a
// hold costs a cursor, not memory. Observe is a low-latency hint that may drop;
// Since (a query by revision, docd-native) is the source of truth.
//
// EVERY method here is required. There used to be four optional capability interfaces
// (Lister, ScopeDeleter, SubtreeWatcher, ConditionalCommitter) that the one store
// implemented and every consumer type-asserted for, and the branches taken when the
// assertion failed were unreachable — one of them claimed a run "best-effort", which is
// to say it would have permitted the double-run the claim exists to prevent. A capability
// a consumer needs belongs in the interface, where a store lacking it fails to compile.
type Store interface {
	// Write is the write: a value merges in at one path iff every precondition holds, as a
	// single commit. Creating, updating, deleting, patching one field, and claiming an
	// address are all this call with different arguments — see Write's own doc.
	//
	// Returns (committed, err). committed == false means a precondition did not hold: the
	// world said no, which is a normal answer and not an error. err means the store could
	// not answer at all — a write over a network, through mounts owned by other processes,
	// can fail, and a caller told otherwise reports success for work that did not happen.
	Write(w Write) (committed bool, err error)
	// Get returns an entity by ref: ok reports whether it exists, err reports whether
	// the read could be performed at all.
	//
	// THE TWO ARE NOT THE SAME THING and the distinction is the point. A durable store
	// reads over a network, through mounts owned by other processes; that read can fail
	// while the entity exists perfectly well. Collapsing both into a single "not found"
	// tells a caller the opposite of the truth at exactly the moment it can least afford
	// it — an agent is told the file it is looking at does not exist, and goes off to
	// create it.
	//
	// When err != nil, ok is meaningless: nothing is known about the entity. A caller
	// that genuinely does not care may discard it, but has to say so.
	Get(ref Ref) (Entity, bool, error)
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
	// AsOf returns a Snapshot: a read-only view fixed at one revision, so every read of it
	// answers as of that commit and no later write is visible to any of them.
	//
	// It is what makes a rule about the verse evaluable AT a change rather than merely after
	// one. Asking "does this condition hold" and "which entities does this select" of the
	// same snapshot makes them answers about one world; asking them of the live store makes
	// them two reads with a window between, which a concurrent commit can slide through — so
	// a firing could act on a member under a condition that had already stopped holding.
	//
	// An out-of-range or no-longer-retained revision is an ERROR here, at the point the
	// snapshot is taken, rather than a quietly-current answer later. That is the same reason
	// Since reports ok=false rather than returning what it happens to still have.
	AsOf(rev int64) (Snapshot, error)
	// List enumerates the stored entities whose ref satisfies pred (nil = all). This is
	// the query that stands in for a namespace: "everything in system X", "everything
	// touching prod". A Go predicate is a placeholder for a match expression — a
	// cross-boundary query cannot stay a closure — but it is what the CLI's listings
	// are written against today.
	List(pred func(Ref) bool) []Entity
	// WatchSubtree observes exactly one (system,kind) slice — or a whole system when kind
	// is "" — rather than the whole store. It is what a scoped trigger watches, so that
	// observation is precisely what the trigger matches and no root watch is held.
	//
	// Unlike Observe it is LEVEL-TRIGGERED at the open: it first replays what already
	// stands in the slice as Set events, then streams changes. A rule is a statement about
	// the world, and a trigger installed over a populated verse should act on the entities
	// that already match it rather than waiting for each to be re-touched.
	WatchSubtree(system, kind string) (<-chan Event, func(), error)
	// Scope returns a view of the store isolated to the named scope: writes land in
	// the scope, reads and Observe/Since see the baseline layered with the scope.
	// This is the seam for local-dev / sandbox federation — a developer's working tree
	// overlaid on the shared verse (see docs/deployment-and-steering.md). The
	// docd/logd-backed store implements real copy-on-write scopes.
	Scope(name string) Store
	// DeleteScope drops a copy-on-write scope and all of its data (federation teardown).
	// Only the baseline store may: a scoped view refuses.
	DeleteScope(name string) error
}

// IsAbsent reports whether a Require's pattern is the absence question.
//
// It has to be askable structurally, not just evaluated, because THE TWO SUBSTRATES SPELL
// ABSENCE DIFFERENTLY. Absence is a property of a PATH, not of a value: evaluated against the
// node at a ref, a missing entity reads as tony null; but docd nests every precondition into
// one root-document match, where a never-written entity is a missing KEY (no null to match)
// and a DELETED one is a key holding a tombstone. So the same question is `!irtype null` in
// one spelling and a negated-or-tombstoned path in the other (see LogdStore.CommitIf).
func IsAbsent(p Payload) bool {
	return p != nil && p.Type == ir.NullType && p.Tag == "!irtype"
}

// Absent is the precondition pattern for "nothing is there". A missing entity reads as tony
// null, and a null PATTERN would match any value (it is the presence test) — so asking about
// the value's type is what distinguishes absent from merely present.
func Absent() Payload { return ir.Null().WithTag("!irtype") }
