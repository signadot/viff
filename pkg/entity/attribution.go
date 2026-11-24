package entity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
)

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// PermsSystem is the kind under viff's root that holds the perms mirror: one record per node
// whose owner is on record, keyed by the node's own address.
const PermsSystem = "perms"

// OwnerField is the field of a perms record naming the node's principal owner.
const OwnerField = "owner"

// PermsKind is the kind of a perms record: one per NODE whose owner is on record.
const PermsKind = "node"

// PermsRootKind is where the ROOT's record stands: `mirror.perms.root`, beside the mirror rather
// than in it. The root's address is the empty path, which as a key would be no key at all — the
// record's fields would land on `mirror.perms.node` itself, where every other record stands
// under them, so a listing of the mirror would answer `owner` and `grants` as though they were
// records of nodes by those names. One address, spelled once here, rather than a skip rule
// every reader of the mirror has to know.
const PermsRootKind = "root"

// PermsRef is the perms record about a node: `mirror.perms.node.<address>`, the address spelled
// as one segment. Flat rather than nested under the node's own segments, so a field named
// `owner` in the mirror can never be mistaken for a child node called owner, and so that the
// owner of any node is a walk up its address prefixes rather than a walk down a second tree.
// Four segments, because that is how deep an entity in viff's half stands: a listing under
// `viff` walks to that depth, and a record one shorter reads as an entity per field.
//
// THE ROOT IS THE ONE EXCEPTION, and it is here so that it is nowhere else: its record is
// PermsRootKind, since the empty path is a key that is not one.
func PermsRef(of Ref) Ref { return permsRecordAt(of.String()) }

// permsRecordAt is where the record for a node's address stands, which is the one place a key
// becomes an address again: the root's beside the mirror, every other in it.
func permsRecordAt(key string) Ref {
	if key == "" {
		return permsRoot
	}
	return ViffRef(PermsSystem, PermsKind, key)
}

// permsRoot is the root's record, `mirror.perms.root` — a node of its own beside the mirror, so
// every reader of the mirror asks for it apart from the rest.
var permsRoot = ViffRef(PermsSystem, PermsRootKind)

// mirrorKey is the node whose record a mirror address holds, and whether it holds one at all: a
// child's name, or the empty path at the mirror's own node.
func mirrorKey(ref Ref) (string, bool) {
	switch {
	case ref.ChildOf(ViffRoot, PermsSystem, PermsKind):
		return ref.Name(), true
	case ref == permsRoot:
		return "", true
	}
	return "", false
}

// ErrForbidden is a write or a read refused by policy: the principal holds nothing at the
// path that admits it. It is the CALLER's answer and not the store's: nothing failed, nothing
// is retried, and the message names the level it wanted and the record that decided. Distinct
// from a precondition that did not hold — the world saying no is a normal answer, and this is
// not the world.
var ErrForbidden = errors.New("refused")

// ErrStands is a write carrying grants (Write.Grants) refused because the node it names stands
// already, or its place is taken by a record: grants are given where a node is created, and
// what a node that stands holds out is changed with `viff perms`. Like a refused condition it
// is the world's answer, and like a refusal it says why.
var ErrStands = errors.New("the node stands")

// ErrBadGrants is grants on a write that no owner could give at the node, or given where the
// write creates no node of its own to carry them: a delete, an array element, a place the
// store's root cannot reach in one commit. It is the caller's to fix, and a retry changes
// nothing.
var ErrBadGrants = errors.New("grants refused")

// Attribution is what an attributed store shares across the views derived from it: the perms
// records this process has read, which nodes it has settled the owner of, what each place
// acting resolves to, and what it has refused. One per underlying store AND PER SCOPE; As
// derives a per-principal view over it, and Scope a child over the scope's overlay.
//
// A SCOPE'S MIRROR IS ITS OWN, layered as the store is. A grant is a record, and a scoped
// write of one lands in the overlay like any world write; a scope's checks read baseline's
// grants through the overlay and its own on top, and baseline's checks never see the overlay
// (docs/perms.md#scopes). One mirror for all of them was wrong both ways: a scoped grant read
// back into it governed baseline's writes in this process, and a restart, loading from
// baseline, forgot every owner a scope's creations had. The child is loaded from and watched
// on the scoped store, which answers overlaid, so it needs no fallback to its parent.
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
	// nodesAt and rootAt are the revisions the loaded mirror has applied every change through, on
	// the records' half and on the root's — each half has its own watch — and the lower is what
	// a read served at a revision waits on (ensure, watermark.go). pending is the barrier in
	// flight, which every read waiting on the view shares.
	nodesAt, rootAt int64
	pending         *barrier
	// progress asks each of the view's watches to say when it is current (Track); moved is
	// closed when the watermark moves, for the reads waiting on it; lost says the stream could
	// not be applied and the mirror is to be read whole instead (follow).
	progress []func(context.Context) (int64, error)
	moved    chan struct{}
	lost     bool
	// readAt is, for a mirror that is NOT loaded, the commit each record in records was read at
	// or after: a check for what was served at a later revision reads it again (recordAt).
	readAt map[string]int64
	// decided is every node this process has settled the owner record of: written it, found
	// it standing, or found nothing to write because the node above owns it already.
	decided map[Ref]struct{}
	// owners memoises resolve: a place acting, `via:<address>`, to the principal that owns it. A
	// miss is not kept, since the record it would have found can appear at any install.
	owners map[string]string
	// groups memoises a group's members, dropped when the group is written through this
	// process, since membership is meant to move.
	groups map[string][]string
	// takenAt is the fence's trie over the loaded mirror — where somebody's places stand —
	// dropped whenever a record moves and rebuilt on the next check that asks. nil until one
	// does, and never kept for a mirror that is not whole: a partial trie would fence the wrong
	// places, and the fence is the one answer that must not be optimistic.
	takenAt *takenTrie
	// takenGen counts every time a record moved. A trie is built with the lock released, from a
	// copy of the records, so one that finishes after a record landed is already stale — and
	// storing it would bring back exactly the place the fence had just learned about. It is
	// stored only if nothing moved while it was built.
	takenGen int
	// placesAt is the trie of where access can differ (places), kept for the loaded mirror under
	// the same counter: a read prunes by it, and now every write walks its value by it, so
	// rebuilding it from every record per call is a cost a source's pass pays per row.
	placesAt *placeTrie
	// rootChildGrants is what a node created with no default above it holds out — the root's
	// default (ChildGrantsField), the one default that is not data since the root has no record.
	// The open viff's is `read: ["*"]`: whatever anybody creates is readable by everyone
	// until a default beneath says otherwise. A record written before defaults existed carries
	// no grants field and is read as holding out this.
	rootChildGrants Levels
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
	// parent is baseline's attribution for a scope's, nil for baseline's own: what a scope
	// refuses, could not read or hid is counted there too, so /status reads the viff.
	parent *Attribution
	// children is one attribution per scope opened through this one, by name, for as long as
	// the scope stands (DropScope).
	children map[string]*Attribution
}

// scope is the attribution for a scope opened through this one, made on first use with this
// one's root default, and the same one thereafter.
func (a *Attribution) scope(name string) *Attribution {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.children[name]; ok {
		return c
	}
	if a.children == nil {
		a.children = map[string]*Attribution{}
	}
	c := &Attribution{rootChildGrants: a.rootChildGrants, parent: a}
	a.children[name] = c
	return c
}

// dropGroups forgets who is in every group, in baseline's attribution and every scope's. A
// group written in baseline is read through every scope's overlay, and each scope memoises what
// it read: dropped only where the write went, somebody taken out of `admin` kept an admin's
// authority in every open scope for the life of the process. All of them rather than the one
// written, since a write beneath a group — its members alone — names the group no less.
func (a *Attribution) dropGroups() {
	top := a
	for top.parent != nil {
		top = top.parent
	}
	var drop func(*Attribution)
	drop = func(at *Attribution) {
		at.mu.Lock()
		at.groups = nil
		kids := make([]*Attribution, 0, len(at.children))
		for _, c := range at.children {
			kids = append(kids, c)
		}
		at.mu.Unlock()
		for _, c := range kids {
			drop(c)
		}
	}
	drop(top)
}

// dropInScopes brings every scope opened through this attribution up to date about a node whose
// record has just moved HERE, in the store s this attribution is over. A scope reads baseline's
// records through its overlay and memoises them — an absence included — so without this a read
// withdrawn in baseline is still honoured under `?scope=<anything>`, and a place just taken in
// baseline is still anybody's there.
//
// A scope that is not loaded forgets the key and reads it again when next asked. A LOADED one
// (Track) cannot forget it, since a key missing from a loaded mirror reads as absent, and it
// cannot wait for its own watch to deliver the write either: that arrives when it arrives, and
// until then the scope checks against the record as it was. So the record is read through the
// scope's store now — which answers overlaid, the scope's own record over baseline's — and put
// where the watch will put the same thing later. A read that fails leaves what was there for the
// watch to correct.
func (a *Attribution) dropInScopes(s Store, key string) {
	a.mu.Lock()
	kids := make(map[string]*Attribution, len(a.children))
	for name, c := range a.children {
		kids[name] = c
	}
	a.mu.Unlock()
	for name, c := range kids {
		scoped := s.Scope(name)
		c.mu.Lock()
		loaded := c.loaded
		if !loaded {
			delete(c.records, key)
		}
		c.dropTaken()
		c.owners = nil
		if ref, err := ParseAddr(key); err == nil {
			delete(c.decided, ref)
		}
		rootDefault := c.rootChildGrants
		c.mu.Unlock()
		if loaded {
			if e, ok, err := scoped.Get(permsRecordAt(key)); err == nil {
				c.mu.Lock()
				if c.records == nil {
					c.records = map[string]*permsRecord{}
				}
				if ok {
					c.records[key] = parseRecord(key, e.Payload, rootDefault)
				} else {
					delete(c.records, key)
				}
				c.dropTaken()
				c.mu.Unlock()
			}
		}
		c.dropInScopes(scoped, key)
	}
}

// DropScope forgets a scope's attribution: what the scope's overlay held is going with the
// scope (LogdStore.DeleteScope), and a mirror of it would be a mirror of nothing.
func (a *Attribution) DropScope(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.children, name)
}

// permsRecord is one perms record as the check reads it.
type permsRecord struct {
	owner  string
	under  string
	grants []Grant
	// childGrants is what is created beneath this node holds out, when the record says
	// (hasChildGrants); a record that says nothing inherits.
	childGrants    Levels
	hasChildGrants bool
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
// The empty principal is a view that stamps nothing and records no owner — a READ that named
// nobody, which holds what is held out to everyone. viffd lets no request write through one.
func As(s Store, principal string) Store {
	if a, ok := s.(*attributed); ok {
		return &attributed{Store: a.Store, attr: a.attr, principal: principal}
	}
	return &attributed{Store: s, attr: &Attribution{rootChildGrants: Levels{Read: {Everyone}}}, principal: principal}
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

// SetRootChildGrants sets what a node created with no default above it holds out. nil or empty
// is nothing: a viff where what anybody creates is theirs and their grantees' alone.
func (a *Attribution) SetRootChildGrants(l Levels) {
	a.mu.Lock()
	children := make([]*Attribution, 0, len(a.children))
	for _, c := range a.children {
		children = append(children, c)
	}
	a.rootChildGrants = l
	a.mu.Unlock()
	for _, c := range children { // a scope's is baseline's: the root has no record anywhere
		c.SetRootChildGrants(l)
	}
}

// RootChildGrants reports what a node created with no default above it holds out.
func (a *Attribution) RootChildGrants() Levels {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rootChildGrants
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
	// rev is the revision what this view is checking was served at, zero for a check that
	// serves nothing (a write's): the view it is checked against has applied the mirror through
	// it (servedAt).
	rev int64
	// deferred says this view is answering an `audience:` grantee already, so another one met
	// while it does admits nobody: a place is read as another place and no further, so two
	// records deferring to each other end rather than chase.
	deferred bool
}

// Write stamps the principal where the write names none, checks it against what its place
// permits, commits through the underlying store, and keeps the perms mirror in step: an
// entity's owner is recorded when it is first written by a principal holding `owner` there,
// and forgotten when the whole entity is deleted. Ownership changes only by create and delete,
// so those are the only two writes that touch the mirror.
//
// THE CHECK IS HERE AND NOWHERE ELSE. A request's store, a firing's causal store and a
// source's reflector are all views derived from this one, so every write viff makes on
// anybody's behalf passes through it — the compare-and-swap ones through Modify included —
// and a write path that does not is a write nothing checks.
func (a *attributed) Write(w Write) (bool, int64, error) {
	if w.Principal == "" {
		w.Principal = a.principal
	}
	// A NAME IN THE ACCESS IS A NAME IN THE ADDRESS (Path.Settled): `{team, "c.secret"}` is
	// `team.c.secret`, and checked as `team` it was a write into somebody else's place by
	// whoever could write the place above it, and a delete of one without being its owner.
	settled, err := w.At.Settled()
	if err != nil {
		return false, 0, err
	}
	w.At = settled
	// THE PATH WRITTEN, however it was spelled: `demo pr 1 stage` and a rule patching `stage` of
	// `demo.pr.1` are one place, so they are checked at one place and recorded at one place. A
	// record stands where ownership CHANGES, so a write into a node the writer already owns
	// writes none (record), and the mirror does not grow a row per field.
	ref := w.At.Ref
	wholeDelete := w.Value == nil && w.At.Access == ""
	// Removing the node is a co-owner's act — eviction, since it takes everything beneath it
	// with it — and not a write into the node; everything else is.
	need := ReadWrite
	if wholeDelete {
		need = OwnerLevel
	}
	act := "writing it"
	if wholeDelete {
		act = "deleting it"
	}
	if w.Grants != nil {
		if err := grantsOnCreate(w, ref); err != nil {
			return false, 0, err
		}
	}
	return a.write(w, ref, need, act, !wholeDelete, 0)
}

// grantsOnCreate refuses grants on a write that creates no node to carry them, and grants no
// owner could give: they are checked as `viff perms --grants` checks what an owner sets on
// their own node's record (checkGrants), and a record is per node, so a delete, an array
// element and a node whose own record stands beneath it (the root, the mirror) carry none.
func grantsOnCreate(w Write, ref Ref) error {
	switch {
	case w.Value == nil && w.At.Access == "":
		return fmt.Errorf("%w: a delete of %s creates nothing to hold them out", ErrBadGrants, ref)
	case w.At.Access != "":
		return fmt.Errorf("%w: %s%s is an element of an array, and grants are held out by a node", ErrBadGrants, ref, w.At.Access)
	case ref.Under(ViffRoot, PermsSystem) || PermsRef(ref).Under(ref.Segs()...):
		return fmt.Errorf("%w: %s holds the perms records, and is created with none of its own", ErrBadGrants, ref)
	}
	if err := checkGrants(w.Grants); err != nil {
		return fmt.Errorf("%w: %w", ErrBadGrants, err)
	}
	return nil
}

// write is Write once the path is settled and what it needs is known. creating says whether the
// write may be admitted on the right to create — every write that is not a delete is asked that
// way first — and asked counts the askings before this one that the store refused as a creation.
//
// WHERE THAT RIGHT IS WHAT ADMITTED IT, THE WRITE SAYS SO TO THE STORE: it goes down conditional
// on nothing standing at each node it was admitted at by a `child-owner` line (access.byGrant),
// and the store settles that at the commit, which is the only moment it is true or false. A
// condition refused means the node stands, and a node that stands is its nearest owner's: the
// write is then asked again as what it turned out to be — not a creation — and is admitted on
// whatever else the writer holds there, or refused.
func (a *attributed) write(w Write, ref Ref, need Level, act string, creating bool, asked int) (bool, int64, error) {
	// A refusal reads as the writer's own condition only on the asking that is NOT a creation
	// (orUncommitted): asked again as one, a refusal by the check is a refusal.
	again := asked > 0 && !creating
	wholeDelete := w.Value == nil && w.At.Access == ""
	acc, err := a.checkFor(w.Principal, need, ref, act, creating)
	if err != nil {
		return a.orUncommitted(again, ref, err)
	}
	// WHAT A NODE HOLDS OUT IS ITS OWNER'S TO SAY, and a creator is the owner only where they
	// hold `owner`: one admitted on `read-write` makes a node its owner above owns.
	if w.Grants != nil && acc.level != OwnerLevel {
		a.attr.noteRefusal(OwnerLevel)
		return false, 0, refusal(OwnerLevel, "giving it grants", w.Principal, ref, acc)
	}
	// AND WHAT IT ASKS ABOUT IS WHAT ITS WRITER MAY READ. A precondition is a match the store
	// answers with committed-or-not, so one placed on a node the writer may not read is a read
	// of it, a guess at a time: `when: [{at: vault.k, pattern: {secret: GUESS}}]` on a write to
	// a place of one's own. Refused as a read of that node is, whatever stands there.
	if !IsSystem(acc.who) {
		if err := a.mayAsk(w.Principal, w.At, w.When); err != nil {
			return false, 0, err
		}
	}
	// AND EVERY NODE THE VALUE REACHES, not only the one the write names. A write is checked at
	// its address, and a value is a tree: `{c: {secret: x}}` written AT `team` lands in
	// `team.c`, which may be somebody else's place — refused when addressed, and admitted when
	// it arrived through its parent. A record governs its node whichever ancestor the write was
	// asked at, which is what a read already promises (prune).
	// absent is what this write is conditional on because it was admitted as a creation there:
	// its own node, or — the node standing, or being somebody's by record — each node its value
	// reaches that a `child-owner` line alone admitted it at. Beneath a node that must be absent
	// nothing stands either, so the one condition covers everything under it.
	var absent []Require
	if acc.byGrant {
		absent = append(absent, Require{At: Path{Ref: ref}, Absent: true})
	}
	if !wholeDelete && w.At.Access == "" && !IsSystem(acc.who) {
		more, err := a.writeBeneath(w.Principal, w.Value, ref.Segs(), creating, acc.byGrant)
		if err != nil {
			return a.orUncommitted(again, ref, err)
		}
		absent = append(absent, more...)
	}
	// WHERE THE RECORD WILL GO, asked BEFORE the write, because afterwards everything the write
	// made exists and nothing can say which of it is new. A write of `probe.thing.1` into an
	// empty place brings `probe`, `probe.thing` and the row into being, and all three are the
	// writer's: the tree is the state space this viff reacts to, so the structure somebody adds
	// is their contribution and not scaffolding around it. Recording only the row left the
	// containers inheriting from above, which reads the same in an open viff and stops the
	// writer dead the day the top is narrowed — they keep the rows they own and cannot make
	// another beside them.
	var made []Ref
	if !wholeDelete && w.Principal != "" && acc.level == OwnerLevel {
		if made, err = a.created(ref, w.Principal, acc.who); err != nil {
			return false, 0, err
		}
	}
	// AND THE CONDITION SITS AT THE TOPMOST NODE THIS WRITE MAKES, which is where its record
	// goes. `made` was decided by asking what stands, before the write; with the condition at the
	// written node alone, an ancestor somebody else brought into being in between — the system,
	// leaving no record — would be recorded as this writer's, though it stood by the time they
	// wrote. Nothing stands beneath a node that is absent, so the one condition covers the rest.
	aboveStood := false
	if acc.byGrant && len(made) > 0 {
		absent[0] = Require{At: Path{Ref: made[0]}, Absent: true}
		aboveStood = true
	}
	sent := w
	sent.Grants = nil
	if len(absent) > 0 {
		sent.When = append(append(make([]Require, 0, len(w.When)+len(absent)), w.When...), absent...)
	}
	// A NODE CREATED WITH GRANTS LANDS WITH ITS RECORD, in one commit anchored at the root
	// (withRecord), conditional on neither standing: no reader of the store ever finds the node
	// without the record that says who may read it. And the check finds it at once: its view of
	// the mirror is kept by a watch that arrives when it arrives, and until then a new node
	// would be judged by the grants above it. So the record is entered in that view BEFORE the
	// write, owned by the system until the commit says whose it is — before the commit the node
	// does not exist, or stands and is not this writer's to act on, so a record hiding it hides
	// nothing anybody was owed. A write that does not land takes it out again (invalidate).
	var rec *permsRecord
	if w.Grants != nil {
		rec = &permsRecord{owner: acc.who, grants: w.Grants}
		sent = withRecord(sent, ref, rec)
		a.attr.enter(ref.String(), &permsRecord{owner: SystemDefault, grants: w.Grants})
	}
	committed, rev, err := a.Store.Write(sent)
	if rec != nil && (err != nil || !committed) {
		a.invalidate(ref.String())
	}
	if err != nil {
		return committed, rev, err
	}
	if !committed {
		if rec != nil {
			return a.notCreated(w, ref, need, act, asked, aboveStood)
		}
		if len(absent) > 0 {
			// One of them stands, or the writer's own condition did not hold. Where the condition
			// sat above the written node, what came to stand may be an ancestor alone, and the
			// write is still a creation beneath it: asked once more as one, with `made` decided
			// afresh. Otherwise it is asked as not a creation, which tells a node that stands from
			// the writer's own condition without reading anything on this path.
			if aboveStood && asked < maxCreatingAsks {
				return a.write(w, ref, need, act, true, asked+1)
			}
			return a.write(w, ref, need, act, false, asked+1)
		}
		return committed, rev, nil
	}
	if ref.Under(ViffRoot, PermsSystem) {
		// The mirror keeps no records about itself, and what this process cached about the
		// node the record is about is stale the moment the record moves.
		if key, ok := mirrorKey(ref); ok {
			a.invalidate(key)
		}
		return committed, rev, nil
	}
	if ref.Under(ViffRoot, PrincipalSystem, GroupKind) {
		a.attr.dropGroups()
	}
	if wholeDelete {
		a.forget(ref)
		return committed, rev, nil
	}
	if rec != nil {
		// The node's record landed with it, WHOEVER owns the place above: a record carrying
		// grants changes what the node holds out even where it does not change its owner. What
		// the write made above the node is recorded as any creation's is.
		a.decide(ref, rec)
		for _, at := range made {
			if a.record(at, acc.who) {
				break
			}
		}
		return committed, rev, nil
	}
	if w.Principal != "" && acc.level == OwnerLevel {
		// FROM THE TOP OF WHAT THIS WRITE MADE, DOWN, until a record is the writer's. Where it
		// goes was decided before the write and the record is written after it, and between the
		// two somebody else may have made the same place: two people writing `probe.a.1` and
		// `probe.b.1` into an empty `probe` both mean to record at `probe`, and one of them
		// wins. The other's record goes at the next node down that they made — `probe.a` — and
		// not nowhere, which left them holding `read` on a row they had just created, inside
		// somebody else's place.
		for _, at := range append(made, ref) {
			if a.record(at, acc.who) {
				break
			}
		}
	}
	return committed, rev, nil
}

// maxCreatingAsks bounds how often a write is asked again AS A CREATION because something came to
// stand above it between the check and the commit. Once: the asking decides afresh what it
// makes, which settles an ancestor that appeared, and a place being built above by somebody
// else as fast as this writer can ask is not one to keep racing for — nor is a writer whose own
// condition is what failed to be charged a write per ancestor for finding out.
const maxCreatingAsks = 1

// orUncommitted is how a refusal reads on the second asking (write): the write was refused by
// the store as a creation, and refused here as anything else. If the node stands, that is the
// answer — it is somebody's, and the refusal names whose. If nothing stands there, no node was
// in the way and what the store refused was the writer's own condition, which is an ordinary
// uncommitted write and not a refusal of the writer.
func (a *attributed) orUncommitted(again bool, ref Ref, refused error) (bool, int64, error) {
	if !again || !errors.Is(refused, ErrForbidden) {
		return false, 0, refused
	}
	stands, err := a.Store.StandsAt(ref.Segs())
	if err != nil || stands {
		return false, 0, refused
	}
	return false, 0, nil
}

// withRecord is a creating write with its node's perms record beside it: one write anchored at
// the store's root, carrying the value at ref and the record at PermsRef(ref), conditional on
// neither standing. logd commits one patch as one transaction, so both land or neither does, at
// one revision on every watch.
//
// ANCHORED AT THE ROOT, the write descends through every node above ref, where the store would
// ask only of ref's parent: so each is asked to be something a write may pass through, as that
// parent would be. And a require that named no place of its own was about ref, and still is.
func withRecord(w Write, ref Ref, rec *permsRecord) Write {
	segs := ref.Segs()
	value := graft{}
	value.put(segs, clone(w.Value))
	value.put(PermsRef(ref).Segs(), Obj{
		OwnerField:  rec.owner,
		GrantsField: GrantsPayload(SegsOf(ref), rec.grants),
	}.Payload())
	when := make([]Require, 0, len(w.When)+len(segs)+1)
	for _, r := range w.When {
		if r.atOwnPlace() {
			r.At = w.At
		}
		when = append(when, r)
	}
	when = append(when, Require{At: ref.At(), Absent: true}, Require{At: PermsRef(ref).At(), Absent: true})
	for i := 1; i < len(segs); i++ {
		when = append(when, Require{At: RefAt(segs[:i]...).At(), descends: true})
	}
	w.At, w.Value, w.When = Path{}, value.node(), when
	return w
}

// graft is a value being built from values at paths: a node per segment, the values at the
// leaves.
type graft map[string]any

func (g graft) put(segs []string, v Payload) {
	for _, s := range segs[:len(segs)-1] {
		next, ok := g[s].(graft)
		if !ok {
			next = graft{}
			g[s] = next
		}
		g = next
	}
	g[segs[len(segs)-1]] = v
}

func (g graft) node() Payload {
	m := make(map[string]*ir.Node, len(g))
	for k, v := range g {
		switch v := v.(type) {
		case graft:
			m[k] = v.node()
		case Payload:
			m[k] = v
		}
	}
	return ir.FromMap(m)
}

// enter puts a record in this view of the mirror, and in every scope's opened through it, ahead
// of the write that will make it true (write): the one entry into the view that is not read from
// the mirror, so whoever enters one takes it out again if the write does not land (invalidate).
func (a *Attribution) enter(key string, rec *permsRecord) {
	a.mu.Lock()
	if a.records == nil {
		a.records = map[string]*permsRecord{}
	}
	a.records[key] = rec
	a.dropTaken()
	kids := make([]*Attribution, 0, len(a.children))
	for _, c := range a.children {
		kids = append(kids, c)
	}
	a.mu.Unlock()
	for _, c := range kids {
		c.enter(key, rec)
	}
}

// notCreated is why a write carrying grants did not land. The node standing, or a record
// taking its place, is ErrStands: grants are given at a creation, and a write that would merge
// into a node that stands is refused rather than turned into an update. An ancestor that came
// to stand is asked once more as a creation beneath it, as any creating write is; a node above
// that holds a value is ErrNotDescendable, as the store says of a write's own parent; and
// anything else is the writer's own condition.
func (a *attributed) notCreated(w Write, ref Ref, need Level, act string, asked int, aboveStood bool) (bool, int64, error) {
	stands, err := a.Store.StandsAt(ref.Segs())
	if err != nil {
		return false, 0, err
	}
	if stands {
		return false, 0, fmt.Errorf("%w: %s stands, and grants are given where a node is created; "+
			"what it holds out is changed with `viff perms`", ErrStands, ref)
	}
	if _, taken, err := a.Store.Get(PermsRef(ref)); err != nil {
		return false, 0, err
	} else if taken {
		return false, 0, fmt.Errorf("%w: %s is taken — a record stands for it — and what it holds "+
			"out is set with `viff perms`", ErrStands, ref)
	}
	if aboveStood && asked < maxCreatingAsks {
		return a.write(w, ref, need, act, true, asked+1)
	}
	segs := ref.Segs()
	for i := len(segs) - 1; i >= 1; i-- {
		n, ok, err := a.Store.ReadAt(segs[:i])
		if err != nil {
			return false, 0, err
		}
		if !ok {
			continue
		}
		if n := ir.Uncomment(n); n.Type != ir.ObjectType && n.Type != ir.NullType {
			where := JoinSegs(segs[:i])
			return false, 0, fmt.Errorf("%w: %s holds %s, so %q is not a place in it", ErrNotDescendable,
				where, standing(n), segs[i])
		}
		break
	}
	return false, 0, nil
}

// Scope re-wraps rather than delegating, so a scoped write off an attributed store keeps its
// principal; the embedded Store's Scope would hand back the inner store and lose it. The view
// carries the SCOPE's attribution (Attribution.scope): its checks read the scope's mirror,
// which holds baseline's grants and the scope's own, and its writes repair that mirror and
// nobody else's. The empty scope is this store.
func (a *attributed) Scope(name string) Store {
	if name == "" {
		return a
	}
	return &attributed{Store: a.Store.Scope(name), attr: a.attr.scope(name), principal: a.principal}
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
	return a.checkFor(principal, need, ref, act, false)
}

// checkFor is check for a caller that says whether it is creating, which is what a
// `child-owner` line is about (accessFor).
func (a *attributed) checkFor(principal string, need Level, ref Ref, act string, creating bool) (access, error) {
	acc, err := a.accessFor(principal, ref, creating)
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
	// fenced says an `owner` grant was held back here because somebody else's place stands
	// beneath this node, and names them where they are one principal. It is what turns "you
	// hold nothing here" into a refusal a reader can act on, since the record that decided is
	// not above the node but under it.
	fenced string
	// unowned says the principal is a place acting with no owner on record, which holds nothing
	// anywhere; the refusal says so rather than naming an owner above that did not decide.
	unowned bool
	// byGrant says `owner` here came from a `child-owner` line and not from a record: it is the
	// right to CREATE the node, so whoever acts on it has to make sure nothing stands there —
	// a write by carrying that as its own condition, a claim by asking.
	byGrant bool
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
	if level == Answer {
		return a.answers(principal, ref)
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

// AnswerGrantsOf is every node whose own record names principal on an `answer` line: where a
// resolver has been granted to answer.
func AnswerGrantsOf(s Store, principal string) ([]Ref, error) {
	a, ok := s.(*attributed)
	if !ok {
		return nil, fmt.Errorf("%s is not an attributed store", principal)
	}
	ents, err := BelowErr(As(a, SystemPerms), nil, 1, ViffRoot, PermsSystem, PermsKind)
	if err != nil {
		return nil, err
	}
	var out []Ref
	for _, e := range ents {
		at, err := ParseAddr(e.Ref.Name())
		if err != nil {
			continue
		}
		rec, err := a.recordAt(at.String(), false)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			continue
		}
		for _, g := range rec.grants {
			if slices.Contains(g.To[Answer], principal) {
				out = append(out, at)
				break
			}
		}
	}
	return out, nil
}

// AnswerersMeeting is every name on an `answer` line that applies where one at ref would: the
// lines above ref that reach it, as far as the nearest record carrying an owner; the lines at ref;
// and the lines beneath it that no other owner's record stands between. It is what a new grant at
// ref could meet — two resolvers answering for one party — and nothing further: a line past
// somebody else's record answers for them, not for whoever owns ref.
func AnswerersMeeting(s Store, ref Ref) ([]string, error) {
	a, ok := s.(*attributed)
	if !ok {
		return nil, fmt.Errorf("%s is not an attributed store", ref)
	}
	var out []string
	segs := ref.Segs()
	for i := len(segs); i >= 0; i-- {
		rec, err := a.recordAt(RefAt(segs[:i]...).String(), false)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			continue
		}
		for _, g := range rec.grants {
			if g.Covers(segs) {
				out = append(out, g.To[Answer]...)
			}
		}
		if rec.owner != "" {
			break
		}
	}
	ents, err := BelowErr(As(a, SystemPerms), nil, 1, ViffRoot, PermsSystem, PermsKind)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		at, err := ParseAddr(e.Ref.Name())
		if err != nil {
			continue
		}
		below := at.Segs()
		if len(below) <= len(segs) || !Under(segs, below, Literal) {
			continue
		}
		fenced := false
		for j := len(below); j > len(segs); j-- {
			rec, err := a.recordAt(RefAt(below[:j]...).String(), false)
			if err != nil {
				return nil, err
			}
			if rec != nil && rec.owner != "" {
				fenced = true
				break
			}
		}
		if fenced {
			continue
		}
		rec, err := a.recordAt(at.String(), false)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			continue
		}
		for _, g := range rec.grants {
			out = append(out, g.To[Answer]...)
		}
	}
	return out, nil
}

// answers is whether an `answer` grant reaching ref names principal — reaching as any grant does,
// up to and including the nearest record carrying an owner. Ownership itself is not consulted:
// Answer is held by being named (Level Answer), so a resolver acting as the owner of the node
// holds nothing here it was not granted.
func (a *attributed) answers(principal string, ref Ref) (bool, error) {
	who, err := a.resolve(principal)
	if err != nil {
		return false, err
	}
	segs := ref.Segs()
	for i := len(segs); i >= 0; i-- {
		key := RefAt(segs[:i]...).String()
		rec, err := a.recordAt(key, false)
		if err != nil {
			return false, err
		}
		if rec == nil {
			continue
		}
		for _, g := range rec.grants {
			if g.Covers(segs) && a.named(g.To[Answer], Answer, principal, who, key) {
				return true, nil
			}
		}
		if rec.owner != "" {
			return false, nil // a grant does not reach through a node carrying its own owner
		}
	}
	return false, nil
}

// MayCreate reports whether principal may bring ref into being: `owner` there, asked as a
// creation, which is what a `child-owner` line above it holds out. It says nothing about whether
// anything stands at ref — a write finds that out at the commit, and CheckTake asks — so it is
// the question "is this held out to them", and the error is a mirror that did not answer.
func MayCreate(s Store, principal string, ref Ref) (bool, error) {
	a, ok := s.(*attributed)
	if !ok {
		return false, fmt.Errorf("%s is not an attributed store", ref)
	}
	acc, err := a.accessFor(principal, ref, true)
	if err != nil {
		return false, err
	}
	return acc.level.Admits(OwnerLevel), nil
}

// CheckInto is the check a listing or a watch makes at a path: may principal read INTO ref —
// the node itself, or what a grant at it holds out beneath it. A kind-rooted node of viff's
// half is the system's and holds out `owner` beneath itself to everyone, and a listing there
// is answered with what stands beneath, though the node itself is nobody's to write; a place
// somebody took and holds nothing out on is refused whole. The error is ErrForbidden, counted
// once, or the store's own.
//
// It is asked ahead of what it admits, which has no revision yet, so it is asked of the view
// brought up to the latest commit this process has seen; what is then served is checked again at
// its own.
func CheckInto(s Store, principal string, ref Ref) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	a, err := a.servedAt(a.Store.Head())
	if err != nil {
		return fmt.Errorf("checking %s at %s: %w", principal, ref, err)
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
// The walk ends at the ROOT, which is a node with a record like any other: what an untaken
// place permits is that record's grants — `owner` beneath the root to everyone in an open viff,
// to an admin group in a shaped one — rather than a line here nobody could read or narrow. Where
// even that record is missing, nobody holds anything and the system still holds everything,
// since it owns the root: a viff whose seed failed refuses rather than standing wide open.
func (a *attributed) accessOf(principal string, ref Ref) (access, error) {
	return a.accessFor(principal, ref, false)
}

// accessFor is accessOf for a caller that says whether it is CREATING, which is what a
// `child-owner` line is about: it confers `owner` on whoever brings a node into being beneath
// it, and says nothing about a node that stands. A node that stands has an owner already — the
// nearest record above it — and a line wide enough to reach it does not make it anybody
// else's. So a read, a listing, a delete, a control verb and every other caller that is not
// creating gets nothing from the line.
//
// THE CHECK DOES NOT ASK WHETHER THE NODE STANDS. It cannot know at the moment that matters,
// which is the commit. A creating caller is told the right it was admitted on (access.byGrant)
// and carries the rest itself: a write goes down conditional on nothing standing there, and
// the store settles it.
func (a *attributed) accessFor(principal string, ref Ref, creating bool) (access, error) {
	if asker, bound, ok := PairOf(principal); ok {
		return a.pairAccess(asker, bound, ref, creating)
	}
	who, err := a.resolve(principal)
	if err != nil {
		return access{}, err
	}
	acc := access{who: who, nearest: SystemDefault + " (the root)"}
	if IsSystem(who) {
		acc.level, acc.control = OwnerLevel, true
		return acc, nil
	}
	// A PLACE NOBODY OWNS ACTS AS NOBODY, and holds nothing — not what is held out to everyone
	// either: an address is not a party, so a node it created would have an owner nobody can
	// be, and what it may do has to come from somebody who answers for it.
	if KindOf(who) == ViaKind {
		acc.unowned = true
		return acc, nil
	}
	segs := ref.Segs()
	crossed := false
	// Down to the root itself, whose address is the empty path: it is a node like any other and
	// its record decides what stands beneath it, which is where a deployment says who may create
	// a top-level place.
	for i := len(segs); i >= 0; i-- {
		key := RefAt(segs[:i]...).String()
		rec, err := a.recordAt(key, false)
		if err != nil {
			return access{}, err
		}
		if rec == nil {
			continue
		}
		if rec.owner != "" && rec.owner == who {
			acc.level, acc.control, acc.byGrant = OwnerLevel, true, false
			return acc, nil
		}
		if rec.under != "" {
			co, err := a.coOwns(who, rec.under, 3)
			if err != nil {
				return access{}, err
			}
			if co {
				acc.level, acc.control, acc.byGrant = OwnerLevel, true, false
				return acc, nil
			}
		}
		if !crossed {
			for _, g := range rec.grants {
				if !g.Covers(segs) {
					continue
				}
				for l, names := range g.To {
					if !a.named(names, l, principal, who, key) {
						continue
					}
					if l == OwnerLevel && !creating {
						continue
					}
					if l == OwnerLevel {
						// THE FENCE: ownership concluded from a grant does not apply at a node
						// somebody else's place stands beneath. Taking a node takes everything
						// under it — ownership descends — so a grant that reaches an untaken
						// node would otherwise hand the taker every place already made beneath
						// it: `github`, taken above somebody's `github.signadot.viff`.
						// Ownership from a RECORD is untouched above:
						// an owner who holds a place keeps it, grantees and all.
						by, f, err := a.fenced(ref, who)
						if err != nil {
							return access{}, err
						}
						if f {
							acc.fenced = by
							continue
						}
						acc.byGrant = true
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
			if key == "" {
				acc.nearest = rec.owner + " at the root"
			}
		}
	}
	return acc, nil
}

// The read side of the view: a read answers only what the principal may read. Get refuses, as
// a write does; a listing, a replay and a watch leave out what may not be seen, so the same
// principal sees the same set whichever way they ask. The system reads everything and pays
// for none of this.

// pairAccess is what a pair holds at ref: what its ASKER holds, and nothing the rule adds or
// takes away. A consequence is the raiser's, so it runs on the raiser's authority (9ce3bva0):
// asked whether I may merge, Victor's yes lets me merge what I may, and neither the answerer
// nor the rule's installer needs that right. The rule is the pair's second party so the record
// says what the task acted through, not so the check can ask it.
//
// What keeps a rule's author from acting as anybody whose records the rule selects is at
// install, not here: a rule that spawns `as: subject` may select only records in a place its
// installer owns (viffd, charterTx.rule), so an asker's record there is written into a place
// the charter was installed to serve.
//
// A SYSTEM ASKER IS NO PARTY. Taken as the system, the task would skip the checks beneath the
// node a write names (write) and carry the rule past its own limits into everything a value
// reaches; so a pair whose asker is the system acts as the rule alone.
func (a *attributed) pairAccess(asker, bound string, ref Ref, creating bool) (access, error) {
	x, err := a.accessFor(asker, ref, creating)
	if err != nil {
		return access{}, err
	}
	if IsSystem(x.who) {
		return a.accessFor(bound, ref, creating)
	}
	return x, nil
}

// keep reports whether the view's principal may read ref, for a listing: false and no error
// for what they may not, an error for a mirror that did not answer.
//
// LEAVING SOMETHING OUT OF A LISTING IS NOT A REFUSAL. The listing is answered, with what the
// reader may see in it, and counting every record withheld from every `viff data` as a
// refused request would bury the count that matters — a request turned away — under the
// mirror's own records, which no listing of viff's half may show a person. What is withheld
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
	if a.parent != nil { // counted at baseline too, said once here
		a.parent.mu.Lock()
		a.parent.unparsed++
		a.parent.mu.Unlock()
	}
}

// noteHidden counts an entity a listing withheld.
func (a *Attribution) noteHidden() {
	a.mu.Lock()
	a.hidden++
	a.mu.Unlock()
	if a.parent != nil {
		a.parent.noteHidden()
	}
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
	return a.withholdAll(out)
}

// withholdAll is withhold over rows already kept, reading the mirror once for all of them.
func (a *attributed) withholdAll(ents []Entity) ([]Entity, error) {
	refs := make([]Ref, len(ents))
	for i, e := range ents {
		refs[i] = e.Ref
	}
	beneath, err := a.recordsBeneath(refs)
	if err != nil {
		return nil, err
	}
	for i := range ents {
		if ents[i], err = a.withhold(ents[i], beneath[ents[i].Ref.String()]); err != nil {
			return nil, err
		}
	}
	return ents, nil
}

// A READ ANSWERS ONLY WHAT MAY BE READ, AT EVERY NODE IT CARRIES. A row the reader may read
// holds a payload, and a node inside that payload can stand under a record of its own that the
// reader may not read: walter's `demo.pr.1.secret`, taken with no grants, is refused to ana at
// its own address — and was handed to her whole by a read of `demo.pr.1` or `demo.pr`, since
// keep asks only of the row. withhold leaves each such node out of the payload, as keep leaves a
// row out of a listing, and counts it as Hidden.
//
// ONLY A RECORD BENEATH THE ROW CAN NARROW IT. The access walk takes the nearest owner on the
// way up and stops taking grants from above once it has crossed one; a grant and a co-owner only
// ever add. So a node below a readable row reads narrower than the row only
// where a record stands between them, and those records are the only places asked. A node left
// out takes what is beneath it, even where a deeper grant would admit the reader: that node is
// still read at its own address, where the walk answers for it exactly.

// recordsBeneath is, for each row, the relative paths of the perms records strictly beneath it,
// shortest first — from the loaded mirror when there is one, and otherwise by listing it once.
func (a *attributed) recordsBeneath(rows []Ref) (map[string][][]string, error) {
	out := map[string][][]string{}
	if len(rows) == 0 || IsSystem(a.principal) {
		return out, nil
	}
	byKey := make(map[string]bool, len(rows))
	for _, r := range rows {
		byKey[r.String()] = true
	}
	var keys []string
	a.attr.mu.Lock()
	loaded := a.attr.loaded
	if loaded {
		for key, rec := range a.attr.records {
			if rec != nil {
				keys = append(keys, key)
			}
		}
	}
	a.attr.mu.Unlock()
	if !loaded {
		mirror := []string{ViffRoot, PermsSystem, PermsKind}
		recs, err := ListAtErr(a.Store, mirror, len(mirror)+1, nil)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			segs := rec.Ref.Segs()
			keys = append(keys, segs[len(segs)-1])
		}
	}
	for _, key := range keys {
		node, err := ParseAddr(key)
		if err != nil {
			continue
		}
		segs := node.Segs()
		for i := len(segs) - 1; i >= 1; i-- {
			row := RefAt(segs[:i]...).String()
			if byKey[row] {
				out[row] = append(out[row], segs[i:])
			}
		}
	}
	for row := range out {
		slices.SortFunc(out[row], func(x, y []string) int { return len(x) - len(y) })
	}
	return out, nil
}

// withhold leaves out of a readable row's payload every node beneath it the reader may not read,
// given the records beneath the row (recordsBeneath).
func (a *attributed) withhold(e Entity, beneath [][]string) (Entity, error) {
	if len(beneath) == 0 || IsSystem(a.principal) {
		return e, nil
	}
	row := e.Ref.Segs()
	var cut [][]string
	for _, rel := range beneath {
		if slices.ContainsFunc(cut, func(c []string) bool { return len(c) <= len(rel) && slices.Equal(c, rel[:len(c)]) }) {
			continue // already left out with the node above it
		}
		at := append(append(make([]string, 0, len(row)+len(rel)), row...), rel...)
		acc, err := a.accessOf(a.principal, RefAt(at...))
		if err != nil {
			a.attr.noteUnreadable()
			return Entity{}, err
		}
		if !acc.level.Admits(Read) {
			cut = append(cut, rel)
		}
	}
	if len(cut) == 0 {
		return e, nil
	}
	payload := clone(e.Payload)
	for _, rel := range cut {
		if dropPath(payload, rel) {
			a.attr.noteHidden()
		}
	}
	e.Payload = payload
	return e, nil
}

// dropPath removes the node at a field path inside a payload, reporting whether one stood there.
func dropPath(n *ir.Node, path []string) bool {
	for i, name := range path {
		n = ir.Uncomment(n)
		if n == nil || n.Type != ir.ObjectType {
			return false
		}
		j := slices.IndexFunc(n.Fields, func(f *ir.Node) bool { return f != nil && fieldName(f) == name })
		if j < 0 {
			return false
		}
		if i == len(path)-1 {
			n.Fields = slices.Delete(n.Fields, j, j+1)
			n.Values = slices.Delete(n.Values, j, j+1)
			return true
		}
		n = n.Values[j]
	}
	return false
}

// narrow is a commit as this view's principal sees it (Commit.Only). A mirror that did not
// answer hides nothing here: a watch has no error to return, and a commit withheld would read
// as a gap, so the check's own count is what says a read went unchecked.
//
// A CREATION IS SEEN ONCE by everyone the grants above it admit, where the node takes the
// default: its owner record follows the write it records, so the commit that creates the node
// reaches a live watch before the record that would hide it lands, and is judged by what stands
// then. Every commit after it is judged by the record. Writing that record first would open a
// worse window — a record standing for a node whose write then failed makes the next creator
// there not its owner — so the window stays, and is one commit wide.
//
// A CREATION WITH GRANTS (Write.Grants) HAS NO WINDOW. Its record lands in the node's own
// commit, and stands in this view before the commit does (attributed.write), so the commit that
// creates the node is judged by what its creator said it holds out. A writer who means the node
// never to be seen more widely than that says so on the write.
//
// A COMMIT IS JUDGED AT ITS OWN REVISION: the view is brought up to it first (servedAt). A view
// that cannot be leaves the commit with nothing in it, as a mirror that did not answer leaves
// out each event it could not check.
func (a *attributed) narrow(c Commit) Commit {
	if IsSystem(a.principal) || c.progress {
		return c // a progress answer carries no change
	}
	a, err := a.servedAt(c.Rev)
	if err != nil {
		return c.Only(func(Ref) bool { return false })
	}
	orig := c.Changes
	c = c.Only(func(r Ref) bool { ok, _ := a.keep(r); return ok })
	// What an event carries is read as a read of its path is: each node in it the reader may not
	// read is left out, and one held out to them beneath a node they may not read is kept. A
	// mirror that did not answer leaves the events keep admitted as they are.
	places, err := a.places()
	if err != nil {
		return c
	}
	changes := make([]Event, 0, len(orig))
	for _, ev := range orig {
		segs := ev.Ref.Segs()
		acc, err := a.accessOf(a.principal, ev.Ref)
		if err != nil {
			continue
		}
		readable := acc.level.Admits(Read)
		if ev.To == nil {
			if readable {
				changes = append(changes, ev)
			}
			continue
		}
		to, keep, err := a.prune(clone(ev.To), segs, readable, places.at(segs))
		if err != nil || !keep {
			continue
		}
		ev.To = to
		changes = append(changes, ev)
	}
	c.Changes = changes
	return c
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

// Get READS FIRST AND CHECKS AFTER, at the revision the read was served at: a check made first
// is made against the view as it stood before the read, and a record committed in between —
// with the node, or a revoke — is one the read serves and the check never saw. A read that
// failed serves nothing, so it is checked against the view as it stands, and a refusal still
// comes before the store's failure.
func (a *attributed) Get(ref Ref) (Entity, bool, error) {
	e, ok, _, err := a.GetCommit(ref)
	return e, ok, err
}

// GetCommit is Get, and the commit the read was served at.
func (a *attributed) GetCommit(ref Ref) (Entity, bool, int64, error) {
	e, ok, rev, err := servedGet(a.Store, ref)
	if IsSystem(a.principal) {
		return e, ok, rev, err
	}
	if err != nil {
		if _, refused := a.check(a.principal, Read, ref, "reading it"); refused != nil {
			return Entity{}, false, 0, refused
		}
		return Entity{}, false, 0, err
	}
	v, err := a.servedAt(rev)
	if err != nil {
		return Entity{}, false, 0, err
	}
	if _, err := v.check(v.principal, Read, ref, "reading it"); err != nil {
		return Entity{}, false, 0, err
	}
	e, ok, err = v.withholdOne(e, ok, nil)
	return e, ok, rev, err
}

// withholdOne is withhold for a single read that has already been checked at its own address.
func (a *attributed) withholdOne(e Entity, ok bool, err error) (Entity, bool, error) {
	if err != nil || !ok {
		return e, ok, err
	}
	kept, err := a.withholdAll([]Entity{e})
	if err != nil {
		return Entity{}, false, err
	}
	return kept[0], true, nil
}

// ReadAt is the node at a path with every node the reader may not read left out of it — at any
// depth, and not only beneath a row: what stands at a node the reader may read is theirs, down
// to a record beneath that says otherwise, and beneath a node they may not read, what a record
// or a grant holds out to them is still theirs. A read of a path answers what reads of each
// node in it would, whichever node the read was asked at.
//
// Nothing of it theirs is a node holding nothing, `{}`: the node stands, and whether the reader
// may ask about it at all is the route's check (CheckInto), made before the read.
func (a *attributed) ReadAt(at []string) (*ir.Node, bool, error) {
	node, ok, _, err := a.ReadAtCommit(at)
	return node, ok, err
}

// ReadAtCommit is ReadAt, and the commit the read was served at.
func (a *attributed) ReadAtCommit(at []string) (*ir.Node, bool, int64, error) {
	node, ok, rev, err := servedReadAt(a.Store, at)
	if err != nil || !ok || IsSystem(a.principal) {
		return node, ok, rev, err
	}
	v, err := a.servedAt(rev)
	if err != nil {
		return nil, false, 0, err
	}
	node, ok, err = v.readable(node, at)
	return node, ok, rev, err
}

// PathsAt is the store's listing with every address the reader may not read left out — the
// listing's half of what ReadAt does by pruning a node.
//
// PER ADDRESS, because a listing has no node to prune: the answer is names, so what decides is
// the same question asked of each one (keep), and a hidden address is counted as a hidden node
// is. Whether the reader may ask about the path at all is the route's check (CheckInto), made
// before the listing as it is before a read.
//
// AN ELEMENT IS ITS HOLDING NODE'S. Visibility changes at a place a record or a grant names, and
// neither names one inside an array — a write at an element is already checked at the node that
// holds it (Write). So the addresses beneath an array stand or fall with it, which is one
// accessOf for the array rather than one per element.
func (a *attributed) PathsAt(at []string, depth int) ([]Path, error) {
	out, _, err := a.PathsAtCommit(at, depth)
	return out, err
}

// PathsAtCommit is PathsAt, and the commit the listing was served at.
func (a *attributed) PathsAtCommit(at []string, depth int) ([]Path, int64, error) {
	out, rev, err := servedPaths(a.Store, at, depth)
	if err != nil || IsSystem(a.principal) {
		return out, rev, err
	}
	v, err := a.servedAt(rev)
	if err != nil {
		return nil, 0, err
	}
	out, err = v.keepPaths(out)
	return out, rev, err
}

// PathsMatching is narrowed as PathsAt is — what the store listed, less what the reader may not
// see, a withheld node taking what is beneath it — AND SO IS WHAT DECIDED THE LISTING. The
// pattern is the store's to match, against what the store holds, so a row is kept only if it
// still matches as this reader reads it: `--if '{secret: {key: hunter2}}'` answering the row for
// a right guess and nothing for a wrong one is a read of `secret` by whoever may not read it.
// And a path reached something only if it reached something they may see, or a listing that
// comes back empty rather than not-found says a withheld node stands.
//
// Each read the store makes for it is checked at the commit it was served at: the store says so
// before it asks about anything that read answered (Narrow.Served), and the view the rest is
// asked of is brought up to it then.
func (a *attributed) PathsMatching(at *kpath.KPath, depth int, pattern *ir.Node, _ *Narrow) ([]Path, bool, error) {
	if IsSystem(a.principal) {
		return a.Store.PathsMatching(at, depth, pattern, nil)
	}
	v := a
	var places *placeTrie
	served := func(rev int64) error {
		if places != nil && rev <= v.rev {
			return nil
		}
		w, err := a.servedAt(rev)
		if err != nil {
			return err
		}
		p, err := w.places()
		if err != nil {
			a.attr.noteUnreadable()
			return err
		}
		v, places = w, p
		return nil
	}
	sees := func(p Path) (bool, error) {
		acc, err := v.accessOf(v.principal, p.Ref)
		if err != nil {
			a.attr.noteUnreadable()
		}
		return acc.level.Admits(Read), err
	}
	out, reached, err := a.Store.PathsMatching(at, depth, pattern, &Narrow{
		Sees:   sees,
		Keeps:  func(p Path) (bool, error) { return v.matchesAsRead(p, pattern, places) },
		Served: served,
	})
	if err != nil {
		return nil, false, err
	}
	kept, err := v.keepPaths(out)
	if err != nil {
		return nil, false, err
	}
	// Everything the path reached is withheld: for this reader it reached nothing.
	return kept, reached && (len(out) == 0 || len(kept) > 0), nil
}

// matchesAsRead reports whether the node at p, which the store's pattern kept, matches as the
// view's principal reads it. A node with no place at or beneath it reads whole or not at all, so
// the store's answer stands for whoever may read it, and nothing is read here; only a node part
// of which may be withheld is read, pruned and matched again.
func (a *attributed) matchesAsRead(p Path, pattern *ir.Node, places *placeTrie) (bool, error) {
	segs := p.Ref.Segs()
	acc, err := a.accessOf(a.principal, p.Ref)
	if err != nil {
		a.attr.noteUnreadable()
		return false, err
	}
	readable := acc.level.Admits(Read)
	t := places.at(segs)
	if t == nil || (len(t.children) == 0 && !t.below) {
		return readable, nil
	}
	// A read of its own, served at a commit of its own, and so checked at that one.
	node, ok, rev, err := servedReadAt(a.Store, segs)
	if err != nil || !ok {
		return false, err
	}
	if rev > a.rev {
		v, err := a.servedAt(rev)
		if err != nil {
			return false, err
		}
		if acc, err = v.accessOf(v.principal, p.Ref); err != nil {
			a.attr.noteUnreadable()
			return false, err
		}
		if places, err = v.places(); err != nil {
			a.attr.noteUnreadable()
			return false, err
		}
		a, readable, t = v, acc.level.Admits(Read), places.at(segs)
	}
	node, keep, err := a.prune(clone(node), segs, readable, t)
	if err != nil || !keep {
		return false, err
	}
	if p.Access != "" {
		kp, err := kpath.Parse(p.Access)
		if err != nil {
			return false, nil
		}
		if node, ok, err = Reach(node, kp, nil); err != nil || !ok {
			return false, nil
		}
	}
	matched, err := SafeMatch(node, pattern)
	return err == nil && matched, nil
}

// keepPaths is a listing as the view's principal may see it: what a READ of the same node would
// carry, path for path. A withheld node takes what is beneath it — except a place beneath it
// that is held out to the reader, which a read keeps (prune), so a listing keeps it too: one
// principal sees one set whichever way they ask. Only where nothing beneath a withheld node can
// differ from it is the subtree passed over without asking.
func (a *attributed) keepPaths(out []Path) ([]Path, error) {
	places, err := a.places()
	if err != nil {
		a.attr.noteUnreadable()
		return nil, err
	}
	kept := make([]Path, 0, len(out))
	// skip is a withheld node nothing beneath can differ from; under is one something can,
	// beneath which each path is asked and none is counted again as hidden.
	skip, under := "", ""
	for _, p := range out {
		key := p.KPath()
		if skip != "" {
			if key == skip || beneath(key, skip) {
				continue
			}
			skip = ""
		}
		quiet := under != "" && (key == under || beneath(key, under))
		if !quiet {
			under = ""
		}
		acc, err := a.accessOf(a.principal, p.Ref)
		if err != nil {
			a.attr.noteUnreadable()
			return nil, err
		}
		if acc.level.Admits(Read) {
			kept = append(kept, p)
			continue
		}
		if quiet {
			continue
		}
		a.attr.noteHidden()
		if t := places.at(p.Ref.Segs()); t == nil || (len(t.children) == 0 && !t.below) {
			skip = key
		} else {
			under = key
		}
	}
	return kept, nil
}

func (a *attributed) readable(node *ir.Node, at []string) (*ir.Node, bool, error) {
	places, err := a.places()
	if err != nil {
		a.attr.noteUnreadable()
		return nil, false, err
	}
	acc, err := a.accessOf(a.principal, RefAt(at...))
	if err != nil {
		a.attr.noteUnreadable()
		return nil, false, err
	}
	out, keep, err := a.prune(clone(node), at, acc.level.Admits(Read), places.at(at))
	if err != nil {
		return nil, false, err
	}
	if !keep {
		return ir.FromMap(map[string]*ir.Node{}), true, nil
	}
	return out, true, nil
}

// placeTrie is the paths where what a reader may read can differ from the node above: every
// perms record, every path a grant names, and the reserved root, whose own record fences viff's
// half off from the root's grants. Beneath a node with nothing in the trie below it, every node
// reads as that node does — ownership descends and a grant covers what is beneath its path — so
// only the trie's paths are asked.
type placeTrie struct {
	children map[string]*placeTrie
	below    bool // a grant here covers what is beneath the node and not the node
}

func (t *placeTrie) add(segs []string, below bool) {
	for _, s := range segs {
		if t.children == nil {
			t.children = map[string]*placeTrie{}
		}
		next := t.children[s]
		if next == nil {
			next = &placeTrie{}
			t.children[s] = next
		}
		t = next
	}
	t.below = t.below || below
}

// at is the trie beneath a path, nil when nothing in it is at or beneath the path.
func (t *placeTrie) at(segs []string) *placeTrie {
	for _, s := range segs {
		if t == nil {
			return nil
		}
		t = t.children[s]
	}
	return t
}

// places reads the trie from the loaded mirror, or by listing the mirror once.
func (a *attributed) places() (*placeTrie, error) {
	t := &placeTrie{}
	t.add([]string{ViffRoot}, false)
	add := func(key string, rec *permsRecord) {
		if rec == nil {
			return
		}
		if ref, err := ParseAddr(key); err == nil {
			t.add(ref.Segs(), false)
		}
		for _, g := range rec.grants {
			t.add(g.At, g.Below)
		}
	}
	a.attr.mu.Lock()
	if a.attr.placesAt != nil {
		kept := a.attr.placesAt
		a.attr.mu.Unlock()
		return kept, nil
	}
	loaded := a.attr.loaded
	if loaded {
		// Built under the lock, from the records themselves, so it is the truth as of this
		// moment and is kept until one of them moves (dropTaken). It is read and never
		// written once built, so every view shares it.
		for key, rec := range a.attr.records {
			add(key, rec)
		}
		a.attr.placesAt = t
	}
	root := a.attr.rootChildGrants
	a.attr.mu.Unlock()
	if loaded {
		return t, nil
	}
	mirror := []string{ViffRoot, PermsSystem, PermsKind}
	recs, err := ListAtErr(a.Store, mirror, len(mirror)+1, nil)
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		key := rec.Ref.Name()
		add(key, parseRecord(key, rec.Payload, root))
	}
	// The root's, beside the mirror and so not in that listing: its grants are the ones every
	// place without a record of its own reads through.
	if e, ok, err := a.Store.Get(permsRoot); err != nil {
		return nil, err
	} else if ok {
		add("", parseRecord("", e.Payload, root))
	}
	return t, nil
}

// prune is the node at segs as the reader sees it, given whether they may read segs itself and
// the places at and beneath it (t). keep is false when nothing of it is theirs. The node is the
// reader's own copy, and is narrowed in place.
func (a *attributed) prune(n *ir.Node, segs []string, readable bool, t *placeTrie) (*ir.Node, bool, error) {
	obj := ir.Uncomment(n)
	if t == nil || (len(t.children) == 0 && !t.below) || obj == nil || obj.Type != ir.ObjectType {
		if !readable {
			a.attr.noteHidden()
		}
		return n, readable, nil
	}
	fields, values := obj.Fields[:0:0], obj.Values[:0:0]
	for i, f := range obj.Fields {
		name := fieldName(f)
		child := append(append(make([]string, 0, len(segs)+1), segs...), name)
		sub, childReadable := t.children[name], readable
		if sub != nil || t.below {
			acc, err := a.accessOf(a.principal, RefAt(child...))
			if err != nil {
				a.attr.noteUnreadable()
				return nil, false, err
			}
			childReadable = acc.level.Admits(Read)
		}
		v, keep, err := a.prune(obj.Values[i], child, childReadable, sub)
		if err != nil {
			return nil, false, err
		}
		if keep {
			fields, values = append(fields, f), append(values, v)
		}
	}
	obj.Fields, obj.Values = fields, values
	if !readable && len(fields) == 0 {
		return nil, false, nil
	}
	return n, true, nil
}

// mayAsk refuses a write whose preconditions ask about what its writer may not read: the node a
// require names, and every place beneath it THE PATTERN REACHES. A pattern is a tree as a value
// is, and a match is of what it names: `{note: y}` at `team` asks nothing about `team.c`, so
// somebody's closed place inside a node does not stop whoever writes the node from guarding
// their write — which is every compare-and-swap, the pattern being what its writer read. A
// require about the write's own place is asked too: its writer holds read-write there, and a
// place inside it may still be somebody else's.
func (a *attributed) mayAsk(principal string, at Path, when []Require) error {
	if len(when) == 0 {
		return nil
	}
	places, err := a.places()
	if err != nil {
		a.attr.noteUnreadable()
		return fmt.Errorf("checking what %s may ask about: %w", principal, err)
	}
	for _, r := range when {
		p := r.At
		if p.Access == "" && len(p.Ref.Segs()) == 0 {
			p = at // the zero path is the write's own place
		}
		if p, err = p.Settled(); err != nil {
			return err
		}
		segs := p.Ref.Segs()
		if err := a.readsAll(principal, segs, nil, true); err != nil {
			return err
		}
		if r.Absent {
			continue // about the path and nothing under it
		}
		if err := a.asksInto(principal, r.Pattern, segs, places.at(segs)); err != nil {
			return err
		}
	}
	return nil
}

// asksInto is mayAsk beneath one node its caller has admitted the pattern AT: t is the places
// at and beneath it. A plain object — `!not` of one included — names the fields it asks about,
// and only those are followed. Anything else compares the node whole or in a way this does not
// follow — a scalar, a literal (`!raw`), an operator — so where something beneath could be
// withheld, all of it has to be readable.
func (a *attributed) asksInto(principal string, pat Payload, segs []string, t *placeTrie) error {
	if t == nil || (len(t.children) == 0 && !t.below) {
		return nil // reads as the node above it does
	}
	n := ir.Uncomment(pat)
	if n == nil || (n.Type == ir.NullType && n.Tag == "") {
		return nil // "something stands here": about the node, which was asked
	}
	// `!not` alone, since a longer tag (`!not.or`, `!not.glob`) composes it with another operator.
	followed := opIn(n.Tag) == "" || n.Tag == "!"+notName
	if n.Type != ir.ObjectType || escaped(n.Tag) || !followed {
		return a.readsAll(principal, segs, t, false)
	}
	for i, f := range n.Fields {
		name := fieldName(f)
		sub := t.children[name]
		if sub == nil && !t.below {
			continue
		}
		child := append(append(make([]string, 0, len(segs)+1), segs...), name)
		if err := a.readsAll(principal, child, nil, true); err != nil {
			return err
		}
		if err := a.asksInto(principal, n.Values[i], child, sub); err != nil {
			return err
		}
	}
	return nil
}

// notName is the match operator that negates its operand, which names fields as a plain object
// does.
const notName = "not"

// readsAll refuses unless principal may read the node at segs and every place beneath it.
func (a *attributed) readsAll(principal string, segs []string, t *placeTrie, nodeOnly bool) error {
	acc, err := a.accessOf(principal, RefAt(segs...))
	if err != nil {
		a.attr.noteUnreadable()
		return fmt.Errorf("checking %s at %s: %w", principal, JoinSegs(segs), err)
	}
	if !acc.level.Admits(Read) {
		a.attr.noteRefusal(Read)
		return refusal(Read, "asking about it in a precondition", principal, RefAt(segs...), acc)
	}
	if t == nil || nodeOnly {
		return nil
	}
	for name, sub := range t.children {
		child := append(append(make([]string, 0, len(segs)+1), segs...), name)
		if err := a.readsAll(principal, child, sub, false); err != nil {
			return err
		}
	}
	return nil
}

// writeBeneath refuses a write whose VALUE reaches a node its writer may not write. The node at
// segs has been checked already; this asks about what the value carries beneath it, and only
// where the answer can differ from the node above — a record, or a path a grant names (places)
// — so a write into a place with nothing recorded beneath it asks nothing at all, which is every
// row a source reflects.
func (a *attributed) writeBeneath(principal string, v Payload, segs []string, creating, absent bool) ([]Require, error) {
	places, err := a.places()
	if err != nil {
		a.attr.noteUnreadable()
		return nil, fmt.Errorf("checking %s beneath %s: %w", principal, JoinSegs(segs), err)
	}
	var must []Require
	if err := a.writeInto(principal, v, segs, places.at(segs), creating, absent, &must); err != nil {
		return nil, err
	}
	return must, nil
}

// writeInto is writeBeneath at one node, which its caller has admitted the write AT: t is the
// places at and beneath it, nil where nothing there can differ.
func (a *attributed) writeInto(principal string, v Payload, segs []string, t *placeTrie, creating, absent bool, must *[]Require) error {
	if t == nil || (len(t.children) == 0 && !t.below) {
		return nil
	}
	// ask is the check at one node the value reaches, and says whether the node is one this
	// write creates: where a `child-owner` line is what admits it, the write is made conditional
	// on nothing standing there — unless a node above is conditional already, beneath which
	// nothing stands either.
	ask := func(need Level, act string, at []string) (bool, error) {
		acc, err := a.accessFor(principal, RefAt(at...), creating)
		if err != nil {
			a.attr.noteUnreadable()
			return false, fmt.Errorf("checking %s at %s: %w", principal, JoinSegs(at), err)
		}
		if !acc.level.Admits(need) {
			a.attr.noteRefusal(need)
			return false, refusal(need, act, principal, RefAt(at...), acc)
		}
		if acc.byGrant && !absent {
			*must = append(*must, Require{At: Path{Ref: RefAt(at...)}, Absent: true})
		}
		return acc.byGrant, nil
	}
	// A VALUE THAT IS NOT AN OBJECT REPLACES THE NODE WHOLE, and so does one an operator is
	// written on: a scalar over `team` takes `team.c` with it as surely as a delete does.
	// Something stands beneath that could differ, so this is the owner's act — and `owner` from
	// a record descends, while `owner` from a grant is already held back here (the fence).
	obj := ir.Uncomment(v)
	if obj == nil || obj.Type != ir.ObjectType || replaces(obj) {
		_, err := ask(OwnerLevel, "replacing it whole, with what stands beneath it", segs)
		return err
	}
	for i, f := range obj.Fields {
		name := fieldName(f)
		sub := t.children[name]
		if sub == nil && !t.below {
			continue // reads as the node above it does, which the caller was admitted at
		}
		child := append(append(make([]string, 0, len(segs)+1), segs...), name)
		need, act := ReadWrite, "writing it"
		if replaces(ir.Uncomment(obj.Values[i])) {
			need, act = OwnerLevel, "deleting it"
		}
		made, err := ask(need, act, child)
		if err != nil {
			return err
		}
		if err := a.writeInto(principal, obj.Values[i], child, sub, creating, absent || made, must); err != nil {
			return err
		}
	}
	return nil
}

// replaces reports whether a value carries a merge operator — `!delete` above all — so that what
// stands at its node goes rather than being merged into. An escaped tag is data and replaces
// nothing.
func replaces(n *ir.Node) bool {
	return n != nil && !escaped(n.Tag) && opIn(n.Tag) != ""
}

func (a *attributed) List(pred func(Ref) bool) []Entity {
	out, _ := a.ListErr(pred)
	return out
}

func (a *attributed) ListAt(at []string, depth int, pred func(Ref) bool) []Entity {
	out, _ := a.ListAtErr(at, depth, pred)
	return out
}

// Since narrows each commit at its own revision, as a live watch does (narrow): the view is
// brought up to a commit before the commit is judged by it. A replay is judged by the view as it
// stands — at or past every commit in it — and not by perms as they stood at each commit.
func (a *attributed) Since(from int64, at ...*kpath.KPath) ([]Commit, error) {
	commits, err := a.Store.Since(from, at...)
	if IsSystem(a.principal) {
		return commits, err
	}
	for i := range commits {
		commits[i] = a.narrow(commits[i])
	}
	return commits, err
}

func (a *attributed) Observe(at ...*kpath.KPath) (<-chan Commit, func()) {
	ch, unsub := a.Store.Observe(at...)
	return a.narrowing(ch), unsub
}

func (a *attributed) WatchSubtree(depth int, prefix ...string) (<-chan Commit, func(), error) {
	ch, unsub, err := a.Store.WatchSubtree(depth, prefix...)
	if err != nil {
		return nil, nil, err
	}
	return a.narrowing(ch), unsub, nil
}

func (a *attributed) WatchPattern(prefix []string, at *kpath.KPath, waitIfAbsent, noInit bool) (<-chan Commit, func(), error) {
	ch, unsub, err := a.Store.WatchPattern(prefix, at, waitIfAbsent, noInit)
	if err != nil {
		return nil, nil, err
	}
	return a.narrowing(ch), unsub, nil
}

// Watch is the store's handle with its stream narrowed as every watch through a view is; the
// phase, Ready, progress and Close are the store's own.
func (a *attributed) Watch(prefix []string, at *kpath.KPath, opts WatchOptions) (*Watch, error) {
	w, err := a.Store.Watch(prefix, at, opts)
	if err != nil {
		return nil, err
	}
	narrowed := *w
	narrowed.Commits = a.narrowing(w.Commits)
	return &narrowed, nil
}

func (a *attributed) AsOf(rev int64, within ...[]string) (Snapshot, error) {
	snap, err := a.Store.AsOf(rev, within...)
	if err != nil || IsSystem(a.principal) {
		return snap, err
	}
	v, err := a.servedAt(snap.Rev())
	if err != nil {
		return nil, err
	}
	return narrowed{Snapshot: snap, a: v}, nil
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
	return n.a.withholdOne(n.Snapshot.Get(ref))
}

func (n narrowed) ReadAt(at []string) (*ir.Node, bool, error) {
	node, ok, err := n.Snapshot.ReadAt(at)
	if err != nil || !ok {
		return node, ok, err
	}
	return n.a.readable(node, at)
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

// Names reports whether a line of grantees names principal at level, read as a perms record's own
// line is (named): everyone, a whole kind, a principal as written or through its owner, a group's
// members, a place acting, and `audience:` as that place is held. It is how a reader asks whether
// an ask's waitingOn waits on somebody, where the line says more than names (mirror.WaitingOnField).
func Names(s Store, names []string, level Level, principal string) (bool, error) {
	a, ok := s.(*attributed)
	if !ok {
		return false, fmt.Errorf("%s is not an attributed store", principal)
	}
	who, err := a.resolve(principal)
	if err != nil {
		return false, err
	}
	return a.named(names, level, principal, who, ""), nil
}

// named reports whether a grantee list names the principal, by the structure of each name
// (Grantee): `*` names everyone; `<kind>:<id>` the principal named exactly that, or whose owner
// resolves to it, since a grant to a person means them and everything of theirs acting as
// itself; `<kind>:*` any principal whose name, or whose owner's, carries the kind; `via:` the
// place acting as that address, or beneath it under a trailing `*`, and never its owner, since
// the address is the identity; `group:` the group's members, each named the same way; `audience:`
// whoever holds level — read or read-write, and nothing else — at that address, asked one place
// deep and no further. at is the
// record the names are on, for a name that does not parse to be said against.
func (a *attributed) named(names []string, level Level, principal, who, at string) bool {
	return a.namedIn(names, level, principal, who, at, map[string]bool{})
}

// namedIn is named, walking a group's members with the groups already seen: a group met a
// second time is skipped, so a cycle ends because the walk does and a group nested any depth
// admits its members.
func (a *attributed) namedIn(names []string, level Level, principal, who, at string, seen map[string]bool) bool {
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
		case g.Kind == AnsweringKind:
			if a.deferred || level != Read {
				continue
			}
			b := *a
			b.deferred = true
			if ok, err := b.answers(principal, RefAt(g.Path...)); err == nil && ok {
				return true
			}
		case g.Kind == AudienceKind:
			if a.deferred || level != Read && level != ReadWrite {
				continue
			}
			b := *a
			b.deferred = true
			if acc, err := b.accessOf(principal, RefAt(g.Path...)); err == nil && acc.level.Admits(level) {
				return true
			}
		case g.Kind == GroupKind:
			if who == "" || seen[g.ID] {
				continue
			}
			seen[g.ID] = true
			ref := GroupRef(g.ID)
			if a.namedIn(a.members(ref), level, principal, who, ref.String(), seen) {
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
	// A FENCE SAYS WHAT IS UNDER, not what is above: the record that decided stands beneath the
	// node, and naming the owner above would send the reader to a record that admits them.
	if acc.unowned {
		return fmt.Errorf("%w: %s holds nothing at %s and %s needs %s; it has no owner on record, "+
			"and a place acts as whoever owns it — install it again as somebody",
			ErrForbidden, principal, ref, act, need)
	}
	if acc.fenced != "" {
		return fmt.Errorf("%w: %s holds %s at %s and %s needs %s; a place beneath it is %s's, "+
			"and ownership here would take it with it",
			ErrForbidden, who, held, ref, act, need, acc.fenced)
	}
	return fmt.Errorf("%w: %s holds %s at %s and %s needs %s; the nearest owner on record is %s",
		ErrForbidden, who, held, ref, act, need, acc.nearest)
}

// noteRefusal counts a refusal, by the level wanted.
func (a *Attribution) noteRefusal(need Level) {
	a.mu.Lock()
	if a.refused == nil {
		a.refused = map[Level]int{}
	}
	a.refused[need]++
	a.mu.Unlock()
	if a.parent != nil {
		a.parent.noteRefusal(need)
	}
}

// noteUnreadable counts a check the mirror could not answer.
func (a *Attribution) noteUnreadable() {
	a.mu.Lock()
	a.unreadable++
	a.mu.Unlock()
	if a.parent != nil {
		a.parent.noteUnreadable()
	}
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
//
// A node CREATED WITH GRANTS is not recorded here: its record changes what the node holds out
// whether or not it changes the owner, so it is written wherever the node is, in the node's own
// commit (withRecord).
//
// It reports whether the node is the writer's — by this record, by one of theirs already
// standing, or through the owner above — and false only where somebody else's record stands.
func (a *attributed) record(ref Ref, owner string) bool {
	// A RECORD ALREADY IN HAND IS THE DECISION. With the mirror loaded every record stands in
	// the cache, and a node written again in a later life of this process — a source
	// re-reflecting an entity it first reflected weeks ago — has its record there. Writing it
	// again would be refused by its own absence precondition, and the store explains a refused
	// write by reading what it descended through: the mirror's whole slice, once per such
	// node, on a store already busy with the pass. Nothing is read here: a cached record is
	// what the checks consult, and it is not less true for having arrived by the load.
	//
	// WHOSE it is comes before whether it is settled: a node this process settled as somebody
	// else's is settled, and is still not this writer's, which is the one answer that sends the
	// caller further down.
	a.attr.mu.Lock()
	known := a.attr.records[ref.String()]
	_, seen := a.attr.decided[ref]
	a.attr.mu.Unlock()
	if known != nil {
		if !seen {
			a.decide(ref, known)
		}
		return known.owner == "" || known.owner == owner
	}
	if seen {
		return true
	}
	// THE SYSTEM IS ONE OWNER, whichever part wrote, AND IT OWNS THE ROOT: a part is attribution
	// on the write and not a change of ownership, so the docket creating a question records
	// nothing — and the question's own record, written next under an absence precondition
	// (Establish), is not refused by one this wrote first. It holds wherever the system writes,
	// including a world-half node beneath a root somebody administers: a record naming a system
	// part there would take the place out of the root's grants and hand it to nobody: a fixture
	// the system wrote, unreachable by the people the root admits.
	above, err := a.ownerAbove(ref)
	if err != nil {
		return true // the store did not answer; the next write closes the gap, and nothing is remembered
	}
	if owner == above || IsSystem(owner) {
		a.decide(ref, nil)
		return true
	}
	// A REFUSED WRITE IS THE RECORD STANDING ALREADY, and an error is the store not answering;
	// only the second leaves the node unknown, so only the second is not remembered. Nothing
	// is returned to the caller either way: the entity write landed, and an owner record that
	// did not is a gap the next write closes.
	// WHAT THE NEW NODE HOLDS OUT is decided here, where its record is written: the nearest
	// default up the tree, the root's at the top, copied onto the record as a grant at the
	// node. Always with a grants field, empty or not, so a record that holds out nothing is
	// told apart from one written before defaults existed.
	grants, err := a.childGrants(ref)
	if err != nil {
		return true // as above: a record holding out a default nobody read is worse than none yet
	}
	committed, _, err := a.Store.Write(Write{
		At:        PermsRef(ref).At(),
		Value:     Obj{OwnerField: owner, GrantsField: GrantsPayload(SegsOf(ref), grants)}.Payload(),
		When:      []Require{Created()},
		Principal: SystemPerms,
	})
	if err != nil {
		return true // the store did not answer; the next write closes the gap, as it always did
	}
	rec := &permsRecord{owner: owner, grants: grants}
	if !committed {
		rec, err = a.recordAt(ref.String(), true) // whoever got there first
		if err != nil {
			return true
		}
	}
	a.decide(ref, rec)
	// Somebody else's record standing here is the one answer that sends the caller further
	// down: the node is theirs, and this writer's own begins beneath it.
	return rec == nil || rec.owner == "" || rec.owner == owner
}

// childGrants is what a node created at ref holds out: the nearest default above it as a
// grant at the node, and nothing when that default is empty.
func (a *attributed) childGrants(ref Ref) ([]Grant, error) {
	levels, err := a.defaultAbove(ref)
	if err != nil {
		return nil, err
	}
	if len(levels) == 0 {
		return []Grant{}, nil
	}
	return grantsOf(ref.Segs(), false, levels), nil
}

// grantsOf is levels as grants at a path: `child-owner` always about what is beneath it, and
// the rest about the path itself unless below says otherwise.
func grantsOf(at []string, below bool, levels Levels) []Grant {
	rest := map[Level][]string{}
	var out []Grant
	for l, names := range levels {
		if l == OwnerLevel {
			out = append(out, Grant{At: at, Below: true, To: map[Level][]string{OwnerLevel: names}})
			continue
		}
		rest[l] = names
	}
	if len(rest) > 0 {
		out = append(out, Grant{At: at, Below: below, To: rest})
	}
	return out
}

// GrantsOf is grantsOf for a caller holding levels somebody typed (`viff perms --grant`).
func GrantsOf(at []string, below bool, levels Levels) []Grant { return grantsOf(at, below, levels) }

// defaultAbove is the default in force for a node created at ref: the nearest record above it
// that says one — the ROOT's record included, which is where a deployment and an operator say
// it — and the process's own where no record does. A record that could not be read is an error
// and not an absence: the default beneath a private place is the private one, and falling back
// to the process's would create what everyone reads because the store was slow.
func (a *attributed) defaultAbove(ref Ref) (Levels, error) {
	segs := ref.Segs()
	for i := len(segs) - 1; i >= 0; i-- {
		rec, err := a.recordAt(RefAt(segs[:i]...).String(), false)
		if err != nil {
			return nil, err
		}
		if rec != nil && rec.hasChildGrants {
			return rec.childGrants, nil
		}
	}
	return a.attr.RootChildGrants(), nil
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
		a.attr.dropTaken()
	}
	if rec != nil && rec.owner != "" {
		if a.attr.owners == nil {
			a.attr.owners = map[string]string{}
		}
		a.attr.owners[ViaKind+":"+key] = rec.owner // what the node confers when it acts (resolve)
	}
	a.attr.mu.Unlock()
	if rec != nil {
		a.attr.dropInScopes(a.Store, key)
	}
}

// ownerAbove is the owner on record nearest above ref: its longest prefix with a record,
// and the system where none has one, since the system owns the root. A record that could not be
// read is an error and not an absence, since the answer where none stands is the system: a
// place acting through a record the store was slow to return would act as the system, which is
// the one principal nothing checks.
func (a *attributed) ownerAbove(ref Ref) (string, error) {
	segs := ref.Segs()
	for i := len(segs) - 1; i >= 0; i-- {
		rec, err := a.recordAt(RefAt(segs[:i]...).String(), false)
		if err != nil {
			return "", err
		}
		if rec != nil && rec.owner != "" {
			return rec.owner, nil
		}
	}
	return SystemDefault, nil
}

// recordAt reads the perms record at an address, nil when the mirror holds none, through the
// cache unless told to look again. The error is the store not answering, and nothing is
// remembered from one.
//
// A MIRROR THAT IS NOT LOADED KEEPS WHAT IT READ, and the commit it read it at: a check for what
// was served at a later revision than that reads the record again, since what it kept may be
// from before the record moved. A loaded one has been brought up to the revision already
// (servedAt).
func (a *attributed) recordAt(key string, fresh bool) (*permsRecord, error) {
	if !fresh {
		a.attr.mu.Lock()
		rec, ok := a.attr.records[key]
		loaded := a.attr.loaded
		current := loaded || a.attr.readAt[key] >= a.rev
		a.attr.mu.Unlock()
		if ok && current {
			return rec, nil
		}
		if loaded {
			return nil, nil // the whole mirror is here, and this key is not in it
		}
	}
	e, ok, rev, err := mirrorGet(a.Store, permsRecordAt(key))
	if err != nil {
		return nil, err
	}
	var rec *permsRecord
	if ok {
		// A line that cannot be read grants nothing: a malformed line refuses the whole field, so
		// a record with one holds out nothing until it is fixed, which is the safe way round and
		// is visible on the record itself. A grantee the grammar does not read is narrower: it is
		// kept, and admits nobody when the check meets it (named). A RECORD WRITTEN BEFORE
		// DEFAULTS EXISTED carries an owner and no grants field at all, and is read as holding out
		// what the root's default says: the open viff's, which is what it held out when written.
		rec = parseRecord(key, e.Payload, a.attr.RootChildGrants())
	}
	a.attr.mu.Lock()
	if a.attr.records == nil {
		a.attr.records = map[string]*permsRecord{}
	}
	a.attr.records[key] = rec
	if !a.attr.loaded {
		if a.attr.readAt == nil {
			a.attr.readAt = map[string]int64{}
		}
		a.attr.readAt[key] = rev
	}
	a.attr.dropTaken()
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
	a.attr.dropTaken()
	a.attr.owners = nil
	if ref, err := ParseAddr(key); err == nil {
		delete(a.attr.decided, ref)
	}
	loaded := a.attr.loaded
	a.attr.mu.Unlock()
	a.attr.dropInScopes(a.Store, key)
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
		// A record is a CHILD of the mirror — the same thing ChildOf says at the top of this
		// file — so the listing asks for one level below it and not for a depth from the root.
		for _, rec := range Below(a.Store, func(r Ref) bool {
			return strings.HasPrefix(r.Name(), prefix)
		}, 1, ViffRoot, PermsSystem, PermsKind) {
			beneath = append(beneath, rec.Ref.Name())
		}
	}
	for _, key := range beneath {
		_, _, _ = a.Store.Write(Write{At: permsRecordAt(key).At(), Principal: SystemPerms})
		a.invalidate(key)
	}
}

// Track loads the whole perms mirror into the view's cache and keeps it there: a watch on
// `mirror.perms` applies every record written — by this process or another — as it lands, so a
// check makes no round trip at all. A key not in the loaded mirror is absent, and no read is
// made to learn it.
//
// WHY IT IS LOADED RATHER THAN READ. The check reads the mirror for every prefix of a written
// address it has not seen, and a source's first pass after a wipe writes thousands of new
// addresses: on a store already busy with those writes, the check's reads queued behind them,
// hit their deadline, and a check whose mirror did not answer refused the write. The mirror is
// small — one record per change of ownership — so holding it is cheaper than asking for it.
//
// The watches are opened BEFORE the load, which is a barrier (refresh): the load is then at or
// past the commit each watch opened at, and what they deliver is applied in order with it
// (follow) — their opening replay of what stood is old news, and every commit after it is
// applied or, where it cannot be applied exactly, waited past by another barrier. What this
// process writes is read back by invalidate the moment it is written, for the writes that follow
// it; a read is ordered by the watermark, not by that (watermark.go). It stops when ctx does.
func Track(ctx context.Context, s Store) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	// Every failure is the store not answering, and is marked so: a mirror that could not be
	// loaded is not a mirror that is empty, and the caller asks again until it loads.
	mirror := []string{ViffRoot, PermsSystem, PermsKind}
	// The watch wants the depth of the records themselves, which is the mirror's own length and
	// the one level its records stand below it.
	ch, unsub, progress, err := watchAsking(a.Store, len(mirror)+1, mirror...)
	if err != nil {
		return Unread(fmt.Errorf("watching the perms mirror: %w", err))
	}
	// THE ROOT'S RECORD IS WATCHED APART, because it stands beside the mirror and not in it
	// (PermsRootKind) and a watch reports at one depth. One record, at its own address, so the
	// depth is that address's own.
	rootCh, rootUnsub, rootProgress, err := watchAsking(a.Store, len(permsRoot.Segs()), permsRoot.Segs()...)
	if err != nil {
		unsub()
		return Unread(fmt.Errorf("watching the root's perms record: %w", err))
	}
	if progress != nil && rootProgress != nil {
		a.attr.mu.Lock()
		a.attr.progress = []func(context.Context) (int64, error){progress, rootProgress}
		a.attr.mu.Unlock()
	}
	// The records and the root's together, in one read: the root's record stands beside the
	// mirror rather than in it, and a loaded mirror answers every key from the map, so a root
	// record left out would read as absent — the one node whose record decides every other.
	if err := a.attr.refresh(a.Store); err != nil {
		unsub()
		rootUnsub()
		return Unread(fmt.Errorf("loading the perms mirror: %w", err))
	}
	go func() {
		defer unsub()
		defer rootUnsub()
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-ch:
				if !ok {
					return
				}
				a.attr.follow(a.Store, false, c)
			case c, ok := <-rootCh:
				if !ok {
					return
				}
				a.attr.follow(a.Store, true, c)
			}
		}
	}()
	return nil
}

// watchAsking is a watch on the mirror that can be asked how far it is current, where the store
// has one (WatchSubtreeProgress), and a plain one with no progress to ask where it does not.
func watchAsking(s Store, depth int, prefix ...string) (<-chan Commit, func(), func(context.Context) (int64, error), error) {
	if h, ok := s.(interface {
		WatchSubtreeProgress(int, ...string) (<-chan Commit, func(), func(context.Context) (int64, error), error)
	}); ok {
		return h.WatchSubtreeProgress(depth, prefix...)
	}
	ch, unsub, err := s.WatchSubtree(depth, prefix...)
	return ch, unsub, nil, err
}

// Tracked reports whether the view holds the whole mirror.
func (a *Attribution) Tracked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loaded
}

// applyLocked is one change to the mirror, as a watch delivered it, at the node whose record
// moved (mirrorKey: a child's name, or the empty path for the root's own). Called with the lock
// held; the caller brings the scopes up to date once it is released (dropInScopes).
func (a *Attribution) applyLocked(key string, ev Event) {
	if a.records == nil {
		a.records = map[string]*permsRecord{}
	}
	if ev.Op == Delete || ev.To == nil {
		delete(a.records, key)
	} else {
		a.records[key] = parseRecord(key, ev.To, a.rootChildGrants)
	}
	a.dropTaken()
	a.owners = nil
	if ref, err := ParseAddr(key); err == nil {
		delete(a.decided, ref)
	}
}

// parseRecord reads one mirror record as the check holds it, applying the transition rule for
// a record from before defaults existed (see recordAt).
func parseRecord(key string, payload Payload, root Levels) *permsRecord {
	// Never nil: the key is an address, the root's being the empty one, and a grants line with
	// no `at` is about the node the record is on (SegsOf).
	node := []string{}
	if segs, err := PathSegs(key); err == nil {
		node = append(node, segs...)
	}
	grants, err := readGrants(node, payload)
	if err != nil {
		// A field the grammar refuses holds out NOTHING, which is the safe way for it to be
		// wrong and a silent one: a record written in a spelling this process no longer reads —
		// in a scope that outlived an upgrade, which the startup migration cannot reach, since
		// nothing enumerates scopes — closes its place with no sign of why. So it is said.
		slog.Warn("perms: a record's grants do not parse, so it holds out nothing", "record", key, "err", err)
	}
	rec := &permsRecord{owner: Str(payload, OwnerField), under: Str(payload, UnderField), grants: grants}
	rec.childGrants, rec.hasChildGrants, _ = readLevels(payload, ChildGrantsField)
	if rec.owner != "" && Get(payload, GrantsField) == nil && len(root) > 0 {
		if ref, err := ParseAddr(key); err == nil {
			rec.grants = []Grant{{At: ref.Segs(), To: map[Level][]string(root)}}
		}
	}
	return rec
}

// resolve is the owner a principal confers: itself, unless it is a place acting — `via:` a
// rule, a source, a resolver — in which case the owner ON THAT PLACE'S OWN RECORD, and nobody
// where it has none. An owner on record is always a principal that resolved already, so one
// record is the whole walk.
//
// ITS OWN RECORD, and not the nearest above. Above an acting place is viff's half, which is the
// system's, so "the owner above" is a way to act as the system by having no record: an install
// that named nobody, or one by somebody holding less than `owner` where they installed. What the
// system installs says so on a record (trigger.Engine.takeForTheSystem, the schedule's claim),
// and a place with no record resolves to itself — an address, which is no party and holds
// nothing (accessOf).
//
// Only a resolution that found a record is kept: the record can arrive at any install, and a
// miss remembered would leave a rule acting as nobody for the life of the process. A record this
// process writes is what the next resolution reads (decide).
func (a *attributed) resolve(principal string) (string, error) {
	// A PAIR ACTS AS ITS ASKER: whose the task's writes are, whose a node it creates is, and
	// whose ids it may write under. What it may DO is both parties' (accessFor), and that is
	// decided by asking each, not by the name it resolves to.
	if asker, _, ok := PairOf(principal); ok {
		return a.resolve(asker)
	}
	if KindOf(principal) != ViaKind {
		return principal, nil
	}
	a.attr.mu.Lock()
	if o, ok := a.attr.owners[principal]; ok {
		a.attr.mu.Unlock()
		return o, nil
	}
	a.attr.mu.Unlock()
	ref, err := ParseAddr(strings.TrimPrefix(principal, ViaKind+":"))
	if err != nil {
		return principal, nil // a via that names no one place acts as nobody's
	}
	rec, err := a.recordAt(ref.String(), false)
	if err != nil {
		return "", err
	}
	if rec == nil || rec.owner == "" {
		return principal, nil
	}
	a.attr.mu.Lock()
	if a.attr.owners == nil {
		a.attr.owners = map[string]string{}
	}
	a.attr.owners[principal] = rec.owner
	a.attr.mu.Unlock()
	return rec.owner, nil
}

// AddGrant appends a grant to the perms record of ref, as the system. The mirror is viffd's
// to write, and a grant is an owner's act that viffd carries out for them — a spawn assigning
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
	if err := g.check(); err != nil {
		return err
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
			grants, _ = readGrants(SegsOf(ref), cur.Payload)
			when = Require{Pattern: Exactly(cur.Payload)}
		}
		grants = append(grants, g)
		var standing Payload
		if exists {
			standing = cur.Payload
		}
		committed, _, err := a.Store.Write(Write{
			At:        rec.At(),
			Value:     Obj{GrantsField: GrantsOver(SegsOf(ref), grants, standing)}.Payload(),
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
// holds out is exactly its resolver's, so a change of resolver is a replacement and not an
// addition. An empty list is written as one — a grants field holding nothing — since a record
// with no grants field at all is one from before defaults existed, read as the root's.
func SetGrants(s Store, ref Ref, gs []Grant) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	if err := checkGrants(gs); err != nil {
		return err
	}
	// THE LIST IS REPLACED, NOT MERGED, in one write guarded by what it read (GrantsOver): no
	// moment stands between the old list and the new one, and a record that changed underneath
	// is read again rather than written over.
	rec := PermsRef(ref)
	for {
		cur, exists, err := a.Store.Get(rec)
		if err != nil {
			return fmt.Errorf("granting at %s: %w", ref, err)
		}
		when := Created()
		var standing Payload
		if exists {
			when, standing = Require{Pattern: Exactly(cur.Payload)}, cur.Payload
		}
		committed, _, err := a.Store.Write(Write{
			At:        rec.At(),
			Value:     Obj{GrantsField: GrantsOver(SegsOf(ref), gs, standing)}.Payload(),
			When:      []Require{when},
			Principal: SystemPerms,
		})
		if err != nil {
			return fmt.Errorf("granting at %s: %w", ref, err)
		}
		if committed {
			break
		}
	}
	a.invalidate(ref.String())
	return nil
}

// CheckChildGrants refuses a default that cannot be one. `answer` is granted by whom it answers
// for, and a resolver's answer for a place is given as the place's owner on record: a default is
// copied into a node somebody else may own, so an `answer` in it would have them answer for
// whoever created there, by nobody's grant.
func CheckChildGrants(levels Levels) error {
	if _, ok := levels[Answer]; ok {
		return fmt.Errorf("child-grants: %q names nobody it answers for: it is granted by whom it "+
			"answers for (viff resolver grant), not handed down", Answer)
	}
	return nil
}

// SetChildGrants sets what a node created beneath ref holds out (ChildGrantsField), as the system: a
// nil map removes the field, so the node inherits again; an empty one says nothing is held
// out.
func SetChildGrants(s Store, ref Ref, levels Levels) error {
	if err := CheckChildGrants(levels); err != nil {
		return err
	}
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	value := Obj{ChildGrantsField: Removed()}
	if levels != nil {
		// A write MERGES, and the default is an object: a level the new default does not name
		// has to be removed by name, or it stands from the default before.
		d := Obj{}
		for _, l := range []Level{Read, ReadWrite, OwnerLevel, Control, Answer} {
			if names, ok := levels[l]; ok {
				d[string(l)] = strList(names)
			} else {
				d[string(l)] = Removed()
			}
		}
		value[ChildGrantsField] = d
	}
	_, _, err := a.Store.Write(Write{At: PermsRef(ref).At(), Value: value.Payload(), Principal: SystemPerms})
	if err != nil {
		return fmt.Errorf("setting the default at %s: %w", ref, err)
	}
	a.invalidate(ref.String())
	return nil
}

// CheckTake is the check a verb makes before it takes a place for principal — an install at a
// place, a record set on one: principal owns ref, or may create it and nothing stands there. It
// is Claim's own admission without the record, for a verb that has work to refuse first.
func CheckTake(s Store, principal string, ref Ref, act string) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	_, err := a.mayTake(principal, ref, act)
	return err
}

// mayTake is whether principal may take ref: `owner` there, asked as a creation — and where
// that came from a `child-owner` line, nothing may stand at the node, since the line is the
// right to create one and a node that stands is its nearest owner's. A place is taken ahead of
// what goes in it, so this asks the store where a write would carry a condition; a node made
// in between is then its maker's by the record their write leaves, which the claim's own
// absence condition loses to.
func (a *attributed) mayTake(principal string, ref Ref, act string) (access, error) {
	acc, err := a.checkFor(principal, OwnerLevel, ref, act, true)
	if err != nil || !acc.byGrant {
		return acc, err
	}
	stands, err := a.Store.StandsAt(ref.Segs())
	if err != nil {
		return acc, fmt.Errorf("checking %s at %s: %w", principal, ref, err)
	}
	if !stands {
		return acc, nil
	}
	// What is held there otherwise, which is not `owner` — a record would have said so above —
	// and is what the refusal names, with whose the node is.
	held, err := a.accessOf(principal, ref)
	if err != nil {
		return acc, fmt.Errorf("checking %s at %s: %w", principal, ref, err)
	}
	a.attr.noteRefusal(OwnerLevel)
	return acc, refusal(OwnerLevel, act, principal, ref, held)
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
	acc, err := a.mayTake(principal, ref, "taking it")
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
	// IN VIFF'S HALF A PLACE IS TAKEN ONLY WHERE SOMETHING STANDS. What stands there is made by
	// the verb that owns it — an install, a spawn, the system's own charter — and the generic
	// write does not reach it, so a record for a node nothing stands at is a reservation: of a
	// name the system has not used yet (`mirror.schedule.date`, a rule the next release adds, a
	// task id a rule will mint), or of a kind nobody has installed into, and whatever is made
	// there afterwards is the reserver's. The world's half is where a place is taken ahead of
	// what goes in it, and is untouched.
	if ref.Reserved() && !IsSystem(acc.who) {
		stands, err := a.Store.StandsAt(ref.Segs())
		if err != nil {
			return fmt.Errorf("claiming %s: %w", ref, err)
		}
		if !stands {
			a.attr.noteRefusal(OwnerLevel)
			return fmt.Errorf("%w: nothing stands at %s, and in viff's half a place is taken only "+
				"where something does: what stands there is made by the verb that owns it "+
				"(charter -a, resolver add, runner add, iod add, task run), and is its maker's",
				ErrForbidden, ref)
		}
	}
	grants, err := a.childGrants(ref)
	if err != nil {
		return fmt.Errorf("claiming %s: %w", ref, err)
	}
	committed, _, err := a.Store.Write(Write{
		At:        PermsRef(ref).At(),
		Value:     Obj{OwnerField: acc.who, GrantsField: GrantsPayload(SegsOf(ref), grants)}.Payload(),
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
		grants, _ := readGrants(SegsOf(ref), cur.Payload)
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
		// Written OVER what stands (GrantsOver): a line that lost its last grantee leaves, the
		// ones beneath it move up, and a level taken off a line is gone from it — none of which
		// a plain array write does, since it merges by position.
		committed, _, err := a.Store.Write(Write{
			At:        rec.At(),
			Value:     Obj{GrantsField: GrantsOver(SegsOf(ref), kept, cur.Payload)}.Payload(),
			When:      []Require{{Pattern: Exactly(cur.Payload)}},
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
func DefaultFor(s Store, ref Ref) (Levels, error) {
	a, ok := s.(*attributed)
	if !ok {
		return nil, nil
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
		var err error
		if grants, err = a.childGrants(ref); err != nil {
			return fmt.Errorf("establishing %s: %w", ref, err)
		}
	}
	rec := Obj{OwnerField: owner, GrantsField: GrantsPayload(SegsOf(ref), grants)}
	if len(under.Segs()) > 0 {
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

// OwnedForSystem says that a node a place of the system's has just made is the system's: where
// principal — a rule firing, `via:` its address — acts as the system, and no record stands at
// ref, and none above it is somebody's, the system's own record is written there, holding out
// the default above. Nothing otherwise, a person's creation having been recorded by their write.
//
// THE RECORD DOES TWO THINGS, and the second is why it is written at all. It says whose the node
// is — which a node with no record says too, by the nearest record above, and that is the
// system's here either way. And it STOPS THE GRANTS ABOVE: a grant does not reach through an
// owner record, and the rows of viff's half hold `child-owner` out to everyone beneath them at
// any depth, so that anybody may make a task or a schedule there. With no record at what the
// system made, that line reaches INSIDE it: a new child beneath a system-spawned task, or a
// field added to a derived calendar fact, is a creation like any other, admitted and recorded
// as its creator's. That a node which stands is not TAKEN by the line (accessFor) does not
// stop it being written into through children that do not stand yet.
//
// SAID BY WHOEVER MAKES THE NODE, rather than concluded by every system write. A write by the
// system records no owner, since the system owns the root; and in viff's half the node above
// is often a seed row that holds `owner` beneath it out to everyone, so that anybody may make a
// task or a schedule there — a grant that, with no record to stop at, reaches what the system
// made too: a task a system rule spawned was anybody's to take, and its gate's with it. But it
// cannot be a rule of the check, because some of what the system makes there is made FOR
// somebody who then writes it — a resolver's cover — and a system record on it would shut
// them out. So each maker says: this one here, a question at pose, a cover under its
// resolver (mirror.Cover).
func OwnedForSystem(s Store, ref Ref, principal string) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	who, err := a.resolve(principal)
	if err != nil || !IsSystem(who) {
		return err
	}
	if rec, err := a.recordAt(ref.String(), false); err != nil || rec != nil {
		return err
	}
	above, err := a.ownerAbove(ref)
	if err != nil || !IsSystem(above) {
		return err
	}
	return Establish(s, ref, who, Ref{}, nil)
}

// RecordSnapshot is a node's perms record as it stood: what a caller that may have to put a node
// back keeps beside the node's content, since whose a node is lives in its record and not in it.
type RecordSnapshot struct {
	ref     Ref
	payload Payload // nil where no record stood
}

// SnapshotRecord reads ref's own perms record, as the system.
func SnapshotRecord(s Store, ref Ref) (RecordSnapshot, error) {
	a, ok := s.(*attributed)
	if !ok {
		return RecordSnapshot{ref: ref}, nil
	}
	e, ok, err := a.Store.Get(PermsRef(ref))
	if err != nil {
		return RecordSnapshot{}, fmt.Errorf("reading the record of %s: %w", ref, err)
	}
	snap := RecordSnapshot{ref: ref}
	if ok {
		snap.payload = clone(e.Payload)
	}
	return snap, nil
}

// RestoreRecord puts a node's perms record back as the snapshot has it — the record that stood,
// or none. It is the other half of putting a node back: content restored under somebody else's
// record is that party's node with the old content in it.
func RestoreRecord(s Store, snap RecordSnapshot) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	rec := PermsRef(snap.ref)
	defer a.invalidate(snap.ref.String())
	// ONE WRITE, whichever it is. A record taken away and then written passes through having
	// none — and a place with no record acts as nobody, in this process and, as the mirror's
	// watch replays the two writes, a moment later again. A raw value replaces what stands
	// whole, the grants list included, which a plain write would merge by position.
	w := Write{At: rec.At(), Principal: SystemPerms}
	if snap.payload != nil {
		w.Value = Raw(AsData(snap.payload))
	}
	if _, _, err := a.Store.Write(w); err != nil {
		return fmt.Errorf("restoring the record of %s: %w", snap.ref, err)
	}
	return nil
}

// ActsAs is who the place at ref acts as when it fires, reflects, answers or ticks — the owner
// on its OWN record (resolve) — and "" where it has none, which is nobody: such a place holds
// nothing anywhere. It is what a verb that makes an acting place asks once it has made it,
// since whose a place is was decided by a record written after the place, and a place left
// standing without one looks installed and does nothing.
func ActsAs(s Store, ref Ref) (string, error) {
	a, ok := s.(*attributed)
	if !ok {
		return "", fmt.Errorf("%s is not an attributed store", ref)
	}
	via := Via(ref)
	who, err := a.resolve(via)
	if err != nil || who == via {
		return "", err
	}
	return who, nil
}

// OwnedByOther reports whether ref is somebody's other than whoever principal acts as, and
// whose. The system is one owner whichever part is named. It is what a maker asks before making
// something under an id that may already be somebody's: a task's id names the work, so a run
// under an id that stands writes over what the last run left — which is right for the party
// whose work it is, and for nobody else.
//
// Whose a node is, is its own record's say where it has one. Where it has none it is somebody's
// only if it STANDS — it is then its nearest owner's, as what the system made is the system's
// with nothing said beside it — and an id nothing stands at is nobody's yet, whoever owns the
// place above it.
func OwnedByOther(s Store, ref Ref, principal string) (string, bool, error) {
	a, ok := s.(*attributed)
	if !ok {
		return "", false, nil
	}
	who, err := a.resolve(principal)
	if err != nil {
		return "", false, err
	}
	owner, err := a.ownerOf(ref)
	if err != nil || owner == "" {
		return "", false, err
	}
	if owner == who || (IsSystem(owner) && IsSystem(who)) {
		return owner, false, nil
	}
	return owner, true, nil
}

// OwnerOf is whose ref is: its own record's owner where it has one, the nearest owner above where
// it has none and STANDS, and "" where nothing stands and no record names anybody — a place not
// yet made is nobody's, whoever owns the place above it. It is what a firing reads to know who
// wrote the subject it is about, since an event carries no writer: the record a creation left is
// the only account of whose the thing is.
func OwnerOf(s Store, ref Ref) (string, error) {
	a, ok := s.(*attributed)
	if !ok {
		return "", fmt.Errorf("%s is not an attributed store", ref)
	}
	return a.ownerOf(ref)
}

// SettledOwnerOf is OwnerOf for a node that may have just been made: what a firing reads the moment
// it hears of a write.
//
// A creation's record lands AFTER the write it records (attributed.write: a record standing for a
// write that then failed would make the next creator there not its owner), and a rule can hear the
// write in between. Read then, a node somebody made under a grant is the place's owner's — the
// rule would take its own installer for the person who wrote it. So: the node's own record the
// moment one stands, and otherwise the owner as it reads once `settle` has passed, which is the
// record at whichever node above it the creation made, or the nearest owner above for a node its
// place's owner made (which leaves no record at all, and costs the wait).
func SettledOwnerOf(s Store, ref Ref, settle time.Duration) (string, error) {
	a, ok := s.(*attributed)
	if !ok {
		return "", fmt.Errorf("%s is not an attributed store", ref)
	}
	deadline := time.Now().Add(settle)
	for {
		rec, err := a.recordAt(ref.String(), true)
		if err != nil {
			return "", err
		}
		if rec != nil && rec.owner != "" {
			return rec.owner, nil
		}
		if !time.Now().Before(deadline) {
			return a.ownerOf(ref)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ownerOf is OwnerOf over this view. A record not in the cache is looked for again before it is
// taken as absent, since a creation's record may have landed after the cache was filled.
func (a *attributed) ownerOf(ref Ref) (string, error) {
	rec, err := a.recordAt(ref.String(), false)
	if err == nil && rec == nil {
		rec, err = a.recordAt(ref.String(), true)
	}
	if err != nil {
		return "", err
	}
	if rec != nil && rec.owner != "" {
		return rec.owner, nil
	}
	stands, err := a.Store.StandsAt(ref.Segs())
	if err != nil || !stands {
		return "", err
	}
	return a.ownerAbove(ref)
}

// ClaimForSystem is Claim for a node the SYSTEM makes under a name it decides — a rule of the
// base charter — where a record already standing is not left as it is. Claim leaves one,
// whoever's, because a place is taken by whoever may and never from its owner; but these names
// are the system's to decide, the seed holds `owner` beneath them out to everyone, and a record
// somebody put there first would make what the system installed theirs: written by the system,
// firing as them, and suspendable by nobody. Theirs is removed and the system's written, which
// is ownership changing the one way it does — delete and create — and displaced says whose went.
func ClaimForSystem(s Store, ref Ref, system string) (displaced string, err error) {
	a, ok := s.(*attributed)
	if !ok {
		return "", nil
	}
	if !IsSystem(system) {
		return "", fmt.Errorf("claiming %s for the system: %q is not the system", ref, system)
	}
	rec, err := a.recordAt(ref.String(), true)
	if err != nil {
		return "", fmt.Errorf("claiming %s: %w", ref, err)
	}
	if rec != nil && !IsSystem(rec.owner) {
		displaced = rec.owner
		if _, _, err := a.Store.Write(Write{At: PermsRef(ref).At(), Principal: SystemPerms}); err != nil {
			return "", fmt.Errorf("claiming %s: removing %s's record: %w", ref, displaced, err)
		}
		a.invalidate(ref.String())
	}
	return displaced, Claim(s, ref, system)
}

// Reestablish is Establish for a node that is made again under the address it had: the record is
// written where none stands, and where one does its grants and its link are REPLACED with these.
// A question is the case — one that stood orphaned, its waiter gone without a decision, is asked
// again under its own id when the gate comes back to it — and who may answer belongs to the
// asking, not to the id: left as the first asking wrote it, a gate whose answerers changed goes
// on admitting the old ones and refusing the new.
func Reestablish(s Store, ref Ref, owner string, under Ref, grants []Grant) error {
	a, ok := s.(*attributed)
	if !ok {
		return nil
	}
	if grants == nil {
		var err error
		if grants, err = a.childGrants(ref); err != nil {
			return fmt.Errorf("establishing %s: %w", ref, err)
		}
	}
	rec := Obj{OwnerField: owner, GrantsField: GrantsPayload(SegsOf(ref), grants)}
	if len(under.Segs()) > 0 {
		rec[UnderField] = under.String()
	}
	created, _, err := a.Store.Write(Write{
		At: PermsRef(ref).At(), Value: rec.Payload(), When: []Require{Created()}, Principal: SystemPerms,
	})
	if err != nil {
		return fmt.Errorf("establishing %s: %w", ref, err)
	}
	a.invalidate(ref.String())
	if created {
		return nil
	}
	if err := SetGrants(s, ref, grants); err != nil {
		return err
	}
	if len(under.Segs()) > 0 {
		return SetUnder(s, ref, under)
	}
	return nil
}

// Recorded reports whether an owner record stands at ref ITSELF, rather than somewhere above it:
// what a caller asks when the record is what makes the node safe to act on — a question, which
// until its own record stands is held out by the docket's row to everybody. An absence is read
// again from the store before it is believed, since the caller is usually waiting for a record
// another write is about to land.
func Recorded(s Store, ref Ref) (bool, error) {
	a, ok := s.(*attributed)
	if !ok {
		return true, nil
	}
	rec, err := a.recordAt(ref.String(), false)
	if err == nil && rec == nil {
		rec, err = a.recordAt(ref.String(), true)
	}
	return rec != nil && rec.owner != "", err
}

// LevelAt is who holds level on ref's own lines — the grants at the node itself, not the ones
// above it that reach it. Nobody, and no error, where ref has no record of its own or its record
// says nothing about level: the answer is what the NODE says, and what a node says nothing about
// is the default above it (DefaultFor), which is the caller's to ask for.
func LevelAt(s Store, ref Ref, level Level) ([]string, error) {
	a, ok := s.(*attributed)
	if !ok {
		return nil, nil
	}
	cur, exists, err := a.Store.Get(PermsRef(ref))
	if err != nil {
		return nil, fmt.Errorf("reading %s at %s: %w", level, ref, err)
	}
	if !exists {
		return nil, nil
	}
	grants, err := readGrants(SegsOf(ref), cur.Payload)
	if err != nil {
		return nil, fmt.Errorf("reading %s at %s: %w", level, ref, err)
	}
	for _, g := range grants {
		if !g.Below && strings.Join(g.At, "\x00") == strings.Join(ref.Segs(), "\x00") {
			if names := g.To[level]; len(names) > 0 {
				return names, nil
			}
		}
	}
	return nil, nil
}

// Members is a group's members, each of whom acts as themselves, with a group named among them
// expanded in turn. A group that names itself, directly or around a cycle, is walked once.
func Members(s Store, group string, seen map[string]bool) []string {
	if seen == nil {
		seen = map[string]bool{}
	}
	if seen[group] {
		return nil
	}
	seen[group] = true
	e, ok, err := s.Get(GroupRef(group))
	if err != nil || !ok {
		return nil
	}
	var out []string
	for _, m := range Strs(e.Payload, MembersField) {
		if g, err := ParseGrantee(m); err == nil && g.Kind == GroupKind {
			out = append(out, Members(s, g.ID, seen)...)
			continue
		}
		out = append(out, m)
	}
	return out
}

// SetLevel replaces who holds level on ref's own lines — the grants at the node itself, not
// beneath it: every such line loses the level, and the first of them (or a new one) gets it
// back with names. Empty names remove the level. A resolver's grant on a question is written
// this way, so holding it out and releasing it move `read-write` and leave `read` as the default
// gave it.
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
			grants, _ = readGrants(SegsOf(ref), cur.Payload)
			when = Require{Pattern: Exactly(cur.Payload)}
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
		var standing Payload
		if exists {
			standing = cur.Payload
		}
		committed, _, err := a.Store.Write(Write{
			At: rec.At(), Value: Obj{GrantsField: GrantsOver(SegsOf(ref), out, standing)}.Payload(), When: []Require{when}, Principal: SystemPerms,
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
// `mirror.principal.<kind>.<id>`, written as the system under an absence precondition. It is the
// record of who has been met; a grant to the kind reads the name, not this.
func Note(s Store, principal string) error {
	g, err := ParsePrincipal(principal)
	if err != nil {
		return err
	}
	if g.Kind == ViaKind || g.Kind == SystemKind {
		return fmt.Errorf("%s: viffd makes that name, and meets nobody by it", principal)
	}
	_, _, err = s.Write(Write{
		At:        PrincipalRef(g.Kind, g.ID).At(),
		Value:     Obj{"first": Val(nowStamp())}.Payload(),
		When:      []Require{Created()},
		Principal: SystemPerms,
	})
	return err
}

// PrincipalSystem is the system under viff's root that lists the principals this viff has
// met, by the kind their name carries: `mirror.principal.user.<email>` for a person, written the
// first time they present themselves over the api, `mirror.principal.runner.<identity>` for a
// worker at its claim. A group's record stands beside them, under GroupKind.
const PrincipalSystem = "principal"

// MembersField is the field of a group listing its members.
const MembersField = "members"

// GroupRef addresses a group.
func GroupRef(name string) Ref { return PrincipalRef(GroupKind, name) }

// IsGroup reports whether an address is a group's.
func IsGroup(ref Ref) bool {
	return ref.ChildOf(ViffRoot, PrincipalSystem, GroupKind) && ref.Name() != ""
}

// UserRef addresses the record of one user having acted here.
func UserRef(principal string) Ref { return PrincipalRef(UserKind, principal) }

// PrincipalRef addresses the record of one principal of a kind having acted here.
func PrincipalRef(kind, principal string) Ref { return ViffRef(PrincipalSystem, kind, principal) }

// The optional read-side methods a store may have pass through, or a wrapped store would look
// like one that cannot say a read failed, and a restore over an unreadable store would read as
// an empty one — the defect ReadFailed exists to refuse. Each answers as the inner store does
// and falls back to what a caller without the method gets.

func (a *attributed) ReadError() error { return ReadFailed(a.Store) }

// StandsAt answers only for what the principal may read into. Whether a node exists is something
// a read tells you, so it is refused where the read would be: asked freely, it would say which
// names stand inside a place held out to nobody.
func (a *attributed) StandsAt(at []string) (bool, error) {
	if !IsSystem(a.principal) {
		if err := CheckInto(a, a.principal, RefAt(at...)); err != nil {
			return false, err
		}
	}
	return a.Store.StandsAt(at)
}

func (a *attributed) ListErr(pred func(Ref) bool) ([]Entity, error) {
	out, _, err := a.ListErrCommit(pred)
	return out, err
}

// ListErrCommit is ListErr, and the commit the listing was served at.
func (a *attributed) ListErrCommit(pred func(Ref) bool) ([]Entity, int64, error) {
	ents, rev, err := servedList(a.Store, pred)
	if err != nil {
		return nil, 0, err
	}
	if IsSystem(a.principal) {
		return ents, rev, nil
	}
	v, err := a.servedAt(rev)
	if err != nil {
		return nil, 0, err
	}
	ents, err = v.keepAll(ents)
	return ents, rev, err
}

func (a *attributed) ListAtErr(at []string, depth int, pred func(Ref) bool) ([]Entity, error) {
	out, _, err := a.ListAtErrCommit(at, depth, pred)
	return out, err
}

// ListAtErrCommit is ListAtErr, and the commit the listing was served at.
func (a *attributed) ListAtErrCommit(at []string, depth int, pred func(Ref) bool) ([]Entity, int64, error) {
	ents, rev, err := servedListAt(a.Store, at, depth, pred)
	if err != nil {
		return nil, 0, err
	}
	if IsSystem(a.principal) {
		return ents, rev, nil
	}
	v, err := a.servedAt(rev)
	if err != nil {
		return nil, 0, err
	}
	ents, err = v.keepAll(ents)
	return ents, rev, err
}

// WatchSubtreeProgress is the store's, narrowed as every watch through a view is; a progress
// answer carries no change, and so nothing to narrow.
func (a *attributed) WatchSubtreeProgress(depth int, prefix ...string) (<-chan Commit, func(), func(context.Context) (int64, error), error) {
	ch, unsub, progress, err := watchAsking(a.Store, depth, prefix...)
	if err != nil {
		return nil, nil, nil, err
	}
	return a.narrowing(ch), unsub, progress, nil
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

func (a *attributed) Close() error {
	if h, ok := a.Store.(interface{ Close() error }); ok {
		return h.Close()
	}
	return nil
}

// takenTrie is where somebody's places stand, by path: what the fence asks about. Only records
// naming a NON-SYSTEM owner are in it, since the system owns the root and co-owns everything
// beneath it — a system-owned place is not somebody else's, and `viff` itself is one.
type takenTrie struct {
	children map[string]*takenTrie
	// mark is the owner of the place standing AT this node, empty where none does. beneath
	// counts the places strictly beneath it, and sole is their owner where they are all one
	// principal's — which is what lets an owner take the node above their own places while
	// stopping everybody else.
	mark    string
	beneath int
	sole    string
}

// addTaken records one owner's place, building the path as it goes.
func (t *takenTrie) addTaken(segs []string, owner string) {
	for _, s := range segs {
		if t.children == nil {
			t.children = map[string]*takenTrie{}
		}
		next := t.children[s]
		if next == nil {
			next = &takenTrie{}
			t.children[s] = next
		}
		t = next
	}
	t.mark = owner
}

// count fills in beneath and sole from the marks in the subtree, post-order. It answers what
// the subtree holds INCLUDING this node's own mark, which is what its parent counts; a node's
// own place is not beneath itself.
func (t *takenTrie) count() (int, string) {
	total, sole := 0, ""
	merge := func(n int, owner string) {
		if n == 0 {
			return
		}
		switch {
		case total == 0:
			sole = owner
		case owner != sole:
			sole = "" // more than one owner in there, so nobody may take the node above them
		}
		total += n
	}
	for _, c := range t.children {
		merge(c.count())
	}
	t.beneath, t.sole = total, sole
	if t.mark != "" {
		merge(1, t.mark)
	}
	return total, sole
}

// at is the trie node at a path, nil when nothing is taken at or beneath it.
func (t *takenTrie) at(segs []string) *takenTrie {
	for _, s := range segs {
		if t == nil {
			return nil
		}
		t = t.children[s]
	}
	return t
}

// fenced reports whether ownership concluded from a grant is held back at ref: a place beneath
// it is somebody else's. It is false where every place beneath is the asker's own, so an owner
// may still take the node above their own places, and false where nothing beneath is taken. The
// string is whose place it is, where they are one principal — what the refusal names.
func (a *attributed) fenced(ref Ref, who string) (string, bool, error) {
	if IsSystem(who) {
		return "", false, nil
	}
	t, err := a.taken()
	if err != nil {
		return "", false, err
	}
	n := t.at(ref.Segs())
	if n == nil || n.beneath == 0 {
		return "", false, nil
	}
	// An empty sole is SEVERAL owners, and an empty who is nobody: the two are not the same
	// principal, and a request that names nobody takes no node above other people's places.
	if n.sole != "" && n.sole == who {
		return "", false, nil
	}
	return n.sole, true, nil
}

// taken builds the trie from the mirror: from the cache when the whole of it is loaded, and by
// listing it otherwise — the same shape places() has, and for the same reason. It is rebuilt
// when a record moves rather than kept in step, since a record moving is rare and a trie that
// disagreed with the mirror would fence the wrong places.
func (a *attributed) taken() (*takenTrie, error) {
	a.attr.mu.Lock()
	if a.attr.takenAt != nil {
		t := a.attr.takenAt
		a.attr.mu.Unlock()
		return t, nil
	}
	gen := a.attr.takenGen
	loaded := a.attr.loaded
	var recs map[string]*permsRecord
	if loaded {
		recs = make(map[string]*permsRecord, len(a.attr.records))
		for k, v := range a.attr.records {
			recs[k] = v
		}
	}
	a.attr.mu.Unlock()

	t := &takenTrie{}
	add := func(key string, rec *permsRecord) {
		if rec == nil || rec.owner == "" || IsSystem(rec.owner) {
			return
		}
		if ref, err := ParseAddr(key); err == nil {
			t.addTaken(ref.Segs(), rec.owner)
		}
	}
	if loaded {
		for key, rec := range recs {
			add(key, rec)
		}
	} else {
		mirror := []string{ViffRoot, PermsSystem, PermsKind}
		ents, err := ListAtErr(a.Store, mirror, len(mirror)+1, nil)
		if err != nil {
			return nil, err
		}
		root := a.attr.RootChildGrants()
		for _, e := range ents {
			key := e.Ref.Name()
			add(key, parseRecord(key, e.Payload, root))
		}
	}
	t.count()
	if loaded {
		a.attr.mu.Lock()
		if a.attr.takenGen == gen {
			a.attr.takenAt = t
		}
		a.attr.mu.Unlock()
	}
	return t, nil
}

// dropTaken forgets the fence's trie because a record moved. The caller holds the lock.
func (a *Attribution) dropTaken() {
	a.takenAt, a.placesAt = nil, nil
	a.takenGen++
}

// created is the nodes ABOVE ref that a write at ref is about to bring into being and that its
// writer holds `owner` at, topmost first — where a creation's owner record may be written. The
// writer made those nodes and everything under them, and ownership is fixed when a node is
// created; the topmost is where the record goes, and the rest are where it goes if somebody
// else's lands there first (Write).
//
// ASKED BEFORE THE WRITE, once, and only where a record would be written at all — the writer
// holds `owner` and the owner above is somebody else. A source reflecting beneath the claim node
// it installed never reaches here: its owner is the one on that record, and record's own
// shortcut answers without a read. So this is a walk per NEW BRANCH, not per entity.
//
// The walk goes UP from the node being written and stops at the first ancestor that fails any
// of three questions, each of which is a way of not being this write's to own:
//
//   - IT IS TAKEN. A record stands there, which `viff perms` writes without an entity.
//   - THE WRITER DOES NOT HOLD `owner` THERE. Holding it at the row says nothing about the node
//     above: a grant of what is BENEATH `sandbox` is not a grant of `sandbox`, and recording the
//     first writer beneath it as its owner handed them a space they were only invited into —
//     and shut everybody else out of it, since a grant does not reach through an owner record.
//     The check is the whole check, so the fence is asked with it.
//   - IT STANDS ALREADY, asked without reading it (StandsAt): the ancestor that ends the walk is
//     as often as not a container with everything in it.
func (a *attributed) created(ref Ref, principal, who string) ([]Ref, error) {
	segs := ref.Segs()
	if len(segs) < 2 {
		return nil, nil // the node itself is the top; record lands where it always would
	}
	// NOT IN VIFF'S HALF, where the structure is the system's rather than anybody's
	// contribution: the kinds under `viff` are seeded, the rows beneath them are held out by
	// the seed, and a task's record belongs at the task. Spawning the first one would otherwise
	// make its requester the owner of `mirror.task` — and of every task anybody ran after.
	if ref.Reserved() {
		return nil, nil
	}
	a.attr.mu.Lock()
	_, decided := a.attr.decided[ref]
	a.attr.mu.Unlock()
	if decided {
		return nil, nil // this process has settled this node already
	}
	above, err := a.ownerAbove(ref)
	if err != nil {
		return nil, err
	}
	if who == above || IsSystem(who) {
		return nil, nil // no record is coming, so nothing to place
	}
	var made []Ref
	for i := len(segs) - 1; i >= 1; i-- {
		at := RefAt(segs[:i]...)
		rec, err := a.recordAt(at.String(), false)
		if err != nil {
			return nil, err
		}
		if rec != nil {
			break
		}
		acc, err := a.accessFor(principal, at, true)
		if err != nil {
			return nil, err
		}
		if acc.level != OwnerLevel {
			break
		}
		stands, err := a.Store.StandsAt(at.Segs())
		if err != nil {
			return nil, fmt.Errorf("asking whether %s stands, before recording its owner: %w", at, err)
		}
		if stands {
			break
		}
		made = append([]Ref{at}, made...)
	}
	return made, nil
}
