package entity

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// subtreeHealAfter and subtreeHealMax bound how long a subtree watch can be DEAD WITHOUT
// KNOWING IT before it re-establishes itself.
//
// A watch can end for reasons the client did not cause: logd drops a watcher whose buffer it
// cannot drain, because Broadcast runs on the committing goroutine and will not block on a slow
// consumer (server/watch.go, "fail it loudly"). Since go-tony v0.0.112 that arrives as a proper
// terminal watch event carrying a resume commit, so the ordinary path below — the events channel
// closing, and the re-watch that follows — learns about it promptly and this timer never fires.
//
// It exists for the case where nothing arrives to be learned from. Before v0.0.112 the failure
// was sent as an ERROR RESPONSE stamped with the watch's id, and libctl routes anything without
// an Event to its table of in-flight REQUESTS, where a watch id has not appeared since its own
// request completed — so it was logged as "dropping response with no matching request" and
// dropped. The watch was then ORPHANED: gone on the server, alive here, its channel silent
// forever, and a trigger over that slice quietly stopped firing with no error anywhere a charter
// author would see. Measured against a server of that vintage, a slice taking sustained writes
// stalled partway through and never moved again.
//
// That specific hole is closed upstream. This stays because the shape of it is not specific: a
// consumer that can only ever be told is a consumer that waits forever whenever the telling is
// what breaks. So the watch does not only wait — a whole interval of silence, from a slice that
// may merely be idle or from one whose watch is gone, is answered by re-establishing, whose
// State resyncs the document and whose diff against the last snapshot emits whatever was missed.
// The interval DOUBLES while nothing arrives, to subtreeHealMax, so an idle slice settles into a
// cheap heartbeat rather than paying a full State every interval; any event resets it.
//
// Vars, not consts, so a test can shorten them: the failure they answer takes thousands of
// writes to provoke, and waiting out the production interval to observe the cure would make
// the test useless.
var (
	subtreeHealAfter = 30 * time.Second
	subtreeHealMax   = 5 * time.Minute
)

// sessionBudget is how long ONE call on the session may take: a read, a write, or an
// attempt to establish a watch. There is a single number because there is a single reason —
// the session serializes, so any call's patience is charged to every other caller — and
// because the one call that had no budget at all is what made a store outage permanent (see
// establish).
const sessionBudget = 10 * time.Second

// MountRoot is the top-level segment CONTROLLER-MOUNTED slices live under, kept
// disjoint from DefaultRoot — and that disjointness is load-bearing, for one reason:
//
// A mount is task-driven, so it has NO HISTORY. A controller backs its subtree with
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

// observer is one live subscriber: a buffered channel plus the count of COMMITS dropped
// while it was full, carried on its next delivery so a drop is never silent (the consumer
// recovers the gap via Since).
type observer struct {
	ch      chan Commit
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
	subs        map[int]*observer
	nextID      int
	closed      bool
	synced      bool               // this store has seeded rev from a first sync
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
			// One budget for every interaction with the store, including the ones
			// libctl bounds by its own number rather than the caller's. Since go-tony
			// v0.0.128 the handshake takes min(wireTimeout, the caller's deadline), so
			// establish() already bounds a reconnect; what remains on this number is the
			// request WRITE, which holds the wire while it runs and would otherwise
			// overrun a ten-second caller by twenty. Verse writes entities, not blobs:
			// ten seconds to put a few KB on a socket means the peer is gone.
			WireTimeout: sessionBudget,
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
	// (the /watch debug stream); see Observe. (The Since
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
// mounts the local:dir-content-<scope> slice. A controller task holds no store, so
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

// nodeToPayload is the read face: a node is already a payload, so the only work is putting its
// fields in a canonical ORDER. Nothing is dropped.
//
// It used to drop every untagged null field at any depth, because verse spelled "this field is
// gone" by writing one — the read half of a matched pair with addRemovedNulls below. That
// convention worked and cost the data model a legal value: a bare null could not be stored as
// DATA anywhere, at any depth, including inside a charter's own `!raw` spec, where by read time
// the escape is consumed and what is left is an ordinary object to walk into. So
// `value: !not {reviewed: null}` — the absence latch trigger-model.md documents — came back as
// `!not {}`, which is a perfectly good pattern meaning something else, and every rule written
// that way silently selected nothing from its first restart onward. A store may choose how it
// spells absence; it may not choose which values you are allowed to hold. Absence is `!delete`
// now, which is what the substrate underneath always used: logd holds a bare null as an
// ordinary value, through restart, log switch and compaction.
//
// The ORDER survives that change and is the reason this function still exists. ir.FromMap
// sorts, and a payload is compared against the last one to decide whether an entity changed —
// so a canonical order is what keeps that answer about the value rather than about the order
// two reads happened to return.
//
// An object KEEPS ITS OWN TAG. Rebuilding used to drop it, which is invisible for the
// record-shaped payloads verse writes — they are untagged objects — and total for one that
// holds a document: a goal whose `doneWhen: !fact {facts: …}` went into the store intact came
// back an untagged object, and read as a match with an unknown `facts` key.
//
// A NULL NODE AT THE ROOT is still absence rather than a value, and that is the store's own
// spelling rather than this convention's: Get reads it as "nothing stands here", which is what
// a path holding nothing answers with.
func nodeToPayload(node *ir.Node) Payload {
	// A null AT THE ROOT is "nothing stands here" — the store's own answer for an empty path,
	// and a different question from what a null FIELD means, which is null. Recursing through
	// this check is what would confuse the two.
	if node == nil || (node.Type == ir.NullType && node.Tag == "") {
		return nil
	}
	return canonicalFields(node)
}

// canonicalFields sorts an object's fields, at any depth, and changes nothing else.
func canonicalFields(n *ir.Node) *ir.Node {
	if n == nil || n.Type != ir.ObjectType {
		return n
	}
	fields := map[string]*ir.Node{}
	for k, v := range ir.ToMap(n) {
		fields[k] = canonicalFields(v)
	}
	return ir.FromMap(fields).WithTag(n.Tag)
}

// addRemovedNulls makes a Patch replace rather than merge: for every field present
// in the old value but absent from the new payload, it appends an explicit null — VERSE's
// tombstone mark, not logd's — so nodeToPayload reads it back as gone. This is what gives
// Put its whole-value replace semantics.
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
			node.Values = append(node.Values, deleteOp())
		}
	}
}

// --- Store: reads and writes ---

// Get returns an entity by ref. An absent (or deleted) path reads back as null.
// Get reads one entity. A failed READ is reported as an error, never as absence: the
// read crosses a network and may traverse a mount owned by another task, so "I could
// not ask" and "it is not there" are genuinely different answers and the caller has to be
// able to tell them apart. A null node IS absence — that is how verse spells a tombstone,
// not logd, which just holds the bare value.
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
// It asks the server's CODE, which is the stable half of a response. logd answers a read
// of a path that does not exist with an error rather than a null, and since go-tony
// v0.0.122 libctl wraps that response instead of flattening it to its message, so
// logdapi.ErrorCode digs the ErrCode* back out through the wrapping.
//
// Getting it wrong in the other direction is what makes this worth stating at length:
// every absent entity whose id, kind, or system had never been written used to come back
// as a read FAILURE — so `GET /entity` for anything absent answered 502 on any verse that
// had ever been written to, and an agent asking after a file was told the store was
// unreachable.
//
// This function matched the MESSAGE until v0.0.122, and that is precisely how it broke:
// v0.0.121 replaced the wrapped "path not found" sentinel with a typed PathError whose
// prose names what resolved and what did not, the old substring matched nothing, and every
// absent read was a 502 again. The code does not move when the prose improves, which is the
// whole reason to ask it. TestAbsentReadsAreNotFailures is what made that bump say so out
// loud, and is what will catch the next one.
//
// Note what is NOT absent: ErrCodeInvalidPath, which logd answers when a path addresses
// something other than an object field. That path can never resolve, so it is the caller's
// path being wrong rather than the store's contents missing, and reporting it as absent
// would answer "nothing there yet" to something no write can ever fix. Asking the code
// gets that distinction for free; the message match had to encode it by hand.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if code := logdapi.ErrorCode(err); code != "" {
		return code == logdapi.ErrCodeNotFound
	}
	// No response error in the chain: either a transport failure, which is emphatically
	// not "nothing stands there", or a pre-v0.0.122 `o` that flattened the response to its
	// message before we could see the code. resolveO will run a skewed `o` with a warning
	// rather than no store at all, so both older spellings stay matched — cheaper than the
	// 502 if it comes to it, and dead weight otherwise.
	msg := err.Error()
	return strings.Contains(msg, "no value at ") || strings.Contains(msg, "path not found")
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
// The commit the write landed at is REPORTED, not inferred: both Patch and PatchIf answer with
// a PatchResult whose Commit is the transaction's own commit number, in the same sequence the
// root watch stamps on the change stream. That correspondence is what lets a writer recognize
// its own write when it comes back around as an observation. (This used to say libctl discarded
// the number; it has not since go-tony v0.0.85-ish, and a caller reading the old comment would
// conclude the correlation was impossible.)
func (s *LogdStore) Write(w Write) (bool, int64, error) {
	ctx, cancel := s.reqCtx()
	defer cancel()
	path := s.writePath(w.At)
	// REFUSED HERE, before the log can hold it. A match operator in a written value is a store
	// that stops being readable — see entity.CheckPatchable, and the two staging deployments
	// that were lost to it. This is the one place every write passes through, which is the only
	// reason it can be a promise rather than a convention each caller keeps.
	if err := CheckPatchable(w.Value); err != nil {
		return false, 0, fmt.Errorf("write %s: %w", w.At, err)
	}
	node := payloadToNode(w.Value)
	if w.Value == nil {
		node = deleteOp() // remove whatever stands there
	}
	if len(w.When) == 0 {
		res, err := s.sess.Patch(ctx, path, node)
		if err != nil {
			return false, 0, fmt.Errorf("write %s: %w", w.At, err)
		}
		return true, res.Commit, nil
	}
	pre := make([]pathPattern, 0, len(w.When))
	for _, r := range w.When {
		pre = append(pre, pathPattern{at: r.path(w), pattern: r.Pattern})
	}
	res, err := s.sess.PatchIf(ctx, path, node, &logdapi.PathData{Path: seg(s.root), Data: rootPattern(pre)})
	if errors.Is(err, libctl.ErrMatchFailed) {
		return false, 0, nil // a precondition did not hold — a revoke, or already committed
	}
	if err != nil {
		return false, 0, fmt.Errorf("write %s: %w", w.At, err)
	}
	return true, res.Commit, nil
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
// (a null pattern is the presence test). A ref that was DELETED still has its key: verse
// tombstones it with a null leaf, not logd — and that mark is what makes the delete
// replayable. So the two are ORed, and a claim released by Delete can be taken again —
// without the second branch, releaseRun would free a claim that claimRun could never
// re-win, and a sequence would run exactly once per subject for the lifetime of the
// document.
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
// writes).
func (s *LogdStore) ensureSynced() {
	s.mu.Lock()
	watching := s.watchCancel != nil
	s.mu.Unlock()
	if watching {
		return // the root watch is live: rev is already current
	}
	ctx, cancel := context.WithTimeout(s.ctx, sessionBudget)
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
	s.synced = true
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
//
// The document is SEEDED by the watch's initial State, which is what makes an event's
// payload the whole entity as of the commit rather than the writer's delta (see
// docs/change-events.md, "Why To is level").
//
// THE SEED IS REPLAYED, not adopted silently: the opening snapshot is diffed against an
// empty one, so everything already standing in the slice arrives as a Set. This is what
// makes the watch level-triggered rather than merely edge-from-open — a rule is a statement
// about the world, and the world includes what was written before anyone was listening. A
// trigger installed over a populated verse acts on the backlog that already matches it,
// instead of waiting for someone to re-touch each entity to announce a state it has held
// all along. A RE-established watch (a force-end, a dropped connection) does not replay:
// its State is diffed against the snapshot the previous one left, so it emits only the gap.
//
// The cost, which is the charter author's to carry: what fires is "matches now", so a
// non-idempotent action behind a state a subject can genuinely re-enter fires again when
// the engine restarts over a verse still in that state. The latch idiom answers it — match
// a field the action advances (see docs/change-events.md) — and it is the same discipline a
// re-entry already needs.
func (s *LogdStore) WatchSubtree(system, kind string) (<-chan Commit, func(), error) {
	// The path as segments as well as a kpath: the two watch event shapes are rooted
	// differently and the document has to hold one of them (see stateDoc).
	segs := []string{s.root, system}
	if kind != "" {
		segs = append(segs, kind)
	}
	path := seg(s.root) + "." + seg(system)
	if kind != "" {
		path += "." + seg(kind)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	w, err := s.establish(ctx, path, &libctl.WatchOptions{})
	if err != nil {
		cancel()
		return nil, nil, err
	}
	out := make(chan Commit, 256)
	go func() {
		defer close(out)
		// doc is the watched subtree as a root-rooted document, maintained by APPLYING
		// each watch event: a State (initial or reconnect) replaces it; a Patch is
		// applied with tony.Patch. We never interpret the patch's structure — logd owns
		// the diff encoding (merge vs !replace vs !delete/!insert, field order, …) — we
		// apply it opaquely and read entities out of the reconstructed object.
		//
		// prev is the last emitted snapshot, and it starts EMPTY — including under the
		// opening State, so the first diff reports the whole slice and the backlog fires.
		// Nothing here distinguishes the opening State from a reconnect's, and nothing has
		// to: after the first diff prev holds the slice, so a reconnect's State diffs to
		// the gap alone.
		doc := ir.Null()
		prev := map[Ref]Payload{}
		// rev is the commit the reconstructed doc now stands at, stamped on every event the
		// next diff emits. Without it a subtree event carried no revision at all while a root
		// event carried its commit — the same stream, one half of it unable to say WHEN.
		// Nothing downstream could then ask a question about a change at the commit it
		// happened at, which is the whole affordance AsOf exists to offer.
		var rev int64
		send := func(c Commit) bool {
			select {
			case out <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}
		apply := func(ev *logdapi.WatchEvent) {
			if ev.Commit > rev {
				rev = ev.Commit
			}
			switch {
			case ev.State != nil:
				doc = stateDoc(ev.State, segs) // full current state of the subtree
			case ev.Patch != nil:
				if nd, err := tony.Patch(doc, ev.Patch); err == nil {
					doc = nd
				}
				// A patch that fails to apply leaves doc unchanged; a later State (e.g.
				// on reconnect) resyncs it. We never decode the patch to recover by hand.
			}
		}
		// emitDiff reconstructs the entity set from doc and emits the diff vs the last
		// snapshot as ONE commit, stamped with the revision the doc now stands at. Returns
		// false if the consumer went away (ctx done) mid-send.
		//
		// It is called once per watch event, never once per burst. Coalescing — apply N
		// deltas, diff once — used to make a rapid N-file write cost one diff instead of N,
		// and it is exact for a LEVEL, since the merged diff still reports each entity's
		// current state. It is lossy for an EDGE: a value that appears at one commit and
		// disappears at the next leaves no trace in a diff spanning both, and a transition the
		// stream never reported cannot be recovered downstream. So the diff is per commit, and
		// the cost is a subtree walk per commit rather than per burst.
		//
		// A commit that changes nothing in this slice emits nothing rather than an empty
		// commit: this watch reports one subtree, and a consumer of it has no use for the
		// revisions where its slice stood still.
		emitDiff := func() bool {
			cur := entitiesFromDoc(doc, s.root, system, kind)
			var changes []Event
			for ref, p := range cur {
				// ir.Node.DeepEqual, NOT reflect.DeepEqual. A node carries structural
				// metadata — Parent, ParentIndex, ParentField — that says where it sits in
				// the document rather than what it holds, and a document rebuilt from a fresh
				// read wires that up anew. reflect compares it; ir's DeepEqual documents that
				// it does not. With reflect, every entity in a slice compared UNEQUAL to the
				// identical value after a resync, so re-establishing a watch re-emitted the
				// whole slice as changed — measured, twenty entities delivered forty events —
				// and every trigger over it would have fired again for nothing.
				if old, ok := prev[ref]; !ok || !old.DeepEqual(p) {
					changes = append(changes, Event{Ref: ref, Op: Set, To: p, Rev: rev})
				}
			}
			for ref := range prev {
				if _, ok := cur[ref]; !ok {
					changes = append(changes, Event{Ref: ref, Op: Delete, Rev: rev})
				}
			}
			prev = cur
			if len(changes) == 0 {
				return true
			}
			// Stable order, so a consumer acting per entity does so deterministically and a
			// test can say what it expects. Map iteration above is not.
			sort.Slice(changes, func(i, j int) bool {
				return changes[i].Ref.String() < changes[j].Ref.String()
			})
			return send(Commit{Rev: rev, Changes: changes})
		}
		backoff := 100 * time.Millisecond
		// A watch can die WITHOUT SAYING SO, and then this loop would wait on a channel
		// nothing will ever send to again. See subtreeHealAfter: the heal timer is what
		// bounds how long that can last.
		heal := time.NewTimer(subtreeHealAfter)
		defer heal.Stop()
		healAfter := subtreeHealAfter
		for {
			// Drain the current watch until it ends. A clean end is a force-end
			// (a mount/unmount overlapping this subtree changed membership →
			// WatchEndedError, reason "session_mounted" or "session_unmounted"
			// since go-tony v0.0.122, one "membership_changed" before it) or a
			// dropped connection — NOT a terminal condition. We re-establish below
			// rather than returning, and the reason does not change that: every
			// ending upstream defines is re-establishable, and re-watching is the
			// whole response. It would matter to code deciding whether the content
			// it is about to see SHOULD differ; this loop only has to catch up.
		pump:
			for {
				select {
				case <-ctx.Done():
					w.Close()
					return
				case <-heal.C:
					// Quiet for a whole interval. Either the slice is idle — in which case
					// re-establishing costs one State and emits nothing — or the watch is
					// dead and this is the only thing that will ever notice. Back off while
					// nothing changes, so an idle slice does not pay this every interval.
					healAfter = min(healAfter*2, subtreeHealMax)
					heal.Reset(healAfter)
					break pump
				case ev, ok := <-w.Events():
					if !ok {
						break pump // watch ended; re-establish
					}
					// It is alive: hold the heal off, and forget any backoff it accrued.
					healAfter = subtreeHealAfter
					if !heal.Stop() {
						select {
						case <-heal.C:
						default:
						}
					}
					heal.Reset(healAfter)
					if ev.ReplayComplete {
						continue // live watch: no replay phase to complete
					}
					// One event in, one diff out. There is no burst-coalescing here: see
					// emitDiff on why a merged diff is exact for a level and lossy for an
					// edge.
					apply(ev)
					if !emitDiff() {
						w.Close()
						return
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
				// RE-WATCH AND RECONCILE, rather than resuming from the commit the ended
				// event names. logd offers an exact resume point (WatchEndedError.Commit) and
				// libctl's own doc says who it is for: "a snapshot-diffing consumer needs
				// neither and can just re-watch and reconcile against its last snapshot."
				// That is precisely this watch — emitDiff holds prev and reports the
				// difference — and measured, the choice is not close. Resuming replays the
				// missed deltas one at a time into a stream that is already the bottleneck: a
				// 2000-entity slice reached 894 of 2000 in three minutes. Re-initializing
				// sends ONE State carrying net state, and the same slice completed in 32s.
				nw, err := s.establish(ctx, path, &libctl.WatchOptions{})
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

// stateDoc re-roots a State event's node under the watched path's segments
// (root[, system[, kind]]), which is what makes it the same document a Patch event
// applies to.
//
// THE TWO EVENT SHAPES DIFFER, and nothing in either one says so. A Patch is the commit's
// delta rooted at the store root (verse.demo.pr.1: {…}) — logd forwards the writer's patch
// verbatim, and a scoped watch's computed diff is re-rooted to match. A State carries the
// value AT the watched path (the logd session extracts it; docd's composed read builds it
// relative to the path), so its top-level fields are ids, not the root. Taking the State
// verbatim leaves a document whose root segment is missing: every read of it navigates
// root → system → kind and finds nothing, so the seed contributes no entities and the
// level state degenerates to whatever has been patched since the watch opened — a deep
// write of one field then surfaces as an entity of one field, and a trigger binds $note
// from a payload with no note in it.
func stateDoc(at *ir.Node, segs []string) *ir.Node {
	node := at
	if node == nil {
		node = ir.Null()
	}
	for i := len(segs) - 1; i >= 0; i-- {
		node = ir.FromMap(map[string]*ir.Node{segs[i]: node})
	}
	return node
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
// an active whole-verse watch is transient and retries on a mount/unmount force-end).
func (s *LogdStore) Observe() (<-chan Commit, func()) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	o := &observer{ch: make(chan Commit, 256)}
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
	w, err := s.establish(wctx, s.root, &libctl.WatchOptions{})
	if err != nil {
		wcancel()
		return // no observers get events until a later Observe retries the open
	}
	s.watchCancel = wcancel
	go s.runWatch(wctx, w)
}

// Since replays commits with Rev > from using a native logd FromCommit watch (a
// query by revision), in commit order. It is a pure read — it derives each event's Op from
// the delta alone (a tombstone is Delete, any value is Set) and mutates no store state, so
// repeated or concurrent calls are consistent. This is what keeps a hold costing a cursor
// rather than a growing buffer.
//
// One replayed delta is one Commit, so a catch-up delivers the same framing the live stream
// does and a consumer cannot tell which it is reading — which is the point: the two paths
// exist to recover each other.
//
// ok is false when the caller must resync from current state, and the only thing that
// decides that is the REPLAY: logd refuses a FromCommit watch under its own floor with a
// terminal "replay_compacted" event, libctl raises it as a WatchEndedError, and the closed
// channel reports it through w.Err(). See TestSinceBelowReplayFloorResyncs.
//
// There used to be a cheaper first guard: a per-instance low-watermark, refusing anything
// below the commit at which THIS STORE first synced. It was added when logd under-delivered
// a compacted range silently, and it outlived that fix while quietly costing the thing a
// cursor is for. A `--since` names a STORE REVISION — the CLI's own help says so — and the
// watermark answered a different question, about this process's memory. So a fresh store
// refused history logd still held, including history it had itself written moments before,
// and every restart truncated what could be read to "since the restart". From the outside
// that reads as flakiness: the same `--since 700` answering three commits, then nothing.
//
// Asking logd is not expensive and it is the only source with standing.
func (s *LogdStore) Since(from int64) ([]Commit, bool) {
	s.ensureSynced()
	// No rev>=from early-out: without a standing watch rev may be stale, so the FromCommit
	// replay is authoritative — it streams commits after `from` and signals ReplayComplete
	// when caught up (empty when there is nothing new), and it is also what reports a range
	// logd can no longer replay.

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

	var out []Commit
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
				out = append(out, Commit{
					Rev:     ev.Commit,
					Changes: s.eventsFromDelta(ev.Commit, ev.Patch, doc),
				})
			}
		}
	}
}

// AsOf returns a snapshot of the whole verse as it stood at rev — one MatchAt over the root,
// which docd answers across base and every logd-backed mount at the same commit (they share
// logd's commit sequence), so the snapshot is consistent across mounts and not merely
// per-path.
//
// It reads the WHOLE root rather than deferring per-ref reads, because the point of a
// snapshot is that two reads of it cannot disagree; a lazy snapshot issuing MatchAt per
// question would be consistent too, but would put a round trip on every predicate a rule
// evaluates. See docSnapshot.
//
// A revision the store cannot answer at — out of range, or beyond current — is an error,
// unlike docAt's replay base, which degrades to an empty document because the replay
// carries every commit it needs. Here there is nothing to make up the difference: a
// snapshot that quietly read current state instead would be indistinguishable from one that
// worked, at exactly the moment being at the right commit is the whole requirement.
func (s *LogdStore) AsOf(rev int64, within ...[2]string) (Snapshot, error) {
	ctx, cancel := s.reqCtx()
	defer cancel()
	if len(within) == 0 {
		node, err := s.matchAt(ctx, seg(s.root), rev)
		if err != nil {
			return nil, err
		}
		return docSnapshot{root: s.root, rev: rev,
			doc: ir.FromMap(map[string]*ir.Node{s.root: nodeOrEmpty(node)})}, nil
	}
	// One read per named subtree, assembled into the same root-rooted shape.
	//
	// It bounds what crosses the wire, and — measured — that is ALL it does. A read costs what
	// it costs to replay the log to that commit, which is independent of the path asked for:
	// on a 300-commit verse, the whole root, the 300-entity slice the commits landed in, and a
	// one-entity slice they never touched all cost ~73ms. So do not reach for this expecting a
	// read to get cheaper. What makes a read cheap is not asking for one — see the roll-forward
	// note in docs/working/firing-residual.md.
	verse := map[string]*ir.Node{}
	for _, st := range within {
		path := seg(s.root) + "." + seg(st[0])
		if st[1] != "" {
			path += "." + seg(st[1])
		}
		node, err := s.matchAt(ctx, path, rev)
		if err != nil {
			return nil, err
		}
		if node == nil {
			continue // nothing stands in that slice at that commit
		}
		graftAt(verse, st, node)
	}
	return docSnapshot{root: s.root, rev: rev,
		doc: ir.FromMap(map[string]*ir.Node{s.root: ir.FromMap(verse)})}, nil
}

// matchAt reads one path at a revision, reporting "nothing there" as a nil node rather than an
// error — a never-written slice is a real state, and a rule evaluated against it selects
// nothing.
func (s *LogdStore) matchAt(ctx context.Context, path string, rev int64) (*ir.Node, error) {
	node, err := s.sess.MatchAt(ctx, path, rev)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s at rev %d: %w", path, rev, err)
	}
	return node, nil
}

func nodeOrEmpty(n *ir.Node) *ir.Node {
	if n == nil {
		return ir.FromMap(map[string]*ir.Node{})
	}
	return n
}

// graftAt places a subtree read at (system, kind) — or at (system, "") — into the verse map
// being assembled, merging with a sibling kind already grafted under the same system.
func graftAt(verse map[string]*ir.Node, st [2]string, node *ir.Node) {
	if st[1] == "" {
		verse[st[0]] = node
		return
	}
	kinds := map[string]*ir.Node{}
	if cur, ok := verse[st[0]]; ok && cur != nil && cur.Type == ir.ObjectType {
		for i, f := range cur.Fields {
			kinds[f.String] = cur.Values[i]
		}
	}
	kinds[st[1]] = node
	verse[st[0]] = ir.FromMap(kinds)
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
		nw, err := s.establish(ctx, s.root, &libctl.WatchOptions{})
		if err != nil {
			continue // retry after the next backoff
		}
		w = nw
	}
}

// handleEvent tasks one watch event: an initial/reconnect State only seeds rev
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
		// A State is rooted AT the watched path while a delta is rooted at the DOCUMENT, so
		// re-root it (stateDoc, which the subtree watch shares) — otherwise the first delta
		// grafts a second tree beside the first and every level read misses.
		s.doc = stateDoc(ev.State, []string{s.root})
		s.mu.Unlock()
	case ev.Patch != nil:
		s.mu.Lock()
		s.doc = applyDelta(s.doc, ev.Patch)
		doc := s.doc
		s.mu.Unlock()
		s.emit(Commit{Rev: ev.Commit, Changes: s.eventsFromDelta(ev.Commit, ev.Patch, doc)})
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

// emit broadcasts one commit to observers, non-blocking with drop-and-gap accounting — the
// contract Observe documents (drops are recovered by consumers via Since).
//
// A commit is dropped or delivered WHOLE. Dropping part of one would hand a consumer a
// transaction with entities missing and no way to know it, which is the one shape the
// framing exists to prevent; the gap count is therefore in commits, not entities.
func (s *LogdStore) emit(c Commit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.subs {
		if o.dropped > 0 {
			gapped := c
			gapped.Gap = o.dropped
			select {
			case o.ch <- gapped:
				o.dropped = 0
			default:
				o.dropped++
			}
			continue
		}
		select {
		case o.ch <- c:
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
	return context.WithTimeout(s.ctx, sessionBudget)
}

// establish opens a watch, bounding the ATTEMPT without bounding the watch.
//
// The distinction is the whole of it. A watch lives until it is closed or its connection
// drops, and that lifetime is the caller's ctx — but CONNECTING is exclusive, and libctl
// retries the dial with backoff until the context it was given says stop, holding the
// session's one connect slot throughout. Hand it a context that never says stop, as every
// watch here used to, and one re-establishing watch owns that slot for the length of an
// outage. Nobody else connects, so nobody else does anything.
//
// go-tony v0.0.128 changed how the others fail, not whether. Before it the connect happened
// under the mutex every request needed, and a Go mutex takes no context: a writer spent its
// ten seconds queued on a lock rather than waiting for the store, was handed the lock after
// the outage, and only then found its deadline gone — reporting `context deadline exceeded`
// for a write that never reached the wire. That was a real deployment, five and a half
// minutes of it, cured by restarting the pod. Since 128 the slot is ctx-aware and a waiter
// fails on its OWN deadline, which is a far better error and the same outcome.
//
// Only the holder can end it, and the holder is here. The starvation test still fails
// without this bound on 128, so this is the fix rather than a workaround for a version of
// libctl that has moved on.
//
// So an attempt gets the same budget every other session call gets, and no more. Patience
// belongs to the RETRY LOOP, which has backoff and holds nothing while it waits; a long
// attempt is not patience, it is one caller's exclusive use of the connection everybody
// shares. libctl uses this context for the connect and the watch handshake only — the Watch
// it returns does not retain it — so bounding the attempt costs the watch nothing.
func (s *LogdStore) establish(ctx context.Context, path string, opts *libctl.WatchOptions) (*libctl.Watch, error) {
	cctx, cancel := context.WithTimeout(ctx, sessionBudget)
	defer cancel()
	return s.sess.Watch(cctx, path, opts)
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
