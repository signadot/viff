package entity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/system/libctl"
	logdapi "github.com/signadot/tony-format/go-tony/system/logd/api"
	"github.com/signadot/tony-format/go-tony/token"
)

// DefaultRoot is the top-level kinded-path segment the verse lives under in logd.
// Log-backed entities are stored at <root>.<system>.<kind>.<id>, so a single watch on
// the root observes the whole (historied) verse.
const DefaultRoot = "verse"

// MountRoot is the top-level segment CONTROLLER-MOUNTED slices live under, kept
// disjoint from DefaultRoot — and that disjointness is load-bearing, for one reason:
//
// A mount is process-driven, so it has NO HISTORY. A controller backs its subtree with
// something that is not the log (a filesystem, an API), and across a mount/unmount there
// is no durable commit sequence to replay; a self-backed controller has no past to ask
// for at all (libctl.Handler says such a controller "should reject a historical read
// rather than answer from an uncoordinated timeline"). So history belongs to the
// log-backed base, and a history-bearing watch must never span a mount.
//
// docd enforces nothing here — it composes a watch whose path is a strict ANCESTOR of a
// mount, and a composed watch silently drops FromCommit and never emits ReplayComplete.
// With mounts under DefaultRoot, the root watch became composed the moment a `verse
// connect` content controller appeared, and Since (a FromCommit replay on the root)
// returned ok=true with ZERO events after running to its internal timeout — a silent,
// successful-looking under-delivery of the engine's catch-up. Mount/unmount are also
// coordinator WRITERS, so the mount additionally blocked ~5s on the overlapping root
// watch before force-ending it.
//
// Holding mounts on a sibling root removes both: "verse" is not a field-prefix of
// "versemnt", so no watch is ever an ancestor of a mount and nothing overlaps. One root
// watch, no subtree enumeration, Since untouched. See entity/composedwatch_test.go and
// docs/trigger-model.md.
//
// This is a PATH-RENDERING split only: the entity namespace is unchanged, so a mounted
// slice is still addressed as the ordinary ref local:dir-content-<scope>:<path>.
const MountRoot = DefaultRoot + "mnt"

// mountedKinds are the (system, kind-prefix) slices served by a controller mount rather
// than by the log, so refPath renders them under MountRoot. Kept here — not derived from
// package source, which imports this package — and held in step by
// source.TestFederationKindsAreMountOwned.
//
// Only the content slice is mounted today: `verse connect` serves file bytes on demand at
// local:dir-content-<scope> and stores nothing. Its sibling local:dir-status-<scope> is
// ordinary log-backed metadata and stays under DefaultRoot, which is what keeps the
// trigger that matches it watchable.
var mountedKinds = []struct{ System, KindPrefix string }{
	{"local", "dir-content-"},
}

// IsMountOwned reports whether a (system, kind) slice is served by a controller mount,
// and so lives under MountRoot with no history.
func IsMountOwned(system, kind string) bool {
	for _, m := range mountedKinds {
		if m.System == system && strings.HasPrefix(kind, m.KindPrefix) {
			return true
		}
	}
	return false
}

// observer is one live subscriber: a buffered channel plus the count of events dropped
// while it was full, flushed to it as a gap marker before its next delivery so a drop is
// never silent (the consumer recovers the gap via Since).
type observer struct {
	ch      chan Event
	dropped int
}

// LogdStore is an entity.Store backed by a logd document server (tony-format), reached
// through the embedded libctl client — the durable, observable substrate the whole verse
// runs on. Entities map to kinded paths (<root>.<system>.<kind>.<id>); reads are Match,
// writes are Patch, and the change stream + revision cursor come from a single logd Watch
// on the root.
//
// Revisions here are logd commit numbers: monotonic but *sparse* — a commit may jump by
// more than one, and other writers advance it. Consumers must treat Rev as an opaque
// monotonic cursor and reconcile via Since — which the engine does. Since is served
// natively by a FromCommit replay watch (a query by revision), so a hold costs a cursor,
// not a buffer.
type LogdStore struct {
	root string
	// sess multiplexes everything: reads, writes, the live root watch, per-trigger
	// subtree watches, and Since's FromCommit replays. One session suffices because logd
	// and docd key each watch by its request id, so a session may hold several
	// concurrent watches — including several on the SAME path (go-tony ≥ v0.0.85;
	// libctl.Watch always stamps an id). Before that a path admitted one watch per
	// session, which is why this used to need a second "-replay" session and a mutex
	// serializing replays against it.
	sess   *libctl.LogdSession
	ctx    context.Context
	cancel context.CancelFunc

	cfg LogdConfig // retained so Scope() can clone it for a scoped child

	mu          sync.Mutex
	rev         int64 // highest logd commit observed (by the lazy root watch, when running)
	openCommit  int64 // commit at which this store first synced — the Since low-watermark
	subs        map[int]*observer
	nextID      int
	closed      bool
	synced      bool               // openCommit has been fixed (by ensureSynced or the lazy watch)
	watchCancel context.CancelFunc // cancels the lazy root watch; nil when no Observe subscribers
	// doc is the watched document as of the last applied commit — seeded by the root
	// watch's State and advanced by each delta. It is what an event's level payload is read
	// out of (see eventsFromDelta); nil until a watch has run.
	doc *ir.Node

	scopesMu sync.Mutex
	scopes   map[string]*LogdStore // lazily-created scoped children, keyed by scope name
}

// LogdConfig configures a LogdStore.
type LogdConfig struct {
	Addr     string // logd address, e.g. "localhost:9091"
	ClientID string // identifies this client to logd
	Root     string // top-level path segment; defaults to DefaultRoot ("verse")
	// Scope selects a copy-on-write scope: reads see the scope overlaid on baseline
	// and writes land in the scope, isolated from baseline. Empty means baseline
	// (the only kind that may DeleteScope). Usually set via Scope(), not directly.
	Scope string
	// ConnectTimeout bounds the initial session connect; 0 uses the default. A caller that
	// retries (waiting for logd/docd to come up) sets this short so each attempt fails fast.
	ConnectTimeout time.Duration
}

// OpenLogdStore connects to logd and returns a ready store. It opens NO watch: reads
// (Get/List/Modify) are on-demand Matches, per-trigger observation is WatchSubtree,
// and the whole-verse root watch behind Observe is opened lazily only while observed
// (see Observe). This is what keeps the store from holding a root watch that a
// controller mount under verse would force-end.
func OpenLogdStore(cfg LogdConfig) (*LogdStore, error) {
	root := cfg.Root
	if root == "" {
		root = DefaultRoot
	}
	cfg.Root = root // retain the resolved root so scoped children inherit it
	ctx, cancel := context.WithCancel(context.Background())

	connectTimeout := cfg.ConnectTimeout
	if connectTimeout == 0 {
		connectTimeout = 10 * time.Second
	}
	dial := func(suffix string) (*libctl.LogdSession, error) {
		sess := libctl.NewLogdSession(&libctl.LogdSessionConfig{
			Addr:     cfg.Addr,
			ClientID: cfg.ClientID + suffix,
			Scope:    cfg.Scope, // "" = baseline; non-empty = COW scope for every op
		})
		cctx, ccancel := context.WithTimeout(ctx, connectTimeout)
		defer ccancel()
		if err := sess.Connect(cctx); err != nil {
			return nil, err
		}
		return sess, nil
	}

	sess, err := dial("")
	if err != nil {
		cancel()
		return nil, err
	}

	s := &LogdStore{
		root: root, cfg: cfg, sess: sess, ctx: ctx, cancel: cancel,
		subs: map[int]*observer{},
	}

	// No standing root watch. The store serves Get/List/Modify on demand (single-ref
	// and subtree Matches) and per-trigger observation via WatchSubtree — so nothing
	// here holds a whole-verse watch that a controller mount under verse would overlap
	// and force-end. A root watch is opened lazily, and only while something Observe()s
	// (the /watch debug stream); see Observe. openCommit stays 0 (the Since
	// low-watermark) and rev is seeded by that lazy watch when it runs.
	return s, nil
}

// --- path + payload mapping ---

// refPath renders an entity ref as a kinded path <root>.<system>.<kind>.<id>, with
// each segment quoted as needed so arbitrary ids (e.g. "signadot/x#42") stay a
// single field. Note: kpaths are '.'-delimited; '/' is a literal character.
//
// A mount-owned slice renders under MountRoot instead, so no watch on this store's root
// is ever an ancestor of a mount (see MountRoot). The ref itself is unchanged — this is
// the one place the two roots are chosen between, and reads/writes route by the rendered
// path, so every caller keeps addressing a mounted slice as an ordinary ref.
func (s *LogdStore) refPath(ref Ref) string {
	return s.rootFor(ref) + "." + seg(ref.System) + "." + seg(ref.Kind) + "." + seg(ref.ID)
}

// rootFor picks the top-level segment a ref renders under: MountRoot for a
// controller-mounted (historyless) slice, this store's root otherwise.
func (s *LogdStore) rootFor(ref Ref) string {
	if IsMountOwned(ref.System, ref.Kind) {
		return seg(MountRoot)
	}
	return seg(s.root)
}

// MountKPath renders the kinded-path <MountRoot>.<system>.<kind> at which a controller
// mounts to own that (system,kind) slice — e.g. verse connect's content controller
// mounts the local:dir-content-<scope> slice. A controller process holds no store, so
// this lets it and the store agree on the path.
//
// It renders under MountRoot, disjoint from the entity root, so a mount never sits below
// a watched path (see MountRoot). A (system,kind) passed here must therefore also satisfy
// IsMountOwned, or the store would render its refs under the other root and reads would
// miss the mount — MountKPath panics rather than let the two drift silently.
func MountKPath(system, kind string) string {
	if !IsMountOwned(system, kind) {
		panic("entity.MountKPath: " + system + ":" + kind + " is not a registered mount-owned slice " +
			"(add it to mountedKinds, else refPath will render it under the entity root and miss the mount)")
	}
	return seg(MountRoot) + "." + seg(system) + "." + seg(kind)
}

func seg(v string) string {
	if token.KPathQuoteField(v) {
		return token.Quote(v, true)
	}
	return v
}

// fieldName is the inverse of seg for the read path: logd renders a field name that
// required quoting (e.g. a numeric-like id "1", or "signadot/x#42") in its quoted
// display form inside watch/state nodes, so unquote it back to the raw segment.
func fieldName(f *ir.Node) string {
	s := f.String
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return token.QuotedToString([]byte(s))
	}
	return s
}

// payloadToNode is the write face of a payload. A payload IS a node now, so this only
// settles the absent case: a nil payload becomes an EMPTY OBJECT (a present entity with
// no fields), never null, which is how an absent entity is spelled. It is cloned because
// the node is about to be parented into a patch document.
func payloadToNode(p Payload) *ir.Node {
	if p == nil {
		return ir.FromMap(map[string]*ir.Node{})
	}
	return clone(p)
}

// nodeToPayload is the read face. A node is already a payload; the only work left is
// dropping null-valued FIELDS of an object — logd's tombstones — so callers see only what
// is present. A scalar payload has no fields and passes through untouched; a null one is an
// absent entity and reads as nil.
//
// It recurses, because a tombstone can be at any depth now that a write can be: removing
// `labels.urgent` leaves a null AT that key, and a reader shown `labels: {urgent: null}`
// would say the label is still there, holding nothing. Arrays are left alone — a null in a
// list is a position, not an absence.
func nodeToPayload(node *ir.Node) Payload {
	if node == nil || node.Type == ir.NullType {
		return nil
	}
	if node.Type != ir.ObjectType {
		return node
	}
	live := map[string]*ir.Node{}
	for k, v := range ir.ToMap(node) {
		if p := nodeToPayload(v); p != nil {
			live[k] = p
		}
	}
	return ir.FromMap(live)
}

// addRemovedNulls makes a Patch replace rather than merge: for every field present
// in the old value but absent from the new payload, it appends an explicit null so
// logd tombstones it. This is what gives Put its whole-value replace semantics.
//
// It applies only when BOTH sides are objects, which is the only case where a merge could
// leave a stale field behind. A scalar (or array) new value replaces the old outright —
// there are no fields to merge into and so nothing to tombstone.
func addRemovedNulls(node, old *ir.Node, p Payload) {
	if old == nil || old.Type != ir.ObjectType || node == nil || node.Type != ir.ObjectType {
		return
	}
	fields := Fields(p)
	for _, f := range old.Fields {
		k := fieldName(f) // old came from Match: keys needing quoting arrive quoted
		if _, ok := fields[k]; !ok {
			node.Fields = append(node.Fields, ir.FromString(k))
			node.Values = append(node.Values, ir.Null())
		}
	}
}

// --- Store: reads and writes ---

// Get returns an entity by ref. An absent (or deleted) path reads back as null.
// Get reads one entity. A failed READ is reported as an error, never as absence: the
// read crosses a network and may traverse a mount owned by another process, so "I could
// not ask" and "it is not there" are genuinely different answers and the caller has to be
// able to tell them apart. A null node IS absence — that is how the log spells a tombstone.
func (s *LogdStore) Get(ref Ref) (Entity, bool, error) {
	ctx, cancel := s.reqCtx()
	defer cancel()
	node, err := s.sess.Match(ctx, s.refPath(ref))
	if isNotFound(err) {
		return Entity{}, false, nil // nothing stands there — an answer, not a failure
	}
	if err != nil {
		return Entity{}, false, fmt.Errorf("read %s: %w", ref, err)
	}
	if node == nil || node.Type == ir.NullType {
		return Entity{}, false, nil
	}
	return Entity{Ref: ref, Payload: nodeToPayload(node)}, true, nil
}

// isNotFound reports whether a read error is the substrate saying "nothing stands there"
// rather than "I could not look" — the distinction Get exists to preserve.
//
// It has to ask the MESSAGE, which is unpleasant and deliberately narrow. logd answers a
// read of a path that does not exist with an error, not a null, and libctl renders the
// server's typed code (api.ErrCodeNotFound) into an opaque string on the way out, so the
// code that would settle this never reaches us. Until it does, this is the discriminator.
//
// Getting it wrong in the other direction is what makes it worth the ugliness: before this,
// every absent entity whose id, kind, or system had never been written came back as a read
// FAILURE — so `GET /entity` for anything absent answered 502 on any verse that had ever
// been written to, and an agent asking after a file was told the store was unreachable.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "path not found")
}

// Write is THE write: value merges in at one path iff every precondition holds, in a single
// commit. Every precondition is folded into ONE root-anchored match issued with the patch
// (docd allows a single match path), so the checks and the write are one step at the server —
// no window for a revoke to slip between them. A failed precondition surfaces as
// ErrMatchFailed → (false, nil): the world said no, which is not an error.
//
// It does not read first. A merge is what logd's patch already does, and a nil value is a
// tombstone, so the only thing a read could add is "which fields did the caller drop" — a
// question the caller can answer without a round trip. That read is the reason a write used
// to cost the same as a read (see go-tony bvm163tyh12krwcqcsn0), which on a growing log is
// most of what a write cost.
//
// There is no commit number in the result because libctl discards it (PatchResult.Commit is
// dropped by doPatch), and a fabricated one is worse than none.
func (s *LogdStore) Write(w Write) (bool, error) {
	ctx, cancel := s.reqCtx()
	defer cancel()
	path := s.writePath(w.At)
	node := payloadToNode(w.Value)
	if w.Value == nil {
		node = ir.Null() // remove whatever stands there
	}
	if len(w.When) == 0 {
		if _, err := s.sess.Patch(ctx, path, node); err != nil {
			return false, fmt.Errorf("write %s: %w", w.At, err)
		}
		return true, nil
	}
	pre := make([]pathPattern, 0, len(w.When))
	for _, r := range w.When {
		pre = append(pre, pathPattern{at: r.path(w), pattern: r.Pattern})
	}
	_, err := s.sess.PatchIf(ctx, path, node, &logdapi.PathData{Path: seg(s.root), Data: rootPattern(pre)})
	if errors.Is(err, libctl.ErrMatchFailed) {
		return false, nil // a precondition did not hold — a revoke, or already committed
	}
	if err != nil {
		return false, fmt.Errorf("write %s: %w", w.At, err)
	}
	return true, nil
}

// writePath renders a path as a kpath: the entity's path, then a segment per field.
func (s *LogdStore) writePath(at Path) string {
	path := s.refPath(at.Ref)
	for _, f := range at.Field {
		path += "." + seg(f)
	}
	return path
}

// replacing returns payload with an explicit null for every field the stored value has and
// the payload does not — what turns a merge into a whole-value replace. It costs a READ, and
// it is the only thing in the store that does; it exists because Put/Modify/CommitIf promise
// replace semantics to callers written against them. A caller that says which fields to drop
// (by writing nil at them) needs none of this.
func (s *LogdStore) replacing(ref Ref, payload Payload) Payload {
	ctx, cancel := s.reqCtx()
	defer cancel()
	node := payloadToNode(payload)
	if old, err := s.sess.Match(ctx, s.refPath(ref)); err == nil {
		addRemovedNulls(node, old, payload)
	}
	return node
}

// rootPattern folds every precondition into ONE match against the root document, which is
// what keeps the checks atomic with the write (docd allows a single match path).
//
// Presence preconditions nest into a single tree, as they always have. An ABSENCE
// precondition cannot: there is no key at its path to carry a pattern, so it becomes its own
// negated branch — "the document does not have run.claim.<id>" — and the branches are ANDed.
func rootPattern(pre []pathPattern) *ir.Node {
	var branches []*ir.Node
	present, any := patTree{}, false
	for _, p := range pre {
		if IsAbsent(p.pattern) {
			branches = append(branches, absentPath(p.at))
			continue
		}
		nestPattern(present, p.at, p.pattern)
		any = true
	}
	if any {
		branches = append(branches, present.node())
	}
	switch len(branches) {
	case 0:
		return ir.FromMap(map[string]*ir.Node{})
	case 1:
		return branches[0]
	default:
		return ir.FromSlice(branches).WithTag("!and")
	}
}

// pathPattern is one precondition reduced to what the match needs: a place and a pattern.
type pathPattern struct {
	at      Path
	pattern Payload
}

// absentPath renders "nothing stands at ref" — which over docd is TWO shapes, not one.
// A ref that was never written has no key at its path, so absence is a negated presence
// (a null pattern is the presence test). A ref that was DELETED still has its key: logd
// tombstones with a null leaf, and the tombstone is what makes the delete replayable. So
// the two are ORed, and a claim released by Delete can be taken again — without the
// second branch, releaseRun would free a claim that claimRun could never re-win, and a
// sequence would run exactly once per subject for the lifetime of the document.
func absentPath(at Path) *ir.Node {
	noKey, tombstone := patTree{}, patTree{}
	nestPattern(noKey, at, ir.Null())    // negated below: no key at the path at all
	nestPattern(tombstone, at, Absent()) // a key holding null: deleted
	return ir.FromSlice([]*ir.Node{noKey.node().WithTag("!not"), tombstone.node()}).WithTag("!or")
}

// nestPattern places pattern at m[system][kind][id][field...], building intermediate maps —
// the nested shape docd's root document has (<root>.<system>.<kind>.<id>.<field>…). Raw
// segment names are used because those are the field names docd stores; seg-quoting is only
// for kpath request strings, not for stored fields. A trailing segment left empty ends the
// nesting there, so a precondition can be about a whole kind subtree (a motion's vote log)
// or about one field inside one entity.
func nestPattern(root patTree, at Path, pattern Payload) {
	segs := at.segments()
	if len(segs) == 0 {
		return
	}
	t := root
	for _, seg := range segs[:len(segs)-1] {
		t = t.child(seg)
	}
	t[segs[len(segs)-1]] = pattern
}

// segments is the path as document field names: system, kind, id, then each field, stopping
// at the first empty one. A ref with no ID addresses a whole kind subtree (a motion's vote
// log), which is why the tail may be short rather than invalid.
func (p Path) segments() []string {
	out := make([]string, 0, 3+len(p.Field))
	for _, seg := range append([]string{p.System, p.Kind, p.ID}, p.Field...) {
		if seg == "" {
			return out
		}
		out = append(out, seg)
	}
	return out
}

// patTree is the nesting under construction: branches are patTrees, LEAVES ARE NODES. It is
// not a map[string]any converted at the end, because that conversion is what used to flatten
// a pattern — an Absent(), a scalar, an operator — into something gomap could represent, i.e.
// into nothing. Keeping leaves as nodes is the whole point of the widening.
type patTree map[string]any

func (t patTree) child(k string) patTree {
	if c, ok := t[k].(patTree); ok {
		return c
	}
	c := patTree{}
	t[k] = c
	return c
}

// node renders the tree, leaving node leaves exactly as they were given.
func (t patTree) node() *ir.Node {
	m := make(map[string]*ir.Node, len(t))
	for k, v := range t {
		switch x := v.(type) {
		case patTree:
			m[k] = x.node()
		case *ir.Node:
			m[k] = x
		}
	}
	return ir.FromMap(m)
}

// Rev returns the highest logd commit observed (an opaque monotonic cursor). With no
// standing root watch, a store that is only written/read (never Observed) has no live
// rev, so this seeds it once from a transient current-state read.
func (s *LogdStore) Rev() int64 {
	s.ensureSynced()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev
}

// ensureSynced brings rev up to the current commit from a transient current-state read,
// for the cursor consumers (Rev's value, Since's watermark) on a store with no lazy root
// watch running. It holds NO standing watch — it opens one, reads the initial State
// commit, and closes it — so it never becomes a long-lived root watch. It needs no
// serialization against a concurrent Observe or Since on the same path: each watch is
// keyed by its own request id (see LogdStore.sess).
//
// It re-reads on every call, because nothing else advances rev on a store nobody
// Observes: a write goes straight to logd and the authoritative commit comes back on a
// watch stream that isn't running, so a once-only seed would pin Rev to whatever the
// commit was when the store opened (a /status reporting storeRev 0 after a hundred
// writes). openCommit — this instance's Since low-watermark — IS once-only: it is a
// floor, and raising it later would tell a live consumer its valid cursor had aged out.
func (s *LogdStore) ensureSynced() {
	s.mu.Lock()
	watching := s.watchCancel != nil
	s.mu.Unlock()
	if watching {
		return // the root watch is live: rev is already current
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	w, err := s.sess.Watch(ctx, s.root, &libctl.WatchOptions{})
	if err != nil {
		return
	}
	defer w.Close()
	select {
	case ev, ok := <-w.Events():
		if ok {
			s.handleEvent(ev) // seeds rev from the State commit (emits nothing)
		}
	case <-ctx.Done():
	}
	s.mu.Lock()
	if !s.synced {
		s.synced = true
		s.openCommit = s.rev // this instance's low-watermark for Since, fixed at first sync
	}
	s.mu.Unlock()
}

// List enumerates entities under the root whose ref satisfies pred (nil = all) via
// a single Match on the root subtree. Tombstoned (deleted) entities are skipped.
// This is the Lister convenience, not part of the core Store interface.
func (s *LogdStore) List(pred func(Ref) bool) []Entity {
	ctx, cancel := s.reqCtx()
	defer cancel()
	node, err := s.sess.Match(ctx, seg(s.root))
	if err != nil || node == nil {
		return nil
	}
	var out []Entity
	for _, ch := range walkEntities(node) {
		if ch.deleted {
			continue
		}
		if pred == nil || pred(ch.ref) {
			out = append(out, Entity{Ref: ch.ref, Payload: ch.payload})
		}
	}
	return out
}

// WatchSubtree observes one (system,kind) slice — the kinded subtree
// <root>.<system>.<kind> — or, when kind is "", a whole system (<root>.<system>, all
// its kinds), streaming a change Event per entity and carrying this store's scope. This
// is the per-trigger observation an engine uses: it watches precisely the subtree its
// trigger matches, rather than the whole verse (docd routes a subtree watch at/below
// its path; a root watch is out of scope by design). A system-level watch is the
// broadest a trigger may take — a truly global (system-less) watch is disallowed.
//
// It maintains the subtree as a document by APPLYING each watch event — a State
// replaces it, a Patch is applied via tony.Patch — and emits the diff of the
// reconstructed entity set vs the last snapshot. It never interprets the patch's
// structure: logd owns the diff encoding (a scoped watch's first delta is a !replace,
// later ones are merges; either applies cleanly), so verse can't and shouldn't decode
// it by hand — doing so silently dropped the first appearance. A burst of events is
// applied then diffed once. unsub (or closing the store) stops it.
func (s *LogdStore) WatchSubtree(system, kind string) (<-chan Event, func(), error) {
	path := seg(s.root) + "." + seg(system)
	if kind != "" {
		path += "." + seg(kind)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	w, err := s.sess.Watch(ctx, path, &libctl.WatchOptions{})
	if err != nil {
		cancel()
		return nil, nil, err
	}
	out := make(chan Event, 256)
	go func() {
		defer close(out)
		// doc is the watched subtree as a root-rooted document, maintained by APPLYING
		// each watch event: a State (initial or reconnect) replaces it; a Patch is
		// applied with tony.Patch. We never interpret the patch's structure — logd owns
		// the diff encoding (merge vs !replace vs !delete/!insert, field order, …) — we
		// apply it opaquely and read entities out of the reconstructed object. prev is
		// the last emitted snapshot.
		doc := ir.Null()
		prev := map[Ref]Payload{}
		send := func(e Event) bool {
			select {
			case out <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}
		apply := func(ev *logdapi.WatchEvent) {
			switch {
			case ev.State != nil:
				doc = ev.State // full current state of the subtree (root-rooted)
			case ev.Patch != nil:
				if nd, err := tony.Patch(doc, ev.Patch); err == nil {
					doc = nd
				}
				// A patch that fails to apply leaves doc unchanged; a later State (e.g.
				// on reconnect) resyncs it. We never decode the patch to recover by hand.
			}
		}
		// emitDiff reconstructs the entity set from doc and emits the diff vs the last
		// snapshot. Returns false if the consumer went away (ctx done) mid-send.
		emitDiff := func() bool {
			cur := entitiesFromDoc(doc, s.root, system, kind)
			for ref, p := range cur {
				if old, ok := prev[ref]; !ok || !reflect.DeepEqual(old, p) {
					if !send(Event{Ref: ref, Op: Set, To: p}) {
						return false
					}
				}
			}
			for ref := range prev {
				if _, ok := cur[ref]; !ok {
					if !send(Event{Ref: ref, Op: Delete}) {
						return false
					}
				}
			}
			prev = cur
			return true
		}
		backoff := 100 * time.Millisecond
		for {
			// Drain the current watch until it ends. A clean end is a force-end
			// (a mount/unmount overlapping this subtree changed membership →
			// WatchEndedError "membership_changed") or a dropped connection — NOT a
			// terminal condition. We re-establish below rather than returning.
		pump:
			for {
				select {
				case <-ctx.Done():
					w.Close()
					return
				case ev, ok := <-w.Events():
					if !ok {
						break pump // watch ended; re-establish
					}
					if ev.ReplayComplete {
						continue // live watch: no replay phase to complete
					}
					apply(ev)
					// Coalesce a burst: apply any already-queued events too (each must be
					// applied to keep doc consistent), then emit one diff — a rapid N-file
					// connect costs one diff, not N.
					closed := false
				drain:
					for {
						select {
						case ev2, ok2 := <-w.Events():
							if !ok2 {
								closed = true
								break drain
							}
							if !ev2.ReplayComplete {
								apply(ev2)
							}
						default:
							break drain
						}
					}
					if !emitDiff() {
						w.Close()
						return
					}
					if closed {
						break pump // watch ended mid-burst; re-establish
					}
				}
			}
			// The watch ended (force-end or drop). Re-establish on the same path — it
			// re-composes against the CURRENT mount set — with backoff. The fresh
			// State event replaces doc with current truth, and emitDiff closes any gap
			// against the last snapshot.
			w.Close()
			if ctx.Err() != nil {
				return
			}
			for {
				nw, err := s.sess.Watch(ctx, path, &libctl.WatchOptions{})
				if err == nil {
					w = nw
					backoff = 100 * time.Millisecond
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, 5*time.Second)
			}
		}
	}()
	return out, cancel, nil
}

// entitiesFromDoc reads the entity set out of a root-rooted subtree document (the doc
// WatchSubtree maintains by applying watch events), keyed by full Ref. It navigates to
// the watched (system[,kind]) node and walks the remaining depth to the entity leaves —
// pure structure, no diff interpretation.
func entitiesFromDoc(doc *ir.Node, root, system, kind string) map[Ref]Payload {
	node := childByKey(childByKey(doc, root), system)
	depth := 2 // kind, id
	if kind != "" {
		node = childByKey(node, kind)
		depth = 1 // id
	}
	out := map[Ref]Payload{}
	if node == nil || node.Type != ir.ObjectType {
		return out
	}
	walkLeaves(node, depth, func(segs []string, v *ir.Node) {
		if v == nil || v.Type == ir.NullType {
			return
		}
		out[refAt(system, kind, segs)] = nodeToPayload(v)
	})
	return out
}

// refAt builds the entity Ref for a leaf found `depth` levels below the watched node:
// with kind set the single seg is the id; for a whole-system watch the two segs are
// kind then id.
func refAt(system, kind string, segs []string) Ref {
	if kind != "" {
		return Ref{System: system, Kind: kind, ID: segs[0]}
	}
	return Ref{System: system, Kind: segs[0], ID: segs[1]}
}

// walkLeaves descends exactly `depth` object levels from node, invoking fn with the
// field-name path and value node at each leaf. It underpins both the (system,kind)
// (depth 1) and whole-system (depth 2) subtree reads.
func walkLeaves(node *ir.Node, depth int, fn func(segs []string, v *ir.Node)) {
	var rec func(n *ir.Node, segs []string)
	rec = func(n *ir.Node, segs []string) {
		if len(segs) == depth {
			fn(segs, n)
			return
		}
		if n == nil || n.Type != ir.ObjectType {
			return
		}
		for i, f := range n.Fields {
			rec(n.Values[i], append(append([]string{}, segs...), fieldName(f)))
		}
	}
	rec(node, nil)
}

// Scope returns a copy-on-write view isolated to the named scope: writes land in
// the scope, and reads/observe/Since see the scope overlaid on baseline. The child
// is a full LogdStore whose sessions carry the scope; it is created lazily and
// cached, since callers ask for a scope per request and each child holds live
// sessions and a watch. An empty name (or this store's own scope) returns self.
//
// On a failure to establish the scoped sessions, this degrades to the baseline
// view rather than failing the request path (the Store interface has no error
// channel here); such a write would land in baseline, visible via Get.
func (s *LogdStore) Scope(name string) Store {
	if name == "" || name == s.cfg.Scope {
		return s
	}
	s.scopesMu.Lock()
	defer s.scopesMu.Unlock()
	if s.scopes == nil {
		s.scopes = map[string]*LogdStore{}
	}
	if child, ok := s.scopes[name]; ok {
		return child
	}
	childCfg := s.cfg
	childCfg.Scope = name
	childCfg.ClientID = s.cfg.ClientID + "-scope-" + name
	child, err := OpenLogdStore(childCfg)
	if err != nil {
		return s // degrade to baseline; cannot isolate right now
	}
	s.scopes[name] = child
	return child
}

// DeleteScope drops a scope and all of its copy-on-write data, and tears down any
// cached child for it. It must be called on a baseline store (empty Scope); logd
// rejects the request from a scoped session.
func (s *LogdStore) DeleteScope(name string) error {
	if s.cfg.Scope != "" {
		return errBaselineOnly
	}
	s.scopesMu.Lock()
	child := s.scopes[name]
	delete(s.scopes, name)
	s.scopesMu.Unlock()
	if child != nil {
		child.Close() // stop the child's watch + sessions before the data goes away
	}
	ctx, cancel := s.reqCtx()
	defer cancel()
	return s.sess.DeleteScope(ctx, name)
}

var errBaselineOnly = errString("DeleteScope must be called on a baseline (unscoped) store")

type errString string

func (e errString) Error() string { return string(e) }

// --- Store: observation ---

// Observe registers a live observer of the change stream. The whole-verse root watch
// that feeds it is opened LAZILY on the first observer and torn down when the last one
// leaves — so a store with no observer (the common case: engines observe per-trigger
// subtrees, not the root) holds no root watch to collide with a controller mount. Like
// the contract says, it is a low-latency hint that drops under backpressure; correctness comes
// from the cursor + Since. Today the only observer is the /watch debug stream (decision:
// an active whole-verse watch is transient and retries on a membership_changed force-end).
func (s *LogdStore) Observe() (<-chan Event, func()) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	o := &observer{ch: make(chan Event, 256)}
	s.subs[id] = o
	if len(s.subs) == 1 && s.watchCancel == nil {
		s.startRootWatchLocked() // first observer: bring the live root watch up
	}
	s.mu.Unlock()
	return o.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(o.ch)
		}
		if len(s.subs) == 0 && s.watchCancel != nil {
			s.watchCancel() // last observer gone: no standing root watch
			s.watchCancel = nil
		}
	}
}

// startRootWatchLocked opens the lazy whole-verse watch and streams it in the
// background. Caller holds s.mu. The watch is bound to its own context so the last
// unsubscribe (Observe) cancels it; runWatch re-establishes it on a drop until then.
// rev is seeded asynchronously by the first State event (handleEvent).
func (s *LogdStore) startRootWatchLocked() {
	wctx, wcancel := context.WithCancel(s.ctx)
	w, err := s.sess.Watch(wctx, s.root, &libctl.WatchOptions{})
	if err != nil {
		wcancel()
		return // no observers get events until a later Observe retries the open
	}
	s.watchCancel = wcancel
	go s.runWatch(wctx, w)
}

// Since replays changes with Rev > from using a native logd FromCommit watch (a
// query by revision), returning them in commit order. It is a pure read — it derives
// each event's Op from the delta alone (a tombstone is Delete, any value is Set) and
// mutates no store state, so repeated or concurrent calls are consistent. This is
// what keeps a hold costing a cursor rather than a growing buffer.
//
// ok is false when the caller must resync from current state: the replay could not
// be established, or `from` predates this store's low-watermark (openCommit) — logd
// silently under-delivers a compacted range rather than erroring, so a cursor older
// than what this instance has observed cannot be served safely.
func (s *LogdStore) Since(from int64) ([]Event, bool) {
	s.ensureSynced() // seed the low-watermark for a store that isn't being Observed
	s.mu.Lock()
	low := s.openCommit
	s.mu.Unlock()
	if from < low {
		return nil, false // older than we've observed; logd may have compacted it
	}
	// No rev>=from early-out: without a standing watch rev may be stale, so let the
	// FromCommit replay itself be authoritative — it streams commits after `from` and
	// signals ReplayComplete when caught up (empty when there is nothing new).

	// Concurrent Since calls need no serialization: each replay is its own id-keyed watch,
	// so several may be in flight on this session at once, on this same path (see
	// LogdStore.sess). Since mutates no store state, so they stay independent.
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	fc := from
	w, err := s.sess.Watch(ctx, s.root, &libctl.WatchOptions{FromCommit: &fc, NoInit: true})
	if err != nil {
		return nil, false
	}
	defer w.Close()

	// A replay carries deltas, and an event owes its consumer the whole entity as of its
	// commit — so replay it the way the live watch does: read the document as it stood AT
	// `from`, then advance it one commit at a time. That is one read for the whole catch-up
	// rather than one per event, and it makes a replayed event indistinguishable from the
	// live one it may be recovering.
	doc := s.docAt(ctx, from)

	var out []Event
	for {
		select {
		case <-ctx.Done():
			return out, true // best-effort: return what replayed
		case ev, ok := <-w.Events():
			if !ok {
				return out, w.Err() == nil
			}
			if ev.ReplayComplete {
				return out, true // caught up to current
			}
			if ev.Patch != nil {
				doc = applyDelta(doc, ev.Patch)
				out = append(out, s.eventsFromDelta(ev.Commit, ev.Patch, doc)...)
			}
		}
	}
}

// docAt reads the whole watched document as it stood at commit, root key and all — the base
// a replay levels its deltas against.
//
// An unreadable base is an EMPTY document, not nil: the read fails when nothing has ever
// been written under the root, and there the replay itself carries every commit an entity
// has, so applying the deltas onto nothing reconstructs it exactly. (A base that fails for
// some other reason leaves the replay levelling only what it can see, which is what it had
// before any of this existed.)
func (s *LogdStore) docAt(ctx context.Context, commit int64) *ir.Node {
	node, err := s.sess.MatchAt(ctx, seg(s.root), commit)
	if err != nil || node == nil {
		return ir.FromMap(map[string]*ir.Node{})
	}
	return ir.FromMap(map[string]*ir.Node{s.root: node})
}

// runWatch consumes the live root watch, re-establishing it if the connection
// drops. On re-establish the fresh initial-state event re-seeds rev (no emit);
// missed deltas are recovered by consumers via Since.
func (s *LogdStore) runWatch(ctx context.Context, w *libctl.Watch) {
	for {
		for ev := range w.Events() {
			s.handleEvent(ev)
		}
		if s.isClosed() || ctx.Err() != nil {
			w.Close()
			return // store closed or last observer left (Observe cancelled ctx)
		}
		time.Sleep(500 * time.Millisecond)
		if s.isClosed() || ctx.Err() != nil {
			return
		}
		nw, err := s.sess.Watch(ctx, s.root, &libctl.WatchOptions{})
		if err != nil {
			continue // retry after the next backoff
		}
		w = nw
	}
}

// handleEvent processes one watch event: an initial/reconnect State only seeds rev
// (no emit — Observe is "from now"); a delta bumps rev and fans change events out to
// observers.
func (s *LogdStore) handleEvent(ev *logdapi.WatchEvent) {
	switch {
	case ev.ReplayComplete:
		return
	case ev.State != nil:
		s.mu.Lock()
		if ev.Commit > s.rev {
			s.rev = ev.Commit
		}
		// A State is rooted AT the watched path (its top-level fields are systems), while a
		// delta is rooted at the DOCUMENT (its top-level field is the root). Wrap the State
		// so both speak the same shape — otherwise the first delta grafts a second tree
		// beside the first and every level read misses.
		s.doc = ir.FromMap(map[string]*ir.Node{s.root: ev.State})
		s.mu.Unlock()
	case ev.Patch != nil:
		s.mu.Lock()
		s.doc = applyDelta(s.doc, ev.Patch)
		doc := s.doc
		s.mu.Unlock()
		for _, e := range s.eventsFromDelta(ev.Commit, ev.Patch, doc) {
			s.emit(e)
		}
	}
}

// eventsFromDelta turns a document-root-rooted delta into change events: a tombstone
// (null leaf) is Delete, any value is Set. Create-vs-update is not distinguished.
//
// To is the entity AS OF THIS COMMIT, not the delta — and that difference is the whole
// reason a field can be written on its own. A consumer asks "does the entity look like
// this?" (entity.Pattern.MatchEvent matches To), so a delta would answer a question nobody
// asked: a trigger reading two fields would stop matching the moment one of them was
// written by itself, though the entity satisfied it perfectly. That failure is silent —
// nothing errors, the trigger simply never fires — so the level state is the contract, and
// the store pays for it here rather than leaving every consumer to remember.
//
// The level state comes from doc, the watched document with this delta already applied, so
// an event reports the world as it was at ITS commit — a transient state that has since been
// overwritten is still dispatched. doc costs nothing: the watch delivers a State to seed it
// and a delta per commit to advance it, which is what a watch is. Reading the entity back
// per event would be exact too, and was the first shape this took, but it puts a network
// round trip per changed entity on the watch goroutine — the stream stalls behind its own
// bookkeeping, and on a log that has grown, each of those reads is not cheap (see go-tony
// bvm163tyh12krwcqcsn0). A nil doc falls back to the delta, which for a whole-entity write
// is the same value.
func (s *LogdStore) eventsFromDelta(commit int64, patch, doc *ir.Node) []Event {
	changes := walkEntities(childByKey(patch, s.root)) // strip the document root
	s.mu.Lock()
	if commit > s.rev {
		s.rev = commit
	}
	s.mu.Unlock()
	out := make([]Event, 0, len(changes))
	for _, ch := range changes {
		if ch.deleted {
			out = append(out, Event{Ref: ch.ref, Op: Delete, Rev: commit})
			continue
		}
		to := ch.payload
		if level := payloadIn(doc, s.root, ch.ref); level != nil {
			to = level
		}
		out = append(out, Event{Ref: ch.ref, Op: Set, To: to, Rev: commit})
	}
	return out
}

// payloadIn reads one entity's whole payload out of a root-rooted document, or nil if it
// does not stand there.
func payloadIn(doc *ir.Node, root string, ref Ref) Payload {
	n := childByKey(childByKey(childByKey(childByKey(doc, root), ref.System), ref.Kind), ref.ID)
	if n == nil || n.Type == ir.NullType {
		return nil
	}
	return nodeToPayload(n)
}

// applyDelta advances a watched document by one commit's patch. A patch that fails to apply
// leaves the document unchanged — a later State (a reconnect) resyncs it — because logd owns
// the diff encoding and decoding it by hand is how a delta gets silently dropped.
//
// With no document there is nothing to advance: a delta before the first State, or a base
// read that could not be served. It stays nil, and levelling falls back to the delta.
// (tony.Patch dereferences a nil document rather than erroring, so this is also the guard.)
func applyDelta(doc, patch *ir.Node) *ir.Node {
	if doc == nil {
		return nil
	}
	if nd, err := tony.Patch(doc, patch); err == nil {
		return nd
	}
	return doc
}

// emit broadcasts to observers, non-blocking with drop-and-gap accounting — the
// the contract Observe documents (drops are recovered by consumers via Since).
func (s *LogdStore) emit(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func (s *LogdStore) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Close stops the watch, closes observer channels, and closes both sessions.
func (s *LogdStore) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	subs := s.subs
	s.subs = map[int]*observer{}
	s.mu.Unlock()

	s.scopesMu.Lock()
	children := s.scopes
	s.scopes = nil
	s.scopesMu.Unlock()
	for _, child := range children {
		child.Close()
	}

	for _, o := range subs {
		close(o.ch)
	}
	s.cancel()
	if s.sess != nil {
		s.sess.Close()
	}
	return nil
}

func (s *LogdStore) reqCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.ctx, 10*time.Second)
}

// --- delta walking ---

// entityChange is one entity-level change extracted from a watch node.
type entityChange struct {
	ref     Ref
	payload Payload
	deleted bool
}

// childByKey returns the value node under key in an object node, or nil.
func childByKey(node *ir.Node, key string) *ir.Node {
	if node == nil || node.Type != ir.ObjectType {
		return nil
	}
	for i, f := range node.Fields {
		if fieldName(f) == key { // a root that needs quoting arrives quoted in the delta
			return node.Values[i]
		}
	}
	return nil
}

// walkEntities descends a node rooted at the verse subtree (its top-level fields
// are systems) exactly three object levels — system, kind, id — and yields each
// entity leaf. A null leaf is a delete (tombstone); any other value is the payload.
func walkEntities(node *ir.Node) []entityChange {
	var out []entityChange
	var rec func(n *ir.Node, segs []string)
	rec = func(n *ir.Node, segs []string) {
		if len(segs) == 3 {
			ref := Ref{System: segs[0], Kind: segs[1], ID: segs[2]}
			if n == nil || n.Type == ir.NullType {
				out = append(out, entityChange{ref: ref, deleted: true})
			} else {
				out = append(out, entityChange{ref: ref, payload: nodeToPayload(n)})
			}
			return
		}
		if n == nil || n.Type != ir.ObjectType {
			return
		}
		for i, f := range n.Fields {
			rec(n.Values[i], append(append([]string{}, segs...), fieldName(f)))
		}
	}
	rec(node, nil)
	return out
}

// compile-time check: LogdStore satisfies Store.
var _ Store = (*LogdStore)(nil)
