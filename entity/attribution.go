package entity

import (
	"errors"
	"fmt"
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

// ErrForbidden is a write or a read refused by policy: the principal holds nothing at the
// path that admits it. It is the CALLER's answer and not the store's: nothing failed, nothing
// is retried, and the message names the level it wanted and the record that decided. Distinct
// from a precondition that did not hold — the world saying no is a normal answer, and this is
// not the world.
var ErrForbidden = errors.New("refused")

// Attribution is what an attributed store shares across the views derived from it: the perms
// records this process has read, which nodes it has settled the owner of, what each address-
// shaped principal resolves to, and whether a refusal refuses. One per underlying store; As
// derives a per-principal view over it.
//
// The cache is an optimisation and not the truth. The owner record is written under an
// absence precondition, so a process that has forgotten (a restart, a second replica) costs
// one refused write per node and never a second owner. A grant is different: a revoked grant
// cached for the life of the process is a check that admits what policy withdrew, so every
// write this process makes to the mirror drops what it cached about that node.
type Attribution struct {
	mu sync.Mutex
	// records memoises the perms record at an address, nil for a known absence. It is what the
	// walk up a node's prefixes reads, and the check consults nothing else.
	records map[string]*permsRecord
	// decided is every node this process has settled the owner record of: written it, found
	// it standing, or found nothing to write because the node above owns it already.
	decided map[Ref]struct{}
	// owners memoises resolve: an address-shaped principal to the principal that owns it. A
	// miss is not kept, since the record it would have found can appear at any install.
	owners map[string]string
	// kinds memoises a principal being listed under a kind; only a listing is kept, since a
	// principal noted stays noted.
	kinds map[string]bool
	// enforce says a refusal refuses. Until it is set the check runs in SHADOW: a refusal is
	// counted and logged and the write proceeds, which is how a deployment finds the flow the
	// seed missed before anything is refused.
	enforce bool
	refused map[Level]int
	// unreadable counts checks the mirror could not answer, which in shadow proceed and are
	// counted apart: a store that is down is not a policy that refused.
	unreadable int
	logf       func(format string, args ...any)
}

// permsRecord is one perms record as the check reads it.
type permsRecord struct {
	owner  string
	grants []Grant
}

// As is a view of s that acts as principal: every write made through it that names no
// principal of its own is stamped with this one, every write is checked against what its
// place permits, and every entity a write brings into being gets its owner recorded. A store
// already attributed is re-derived rather than wrapped twice, sharing what the first knows.
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

// PolicyOf is the attribution an attributed store shares — where enforcement is switched and
// the shadow counters read — and nil for a store that is not attributed.
func PolicyOf(s Store) *Attribution {
	if a, ok := s.(*attributed); ok {
		return a.attr
	}
	return nil
}

// Enforce switches a refusal from counted to refused.
func (a *Attribution) Enforce(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enforce = on
}

// Enforcing reports whether a refusal refuses.
func (a *Attribution) Enforcing() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enforce
}

// Log sets where a shadow refusal is reported, one line each.
func (a *Attribution) Log(logf func(format string, args ...any)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.logf = logf
}

// Refusals is what the check has refused since this process started, by the level the write
// wanted, and how many checks the mirror could not answer. In shadow the writes proceeded;
// enforced, they did not.
func (a *Attribution) Refusals() (byLevel map[string]int, unreadable int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	byLevel = map[string]int{}
	for l, n := range a.refused {
		byLevel[string(l)] = n
	}
	return byLevel, a.unreadable
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

// Write stamps the principal where the write names none, checks it against what its place
// permits, commits through the underlying store, and keeps the perms mirror in step: an
// entity's owner is recorded when it is first written by a principal holding `owner` there,
// and forgotten when the whole entity is deleted. Ownership changes only by create and delete,
// so those are the only two writes that touch the mirror.
//
// THE CHECK IS HERE AND NOWHERE ELSE. A request's store, a firing's causal store and a
// source's reflector are all views derived from this one, so every write verse makes on
// anybody's behalf passes through it — the compare-and-swap ones through Modify included —
// and a write path that does not is a write nothing checks.
func (a *attributed) Write(w Write) (bool, int64, error) {
	if w.Principal == "" {
		w.Principal = a.principal
	}
	ref := w.At.Ref
	wholeDelete := w.Value == nil && w.At.IsEntity() && w.Sub == ""
	// A whole-entity delete is a co-owner's act — eviction — and not a write into the node;
	// everything else is.
	need := ReadWrite
	if wholeDelete {
		need = OwnerLevel
	}
	acc, err := a.accessOf(w.Principal, ref)
	if err != nil {
		if a.attr.noteUnreadable() {
			return false, 0, fmt.Errorf("checking %s at %s: %w", w.Principal, ref, err)
		}
	} else if !acc.level.Admits(need) {
		if a.attr.noteRefusal(need, w.Principal, ref, acc) {
			return false, 0, refusal(need, w.Principal, ref, acc)
		}
	}
	committed, rev, err := a.Store.Write(w)
	if !committed || err != nil {
		return committed, rev, err
	}
	if ref.Reserved() && ref.System() == PermsSystem {
		// The mirror keeps no records about itself, and what this process cached about the
		// node the record is about is stale the moment the record moves.
		if ref.Kind() == PermsKind {
			a.attr.invalidate(ref.ID())
		}
		return committed, rev, nil
	}
	if wholeDelete {
		a.forget(ref)
		return committed, rev, nil
	}
	if w.Principal != "" && acc.level == OwnerLevel {
		a.record(ref, acc.who)
	}
	return committed, rev, nil
}

// Scope re-wraps rather than delegating, so a scoped write off an attributed store keeps its
// principal; the embedded Store's Scope would hand back the inner store and lose it.
func (a *attributed) Scope(name string) Store {
	return &attributed{Store: a.Store.Scope(name), attr: a.attr, principal: a.principal}
}

// access is what the check found: the principal a write acts as once an address is resolved
// to its owner, the level held at the node, whether control is held there, and the nearest
// owner record consulted, for the refusal to name.
type access struct {
	who     string
	level   Level
	control bool
	nearest string
}

// Holds reports whether principal holds level at ref through s, which must be an attributed
// store. The error is a mirror that did not answer, which is not a false: a check that fails
// closed on an unreadable mirror refuses everything while the store is down, and one that
// fails open admits everything, and neither is an answer this function gives.
func Holds(s Store, principal string, level Level, ref Ref) (bool, error) {
	a, ok := s.(*attributed)
	if !ok {
		return false, fmt.Errorf("%s is not an attributed store", ref)
	}
	acc, err := a.accessOf(principal, ref)
	if err != nil {
		return false, err
	}
	if level == Control {
		return acc.control, nil
	}
	return acc.level.Admits(level), nil
}

// accessOf is the check: what principal holds at ref.
//
// The principal is first resolved to the owner it confers — a rule or a source acts as whoever
// owns its record — and the system holds everything, since it owns the root and co-owns every
// node beneath. Otherwise the node's prefixes are walked from the node up:
//
//   - a record whose owner is the principal makes them a CO-OWNER, whatever stands between:
//     ownership descends all the way down.
//   - a record's GRANTS reach the node only until an owner record has been passed on the way
//     up — a grant does not cross a node carrying its own owner. The grants on that owner
//     record itself do reach it, since they hang there; the grants above do not.
//
// And the base case at the top, where no record was found: the root is the system's, and the
// world's half of the tree is open — everyone holds `owner` there until a record says
// otherwise, which is the open verse the model describes, and the one grant that is not data.
func (a *attributed) accessOf(principal string, ref Ref) (access, error) {
	who := a.resolve(principal)
	acc := access{who: who, nearest: SystemPrincipal + " (the root)"}
	if who == SystemPrincipal {
		acc.level, acc.control = OwnerLevel, true
		return acc, nil
	}
	segs := ref.Segs()
	crossed := false
	for i := len(segs); i >= 1; i-- {
		key := RefAt(segs[:i]...).String()
		rec, err := a.recordAt(key, false)
		if err != nil {
			return access{}, err
		}
		if rec == nil {
			continue
		}
		if rec.owner != "" && rec.owner == who {
			acc.level, acc.control = OwnerLevel, true
			return acc, nil
		}
		if !crossed {
			for _, g := range rec.grants {
				if !g.Covers(segs) {
					continue
				}
				for l, names := range g.To {
					if !a.named(names, who) {
						continue
					}
					if l == Control {
						acc.control = true
					} else if l.rank() > acc.level.rank() {
						acc.level = l
					}
				}
			}
		}
		if rec.owner != "" && !crossed {
			crossed = true
			acc.nearest = rec.owner + " at " + key
		}
	}
	if !crossed && !ref.Reserved() {
		acc.level, acc.control = OwnerLevel, true
	}
	return acc, nil
}

// named reports whether a grantee list names the principal: by name, as everyone, or as a kind
// the principal is listed under.
func (a *attributed) named(names []string, who string) bool {
	for _, n := range names {
		switch n {
		case who, Everyone:
			return true
		case UserKind, RunnerKind, AgentKind:
			if who != "" && a.listedAs(n, who) {
				return true
			}
		}
	}
	return false
}

// listedAs reports whether who is listed under `verse.principal.<kind>`.
func (a *attributed) listedAs(kind, who string) bool {
	key := kind + "/" + who
	a.attr.mu.Lock()
	yes := a.attr.kinds[key]
	a.attr.mu.Unlock()
	if yes {
		return true
	}
	_, ok, err := a.Store.Get(PrincipalRef(kind, who))
	if err != nil || !ok {
		return false
	}
	a.attr.mu.Lock()
	if a.attr.kinds == nil {
		a.attr.kinds = map[string]bool{}
	}
	a.attr.kinds[key] = true
	a.attr.mu.Unlock()
	return true
}

// refusal spells a refused write: who wanted what, where, and what decided it.
func refusal(need Level, principal string, ref Ref, acc access) error {
	who := principal
	if who == "" {
		who = "an unattributed request"
	} else if acc.who != principal {
		who = principal + " (acting as " + acc.who + ")"
	}
	held := "nothing"
	if acc.level != NoAccess {
		held = string(acc.level)
	}
	return fmt.Errorf("%w: %s holds %s at %s and %s needs %s; the nearest owner on record is %s",
		ErrForbidden, who, held, ref, verb(need), need, acc.nearest)
}

func verb(need Level) string {
	if need == OwnerLevel {
		return "deleting it"
	}
	return "writing it"
}

// noteRefusal counts a refusal and reports whether it refuses.
func (a *Attribution) noteRefusal(need Level, principal string, ref Ref, acc access) bool {
	a.mu.Lock()
	if a.refused == nil {
		a.refused = map[Level]int{}
	}
	a.refused[need]++
	enforce, logf := a.enforce, a.logf
	a.mu.Unlock()
	if !enforce && logf != nil {
		logf("perms: %v", refusal(need, principal, ref, acc))
	}
	return enforce
}

// noteUnreadable counts a check the mirror could not answer and reports whether that refuses.
func (a *Attribution) noteUnreadable() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.unreadable++
	return a.enforce
}

// record settles the owner of ref, once: the principal that held `owner` there when it was
// first written — resolved through the perms mirror already, since a charter or a source acts
// as whoever owns it.
//
// A RECORD IS WRITTEN ONLY WHERE OWNERSHIP CHANGES. The owner of any node is the nearest record
// up its address, and the root is the system's, so a node whose owner is the one already on
// record above it needs no record of its own: the mirror records exactly the changes of
// ownership down a subtree, and a source reflecting ten thousand refs under a slice the
// system owns writes none. What it does write goes under an absence precondition: the first
// writer of a node is its owner and a later write by anybody cannot move that, which is the
// "fixed when the node is created" half of the model with no read ahead of the write.
func (a *attributed) record(ref Ref, owner string) {
	a.attr.mu.Lock()
	_, seen := a.attr.decided[ref]
	a.attr.mu.Unlock()
	if seen {
		return
	}
	if owner == a.ownerAbove(ref) {
		a.decide(ref, nil)
		return
	}
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
	rec := &permsRecord{owner: owner}
	if !committed {
		rec, err = a.recordAt(ref.String(), true) // whoever got there first
		if err != nil {
			return
		}
	}
	a.decide(ref, rec)
}

// decide remembers that ref's owner record is settled, and what the mirror now says at that
// address for the walks and resolutions that read it — including that it says nothing.
func (a *attributed) decide(ref Ref, rec *permsRecord) {
	key := ref.String()
	a.attr.mu.Lock()
	if a.attr.decided == nil {
		a.attr.decided = map[Ref]struct{}{}
	}
	a.attr.decided[ref] = struct{}{}
	if a.attr.records == nil {
		a.attr.records = map[string]*permsRecord{}
	}
	a.attr.records[key] = rec
	if rec != nil && rec.owner != "" {
		if a.attr.owners == nil {
			a.attr.owners = map[string]string{}
		}
		a.attr.owners[key] = rec.owner
	}
	a.attr.mu.Unlock()
}

// ownerAbove is the owner on record nearest above ref: its longest prefix with a record,
// and the system where none has one, since the system owns the root.
func (a *attributed) ownerAbove(ref Ref) string {
	segs := ref.Segs()
	for i := len(segs) - 1; i >= 1; i-- {
		rec, err := a.recordAt(RefAt(segs[:i]...).String(), false)
		if err == nil && rec != nil && rec.owner != "" {
			return rec.owner
		}
	}
	return SystemPrincipal
}

// recordAt reads the perms record at an address, nil when the mirror holds none, through the
// cache unless told to look again. The error is the store not answering, and nothing is
// remembered from one.
func (a *attributed) recordAt(key string, fresh bool) (*permsRecord, error) {
	if !fresh {
		a.attr.mu.Lock()
		rec, ok := a.attr.records[key]
		a.attr.mu.Unlock()
		if ok {
			return rec, nil
		}
	}
	e, ok, err := a.Store.Get(VerseRef(PermsSystem, PermsKind, key))
	if err != nil {
		return nil, err
	}
	var rec *permsRecord
	if ok {
		grants, gerr := ParseGrants(e.Payload)
		if gerr != nil {
			// A line that cannot be read grants nothing, and says so once where the record
			// is read rather than silently at every check.
			a.attr.mu.Lock()
			logf := a.attr.logf
			a.attr.mu.Unlock()
			if logf != nil {
				logf("perms: record at %s: %v", key, gerr)
			}
		}
		rec = &permsRecord{owner: Str(e.Payload, OwnerField), grants: grants}
	}
	a.attr.mu.Lock()
	if a.attr.records == nil {
		a.attr.records = map[string]*permsRecord{}
	}
	a.attr.records[key] = rec
	a.attr.mu.Unlock()
	return rec, nil
}

// invalidate drops what this process cached about a node's record — and every resolution,
// since any of them may have read it — so the next check reads the mirror as it now stands.
func (a *Attribution) invalidate(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.records, key)
	a.owners = nil
	if ref, err := ParseAddr(key); err == nil {
		delete(a.decided, ref)
	}
}

// forget drops the owner record of a deleted entity, so a later creation at the address gets
// an owner of its own rather than inheriting whoever had it before.
func (a *attributed) forget(ref Ref) {
	a.attr.invalidate(ref.String())
	_, _, _ = a.Store.Write(Write{At: PermsRef(ref).At(), Principal: SystemPrincipal})
}

// resolve is the owner a principal confers: itself, unless it is the address of a node in
// verse's own half — a rule, a source — in which case the owner of that node: the nearest
// record at or above it, and the system where none stands, since the system owns every
// kind-rooted node. A rule installed before its record had an owner therefore acts as the
// system until the charter is re-applied, and not as an address, because an address is not a
// party and cannot be answerable for anything. A user's email is not in verse's half and
// resolves to itself, which is what makes one string serve both.
//
// Only a resolution that found a record is kept: the record can arrive at any install, and a
// miss remembered would leave every entity the rule creates the system's for the life of the
// process. A record this process writes is what the next resolution reads (decide).
func (a *attributed) resolve(principal string) string {
	a.attr.mu.Lock()
	if o, ok := a.attr.owners[principal]; ok {
		a.attr.mu.Unlock()
		return o
	}
	a.attr.mu.Unlock()
	ref, err := ParseAddr(principal)
	if err != nil || !ref.Reserved() {
		return principal
	}
	p, found := principal, false
	for hop := 0; hop < 4; hop++ {
		rec, err := a.recordAt(p, false)
		if err != nil || rec == nil || rec.owner == "" || rec.owner == p {
			break
		}
		p, found = rec.owner, true
	}
	if !found {
		return a.ownerAbove(ref)
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
func UserRef(principal string) Ref { return PrincipalRef(UserKind, principal) }

// PrincipalRef addresses the record of one principal of a kind having acted here.
func PrincipalRef(kind, principal string) Ref { return VerseRef(PrincipalSystem, kind, principal) }

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
