package entity

import "github.com/signadot/tony-format/go-tony/ir"

// Reader is the READ half of a Store: what it takes to evaluate a predicate over the verse
// and to select the entities satisfying one. It exists so that "evaluate this against the
// store" and "evaluate this against the store as it stood at commit C" are the same call
// with a different receiver, rather than two code paths that must be kept saying the same
// thing.
//
// A Store is a Reader. So is a Snapshot. Anything that only reads should say so by taking
// this — a match, a formula, a selection — since none of them has any business writing,
// observing, or scoping.
type Reader interface {
	// Get returns an entity by ref: ok reports whether it exists, err reports whether the
	// read could be performed at all. See Store.Get — the distinction is the point.
	Get(ref Ref) (Entity, bool, error)
	// List enumerates the stored entities whose ref satisfies pred (nil = all).
	List(pred func(Ref) bool) []Entity
}

// Snapshot is a Reader fixed at one revision: every read answers as of that commit, and no
// read of it can observe a later write. That is what makes two questions asked of the same
// snapshot — "does this condition hold" and "which entities does this select" — answers about
// ONE world rather than two reads that a concurrent commit can slide between.
//
// It is a value, not a lease: nothing is held open on the store, and letting one go costs
// nothing. Reading a revision the store no longer retains is an error at the point the
// snapshot is taken, not a silently wrong answer later.
type Snapshot interface {
	Reader
	// Rev reports the revision this snapshot reads at.
	Rev() int64
}

// docSnapshot is a Snapshot over one root-rooted document read at a commit — the whole verse
// as it stood, in hand. Both reads are then pure functions of that node: no further round
// trip, and no way for a later commit to be visible to one read and not another.
//
// Holding the whole document is the right trade at verse's scale and is what the substrate
// hands back anyway (one MatchAt over the root). If a verse ever outgrows that, the
// interface is what stays: a snapshot that reads lazily at a pinned commit satisfies it
// identically.
type docSnapshot struct {
	root string   // the document's root key, so a ref can be navigated to
	rev  int64    // the commit this reads at
	doc  *ir.Node // root-rooted: {<root>: {system: {kind: {id: payload}}}}
}

func (s docSnapshot) Rev() int64 { return s.rev }

// Get reads one entity out of the snapshot. A missing or tombstoned path is absence, and
// the error is always nil: the read that could have failed already happened, when the
// snapshot was taken.
func (s docSnapshot) Get(ref Ref) (Entity, bool, error) {
	p := payloadIn(s.doc, s.root, ref)
	if p == nil {
		return Entity{}, false, nil
	}
	return Entity{Ref: ref, Payload: p}, true, nil
}

// List enumerates the snapshot's entities whose ref satisfies pred (nil = all), skipping
// tombstones — the same walk Store.List does, over a document that is already in hand.
func (s docSnapshot) List(pred func(Ref) bool) []Entity {
	var out []Entity
	for _, ch := range walkEntities(childByKey(s.doc, s.root)) {
		if ch.deleted {
			continue
		}
		if pred == nil || pred(ch.ref) {
			out = append(out, Entity{Ref: ch.ref, Payload: ch.payload})
		}
	}
	return out
}

// compile-time checks: a Store reads like a Reader, and a docSnapshot is a Snapshot.
var (
	_ Reader   = (Store)(nil)
	_ Snapshot = docSnapshot{}
)
