package entity

import (
	"sync"
)

// SystemPrincipal is versed itself: the principal of a write nothing outside verse asked for
// — a mirror's projection, a question posed, the charter restored at start. It owns the root
// of the tree and every kind-rooted node under it, and it co-owns everything below.
const SystemPrincipal = "system"

// PermsSystem is the kind under verse's root that holds the perms mirror: one record per node
// whose owner is on record, keyed by the node's own address.
const PermsSystem = "perms"

// OwnerField is the field of a perms record naming the node's principal owner.
const OwnerField = "owner"

// PermsKind is the kind of a perms record: one per NODE whose owner is on record.
const PermsKind = "node"

// PermsRef is the perms record about a node: `verse.perms.node.<address>`, the address spelled
// as one segment. Flat rather than nested under the node's own segments, so a field named
// `owner` in the mirror can never be mistaken for a child node called owner, and so that the
// owner of any node is a walk up its address prefixes rather than a walk down a second tree.
// Four segments, because that is how deep an entity in verse's half stands: a listing under
// `verse` walks to that depth, and a record one shorter reads as an entity per field.
func PermsRef(of Ref) Ref { return VerseRef(PermsSystem, PermsKind, of.String()) }

// Attribution is what an attributed store shares across the views derived from it: which
// nodes this process has already seen an owner recorded for, and what each address-shaped
// principal resolves to. One per underlying store; As derives a per-principal view over it.
//
// The cache is an optimisation and not the truth. The owner record is written under an
// absence precondition, so a process that has forgotten (a restart, a second replica) costs
// one refused write per node and never a second owner.
type Attribution struct {
	mu sync.Mutex
	// recorded is every node whose owner record this process has written or found standing.
	recorded map[Ref]struct{}
	// owners memoises resolve: an address-shaped principal to the principal that owns it.
	owners map[string]string
}

// As is a view of s that acts as principal: every write made through it that names no
// principal of its own is stamped with this one, and every entity a write brings into being
// gets its owner recorded. A store already attributed is re-derived rather than wrapped
// twice, sharing what the first knows.
//
// The empty principal is a view that stamps nothing — an api request that named nobody —
// and records no owner, so the node reads as its parent's, which is the answer a
// read-write grantee's creation gets too.
func As(s Store, principal string) Store {
	if a, ok := s.(*attributed); ok {
		return &attributed{Store: a.Store, attr: a.attr, principal: principal}
	}
	return &attributed{Store: s, attr: &Attribution{}, principal: principal}
}

// PrincipalOf reports the principal a store acts as, and "" for one that is not attributed.
func PrincipalOf(s Store) string {
	if a, ok := s.(*attributed); ok {
		return a.principal
	}
	return ""
}

// attributed is the store a principal writes through. Everything but Write and Scope
// delegates, so it is a stamp and not a second store, the same shape as the engine's causal
// store — and for the same reason: Modify takes a Store, so the compare-and-swap path is
// covered without knowing this exists.
type attributed struct {
	Store
	attr      *Attribution
	principal string
}

// Write stamps the principal where the write names none, commits through the underlying
// store, and keeps the perms mirror in step: an entity's owner is recorded when it is first
// written, and forgotten when the whole entity is deleted. Ownership changes only by create
// and delete, so those are the only two writes that touch the mirror.
func (a *attributed) Write(w Write) (bool, int64, error) {
	if w.Principal == "" {
		w.Principal = a.principal
	}
	committed, rev, err := a.Store.Write(w)
	if !committed || err != nil {
		return committed, rev, err
	}
	ref := w.At.Ref
	if ref.Reserved() && ref.System() == PermsSystem {
		return committed, rev, nil // the mirror keeps no records about itself
	}
	if w.Value == nil && w.At.IsEntity() && w.Sub == "" {
		a.forget(ref)
		return committed, rev, nil
	}
	if w.Principal != "" {
		a.record(ref, w.Principal)
	}
	return committed, rev, nil
}

// Scope re-wraps rather than delegating, so a scoped write off an attributed store keeps its
// principal; the embedded Store's Scope would hand back the inner store and lose it.
func (a *attributed) Scope(name string) Store {
	return &attributed{Store: a.Store.Scope(name), attr: a.attr, principal: a.principal}
}

// record writes the owner of ref, once. The requester holds `owner` everywhere under the
// wide-open default, so the requester is the owner — resolved through the perms mirror when
// the principal is itself an address, since a charter or a source acts as whoever owns it.
//
// Under an absence precondition: the first writer of a node is its owner and a later write
// by anybody cannot move that, which is the "fixed when the node is created" half of the
// model with no read ahead of the write.
func (a *attributed) record(ref Ref, principal string) {
	a.attr.mu.Lock()
	_, seen := a.attr.recorded[ref]
	a.attr.mu.Unlock()
	if seen {
		return
	}
	owner := a.resolve(principal)
	// A REFUSED WRITE IS THE RECORD STANDING ALREADY, and an error is the store not answering;
	// only the second leaves the node unknown, so only the second is not remembered. Nothing
	// is returned to the caller either way: the entity write landed, and an owner record that
	// did not is a gap the next write closes.
	committed, _, err := a.Store.Write(Write{
		At:        PermsRef(ref).At(),
		Value:     Obj{OwnerField: owner}.Payload(),
		When:      []Require{Created()},
		Principal: SystemPrincipal,
	})
	if err != nil {
		return
	}
	_ = committed
	a.attr.mu.Lock()
	if a.attr.recorded == nil {
		a.attr.recorded = map[Ref]struct{}{}
	}
	a.attr.recorded[ref] = struct{}{}
	a.attr.mu.Unlock()
}

// forget drops the owner record of a deleted entity, so a later creation at the address gets
// an owner of its own rather than inheriting whoever had it before.
func (a *attributed) forget(ref Ref) {
	a.attr.mu.Lock()
	delete(a.attr.recorded, ref)
	a.attr.mu.Unlock()
	_, _, _ = a.Store.Write(Write{At: PermsRef(ref).At(), Principal: SystemPrincipal})
}

// resolve is the owner a principal confers: itself, unless it is the address of a node with
// an owner on record — a rule, a source — in which case the owner of that node, and so on up,
// bounded so that a mirror written in a loop cannot spin this. A user's email looks up
// nothing and resolves to itself, which is what makes one string serve both.
func (a *attributed) resolve(principal string) string {
	a.attr.mu.Lock()
	if o, ok := a.attr.owners[principal]; ok {
		a.attr.mu.Unlock()
		return o
	}
	a.attr.mu.Unlock()
	p := principal
	for hop := 0; hop < 4; hop++ {
		rec, ok, err := a.Store.Get(VerseRef(PermsSystem, PermsKind, p))
		if err != nil {
			return principal // the store did not answer; do not remember a guess
		}
		if !ok {
			break
		}
		owner := Str(rec.Payload, OwnerField)
		if owner == "" || owner == p {
			break
		}
		p = owner
	}
	a.attr.mu.Lock()
	if a.attr.owners == nil {
		a.attr.owners = map[string]string{}
	}
	a.attr.owners[principal] = p
	a.attr.mu.Unlock()
	return p
}

// PrincipalSystem is the system under verse's root that lists the principals this verse has
// met, by kind: `verse.principal.user.<email>` for a person, written the first time they
// present themselves over the api. A user is specified in the CLI's config for now, and this
// is where it becomes data. A runner is the other kind a principal can be, and gets its own.
const PrincipalSystem = "principal"

// UserKind is the kind of a principal who is a person, identified by email.
const UserKind = "user"

// UserRef addresses the record of one user having acted here.
func UserRef(principal string) Ref { return VerseRef(PrincipalSystem, UserKind, principal) }

// The optional read-side methods a store may have pass through, or a wrapped store would look
// like one that cannot say a read failed, and a restore over an unreadable store would read as
// an empty one — the defect ReadFailed exists to refuse. Each answers as the inner store does
// and falls back to what a caller without the method gets.

func (a *attributed) ReadError() error { return ReadFailed(a.Store) }

func (a *attributed) ListErr(pred func(Ref) bool) ([]Entity, error) { return ListErr(a.Store, pred) }

func (a *attributed) ListAtErr(at []string, depth int, pred func(Ref) bool) ([]Entity, error) {
	return ListAtErr(a.Store, at, depth, pred)
}

func (a *attributed) WatchSubtreeAt(depth int, waitIfAbsent bool, prefix ...string) (<-chan Commit, func(), error) {
	if n, ok := a.Store.(interface {
		WatchSubtreeAt(int, bool, ...string) (<-chan Commit, func(), error)
	}); ok {
		return n.WatchSubtreeAt(depth, waitIfAbsent, prefix...)
	}
	ch, unsub := a.Store.Observe()
	return ch, unsub, nil
}

func (a *attributed) Standing(prefix []string) (bool, error) {
	if h, ok := a.Store.(interface{ Standing([]string) (bool, error) }); ok {
		return h.Standing(prefix)
	}
	return true, nil
}

func (a *attributed) Close() error {
	if h, ok := a.Store.(interface{ Close() error }); ok {
		return h.Close()
	}
	return nil
}
