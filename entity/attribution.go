package entity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

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
// records this process has read, which nodes it has settled the owner of, what each place
// acting resolves to, and what it has refused. One per underlying store; As derives
// a per-principal view over it.
//
// The cache is an optimisation and not the truth. The owner record is written under an
// absence precondition, so a process that has forgotten (a restart, a second replica) costs
// one refused write per node and never a second owner. A grant is different: a revoked grant
// cached for the life of the process is a check that admits what policy withdrew, so every
// write this process makes to the mirror drops what it cached about that node.
type Attribution struct {
	mu sync.Mutex
	// records memoises the perms record at an address, nil for a known absence. It is what the
	// walk up a node's prefixes reads, and the check consults nothing else. Once loaded holds
	// the whole mirror (Track), a key that is not here is absent and no read is made for it.
	records map[string]*permsRecord
	loaded  bool
	// decided is every node this process has settled the owner record of: written it, found
	// it standing, or found nothing to write because the node above owns it already.
	decided map[Ref]struct{}
	// owners memoises resolve: a place acting, `via:<address>`, to the principal that owns it. A
	// miss is not kept, since the record it would have found can appear at any install.
	owners map[string]string
	// groups memoises a group's members, dropped when the group is written through this
	// process, since membership is meant to move.
	groups map[string][]string
	// rootDefault is what a node created with no default above it holds out — the root's
	// default (DefaultField), the one default that is not data since the root has no record.
	// The open verse's is `read: ["*"]`: whatever anybody creates is readable by everyone
	// until a default beneath says otherwise. A record written before defaults existed carries
	// no grants field and is read as holding out this.
	rootDefault Levels
	// refused counts what the check refused, by the level the write or read wanted; unreadable
	// counts checks the mirror could not answer, apart, since a store that is down is not a
	// policy that refused; hidden counts what listings, replays and watches left out for a
	// reader, apart again, since a listing that leaves something out is answered.
	refused    map[Level]int
	unreadable int
	hidden     int
	// unparsed counts grantee names a check met that the grammar does not read, which admit
	// nobody — a record from before the grammar, or a line nothing refused where it was written —
	// apart again, since it is neither a refusal nor the store; said names what has been logged.
	unparsed int
	said     map[string]bool
}

// permsRecord is one perms record as the check reads it.
type permsRecord struct {
	owner  string
	under  string
	grants []Grant
	// defaults is what is created beneath this node holds out, when the record says
	// (hasDefault); a record that says nothing inherits.
	defaults   Levels
	hasDefault bool
}

// UnderField is the field of a perms record naming the node this node is ALSO under, for
// ownership: a question stands at its own address and is co-owned by whoever owns what
// posed it — the task, the rule — exactly as it would be had it stood beneath them. The
// link confers co-ownership and nothing else: a grant on the origin does not reach through
// it, which is what keeps a runner's grant on its task away from the questions the task
// raised.
const UnderField = "under"

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
	return &attributed{Store: s, attr: &Attribution{rootDefault: Levels{Read: {Everyone}}}, principal: principal}
}

// PrincipalOf reports the principal a store acts as, and "" for one that is not attributed.
func PrincipalOf(s Store) string {
	if a, ok := s.(*attributed); ok {
		return a.principal
	}
	return ""
}

// PolicyOf is the attribution an attributed store shares — where the open reading rule is
// switched and the refusal counters read — and nil for a store that is not attributed.
func PolicyOf(s Store) *Attribution {
	if a, ok := s.(*attributed); ok {
		return a.attr
	}
	return nil
}

// SetRootDefault sets what a node created with no default above it holds out. nil or empty
// is nothing: a verse where what anybody creates is theirs and their grantees' alone.
func (a *Attribution) SetRootDefault(l Levels) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rootDefault = l
}

// RootDefault reports what a node created with no default above it holds out.
func (a *Attribution) RootDefault() Levels {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rootDefault
}

// Refusals is what the check has refused since this process started, by the level the write
// or read wanted, and how many checks the mirror could not answer. It is what /status reports:
// a count that climbs is a flow somebody is being refused, which is either the policy's
// intent or a row the seed is missing.
func (a *Attribution) Refusals() (byLevel map[string]int, unreadable int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	byLevel = map[string]int{}
	for l, n := range a.refused {
		byLevel[string(l)] = n
	}
	return byLevel, a.unreadable
}

// Hidden is how many entities listings, replays and watches have left out for a reader who
// may not see them.
func (a *Attribution) Hidden() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hidden
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
	act := "writing it"
	if wholeDelete {
		act = "deleting it"
	}
	acc, err := a.check(w.Principal, need, ref, act)
	if err != nil {
		return false, 0, err
	}
	committed, rev, err := a.Store.Write(w)
	if !committed || err != nil {
		return committed, rev, err
	}
	if ref.Reserved() && ref.System() == PermsSystem {
		// The mirror keeps no records about itself, and what this process cached about the
		// node the record is about is stale the moment the record moves.
		if ref.Kind() == PermsKind {
			a.invalidate(ref.ID())
		}
		return committed, rev, nil
	}
	if IsGroup(ref) {
		a.attr.mu.Lock()
		delete(a.attr.groups, ref.String())
		a.attr.mu.Unlock()
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

// Check is the decision the store makes ahead of a write, asked ahead of something else: does
// principal hold level at ref, and if not, is that refused. It is what a route asks before
// delivering an answer whose record it will only write afterwards, so a refusal is a refusal
// and not a record that failed to land. The error is ErrForbidden for a refusal, and the store's
// own error when the mirror did not answer — which refuses too, as an unreadable precondition
// does: a check that fails open admits everything while the store is down.
//
// act is what the caller is about to do, in the refusal's words — "answering it",
// "controlling it", "setting its record" — since the level alone does not say: `owner` is
// what a delete needs and what reading a node's own record needs, and a refusal that guessed
// the act from the level told an owner-record reader they may not delete.
func Check(s Store, principal string, level Level, ref Ref, act string) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil // a store that attributes nothing checks nothing
	}
	_, err := a.check(principal, level, ref, act)
	return err
}

func (a *attributed) check(principal string, need Level, ref Ref, act string) (access, error) {
	acc, err := a.accessOf(principal, ref)
	if err != nil {
		a.attr.noteUnreadable()
		return acc, fmt.Errorf("checking %s at %s: %w", principal, ref, err)
	}
	held := acc.level.Admits(need)
	if need == Control {
		held = acc.control
	}
	if !held {
		a.attr.noteRefusal(need)
		return acc, refusal(need, act, principal, ref, acc)
	}
	return acc, nil
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

// CheckInto is the check a listing or a watch makes at a path: may principal read INTO ref —
// the node itself, or what a grant at it holds out beneath it. A kind-rooted node of verse's
// half is the system's and holds out `owner` beneath itself to everyone, and a listing there
// is answered with what stands beneath, though the node itself is nobody's to write; a place
// somebody took and holds nothing out on is refused whole. The error is ErrForbidden, counted
// once, or the store's own.
func CheckInto(s Store, principal string, ref Ref) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	acc, err := a.accessOf(principal, ref)
	if err != nil {
		a.attr.noteUnreadable()
		return fmt.Errorf("checking %s at %s: %w", principal, ref, err)
	}
	if acc.level.Admits(Read) {
		return nil
	}
	// Beneath: a probe one segment down, which a grant at the node covering what is beneath
	// it reaches and a record above it decides as it would for any child.
	below, err := a.accessOf(principal, RefAt(append(ref.Segs(), "*")...))
	if err != nil {
		a.attr.noteUnreadable()
		return fmt.Errorf("checking %s at %s: %w", principal, ref, err)
	}
	if below.level.Admits(Read) {
		return nil
	}
	a.attr.noteRefusal(Read)
	return refusal(Read, "reading it", principal, ref, acc)
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
	acc := access{who: who, nearest: SystemDefault + " (the root)"}
	if IsSystem(who) {
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
		if rec.under != "" {
			co, err := a.coOwns(who, rec.under, 3)
			if err != nil {
				return access{}, err
			}
			if co {
				acc.level, acc.control = OwnerLevel, true
				return acc, nil
			}
		}
		if !crossed {
			for _, g := range rec.grants {
				if !g.Covers(segs) {
					continue
				}
				for l, names := range g.To {
					if !a.named(names, principal, who, key) {
						continue
					}
					if l == Control || l == OwnerLevel {
						acc.control = true // an owner holds control, by grant as by record
					}
					if l != Control && l.rank() > acc.level.rank() {
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

// The read side of the view: a read answers only what the principal may read. Get refuses, as
// a write does; a listing, a replay and a watch leave out what may not be seen, so the same
// principal sees the same set whichever way they ask. The system reads everything and pays
// for none of this.

// keep reports whether the view's principal may read ref, for a listing: false and no error
// for what they may not, an error for a mirror that did not answer.
//
// LEAVING SOMETHING OUT OF A LISTING IS NOT A REFUSAL. The listing is answered, with what the
// reader may see in it, and counting every record withheld from every `verse data` as a
// refused request would bury the count that matters — a request turned away — under the
// mirror's own records, which no listing of verse's half may show a person. What is withheld
// is counted apart (Hidden).
func (a *attributed) keep(ref Ref) (bool, error) {
	if IsSystem(a.principal) {
		return true, nil
	}
	acc, err := a.accessOf(a.principal, ref)
	if err != nil {
		a.attr.noteUnreadable()
		return false, err
	}
	if acc.level.Admits(Read) {
		return true, nil
	}
	a.attr.noteHidden()
	return false, nil
}

// Unparsed is how many times a check met a grantee name the grammar does not read, which
// admitted nobody.
func (a *Attribution) Unparsed() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.unparsed
}

// noteUnparsed counts a grantee that does not parse, and says it once per record and name:
// nothing rewrites it, so what it holds out is nothing, said out loud.
func (a *Attribution) noteUnparsed(at, name string, err error) {
	a.mu.Lock()
	a.unparsed++
	key := at + "\x00" + name
	first := !a.said[key]
	if first {
		if a.said == nil {
			a.said = map[string]bool{}
		}
		a.said[key] = true
	}
	a.mu.Unlock()
	if first {
		slog.Warn("perms: a grantee the grammar does not read admits nobody", "record", at, "grantee", name, "err", err)
	}
}

// noteHidden counts an entity a listing withheld.
func (a *Attribution) noteHidden() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hidden++
}

func (a *attributed) keepAll(ents []Entity) ([]Entity, error) {
	if IsSystem(a.principal) {
		return ents, nil
	}
	out := ents[:0:0]
	for _, e := range ents {
		ok, err := a.keep(e.Ref)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// narrow is a commit as this view's principal sees it (Commit.Only). A mirror that did not
// answer hides nothing here: a watch has no error to return, and a commit withheld would read
// as a gap, so the check's own count is what says a read went unchecked.
//
// A CREATION IS SEEN ONCE by everyone the open base case admits: the owner record follows the
// write it records, so the commit that creates a node reaches a live watch before the record
// that would hide it lands, and is judged by what stands then. Every commit after it is judged
// by the record. Writing the record first would close that window and open a worse one — a
// record standing for a node whose write then failed makes the next creator there not its
// owner — so the window stays, and is one commit wide.
func (a *attributed) narrow(c Commit) Commit {
	if IsSystem(a.principal) {
		return c
	}
	return c.Only(func(r Ref) bool { ok, _ := a.keep(r); return ok })
}

func (a *attributed) narrowing(in <-chan Commit) <-chan Commit {
	if IsSystem(a.principal) {
		return in
	}
	out := make(chan Commit)
	go func() {
		defer close(out)
		for c := range in {
			out <- a.narrow(c)
		}
	}()
	return out
}

func (a *attributed) Get(ref Ref) (Entity, bool, error) {
	if !IsSystem(a.principal) {
		if _, err := a.check(a.principal, Read, ref, "reading it"); err != nil {
			return Entity{}, false, err
		}
	}
	return a.Store.Get(ref)
}

func (a *attributed) List(pred func(Ref) bool) []Entity {
	out, _ := a.keepAll(a.Store.List(pred))
	return out
}

func (a *attributed) ListAt(at []string, depth int, pred func(Ref) bool) []Entity {
	out, _ := a.keepAll(a.Store.ListAt(at, depth, pred))
	return out
}

func (a *attributed) Since(from int64, at ...string) ([]Commit, error) {
	commits, err := a.Store.Since(from, at...)
	if IsSystem(a.principal) {
		return commits, err
	}
	for i := range commits {
		commits[i] = a.narrow(commits[i])
	}
	return commits, err
}

func (a *attributed) Observe() (<-chan Commit, func()) {
	ch, unsub := a.Store.Observe()
	return a.narrowing(ch), unsub
}

func (a *attributed) WatchSubtree(depth int, prefix ...string) (<-chan Commit, func(), error) {
	ch, unsub, err := a.Store.WatchSubtree(depth, prefix...)
	if err != nil {
		return nil, nil, err
	}
	return a.narrowing(ch), unsub, nil
}

func (a *attributed) AsOf(rev int64, within ...[]string) (Snapshot, error) {
	snap, err := a.Store.AsOf(rev, within...)
	if err != nil || IsSystem(a.principal) {
		return snap, err
	}
	return narrowed{Snapshot: snap, a: a}, nil
}

// narrowed is a snapshot read through a view: what the principal may not read is not in it.
type narrowed struct {
	Snapshot
	a *attributed
}

func (n narrowed) Get(ref Ref) (Entity, bool, error) {
	if _, err := n.a.check(n.a.principal, Read, ref, "reading it"); err != nil {
		return Entity{}, false, err
	}
	return n.Snapshot.Get(ref)
}

func (n narrowed) List(pred func(Ref) bool) []Entity {
	out, _ := n.a.keepAll(n.Snapshot.List(pred))
	return out
}

func (n narrowed) ListAt(at []string, depth int, pred func(Ref) bool) []Entity {
	out, _ := n.a.keepAll(n.Snapshot.ListAt(at, depth, pred))
	return out
}

// Head is what a docSnapshot answers beside Rev, kept for a caller that asks the snapshot.
func (n narrowed) Head() int64 {
	if h, ok := n.Snapshot.(interface{ Head() int64 }); ok {
		return h.Head()
	}
	return n.Snapshot.Rev()
}

// coOwns reports whether who owns a record at or above addr — the node a record said it was
// under — following further `under` links a bounded number of hops. Only ownership is read
// here: the grants along the way are the origin's and stop at it.
func (a *attributed) coOwns(who, addr string, hops int) (bool, error) {
	if hops == 0 || who == "" {
		return false, nil
	}
	ref, err := ParseAddr(addr)
	if err != nil {
		return false, nil // a link that names no place links nothing
	}
	segs := ref.Segs()
	for i := len(segs); i >= 1; i-- {
		rec, err := a.recordAt(RefAt(segs[:i]...).String(), false)
		if err != nil {
			return false, err
		}
		if rec == nil {
			continue
		}
		if rec.owner != "" && rec.owner == who {
			return true, nil
		}
		if rec.under != "" {
			if co, err := a.coOwns(who, rec.under, hops-1); err != nil || co {
				return co, err
			}
		}
	}
	return false, nil
}

// named reports whether a grantee list names the principal, by the structure of each name
// (Grantee): `*` names everyone; `<kind>:<id>` the principal named exactly that, or whose owner
// resolves to it, since a grant to a person means them and everything of theirs acting as
// itself; `<kind>:*` any principal whose name, or whose owner's, carries the kind; `via:` the
// place acting as that address, or beneath it under a trailing `*`, and never its owner, since
// the address is the identity; `group:` the group's members, each named the same way. at is the
// record the names are on, for a name that does not parse to be said against.
func (a *attributed) named(names []string, principal, who, at string) bool {
	return a.namedIn(names, principal, who, at, map[string]bool{})
}

// namedIn is named, walking a group's members with the groups already seen: a group met a
// second time is skipped, so a cycle ends because the walk does and a group nested any depth
// admits its members.
func (a *attributed) namedIn(names []string, principal, who, at string, seen map[string]bool) bool {
	for _, n := range names {
		g, err := ParseGrantee(n)
		if err != nil {
			a.attr.noteUnparsed(at, n, err)
			continue
		}
		switch {
		case g.Kind == Everyone:
			return true
		case g.Kind == ViaKind:
			if KindOf(principal) != ViaKind {
				continue
			}
			if s := g.String(); principal == s ||
				g.Path[len(g.Path)-1] == Everyone && strings.HasPrefix(principal, strings.TrimSuffix(s, Everyone)) {
				return true
			}
		case g.Kind == GroupKind:
			if who == "" || seen[g.ID] {
				continue
			}
			seen[g.ID] = true
			ref := GroupRef(g.ID)
			if a.namedIn(a.members(ref), principal, who, ref.String(), seen) {
				return true
			}
		case g.ID == Everyone:
			if KindOf(principal) == g.Kind || KindOf(who) == g.Kind {
				return true
			}
		default:
			if s := g.String(); principal == s || who == s {
				return true
			}
		}
	}
	return false
}

// members reads a group's members, through the cache; a group that cannot be read has none.
func (a *attributed) members(ref Ref) []string {
	key := ref.String()
	a.attr.mu.Lock()
	ms, ok := a.attr.groups[key]
	a.attr.mu.Unlock()
	if ok {
		return ms
	}
	e, found, err := a.Store.Get(ref)
	if err != nil {
		return nil // not remembered: the store did not answer
	}
	if found {
		ms = Strs(e.Payload, MembersField)
	}
	a.attr.mu.Lock()
	if a.attr.groups == nil {
		a.attr.groups = map[string][]string{}
	}
	a.attr.groups[key] = ms
	a.attr.mu.Unlock()
	return ms
}

// refusal spells a refused write: who wanted what, where, and what decided it.
func refusal(need Level, act, principal string, ref Ref, acc access) error {
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
		ErrForbidden, who, held, ref, act, need, acc.nearest)
}

// noteRefusal counts a refusal, by the level wanted.
func (a *Attribution) noteRefusal(need Level) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refused == nil {
		a.refused = map[Level]int{}
	}
	a.refused[need]++
}

// noteUnreadable counts a check the mirror could not answer.
func (a *Attribution) noteUnreadable() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.unreadable++
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
	// A RECORD ALREADY IN HAND IS THE DECISION. With the mirror loaded every record stands in
	// the cache, and a node written again in a later life of this process — a source
	// re-reflecting an entity it first reflected weeks ago — has its record there. Writing it
	// again would be refused by its own absence precondition, and the store explains a refused
	// write by reading what it descended through: the mirror's whole slice, once per such
	// node, on a store already busy with the pass. Nothing is read here: a cached record is
	// what the checks consult, and it is not less true for having arrived by the load.
	a.attr.mu.Lock()
	known := a.attr.records[ref.String()]
	a.attr.mu.Unlock()
	if known != nil {
		a.decide(ref, known)
		return
	}
	// THE SYSTEM IS ONE OWNER, whichever part wrote: a part is attribution on the write, not a
	// change of ownership, so the docket creating a question beneath the system's root records
	// nothing — and the question's own record, written next under an absence precondition
	// (Establish), is not refused by one this wrote first.
	if above := a.ownerAbove(ref); owner == above || IsSystem(owner) && IsSystem(above) {
		a.decide(ref, nil)
		return
	}
	// A REFUSED WRITE IS THE RECORD STANDING ALREADY, and an error is the store not answering;
	// only the second leaves the node unknown, so only the second is not remembered. Nothing
	// is returned to the caller either way: the entity write landed, and an owner record that
	// did not is a gap the next write closes.
	// WHAT THE NEW NODE HOLDS OUT is decided here, where its record is written: the nearest
	// default up the tree, the root's at the top, copied onto the record as a grant at the
	// node. Always with a grants field, empty or not, so a record that holds out nothing is
	// told apart from one written before defaults existed.
	grants := a.defaultGrants(ref)
	committed, _, err := a.Store.Write(Write{
		At:        PermsRef(ref).At(),
		Value:     Obj{OwnerField: owner, GrantsField: GrantsPayload(ref.Segs(), grants)}.Payload(),
		When:      []Require{Created()},
		Principal: SystemPerms,
	})
	if err != nil {
		return
	}
	rec := &permsRecord{owner: owner, grants: grants}
	if !committed {
		rec, err = a.recordAt(ref.String(), true) // whoever got there first
		if err != nil {
			return
		}
	}
	a.decide(ref, rec)
}

// defaultGrants is what a node created at ref holds out: the nearest default above it as a
// grant at the node, and nothing when that default is empty.
func (a *attributed) defaultGrants(ref Ref) []Grant {
	levels := a.defaultAbove(ref)
	if len(levels) == 0 {
		return []Grant{}
	}
	return []Grant{{At: ref.Segs(), To: map[Level][]string(levels)}}
}

// defaultAbove is the default in force for a node created at ref: the nearest record above it
// that says one, and the root's where none does.
func (a *attributed) defaultAbove(ref Ref) Levels {
	segs := ref.Segs()
	for i := len(segs) - 1; i >= 1; i-- {
		rec, err := a.recordAt(RefAt(segs[:i]...).String(), false)
		if err == nil && rec != nil && rec.hasDefault {
			return rec.defaults
		}
	}
	return a.attr.RootDefault()
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
	// What was WRITTEN is cached; a decision that nothing needed writing caches nothing, since
	// a record may stand there already for another reason — a question's, established beside
	// the entity — and "absent" over it would hide it from every check until the next write
	// to the mirror. An absence is learned by reading, never by deciding.
	if rec != nil {
		if a.attr.records == nil {
			a.attr.records = map[string]*permsRecord{}
		}
		a.attr.records[key] = rec
	}
	if rec != nil && rec.owner != "" {
		if a.attr.owners == nil {
			a.attr.owners = map[string]string{}
		}
		a.attr.owners[ViaKind+":"+key] = rec.owner // what the node confers when it acts (resolve)
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
	return SystemDefault
}

// recordAt reads the perms record at an address, nil when the mirror holds none, through the
// cache unless told to look again. The error is the store not answering, and nothing is
// remembered from one.
func (a *attributed) recordAt(key string, fresh bool) (*permsRecord, error) {
	if !fresh {
		a.attr.mu.Lock()
		rec, ok := a.attr.records[key]
		loaded := a.attr.loaded
		a.attr.mu.Unlock()
		if ok {
			return rec, nil
		}
		if loaded {
			return nil, nil // the whole mirror is here, and this key is not in it
		}
	}
	e, ok, err := a.Store.Get(VerseRef(PermsSystem, PermsKind, key))
	if err != nil {
		return nil, err
	}
	var rec *permsRecord
	if ok {
		// A line that cannot be read grants nothing: ParseGrants refuses the whole field, so a
		// record with one bad line holds out nothing until it is fixed, which is the safe way
		// round and is visible on the record itself. A RECORD WRITTEN BEFORE DEFAULTS EXISTED
		// carries an owner and no grants field at all, and is read as holding out what the
		// root's default says: the open verse's, which is what it held out when written.
		rec = parseRecord(key, e.Payload, a.attr.RootDefault())
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
// With the mirror loaded, a missing key would read as absent, so the record is read back now
// rather than dropped: what this process just wrote is what it checks against next.
func (a *attributed) invalidate(key string) {
	a.attr.mu.Lock()
	delete(a.attr.records, key)
	a.attr.owners = nil
	if ref, err := ParseAddr(key); err == nil {
		delete(a.attr.decided, ref)
	}
	loaded := a.attr.loaded
	a.attr.mu.Unlock()
	if loaded {
		_, _ = a.recordAt(key, true)
	}
}

// forget drops the owner record of a deleted entity, so a later creation at the address gets
// an owner of its own rather than inheriting whoever had it before — AND EVERY RECORD BENEATH
// IT, since a whole delete takes the subtree and a record left standing for a node that is
// gone is a ghost: it owns an address nothing stands at, and refuses the next creator there a
// place that is nobody's. The mirror is keyed by address, so the records beneath are found by
// listing it, which costs the mirror's size once per whole delete.
func (a *attributed) forget(ref Ref) {
	a.invalidate(ref.String())
	_, _, _ = a.Store.Write(Write{At: PermsRef(ref).At(), Principal: SystemPerms})
	prefix := ref.String() + "."
	var beneath []string
	a.attr.mu.Lock()
	loaded := a.attr.loaded
	if loaded {
		for key, rec := range a.attr.records {
			if rec != nil && strings.HasPrefix(key, prefix) {
				beneath = append(beneath, key)
			}
		}
	}
	a.attr.mu.Unlock()
	if !loaded {
		// Not loaded: the mirror is listed, which costs its size once per whole delete.
		for _, rec := range a.Store.ListAt([]string{VerseRoot, PermsSystem, PermsKind}, Depth+1, func(r Ref) bool {
			return strings.HasPrefix(r.ID(), prefix)
		}) {
			beneath = append(beneath, rec.Ref.ID())
		}
	}
	for _, key := range beneath {
		_, _, _ = a.Store.Write(Write{At: VerseRef(PermsSystem, PermsKind, key).At(), Principal: SystemPerms})
		a.invalidate(key)
	}
}

// Track loads the whole perms mirror into the view's cache and keeps it there: a watch on
// `verse.perms` applies every record written — by this process or another — as it lands, so a
// check makes no round trip at all. A key not in the loaded mirror is absent, and no read is
// made to learn it.
//
// WHY IT IS LOADED RATHER THAN READ. The check reads the mirror for every prefix of a written
// address it has not seen, and a source's first pass after a wipe writes thousands of new
// addresses: on a store already busy with those writes, the check's reads queued behind them,
// hit their deadline, and a check whose mirror did not answer refused the write. The mirror is
// small — one record per change of ownership — so holding it is cheaper than asking for it.
//
// The watch is opened after the load, and its opening replay of what stands is applied over
// the load, which is idempotent; what this process writes is read back by invalidate the moment
// it is written, so the watch's copy of that write changes nothing. It stops when ctx does.
func Track(ctx context.Context, s Store) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	// Both failures are the store not answering, and both are marked so: a mirror that could
	// not be loaded is not a mirror that is empty, and the caller asks again until it loads.
	ents, err := ListAtErr(a.Store, []string{VerseRoot, PermsSystem, PermsKind}, Depth+1, nil)
	if err != nil {
		return Unread(fmt.Errorf("loading the perms mirror: %w", err))
	}
	ch, unsub, err := a.Store.WatchSubtree(Depth+1, VerseRoot, PermsSystem, PermsKind)
	if err != nil {
		return Unread(fmt.Errorf("watching the perms mirror: %w", err))
	}
	a.attr.mu.Lock()
	if a.attr.records == nil {
		a.attr.records = map[string]*permsRecord{}
	}
	for _, e := range ents {
		a.attr.records[e.Ref.ID()] = parseRecord(e.Ref.ID(), e.Payload, a.attr.rootDefault)
	}
	a.attr.owners = nil
	a.attr.loaded = true
	a.attr.mu.Unlock()
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-ch:
				if !ok {
					return
				}
				for _, ev := range c.Changes {
					if !ev.Ref.Reserved() || ev.Ref.System() != PermsSystem || ev.Ref.Kind() != PermsKind {
						continue
					}
					a.apply(ev)
				}
			}
		}
	}()
	return nil
}

// Tracked reports whether the view holds the whole mirror.
func (a *Attribution) Tracked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loaded
}

// apply is one change to the mirror, as the watch delivered it.
func (a *attributed) apply(ev Event) {
	key := ev.Ref.ID()
	a.attr.mu.Lock()
	defer a.attr.mu.Unlock()
	if a.attr.records == nil {
		a.attr.records = map[string]*permsRecord{}
	}
	if ev.Op == Delete || ev.To == nil {
		delete(a.attr.records, key)
	} else {
		a.attr.records[key] = parseRecord(key, ev.To, a.attr.rootDefault)
	}
	a.attr.owners = nil
	if ref, err := ParseAddr(key); err == nil {
		delete(a.attr.decided, ref)
	}
}

// parseRecord reads one mirror record as the check holds it, applying the transition rule for
// a record from before defaults existed (see recordAt).
func parseRecord(key string, payload Payload, root Levels) *permsRecord {
	node, _ := PathSegs(key)
	grants, _ := ParseGrants(node, payload)
	rec := &permsRecord{owner: Str(payload, OwnerField), under: Str(payload, UnderField), grants: grants}
	rec.defaults, rec.hasDefault, _ = ParseLevels(payload, DefaultField)
	if rec.owner != "" && Get(payload, GrantsField) == nil && len(root) > 0 {
		if ref, err := ParseAddr(key); err == nil {
			rec.grants = []Grant{{At: ref.Segs(), To: map[Level][]string(root)}}
		}
	}
	return rec
}

// resolve is the owner a principal confers: itself, unless it is a place acting — `via:` a
// rule, a source, a binding — in which case the owner of that node: the nearest record at or
// above it, and the system where none stands, since the system owns every kind-rooted node. A
// rule installed before its record had an owner therefore acts as the system until the charter
// is re-applied, and not as an address, because an address is not a party and cannot be
// answerable for anything. An owner on record is always a principal that resolved already, so
// one record is the whole walk.
//
// Only a resolution that found a record is kept: the record can arrive at any install, and a
// miss remembered would leave every entity the rule creates the system's for the life of the
// process. A record this process writes is what the next resolution reads (decide).
func (a *attributed) resolve(principal string) string {
	if KindOf(principal) != ViaKind {
		return principal
	}
	a.attr.mu.Lock()
	if o, ok := a.attr.owners[principal]; ok {
		a.attr.mu.Unlock()
		return o
	}
	a.attr.mu.Unlock()
	ref, err := ParseAddr(strings.TrimPrefix(principal, ViaKind+":"))
	if err != nil {
		return principal // a via that names no one place acts as nobody's
	}
	rec, err := a.recordAt(ref.String(), false)
	if err != nil || rec == nil || rec.owner == "" {
		return a.ownerAbove(ref)
	}
	a.attr.mu.Lock()
	if a.attr.owners == nil {
		a.attr.owners = map[string]string{}
	}
	a.attr.owners[principal] = rec.owner
	a.attr.mu.Unlock()
	return rec.owner
}

// AddGrant appends a grant to the perms record of ref, as the system. The mirror is versed's
// to write, and a grant is an owner's act that versed carries out for them — a spawn assigning
// a task to a runner creates the runner's grant on it — so it bypasses the check the way an
// owner record does, and what this process cached about the node is dropped with it.
//
// A read-modify-write under a precondition, since the grants are one list and a merge would
// replace it: two grants landing at once would otherwise keep one.
func AddGrant(s Store, ref Ref, g Grant) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil // a store that attributes nothing keeps no mirror
	}
	rec := PermsRef(ref)
	for {
		cur, exists, err := a.Store.Get(rec)
		if err != nil {
			return fmt.Errorf("granting at %s: %w", ref, err)
		}
		var grants []Grant
		when := Created()
		if exists {
			grants, _ = ParseGrants(ref.Segs(), cur.Payload)
			when = Require{Pattern: Unchanged(cur.Payload)}
		}
		grants = append(grants, g)
		committed, _, err := a.Store.Write(Write{
			At:        rec.At(),
			Value:     Obj{GrantsField: GrantsPayload(ref.Segs(), grants)}.Payload(),
			When:      []Require{when},
			Principal: SystemPerms,
		})
		if err != nil {
			return fmt.Errorf("granting at %s: %w", ref, err)
		}
		if committed {
			a.invalidate(ref.String())
			return nil
		}
	}
}

// SetGrants replaces the grants on the perms record of ref, as the system: what a question
// holds out is exactly its binding's, so a change of binding is a replacement and not an
// addition. An empty list is written as one — a grants field holding nothing — since a record
// with no grants field at all is one from before defaults existed, read as the root's.
func SetGrants(s Store, ref Ref, gs []Grant) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	value := Obj{GrantsField: GrantsPayload(ref.Segs(), gs)}
	_, _, err := a.Store.Write(Write{At: PermsRef(ref).At(), Value: value.Payload(), Principal: SystemPerms})
	if err != nil {
		return fmt.Errorf("granting at %s: %w", ref, err)
	}
	a.invalidate(ref.String())
	return nil
}

// SetDefault sets what a node created beneath ref holds out (DefaultField), as the system: a
// nil map removes the field, so the node inherits again; an empty one says nothing is held
// out.
func SetDefault(s Store, ref Ref, levels Levels) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	value := Obj{DefaultField: Removed()}
	if levels != nil {
		// A write MERGES, and the default is an object: a level the new default does not name
		// has to be removed by name, or it stands from the default before.
		d := Obj{}
		for _, l := range []Level{Read, ReadWrite, OwnerLevel, Control} {
			if names, ok := levels[l]; ok {
				d[string(l)] = strList(names)
			} else {
				d[string(l)] = Removed()
			}
		}
		value[DefaultField] = d
	}
	_, _, err := a.Store.Write(Write{At: PermsRef(ref).At(), Value: value.Payload(), Principal: SystemPerms})
	if err != nil {
		return fmt.Errorf("setting the default at %s: %w", ref, err)
	}
	a.invalidate(ref.String())
	return nil
}

// Claim writes the owner record of ref for principal where none stands — the record a place
// needs before it can carry a default or a grant of its own — holding out what the default
// above says, exactly as a creation would. principal must hold `owner` there already: this is
// a place being taken by whoever may take it, never a way to take one from its owner. A record
// already standing is left as it is, whoever's it is; the caller decides what that means.
func Claim(s Store, ref Ref, principal string) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	acc, err := a.check(principal, OwnerLevel, ref, "taking it")
	if err != nil {
		return err
	}
	rec, err := a.recordAt(ref.String(), true)
	if err != nil {
		return fmt.Errorf("claiming %s: %w", ref, err)
	}
	if rec != nil {
		return nil
	}
	grants := a.defaultGrants(ref)
	committed, _, err := a.Store.Write(Write{
		At:        PermsRef(ref).At(),
		Value:     Obj{OwnerField: acc.who, GrantsField: GrantsPayload(ref.Segs(), grants)}.Payload(),
		When:      []Require{Created()},
		Principal: SystemPerms,
	})
	if err != nil {
		return fmt.Errorf("claiming %s: %w", ref, err)
	}
	a.invalidate(ref.String())
	_ = committed // refused is a record standing already, which the next read shows
	return nil
}

// Revoke takes grantees off the grants at ref's own record, level by level: every line at the
// node loses the named grantees at the named levels, and a line left holding out nothing goes.
// One line's worth, so that taking one person's read back leaves everybody else's standing —
// which replacing the list whole (SetGrants) does not.
func Revoke(s Store, ref Ref, levels Levels) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	rec := PermsRef(ref)
	for {
		cur, exists, err := a.Store.Get(rec)
		if err != nil {
			return fmt.Errorf("revoking at %s: %w", ref, err)
		}
		if !exists {
			return nil // nothing held out here to take back
		}
		grants, _ := ParseGrants(ref.Segs(), cur.Payload)
		kept := make([]Grant, 0, len(grants))
		for _, g := range grants {
			to := map[Level][]string{}
			for level, names := range g.To {
				gone := map[string]bool{}
				for _, n := range levels[level] {
					gone[n] = true
				}
				var rest []string
				for _, n := range names {
					if !gone[n] {
						rest = append(rest, n)
					}
				}
				if len(rest) > 0 {
					to[level] = rest
				}
			}
			if len(to) > 0 {
				kept = append(kept, Grant{At: g.At, Below: g.Below, To: to})
			}
		}
		committed, _, err := a.Store.Write(Write{
			At:        rec.At(),
			Value:     Obj{GrantsField: GrantsPayload(ref.Segs(), kept)}.Payload(),
			When:      []Require{{Pattern: Unchanged(cur.Payload)}},
			Principal: SystemPerms,
		})
		if err != nil {
			return fmt.Errorf("revoking at %s: %w", ref, err)
		}
		if committed {
			a.invalidate(ref.String())
			return nil
		}
	}
}

// DefaultFor is what a node created at ref holds out: the nearest default up the tree, the
// root's at the top — for a caller that writes a record of its own (Establish) and wants to
// say so in the same words a creation would.
func DefaultFor(s Store, ref Ref) Levels {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	return a.defaultAbove(ref)
}

// Establish writes ref's own record where none stands — owned by owner, under another node
// when under says so, holding out grants, or the default above it when grants is nil — as the
// system and under an absence precondition, so a record already standing is left as it is. It
// is how a node that is the system's gets an owner record of its own where the elision would
// have written none: a question, so that what reaches it is decided on its record and not by
// the grants above it.
func Establish(s Store, ref Ref, owner string, under Ref, grants []Grant) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	if grants == nil {
		grants = a.defaultGrants(ref)
	}
	rec := Obj{OwnerField: owner, GrantsField: GrantsPayload(ref.Segs(), grants)}
	if under.Len() > 0 {
		rec[UnderField] = under.String()
	}
	_, _, err := a.Store.Write(Write{
		At: PermsRef(ref).At(), Value: rec.Payload(), When: []Require{Created()}, Principal: SystemPerms,
	})
	if err != nil {
		return fmt.Errorf("establishing %s: %w", ref, err)
	}
	a.invalidate(ref.String())
	return nil
}

// SetLevel replaces who holds level on ref's own lines — the grants at the node itself, not
// beneath it: every such line loses the level, and the first of them (or a new one) gets it
// back with names. Empty names remove the level. A binding's grant on a question is written
// this way, so binding and unbinding move `read-write` and leave `read` as the default gave it.
func SetLevel(s Store, ref Ref, level Level, names []string) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	rec := PermsRef(ref)
	for {
		cur, exists, err := a.Store.Get(rec)
		if err != nil {
			return fmt.Errorf("setting %s at %s: %w", level, ref, err)
		}
		var grants []Grant
		when := Created()
		if exists {
			grants, _ = ParseGrants(ref.Segs(), cur.Payload)
			when = Require{Pattern: Unchanged(cur.Payload)}
		}
		own := -1
		out := make([]Grant, 0, len(grants)+1)
		for _, g := range grants {
			if strings.Join(g.At, "\x00") == strings.Join(ref.Segs(), "\x00") && !g.Below {
				to := map[Level][]string{}
				for l, ns := range g.To {
					if l != level {
						to[l] = ns
					}
				}
				g.To = to
				if own < 0 {
					own = len(out)
				} else if len(to) == 0 {
					continue
				}
			}
			out = append(out, g)
		}
		if len(names) > 0 {
			if own < 0 {
				own = len(out)
				out = append(out, Grant{At: ref.Segs(), To: map[Level][]string{}})
			}
			out[own].To[level] = names
		} else if own >= 0 && len(out[own].To) == 0 {
			out = append(out[:own], out[own+1:]...)
		}
		committed, _, err := a.Store.Write(Write{
			At: rec.At(), Value: Obj{GrantsField: GrantsPayload(ref.Segs(), out)}.Payload(), When: []Require{when}, Principal: SystemPerms,
		})
		if err != nil {
			return fmt.Errorf("setting %s at %s: %w", level, ref, err)
		}
		if committed {
			a.invalidate(ref.String())
			return nil
		}
	}
}

// SetUnder records that the node at ref is also under another, for ownership (UnderField):
// what a question is posed by co-owns it. As the system, like every write to the mirror.
func SetUnder(s Store, ref, under Ref) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	_, _, err := a.Store.Write(Write{
		At: PermsRef(ref).At(), Value: Obj{UnderField: under.String()}.Payload(), Principal: SystemPerms,
	})
	if err != nil {
		return fmt.Errorf("linking %s under %s: %w", ref, under, err)
	}
	a.invalidate(ref.String())
	return nil
}

// Note lists a principal under the kind its name carries, the first time:
// `verse.principal.<kind>.<id>`, written as the system under an absence precondition. It is the
// record of who has been met; a grant to the kind reads the name, not this.
func Note(s Store, principal string) error {
	g, err := ParsePrincipal(principal)
	if err != nil {
		return err
	}
	if g.Kind == ViaKind || g.Kind == SystemKind {
		return fmt.Errorf("%s: versed makes that name, and meets nobody by it", principal)
	}
	_, _, err = s.Write(Write{
		At:        PrincipalRef(g.Kind, g.ID).At(),
		Value:     Obj{"first": Val(nowStamp())}.Payload(),
		When:      []Require{Created()},
		Principal: SystemPerms,
	})
	return err
}

// PrincipalSystem is the system under verse's root that lists the principals this verse has
// met, by the kind their name carries: `verse.principal.user.<email>` for a person, written the
// first time they present themselves over the api, `verse.principal.runner.<identity>` for a
// worker at its claim. A group's record stands beside them, under GroupKind.
const PrincipalSystem = "principal"

// MembersField is the field of a group listing its members.
const MembersField = "members"

// GroupRef addresses a group.
func GroupRef(name string) Ref { return PrincipalRef(GroupKind, name) }

// IsGroup reports whether an address is a group's.
func IsGroup(ref Ref) bool {
	return ref.Reserved() && ref.System() == PrincipalSystem && ref.Kind() == GroupKind && ref.ID() != ""
}

// UserRef addresses the record of one user having acted here.
func UserRef(principal string) Ref { return PrincipalRef(UserKind, principal) }

// PrincipalRef addresses the record of one principal of a kind having acted here.
func PrincipalRef(kind, principal string) Ref { return VerseRef(PrincipalSystem, kind, principal) }

// The optional read-side methods a store may have pass through, or a wrapped store would look
// like one that cannot say a read failed, and a restore over an unreadable store would read as
// an empty one — the defect ReadFailed exists to refuse. Each answers as the inner store does
// and falls back to what a caller without the method gets.

func (a *attributed) ReadError() error { return ReadFailed(a.Store) }

func (a *attributed) ListErr(pred func(Ref) bool) ([]Entity, error) {
	ents, err := ListErr(a.Store, pred)
	if err != nil {
		return nil, err
	}
	return a.keepAll(ents)
}

func (a *attributed) ListAtErr(at []string, depth int, pred func(Ref) bool) ([]Entity, error) {
	ents, err := ListAtErr(a.Store, at, depth, pred)
	if err != nil {
		return nil, err
	}
	return a.keepAll(ents)
}

func (a *attributed) WatchSubtreeAt(depth int, waitIfAbsent bool, prefix ...string) (<-chan Commit, func(), error) {
	if n, ok := a.Store.(interface {
		WatchSubtreeAt(int, bool, ...string) (<-chan Commit, func(), error)
	}); ok {
		ch, unsub, err := n.WatchSubtreeAt(depth, waitIfAbsent, prefix...)
		if err != nil {
			return nil, nil, err
		}
		return a.narrowing(ch), unsub, nil
	}
	ch, unsub := a.Store.Observe()
	return a.narrowing(ch), unsub, nil
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
