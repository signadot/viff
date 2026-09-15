package entity

import (
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
)

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
	// Returns (committed, rev, err). committed == false means a precondition did not hold:
	// the world said no, which is a normal answer and not an error. err means the store could
	// not answer at all — a write over a network, through mounts owned by other tasks,
	// can fail, and a caller told otherwise reports success for work that did not happen.
	//
	// rev is the revision the write landed at, and is meaningful ONLY when committed is true;
	// a write that did not happen has no revision and reports zero. It is the same number the
	// change stream will carry for this write (Commit.Rev), which is what makes it useful:
	// without it a writer cannot correlate its own write with the commit it later observes,
	// and so cannot tell a change it caused from one it merely saw.
	Write(w Write) (committed bool, rev int64, err error)
	// Get returns an entity by ref: ok reports whether it exists, err reports whether
	// the read could be performed at all.
	//
	// THE TWO ARE NOT THE SAME THING and the distinction is the point. A durable store
	// reads over a network, through mounts owned by other tasks; that read can fail
	// while the entity exists perfectly well. Collapsing both into a single "not found"
	// tells a caller the opposite of the truth at exactly the moment it can least afford
	// it — an agent is told the file it is looking at does not exist, and goes off to
	// create it.
	//
	// When err != nil, ok is meaningless: nothing is known about the entity. A caller
	// that genuinely does not care may discard it, but has to say so.
	Get(ref Ref) (Entity, bool, error)
	// Observe returns a channel of live commits (from now) and an unsubscribe func — a
	// low-latency hint, not the source of truth: it may drop under backpressure, marking
	// the next delivered commit with a Gap. Correctness comes from the revision cursor +
	// Since.
	//
	// Each commit's Changes are answered AT THE PATTERNS GIVEN: every node one of them reaches,
	// before the commit or after it, whose node differs (docs/working/paths-and-nodes.md). With
	// none, a commit carries no Changes, and Commit.Touches is how to ask about a path.
	Observe(at ...*kpath.KPath) (<-chan Commit, func())
	// Since returns retained commits with Rev > from, in order, with Changes answered at the
	// patterns given as Observe answers them, read under the literal prefix they share — the
	// whole verse when they share none, or when none are given. This is the cursor-based
	// catch-up (a docd query by revision).
	//
	// THE ERROR IS THE ONE THIS REPLAY FAILED WITH, and the commits are whatever it delivered
	// before it did — a truncated catch-up comes back with both, so a cursor-keeping caller can
	// advance over what arrived and resume, and nobody is told they are caught up when they are
	// not. Only `errors.Is(err, ErrReplayTooOld)` means the history is GONE and the answer is to
	// resync; every other failure means the store could not answer this time.
	//
	// It reported one bit for a while, and a bit cannot tell those apart: a store that was down,
	// a replay its own consumer could not keep up with, and a genuinely expired cursor all
	// arrived as "your cursor is too old" (git-issue 3qf2z59dh12ksdazg9n0).
	//
	// `at` NARROWS WHAT IT COSTS, not just what it returns — the base document and the log range
	// are both read at that path — so a caller watching one slice should ask about one slice.
	Since(from int64, at ...*kpath.KPath) (commits []Commit, err error)
	// Rev returns the current store revision, syncing with the store if it must.
	Rev() int64
	// Head is the highest commit this PROCESS has been told about, with no round trip to
	// learn of any others — see LogdStore.Head. Rev and Head answer different questions
	// (last change vs where the store is), and a caller on a timer wants this one.
	Head() int64
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
	// Since reports an error rather than returning what it happens to still have.
	//
	// `within` narrows what the snapshot holds to the given (system, kind) subtrees — kind ""
	// meaning a whole system, no arguments meaning the whole verse. It is not a filter for
	// convenience: reading the whole verse costs more as the verse GROWS, so a caller
	// evaluating a rule at every commit pays for entities the rule cannot see. Pass the
	// subtrees the predicates read and the cost stops depending on everything else.
	AsOf(rev int64, within ...[]string) (Snapshot, error)
	// List enumerates the stored entities whose ref satisfies pred (nil = all). This is
	// the query that stands in for a namespace: "everything in system X", "everything
	// touching prod". A Go predicate is a placeholder for a match expression — a
	// cross-boundary query cannot stay a closure — but it is what the CLI's listings
	// are written against today.
	List(pred func(Ref) bool) []Entity
	// ListAt enumerates the nodes at a depth the caller names — see Reader.ListAt.
	ListAt(at []string, depth int, pred func(Ref) bool) []Entity
	// ReadAt is the node standing at a path — see Reader.ReadAt.
	ReadAt(at []string) (node *ir.Node, ok bool, err error)
	// WatchSubtree observes exactly the slice standing at a prefix of an address — one
	// (system, kind), or a whole system given the system alone — rather than the whole store.
	// It is what a scoped trigger watches, so that observation is precisely what the trigger
	// matches and no root watch is held.
	//
	// A PREFIX rather than a (system, kind) pair, because that is what it always was: the
	// implementation built a path out of the two and the second was optional. A trailing
	// empty segment is dropped, so the pair form still says what it said.
	//
	// DEPTH IS THE CALLER'S, and it is the whole address's depth rather than the depth below
	// the prefix — the same number `ListAt` takes, for the same reason. A document does not say
	// where its entities end (docs/working/entity-tree.md), so a watch cannot know. Reporting at
	// a fixed arity delivers a change to something deeper as a change to its three-segment
	// ancestor, and every consumer comparing the reported ref to an address it already holds —
	// the causal chain above all — is then comparing an ancestor with a descendant and
	// answering no.
	//
	// The caller always knows. A rule's own pattern says how deep its subjects are, and a
	// caller watching one entity passes that entity's depth.
	//
	// The depth bounds a report from BELOW only. A value that bottomed out above it — a scalar
	// at the prefix, or at the address being watched — is reported where it stands.
	//
	// Unlike Observe it is LEVEL-TRIGGERED at the open: it first replays what already
	// stands in the slice as Set events, then streams changes. A rule is a statement about
	// the world, and a trigger installed over a populated verse should act on the entities
	// that already match it rather than waiting for each to be re-touched.
	//
	// It is EDGE-FAITHFUL: one commit in, at most one commit out, never several folded
	// together. A watch that coalesced a burst would be exact for a level and lossy for an
	// edge — a value that appeared and disappeared across two commits of one burst would
	// leave no trace in the merged diff — and a consumer cannot recover a transition the
	// stream never reported.
	WatchSubtree(depth int, prefix ...string) (<-chan Commit, func(), error)
	// WatchPattern is WatchSubtree asked with the PATTERN itself, which is what a question that
	// reaches across depths needs: `demo..` has no one depth to report at, and a prefix and a
	// number cannot say it. The place watched is its own argument, because a rule about one node
	// watches the place above it. The two flags are the watch's manners — whether to wait on a
	// path holding nothing, and whether to open with what already stands there.
	WatchPattern(prefix []string, at *kpath.KPath, waitIfAbsent, noInit bool) (<-chan Commit, func(), error)
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

// ABSENCE IS NOT A PATTERN, and used to be spelled as one: `Absent()` returned `!irtype null`,
// which the store recognized structurally and never evaluated. See Require.Absent — the
// question moved onto its own field, where it cannot be mistaken for the value question that
// spelling actually asks.
