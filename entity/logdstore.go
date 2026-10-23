package entity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"github.com/signadot/tony-format/go-tony/mergeop"
	"github.com/signadot/tony-format/go-tony/system/libctl"
	logdapi "github.com/signadot/tony-format/go-tony/system/logd/api"
	"github.com/signadot/tony-format/go-tony/token"
)

// DefaultRoot is the top-level kinded-path segment the verse lives under in logd.
// Log-backed entities are stored at <root>.<system>.<kind>.<id>, so a single watch on
// the root observes the whole (historied) verse.
const DefaultRoot = "verse"

// subtreeHealAfter and subtreeHealMax bound how long a subtree watch can be DEAD WITHOUT
// KNOWING IT before it finds out.
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
// may merely be idle or from one whose watch is gone, is answered by ASKING THE WATCH
// (libctl Watch.Progress): one small request the server answers with its head, which a watch
// that is gone does not answer. Only then is it re-established, whose State resyncs the document
// and whose diff against the last snapshot emits whatever was missed. Re-establishing at every
// silence instead was measured on staging: most slices see an event about once a minute, so
// ~390 watches each paid a State of their whole subtree every minute, 13 a second, a third of
// logd's read work (git-issue v13rvrcth12krdjbvdn0). The interval DOUBLES while nothing arrives,
// to subtreeHealMax, so an idle slice is asked less and less often; any event resets it.
//
// healProbeWithin is how long the ask may take before silence is read as death. Generous
// against a loaded store, since a false death costs the State the ask exists to avoid.
//
// Vars, not consts, so a test can shorten them: the failure they answer takes thousands of
// writes to provoke, and waiting out the production interval to observe the cure would make
// the test useless.
var (
	subtreeHealAfter = 30 * time.Second
	subtreeHealMax   = 5 * time.Minute
	healProbeWithin  = 10 * time.Second
)

// subtreeReopens counts the subtree watches re-established after they ended or fell silent: an
// instrument for the test that holds a quiet, live watch is asked and not replaced.
var subtreeReopens atomic.Int64

// askProgress asks a watch for its progress off the pump's loop, within healProbeWithin, and
// answers nil for a head and the error for no answer.
func askProgress(ctx context.Context, w *libctl.Watch) <-chan error {
	done := make(chan error, 1)
	go func() {
		pctx, cancel := context.WithTimeout(ctx, healProbeWithin)
		defer cancel()
		_, err := w.Progress(pctx)
		done <- err
	}()
	return done
}

// sessionBudget is how long ONE call on the session may take: a read, or an attempt to
// establish a watch. The one call that had no budget at all is what made a store outage
// permanent (see establish).
//
// It was one number for every call, on the reasoning that the session serializes so any
// call's patience is charged to every other caller. That reasoning ended with go-tony
// v0.0.166: reads run beside logd's request loop, bounded per session, and only writes stay
// on it. So a read's patience is now its own, and a write's is spent in a queue with every
// other write on the session — which is why they no longer want the same number.
const sessionBudget = 10 * time.Second

// writeBudget is that number for a WRITE, which waits differently: writes are dispatched on
// logd's request loop and each one holds it until its commit lands, so a write's wait is the
// sum of the writes in front of it rather than its own cost.
//
// Ten seconds was sized when a store held seed data. It is not what a verse reflecting several
// repositories spends: staging's three sources each failed at exactly 10s — `write
// git:ref:signadot/pull/1320/head: context deadline exceeded` — and none of them had reflected
// in over half an hour, because every pass re-queued behind the same wall.
//
// Thirty is a working number and not a principled one: it buys a queue three times as deep
// before a source gives up, and it covers for the queue rather than removing it. What removes
// it is fewer, larger commits, or writes leaving that loop the way reads did.
const writeBudget = 30 * time.Second

// replayBudget is that number for a CATCH-UP, which is neither a read nor a write: it is a
// watch that streams a range, so it waits for logd to fetch every patch in that range before
// the first event arrives, and then for this process to level each one onto the document.
//
// Fifteen seconds is what it has always been. It is stated as a name rather than a literal
// because it is the boundary a caller is told about — a replay cut off here answers with the
// commits it got AND the deadline, so nobody reads a truncated catch-up as a complete one.
const replayBudget = 15 * time.Second

// Every entity renders under the store's one root, and nothing under that root is served by
// anything but the log. That is what lets a history-bearing watch or replay on any path be
// answered from logd's commit sequence: a controller mount (docd's mount face) backs a
// subtree with something that is not the log, and a watch whose path is a strict ancestor
// of one is composed by docd, which drops FromCommit and never emits ReplayComplete. The
// store mounts nothing, so no watch it opens is ever composed.

// observer is one live subscriber: a buffered channel plus the count of COMMITS dropped
// while it was full, carried on its next delivery so a drop is never silent (the consumer
// recovers the gap via Since).
type observer struct {
	ch      chan Commit
	dropped int
	// at is what the observer asked about: the patterns its commits' Changes are answered at.
	at []*kpath.KPath
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
	logger *slog.Logger

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
	// out of (see emit, payloadIn); nil until a watch has run.
	doc *ir.Node

	// readErr is the last read this store COULD NOT PERFORM, and it exists because List
	// cannot say so in its signature: it answers a slice, so a store that is unreadable
	// answers the empty one — which is what a verse holding nothing answers too.
	//
	// That confusion is not theoretical. A store whose log holds a patch it cannot replay
	// (raw.go, and the guard that now refuses to write one) reads as EMPTY: `verse data
	// --paths` prints nothing, `verse charter` shows a charter of no rules, and writes go
	// on reporting success — a verse that looks alive and answers nothing, diagnosable only by
	// reading docd's log. Remembering the failure is what lets the surfaces above say "this
	// store cannot be read" instead of "there is nothing here".
	readErr error

	scopesMu sync.Mutex
	scopes   map[string]*LogdStore // lazily-created scoped children, keyed by scope name
}

// ReadError is the last read this store could not perform, or nil. The next read that succeeds
// clears it, so it answers about NOW rather than about the store's whole history.
//
// An optional capability rather than a method on Store: a caller that wants it asks for it
// (`interface{ ReadError() error }`), and the in-memory stores that cannot fail this way need
// no implementation.
func (s *LogdStore) ReadError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readErr
}

// noteRead records whether a read that cannot report its own failure succeeded.
func (s *LogdStore) noteRead(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readErr = err
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
	// Logger receives what this store cannot report through a return value — a watch delta
	// that did not apply, whose cost every event after it pays. Nil uses the default.
	Logger *slog.Logger
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
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
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
		logger: cfg.Logger, subs: map[int]*observer{},
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
func (s *LogdStore) refPath(ref Ref) string {
	return s.pathAt(ref.Segs())
}

// pathAt renders a path's segments under this store's root, the way refPath renders a ref:
// the two ways of naming one entity reach the same place.
func (s *LogdStore) pathAt(at []string) string {
	path := seg(s.root)
	for _, sg := range at {
		path += "." + seg(sg)
	}
	return path
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

// payloadToNode is the write face of a payload. A payload IS a node, so this only clones —
// the node is about to be parented into a patch document, and ir.FromMap re-parents what it
// is handed.
//
// It used to map a nil payload to an EMPTY OBJECT, because null was how an absent entity was
// spelled and a write of nil must not be mistaken for one. Absence is `!delete` (deleteOp),
// which Write spells for itself, so nothing here has to avoid null — and avoiding it is what
// made `null` unwritable as a payload. A nil payload is the payload null.
func payloadToNode(p Payload) *ir.Node {
	if p == nil {
		return ir.Null()
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
// A NULL NODE AT THE ROOT used to be absence rather than a value — the last place the
// convention above still held, one level up from the fields it had been lifted off. It cost
// the same thing at the top that it cost at every depth: `payload: null` was a legal tony
// value that no entity could hold, because Get read it back as "nothing stands here". Absence
// is a property of the PATH, and the substrate says so — logd removes the key outright when a
// `!delete` executes, so an absent entity has no node to read and Match answers not_found.
// There is nothing left for a null to disambiguate.
//
// COMMENTS ARE STRIPPED, and that is the one thing this does drop — because the store keeps
// them and verse is a caller that wants values. logd's own contract says so in those terms:
// "comments go in and come back. A caller that does not want them strips them, which is one
// call" (system/logd/api's NextState), and SameState counts them, so a comment-only edit is a
// commit every watch sees.
//
// It has to happen HERE, at the one boundary every payload verse hands out is made at, because
// a head comment WRAPS the value it precedes in a CommentType node. A payload standing behind
// one is not an object as far as anything that asks: Get, Str, Fields and IsObj all answer
// empty, `verse data demo.pr.1.stage` refuses the field, and a `$stage` binding comes back
// blank. Worse, tony.Match sees through comments, so a trigger SELECTS the entity and then
// reads nothing out of it — a rule that fires on the right subject and acts on nothing.
//
// Verse does not write comments (its parse does not ask for them), so this is about what
// somebody else wrote: a store is not verse's alone, and a document put there by `o` or by
// hand is one verse still has to be able to read.
func nodeToPayload(node *ir.Node) Payload {
	if node == nil {
		return nil
	}
	return canonicalFields(node)
}

// canonicalFields sorts an object's fields, at any depth, and changes nothing else.
// A COMMENT WRAPPER IS SORTED INSIDE AND KEPT. A head comment wraps the value it precedes, so
// the object to canonicalize is under the wrapper — and rebuilding from the wrapper would drop
// it, which is how a store comes to keep line comments and lose head ones. Half a document's
// comments is worse than none, because it looks like it worked (logd's own NextState makes the
// same argument about the same hazard).
func canonicalFields(n *ir.Node) *ir.Node {
	if n == nil {
		return n
	}
	obj := ir.Uncomment(n)
	if obj == nil || obj.Type != ir.ObjectType {
		return n
	}
	fields := map[string]*ir.Node{}
	for k, v := range ir.ToMap(obj) {
		fields[k] = canonicalFields(v)
	}
	sorted := ir.FromMap(fields).WithTag(obj.Tag)
	sorted.Comment = obj.Comment
	return rewrap(n, sorted)
}

// rewrap puts sorted back inside whatever comment wrappers stood around orig, outermost last,
// so the head comments a document was written with survive being canonicalized.
func rewrap(orig, sorted *ir.Node) *ir.Node {
	if orig == nil || orig.Type != ir.CommentType || len(orig.Values) != 1 {
		return sorted
	}
	inner := rewrap(orig.Values[0], sorted)
	out := &ir.Node{Type: ir.CommentType, Values: []*ir.Node{inner}, Lines: orig.Lines, Comment: orig.Comment}
	inner.Parent = out
	return out
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

// Get reads one entity. A failed READ is reported as an error, never as absence: the
// read crosses a network and may traverse a mount owned by another task, so "I could
// not ask" and "it is not there" are genuinely different answers and the caller has to be
// able to tell them apart.
//
// ABSENCE IS A MISSING KEY, not a null value. A `!delete` removes the key, so a read of a
// deleted (or never-written) ref answers not_found, which isNotFound turns into ok=false; a
// null node is the entity's payload, `payload: null`, a value like any other. Reading that as
// absence too is how a legal tony value came to be one no entity could hold.
//
// THE STORE SETTLES IT, since go-tony v0.0.201: a read answers null iff a null was written
// there, and absence is not_found at every depth whether or not an ancestor resolves. Before
// that a path with nothing above it answered a bare null — the same answer an entity holding
// null gives — so verse asked the parent whether the key stood and read absence off that. It
// cost a second round trip and it was verse guessing at a rule the store now states.
func (s *LogdStore) Get(ref Ref) (Entity, bool, error) {
	e, ok, _, err := s.GetCommit(ref)
	return e, ok, err
}

// GetCommit is Get, and also the commit the read was served at (libctl's MatchCommit): the
// revision a view that checks the read has to have caught its perms up to (Attribution.ensure).
// Zero where the store said nothing — a path nothing stands at is answered by an error, which
// carries no commit — and the caller then falls back to the latest one this process has seen.
func (s *LogdStore) GetCommit(ref Ref) (Entity, bool, int64, error) {
	ctx, cancel := s.reqCtx()
	defer cancel()
	node, commit, err := s.sess.MatchCommit(ctx, s.refPath(ref))
	if isNotFound(err) {
		return Entity{}, false, 0, nil // nothing stands there — an answer, not a failure
	}
	if err != nil {
		return Entity{}, false, 0, fmt.Errorf("read %s: %w", ref, err)
	}
	if node == nil {
		return Entity{}, false, commit, nil
	}
	return Entity{Ref: ref, Payload: nodeToPayload(node)}, true, commit, nil
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
// A CONFLICTING ANCESTOR IS ALSO NOTHING STANDING THERE. Since go-tony v0.0.187 logd
// separates "nothing is there" (not_found) from "something is there of a shape that cannot
// hold this" (path_conflict) — a read under an entity whose payload is a scalar answers the
// second. Both are answers about the store's CONTENTS,
// and every caller here is asking one question: does anything stand at this path. A scalar
// at demo:pr:1 means nothing stands at demo:pr:1:sub, the same as never having written it,
// and a write can make it so — replace the scalar and the path resolves.
//
// Note what is NOT absent: ErrCodeInvalidPath, which logd answers when a path is not a
// well-formed question — a wildcard names a set where a read answers one. That path can
// never resolve WHATEVER the store holds, so it is the caller's path being wrong rather
// than the store's contents missing, and reporting it as absent would answer "nothing there
// yet" to something no write can ever fix. That is the line: contents a write could change,
// against a question no state could answer. Asking the code gets it for free; the message
// match had to encode it by hand.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if code := logdapi.ErrorCode(err); code != "" {
		return code == logdapi.ErrCodeNotFound || code == logdapi.ErrCodePathConflict
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
	ctx, cancel := s.writeCtx()
	defer cancel()
	path := s.writePath(w.At)
	// REFUSED HERE, before the log can hold it. A match operator in a written value is a store
	// that stops being readable — see entity.CheckPatchable, and the two staging deployments
	// that were lost to it. This is the one place every write passes through, which is the only
	// reason it can be a promise rather than a convention each caller keeps.
	if err := CheckPatchable(w.Value); err != nil {
		return false, 0, fmt.Errorf("write %s: %w", w.At, err)
	}
	// GRANTS ARE THE ATTRIBUTED STORE'S, which writes them as a record beside the node in the
	// same commit (Write.Grants). Here they would be dropped, and the node created held out as
	// the default holds it — the one thing a writer who gave them meant not to happen.
	if w.Grants != nil {
		return false, 0, fmt.Errorf("write %s: %w: grants are written by an attributed store, beside the node", w.At, ErrBadGrants)
	}
	node := payloadToNode(w.Value)
	if w.Value == nil {
		node = deleteOp() // remove whatever stands there
	}
	// A WRITE DESCENDS ONLY THROUGH SOMETHING A FIELD CAN LIVE IN. `verse data demo pr 1 votes
	// bo --put '"yes"'` names `bo` under `votes`, and where `votes` holds an array there is
	// nothing to merge into: the patch writes an object holding the one field where the array
	// stood, every ballot with it, exit 0 — and the answer echoes the value written rather than
	// the entity, so nothing in the transcript says anything was lost. A scalar goes the same
	// way. git-issue mbjpdr6sh12ksa5zgsn0 has the transcripts.
	//
	// So a write that descends carries a precondition about the place it descends THROUGH:
	// nothing stands there, or what does is an object or a null. descendablePath is the pattern
	// and carries why absence and null are on the admitting side — briefly, that an address is a
	// path of any depth, so refusing to build one refuses a package deriving a deep entity.
	parent, descends, err := descendsThrough(w)
	if err != nil {
		return false, 0, fmt.Errorf("write %s: %w", w.At, err)
	}
	if len(w.When) == 0 && !descends {
		// THE WRITER RIDES THE WRITE: logd records w.Principal as the commit's author and every
		// delta event carries it back (WatchEvent.Author), so a reader of the log learns who
		// without a second record. Empty names nobody, and logd keeps it so.
		res, err := s.sess.PatchWith(ctx, path, node, libctl.PatchOpts{Author: w.Principal})
		if err != nil {
			return false, 0, fmt.Errorf("write %s: %w", w.At, err)
		}
		return true, res.Commit, nil
	}
	pre := make([]pathPattern, 0, len(w.When)+1)
	if descends {
		pre = append(pre, pathPattern{at: parent, object: true})
	}
	for _, r := range w.When {
		at, sub := r.path(w), ""
		// THE WRITE'S OWN PLACE IS THE ELEMENT when there is a Sub, so a guard that names no
		// place of its own is about the element — `--create` at `votes[0]` asks whether
		// `votes[0]` is there, not whether the entity is. It is asked with `!at` rather than by
		// nesting (elemPattern); a require that spells a place out is nested as it names it.
		if w.At.Access != "" && r.atOwnPlace() {
			// `!at` carries the address as a TAG ARGUMENT, which has no quoting: a step holding
			// a top-level comma or an unbalanced parenthesis cannot be written in one, and a
			// tag that reads back as something else is worse than a refusal (ir.TagArgOK).
			if !ir.TagArgOK(w.At.Access) {
				return false, 0, fmt.Errorf("write %s: a precondition here cannot be "+
					"written: it is asked with !at(%s), and a tag argument has no quoting, so "+
					"a comma or an unpaired parenthesis in the address cannot be spelled in "+
					"one. Write it unconditionally, or guard the field that holds the array",
					w.At, w.At.Access)
			}
			sub = w.At.Access
		}
		pre = append(pre, pathPattern{at: at, sub: sub, pattern: r.Pattern, absent: r.Absent, object: r.descends})
	}
	res, err := s.sess.PatchWith(ctx, path, node, libctl.PatchOpts{Match: s.preconditionAt(pre), Author: w.Principal})
	if errors.Is(err, libctl.ErrMatchFailed) {
		// SAY WHICH REFUSAL IT WAS, when the write descended. "A condition did not hold" is the
		// honest answer for a caller's own guard, and it is the wrong one here: the whole
		// defect this guard closes was a transcript that did not say what happened to the data.
		// One read, on the failure path only.
		if descends {
			if err := s.descendRefusal(ctx, w, parent); err != nil {
				return false, 0, err
			}
		}
		return false, 0, nil // a precondition did not hold — a revoke, or already committed
	}
	if err != nil {
		return false, 0, fmt.Errorf("write %s: %w", w.At, err)
	}
	return true, res.Commit, nil
}

// descendsThrough is the place a write DESCENDS THROUGH — its address minus the last step —
// and whether there is one to ask about.
//
// IT IS COMPUTED ON THE ADDRESS AS A KPATH, which is the canonical spelling of one and the only
// thing both forms of a write share. `verse data demo pr 1 votes bo` arrives as a Path from
// TargetOf — a Ref of names plus an Access when a step reaches into an array; `--at
// demo.pr."1".votes.bo` arrives as a Ref of that whole depth with no Access at all
// (versed.addressAt, entity.RefOfAny). Path.segments() reads the Ref's own segments either way,
// so the question is asked once rather than once per spelling.
//
// The store path is NOT what is parsed, deliberately: refPath prefixes the logd root, which is
// spelled `verse` by default and would read as the reserved half's own root. The address
// carries VerseRoot only when it is genuinely in verse's half.
//
// THERE IS NO DEPTH TEST, and its absence is the whole design. An address is a path of any
// depth and entity-hood is a lens over one document, so `verse-dev.drift.head.abc` is a package
// deriving an entity AND a field under `verse-dev.drift.head` — the same write, and nothing in
// it says which was meant. Asking every write the same question is what makes that not matter:
// what is refused is descending through a VALUE, and creating where nothing stands is left
// alone at every depth (see descendablePath).
//
// An ARRAY ACCESS (Path.Access) answers false, and not for want of a way to say it. The guard
// admits an object, a null, or nothing at all, and the parent of an element must be an ARRAY —
// the one shape it refuses. Nothing is lost by not asking: the substrate refuses an element
// write whose parent is not an array standing there, naming what it found instead. A wildcard
// answers false too — it names no one place to ask about.
func descendsThrough(w Write) (Path, bool, error) {
	if w.At.Access != "" {
		return Path{}, false, nil
	}
	segs := w.At.segments()
	if len(segs) < 2 {
		return Path{}, false, nil // nothing above it to descend through
	}
	// JoinSegs, not KPathOf: these are the write's segments, names whatever they hold, and not
	// words for the step clauses to read.
	at, err := ParseAt(JoinSegs(segs))
	if err != nil {
		return Path{}, false, err
	}
	for kp := at; kp != nil; kp = kp.Next {
		if kp.Field == nil || kp.FieldAll {
			return Path{}, false, nil
		}
	}
	ref, ok := RefOfAny(segs[:len(segs)-1])
	if !ok {
		return Path{}, false, nil
	}
	return ref.At(), true, nil
}

// descendRefusal names what a write descended through, and returns nil when the parent is fine
// — which means some other precondition failed and the caller's own "did not hold" is the
// right answer after all.
//
// It reads, and it is the only read on this path. That is affordable because it runs ONLY after
// a write has already been refused: the cost lands on the failure, and what it buys is the
// difference between a transcript a reader can act on and one they cannot.
func (s *LogdStore) descendRefusal(ctx context.Context, w Write, parent Path) error {
	node, err := s.sess.Match(ctx, s.writePath(parent))
	if err != nil || node == nil {
		return nil // cannot say; leave it as the ordinary refusal
	}
	// AN OBJECT OR A NULL IS INCONCLUSIVE, and saying so is the difference between a message and
	// a wrong message: descendablePath admits both, so a parent that is either did not cause
	// this refusal. Somebody else's precondition did.
	n := ir.Uncomment(node)
	if n.Type == ir.ObjectType || n.Type == ir.NullType {
		return nil
	}
	// SPELLED AS THE ADDRESS, not as the store path. refPath prefixes the logd root — `verse` by
	// default, which is also how the reserved half is spelled — and a reader who typed
	// `demo pr 2 votes bo` has never seen it and cannot act on it.
	where := JoinSegs(parent.segments())
	return fmt.Errorf("%w: %s holds %s, so %q is not a place in it — write the whole value AT "+
		"%s, or address an element of it",
		ErrNotDescendable, where, standing(n), lastStep(w.At), where)
}

// standing names what a write ran into, in the words a reader would use for it.
func standing(n *ir.Node) string {
	if n.Type == ir.ArrayType {
		return "an array"
	}
	return "a scalar"
}

// lastStep is the step a write named under its parent — what the refusal quotes back.
func lastStep(at Path) string {
	segs := at.segments()
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}

// Head is the highest commit this session knows of, WITHOUT asking the store.
//
// It is libctl's mark (KnownCommit), raised by every answer that carries a commit — reads,
// writes, watch events, and the ping's pong. So a busy session tracks the head from traffic it
// already makes, and an idle one from the heartbeat it already sends: no watch, no poll, no
// read. That is what makes it safe on a timer, which Rev is not — Rev syncs, and its sync opens
// a watch on the ROOT that logd grants one of per call.
//
// It answers "where is the store", which advances on a write whether or not that write changed
// anything. Rev answers "when did anything I care about last change", which does not: a
// same-value write raises no event (TestSameValueWriteRaisesNoCommit). Two questions, and this
// is the one nothing has to pay for.
func (s *LogdStore) Head() int64 {
	return s.sess.KnownCommit()
}

// writePath renders a write's target as a kpath: the path's own names, then the access fragment
// when the target is an array element.
//
// An access takes NO dot before it, which is the kpath grammar's shape: `…."1".votes[0]`, and
// `…."1"[0]` when the node is itself an array.
func (s *LogdStore) writePath(at Path) string {
	path := s.refPath(at.Ref)
	if at.Access == "" {
		return path
	}
	if StepIsAccess(at.Access) {
		return path + at.Access
	}
	return path + "." + at.Access
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

// preconditionAt folds every precondition into ONE match, which is what keeps the checks
// atomic with the write (docd allows a single match path), and asks it AS DEEP AS THE
// QUESTIONS ALLOW.
//
// THE DEPTH IS THE COST. A precondition is evaluated by reading the value at its path and
// matching the pattern against it, so one asked at the root reads the whole verse — and
// nearly every write carries one, since a write that descends carries a guard about the place
// it descends through. Measured on a 1500-entity store: 24ms a write, growing with what the
// store holds, against 0.8ms for the same write with no precondition. Asked under the
// branches' common prefix instead, a write reads the slice it is about.
//
// Presence preconditions about a field nest into a single tree. The others cannot, and each
// becomes its own branch with the branches ANDed: an ABSENCE has no key at its path to carry
// a pattern, so it is a negated nesting — "this object does not have <id>" — and a pattern at
// an ELEMENT hangs off an `!at` rather than a key, which a shared tree would overwrite
// (elemPattern).
func (s *LogdStore) preconditionAt(pre []pathPattern) *logdapi.PathData {
	prefix := commonFloor(pre)
	return &logdapi.PathData{Path: s.pathAt(prefix), Data: patternUnder(pre, len(prefix))}
}

// floor is how many of a branch's leading segments may become part of the precondition's
// PATH rather than of its pattern.
//
// A question about a VALUE can be asked at the value: descendablePath asks what stands at the
// parent, and reads the same answer from the parent's own path — its truth table survives the
// collapse there, the absent column included, because an absent path is read as null.
//
// Every other question is about a KEY — whether one is there (absentPath), or what is under
// one (a require's pattern, an element's `!at`) — and a key is only observable from the object
// that holds it: at the key's own path, a key holding null and no key at all are both null. So
// those stop one segment short of their own place.
func (p pathPattern) floor() int {
	n := len(p.at.segments())
	if p.object {
		return n
	}
	return max(n-1, 0)
}

// commonFloor is the deepest segment prefix EVERY branch can be asked below — the longest
// common prefix of the branches, bounded by the shallowest floor among them.
func commonFloor(pre []pathPattern) []string {
	if len(pre) == 0 {
		return nil
	}
	prefix := pre[0].at.segments()[:pre[0].floor()]
	for _, p := range pre[1:] {
		segs := p.at.segments()[:p.floor()]
		if len(segs) < len(prefix) {
			prefix = prefix[:len(segs)]
		}
		for i := range prefix {
			if segs[i] != prefix[i] {
				prefix = prefix[:i]
				break
			}
		}
	}
	return prefix
}

func patternUnder(pre []pathPattern, depth int) *ir.Node {
	var branches []*ir.Node
	// EVERY PATTERN HOLDS, each in a tree where it displaces nothing. Nested into one tree, a
	// second require about the same place replaced the first, and one about a field under a
	// place another require asked about replaced that one: `[!not {status: answered},
	// !not {status: orphaned}]` reached logd as the orphaned half alone (git-issue
	// 874azhtjh12krn1xqsn0). A pattern that would collide starts a tree of its own, and the
	// trees are branches like any other, all of which must hold.
	var present []patTree
	for _, p := range pre {
		segs := p.at.segments()[depth:]
		switch {
		case p.sub != "":
			branches = append(branches, elemPattern(p, segs))
		case p.absent:
			branches = append(branches, absentPath(segs))
		case p.object:
			branches = append(branches, descendablePath(segs))
		default:
			i := slices.IndexFunc(present, func(t patTree) bool { return t.fits(segs) })
			if i < 0 {
				present, i = append(present, patTree{}), len(present)
			}
			nestPattern(present[i], segs, p.pattern)
		}
	}
	for _, t := range present {
		branches = append(branches, t.node())
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

// pathPattern is one precondition reduced to what the match needs: a place, and either a
// pattern to evaluate there or the question of whether anything is there at all.
type pathPattern struct {
	at Path
	// sub is the place inside the node at `at` when it is one an address of names cannot spell —
	// an array element, and anything under it. Set, it is what the pattern is about, and at
	// names the node the walk to it starts from (Path.Access).
	sub     string
	pattern Payload
	absent  bool // nothing stands at at; pattern is unused
	object  bool // nothing stands at at, or an object does; pattern is unused
}

// absentPath renders "nothing stands at ref": there is no key at the path, which is a negated
// presence (a null pattern is the presence test).
//
// ONE SHAPE, not two. This used to OR a second branch — a key holding a null, on the belief
// that a deleted ref keeps its key with a tombstone in it — so that a claim released by Delete
// could be taken again. The belief was wrong: logd EXECUTES a `!delete` by removing the key, so
// a deleted ref reads back not_found and matches the first branch like any never-written one.
// The second branch never fired, and releaseRun/claimRun round-trip on the first alone
// (TestClaimAfterReleaseIsAbsentByTheKey).
//
// Dropping it is not just tidying, because a null is a legal payload now. That branch was
// `!irtype null`, which asks whether the value here has type null — true of an entity that
// stands there holding one. A create precondition would have passed straight over a live
// entity and clobbered it. (Require.Absent is the other half of the same correction.)
func absentPath(segs []string) *ir.Node {
	noKey := patTree{}
	nestPattern(noKey, segs, ir.Null()) // negated: no key at the path at all
	return noKey.node().WithTag("!not")
}

// elemPattern renders a precondition about an array ELEMENT: the pattern hangs off an `!at` at
// the entity that holds it — `{demo: {pr: {1: !at(votes[0]) <pattern>}}}` — rather than nesting
// one key deeper, since an index is not a key and there is nothing to nest under.
//
// IT SAYS NOTHING ABOUT THE OTHER ELEMENTS, which is why it is `!at` and not an array-shaped
// pattern. A bare array pattern matches POSITIONALLY AND BY LENGTH, so asking about `votes[1]`
// with `[null, {choice: approve}]` would also swear the array holds exactly two ballots — a
// guard on one vote refusing because somebody else cast another. `!at` walks the kpath and
// applies the pattern where it lands: one element, one question, and a ballot appended beside
// it is nobody's business. That is the reading a nested field pattern already gives, where a
// sibling field is not asked about either.
//
// ABSENCE NEGATES THE WHOLE NESTING rather than the `!at`, because a path that names nothing
// fails whatever it says: `!not {…: !at(votes[9]) null}` holds when the element is not there,
// and equally when the array, the entity, or anything above them is not — which is what
// absentPath answers for a field. What follows is then refused by the substrate, which knows
// whether an array stands there and says so with its length.
//
// ITS OWN BRANCH, like absentPath and descendablePath: the leaf is a tagged node rather than a
// subtree, and patTree.child would replace it with a fresh map the moment another precondition
// nested beneath the same entity.
func elemPattern(p pathPattern, segs []string) *ir.Node {
	pattern := p.pattern
	if p.absent || pattern == nil {
		pattern = Present() // a null matches any value, so only a missing element fails it
	}
	// UNCOMMENTED FIRST, and cloned because composing the tag writes to the node while the
	// pattern is the caller's. A head comment wraps the value it precedes, and a match sees
	// under a wrapper before it reads a tag — so a tag composed onto the wrapper would be
	// dropped, and the pattern asked about the ENTITY rather than the element. Nothing is lost
	// with the wrapper: a comment describes a value and no question about the data sees it.
	leaf := ir.Uncomment(pattern).Clone()
	leaf.Tag = ir.TagCompose("!at", []string{p.sub}, leaf.Tag)
	t := patTree{}
	nestPattern(t, segs, leaf)
	if p.absent {
		return t.node().WithTag("!not")
	}
	return t.node()
}

// descendablePath renders "nothing stands here, or what does can be descended through" — what
// a write reaching past this point needs to assume, and no more than that.
//
// `!not {…parent: !not.or [{}, !irtype null]}`, and every layer is load-bearing. `{}` matches
// any object and nothing else; `!irtype null` matches a null; the `!not.or` of the two matches
// an array or a scalar, which are exactly the values a descent would replace; and a pattern at
// a key that is not there fails whatever it says, so the outer negation answers TRUE for an
// absent parent. Checked, all five (TestAWriteDescendsOnlyThroughAValueThatStands):
//
//	object true · array FALSE · scalar FALSE · null true · absent true
//
// THE ABSENT COLUMN IS THE DESIGN, not a concession. "A write does not create the path it
// descends through" was the stricter rule and it cannot be had: an address is a path of any
// depth and entity-hood is a lens over one document, so a package deriving
// `verse-dev.drift.head.abc` where `verse-dev.drift.head` was never written IS the write that
// rule forbids. Twenty-odd tests across entity, trigger and source said so when it was tried.
//
// THE NULL COLUMN IS A DECISION. A null payload is a value (git-issue a1k0x5r0h12ksbhxg1n0), so
// descending through one does replace something — but nothing a reader can read, and absence
// reads back as a bare null from a path whose ancestors do not resolve, so refusing it would
// refuse creation under a different name.
//
// ITS OWN BRANCH, like absentPath and for the same reason: nestPattern merges into a shared
// tree and patTree.child replaces a leaf with a fresh subtree, so this guard and a caller's own
// pattern beneath it would silently overwrite one another.
func descendablePath(segs []string) *ir.Node {
	admissible := []*ir.Node{
		ir.FromMap(map[string]*ir.Node{}), // an object: descend into it
		ir.Null().WithTag("!irtype"),      // a null: nothing readable to lose
	}
	// ASKED AT THE PARENT ITSELF, where the two negations cancel: `!not {p: !not.or […]}` says
	// "p is not one of the values a descent would replace", and read at p's own path that is
	// `!or […]` — p IS one of the values a descent may pass through. The absent column comes
	// out the same, because an absent path is read as null and a null is admissible.
	if len(segs) == 0 {
		return ir.FromSlice(admissible).WithTag("!or")
	}
	t := patTree{}
	nestPattern(t, segs, ir.FromSlice(admissible).WithTag("!not.or"))
	return t.node().WithTag("!not")
}

// nestPattern places pattern at m[segs[0]][segs[1]]…, building intermediate maps — the
// nested shape a document has below the path the match is asked at. Raw segment names are
// used because those are the field names docd stores; seg-quoting is only for kpath request
// strings, not for stored fields. The segments are what is left of a branch's address once
// the precondition's own path has taken its prefix, so an empty tail nests nothing.
func nestPattern(root patTree, segs []string, pattern Payload) {
	if len(segs) == 0 {
		return
	}
	t := root
	for _, seg := range segs[:len(segs)-1] {
		t = t.child(seg)
	}
	t[segs[len(segs)-1]] = pattern
}

// segments is the path as document field names: the address's own segments — its reserved root
// included, since that is a level of the document like any other — then each field, stopping at
// the first empty one. A ref with no ID addresses a whole kind subtree (a motion's vote log),
// which is why the tail may be short rather than invalid.
//
// Asking the REF for its segments is what keeps a precondition pointed at the node it names. An
// absence check built any other way asks about a place where nothing stands, so a create-only
// write always fires — and a cover's read position resets every time a question is posed.
func (p Path) segments() []string {
	segs := p.Ref.Segs()
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg == "" {
			return out
		}
		out = append(out, seg)
	}
	return out
}

// patTree is the nesting under construction: branches are patTrees, LEAVES ARE NODES. It is
// not a map[string]any converted at the end, because that conversion is what used to flatten
// a pattern — a scalar, an operator, a `!irtype null` — into something gomap could represent, i.e.
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

// fits reports whether a pattern nested at segs would displace nothing: no pattern stands there
// already, and none stands at a place segs runs through.
func (t patTree) fits(segs []string) bool {
	for i, seg := range segs {
		v, ok := t[seg]
		if !ok {
			return true
		}
		c, sub := v.(patTree)
		if i == len(segs)-1 || !sub {
			return false
		}
		t = c
	}
	return true
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
	w, err := s.sess.Watch(ctx, s.root, &libctl.WatchOptions{WaitIfAbsent: true})
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
// a single Match on the root subtree.
//
// It walks a MATERIALIZED document, so there are no tombstones in it to skip: a deleted entity
// has no key (logd removes it) and is simply not there to walk. That is why this reads with
// walkEntityDoc rather than the delta walk — every leaf it finds is a payload, null included.
func (s *LogdStore) List(pred func(Ref) bool) []Entity {
	out, _ := s.ListErr(pred)
	return out
}

// ListErr is List, answering with the error THIS read failed with.
//
// The pair exists because `readErr` is one field for the whole store and every reader shares it
// — the engine, the sources and a request in flight — so "the last read failed" is not the same
// question as "my read failed". Whichever one a caller asks, it gets the other's answer as soon
// as somebody else reads in between: a neighbour's success hides this read's failure, and a
// neighbour's failure condemns a read that was fine. A request that could not read its own store
// then answered 200 with an empty document (git-issue tyr7f6dhh12ksdd4gnn0).
//
// The store-wide flag stays for the callers that are asking about the store rather than about a
// read of their own — a charter restore deciding whether "no rules" means no rules
// (entity.ReadFailed, trigger/charter.go).
func (s *LogdStore) ListErr(pred func(Ref) bool) ([]Entity, error) {
	out, _, err := s.ListErrCommit(pred)
	return out, err
}

// ListErrCommit is ListErr, and the commit the listing was served at — zero where the store
// said none (GetCommit).
func (s *LogdStore) ListErrCommit(pred func(Ref) bool) ([]Entity, int64, error) {
	ctx, cancel := s.reqCtx()
	defer cancel()
	node, commit, err := s.sess.MatchCommit(ctx, seg(s.root))
	// A VERSE NOBODY HAS WRITTEN TO answers not_found here, since go-tony v0.0.201 — the root is
	// a key like any other and nothing has put it there yet. It is absence, not an outage, and
	// reporting it as one told an operator on their first read that the store was unavailable.
	if isNotFound(err) {
		s.noteRead(nil)
		return nil, 0, nil
	}
	s.noteRead(err)
	if err != nil || node == nil {
		return nil, commit, err
	}
	var out []Entity
	for _, e := range walkEntityDoc(node) {
		if pred == nil || pred(e.Ref) {
			out = append(out, e)
		}
	}
	return out, commit, nil
}

// ListAt enumerates the nodes at a stated depth under a prefix — see Reader.ListAt for why the
// depth is the caller's.
//
// It reads the SUBTREE rather than the root: a question about `verse-dev.drift.head` has no
// business materializing the whole verse, and the prefix is exactly the path the substrate
// routes on.
func (s *LogdStore) ListAt(at []string, depth int, pred func(Ref) bool) []Entity {
	out, _ := s.ListAtErr(at, depth, pred)
	return out
}

// ReadAt is the node standing at a path, in one match there — see Reader.ReadAt. Not-found and a
// conflicting ancestor are nothing standing (isNotFound); anything else that fails is the read
// failing, and says so.
func (s *LogdStore) ReadAt(at []string) (*ir.Node, bool, error) {
	node, ok, _, err := s.ReadAtCommit(at)
	return node, ok, err
}

// ReadAtCommit is ReadAt, and the commit the read was served at — zero where the store said
// none (GetCommit).
func (s *LogdStore) ReadAtCommit(at []string) (*ir.Node, bool, int64, error) {
	ctx, cancel := s.reqCtx()
	defer cancel()
	path := s.pathAt(at)
	node, commit, err := s.sess.MatchCommit(ctx, path)
	if isNotFound(err) {
		s.noteRead(nil)
		return nil, false, 0, nil
	}
	s.noteRead(err)
	if err != nil {
		return nil, false, 0, fmt.Errorf("read %s: %w", JoinSegs(at), err)
	}
	if node == nil {
		return nil, false, commit, nil
	}
	return nodeToPayload(node), true, commit, nil
}

// PathsAt is every address beneath at, deepest last within each branch — the listing
// `verse data --paths` prints — answered BY THE STORE rather than by walking a document it
// sent. depth bounds how far beneath `at` the walk goes: 0 is `at` alone, so the listing is
// empty, 1 is its children, and Unbounded is everything.
//
// THE NUMBER IS THE STORE'S OWN. A depth counted from the address is what logd's descent takes
// (api.MatchRequest.Depth), so the reader's number is the one on the wire and nothing here
// converts between a ceiling and a bound.
//
// The store knows the names: a descent (`<at>..`) with `return: path` answers every node
// beneath, each once and in document order, reading no node at all to do it -- one table per
// container it enters, however large the containers are. So a listing of ten thousand records
// costs the tables rather than ten thousand reads, in one paged request rather than one per
// container, and never materializes the subtree -- which is also how it lists a container too
// large for a read's budget to answer at all.
//
// AN ARRAY IS A LEVEL LIKE ANY OTHER: its members are positions, and the descent enters them as
// it enters an object's fields. Identity is the store's to give (!logd-key), a `!key` tag is
// refused on the way in (checkNotKeyed), and the verse surface for the store's keying is not
// designed yet -- so nothing here has to read an array to find out what its elements are called.
//
// The addresses beneath an element are the same ones the document walk spells, since below an
// element a name extends the ACCESS rather than the address (entity.Path).
func (s *LogdStore) PathsAt(at []string, depth int) ([]Path, error) {
	out, _, err := s.PathsAtCommit(at, depth)
	return out, err
}

// PathsAtCommit is PathsAt, and the commit the listing was served at: the one descent's, since
// every page of it reads the commit its first did. Zero where the store said none (GetCommit).
func (s *LogdStore) PathsAtCommit(at []string, depth int) ([]Path, int64, error) {
	if depth == 0 {
		return nil, 0, nil // the node alone, and a listing is what stands BENEATH it
	}
	ctx, cancel := s.reqCtx()
	defer cancel()
	path := s.pathAt(at)
	// ONE DESCENT, `<at>..`, answers every node beneath in document order — a node before what
	// is under it, and at each level the store's order — which is the walk a listing prints,
	// in one paged request rather than one per container. A depth bounds it on the server, so
	// what is past the bound is neither walked nor shipped.
	q := libctl.SetQuery{Path: path + "..", Return: logdapi.ReturnPath}
	if depth > 0 {
		d := depth
		q.Depth = &d
	}
	var out []Path
	commit, err := s.sess.MatchEachQuery(ctx, q, func(m libctl.SetMember) error {
		p, err := s.pathOfMember(m.Path)
		if err != nil {
			return err
		}
		if p.Access == "" && slices.Equal(p.Ref.Segs(), at) {
			return nil // the node itself: a listing is what is beneath it
		}
		out = append(out, p)
		return nil
	})
	if isNotFound(err) {
		s.noteRead(nil)
		return nil, 0, nil // nothing stands there, which is an answer (see iterTypeAt)
	}
	s.noteRead(err)
	if err != nil {
		return nil, 0, fmt.Errorf("list %s: %w", JoinSegs(at), err)
	}
	return out, commit, nil
}

// pathOfMember is the address a member of a descent stands at, from the store's own spelling of
// its path: the root comes off, the leading field names are the address, and from the first
// bracket segment on everything is ACCESS inside the node that holds it (entity.Path) — a name
// after an element extends the access rather than the address.
func (s *LogdStore) pathOfMember(p string) (Path, error) {
	kp, err := kpath.Parse(p)
	if err != nil {
		return Path{}, fmt.Errorf("a member's path %q: %w", p, err)
	}
	if kp == nil || kp.Field == nil {
		return Path{}, fmt.Errorf("a member's path %q does not start at the root", p)
	}
	var names []string
	x := kp.Next // past the root segment
	for ; x != nil && x.Field != nil; x = x.Next {
		names = append(names, *x.Field)
	}
	return Path{Ref: RefAt(names...), Access: x.String()}, nil
}

// StandsAt reports whether a node stands at a path, asked of the store's own index: a match that
// returns the node's kind and nothing of what it holds.
func (s *LogdStore) StandsAt(at []string) (bool, error) {
	ctx, cancel := s.reqCtx()
	defer cancel()
	kind, err := s.iterTypeAt(ctx, s.pathAt(at))
	return kind != "", err
}

// iterTypeAt is what kind of node stands at path — the kind that says what is under it and which
// wildcard reaches it — or "" when nothing stands there. Nothing is an answer, not a failure:
// listing a slice before anything is written to it is what an engine and an install both do.
func (s *LogdStore) iterTypeAt(ctx context.Context, path string) (string, error) {
	kind := ""
	_, err := s.sess.MatchEachReturning(ctx, path, nil, logdapi.ReturnIterType, func(m libctl.SetMember) error {
		kind = m.IterType
		return nil
	})
	if isNotFound(err) {
		s.noteRead(nil)
		return "", nil
	}
	s.noteRead(err)
	if err != nil {
		return "", fmt.Errorf("list %s: %w", path, err)
	}
	return kind, nil
}

// PathsMatching answers a listing over a PATH — `demo.pr.*`, `demo..stage` — as one descent on
// the wire, which is what the store's own depth exists for: the path, a `..` after it where it
// does not end in one already, and the depth exactly as the reader gave it. Nothing here counts.
// A depth is one budget for the whole path, counted from the node the path names, and what that
// means for a path with more than one `..` is logd's to say and is said once, there.
//
// A PREDICATE IS ASKED OF WHAT THE PATH REACHED, not of everything beneath it: the store's
// pattern is matched against each member of a set, so the path is read first with the pattern —
// which nodes it keeps — and the descent is kept to those. Both come back in document order, a
// node before what is under it, so one pass pairs them.
//
// THE PATTERN IS MATCHED BY THE STORE, AGAINST WHAT THE STORE HOLDS, which for a reader who may
// not read all of a node is more than they may ask about: a row kept because of a field withheld
// from them says what the field holds to anybody willing to guess. So a view narrows the answer
// (Narrow): each row the pattern kept is asked again as its reader reads it, and whether the
// path reached anything is asked of what they may see.
func (s *LogdStore) PathsMatching(at *kpath.KPath, depth int, pattern *ir.Node, as *Narrow) ([]Path, bool, error) {
	prefix, rest := LiteralPrefix(at)
	ctx, cancel := s.reqCtx()
	defer cancel()
	asked := seg(s.root)
	if spelled := at.String(); spelled != "" {
		if !strings.HasPrefix(spelled, ".") && !strings.HasPrefix(spelled, "[") &&
			!strings.HasPrefix(spelled, "{") && !strings.HasPrefix(spelled, "(") {
			asked += "."
		}
		asked += spelled
	}
	descends := hasDescend(rest)
	bound := func(q libctl.SetQuery, has bool) libctl.SetQuery {
		// A depth on a path with no `..` is refused by the store, and means nothing: what the
		// path reaches is then fixed by its length.
		if depth >= 0 && has {
			d := depth
			q.Depth = &d
		}
		return q
	}
	// read gathers a read's members before handing them on, so that the view is told the commit
	// the read was served at (Narrow.Served) before it is asked about any of them — and so that
	// what it asks, which may read the store again (Keeps), is not asked from inside a set read
	// that is still arriving.
	read := func(q libctl.SetQuery, fn func(string) error) error {
		var members []string
		commit, err := s.sess.MatchEachQuery(ctx, q, func(m libctl.SetMember) error {
			members = append(members, m.Path)
			return nil
		})
		if isNotFound(err) {
			err = nil // nothing stands there, which is an answer
		}
		s.noteRead(err)
		if err != nil {
			return fmt.Errorf("list %s: %w", at, err)
		}
		if len(members) > 0 && as != nil && as.Served != nil {
			if err := as.Served(commit); err != nil {
				return err
			}
		}
		for _, m := range members {
			if err := fn(m); err != nil {
				return err
			}
		}
		return nil
	}
	var kept map[string]bool
	if pattern != nil {
		kept = map[string]bool{}
		q := bound(libctl.SetQuery{Path: asked, Pattern: pattern, Return: logdapi.ReturnPath}, descends)
		if err := read(q, func(member string) error {
			if as != nil && as.Keeps != nil {
				p, err := s.pathOfMember(member)
				if err != nil {
					return err
				}
				if ok, err := as.Keeps(p); err != nil || !ok {
					return err
				}
			}
			kept[member] = true
			return nil
		}); err != nil {
			return nil, false, err
		}
		if len(kept) == 0 {
			// The filter kept nothing: whether the PATH reached anything is a second question,
			// and the one that tells a wrong address from an honest empty answer — asked of what
			// the reader may see, since a node they may not is one they may not learn stands.
			reached := false
			q := bound(libctl.SetQuery{Path: asked, Return: logdapi.ReturnPath}, descends)
			err := read(q, func(member string) error {
				if reached || as == nil || as.Sees == nil {
					reached = true
					return nil
				}
				p, err := s.pathOfMember(member)
				if err != nil {
					return err
				}
				reached, err = as.Sees(p)
				return err
			})
			return nil, reached, err
		}
	}
	listed := asked
	if !strings.HasSuffix(listed, "..") {
		listed += ".."
	}
	var out []Path
	// The kept nodes this member stands under, innermost last: a kept node may stand beneath
	// another, and leaving the inner one is not leaving the outer.
	var under []string
	reached := false
	err := read(bound(libctl.SetQuery{Path: listed, Return: logdapi.ReturnPath}, true), func(member string) error {
		reached = true
		if kept != nil {
			for len(under) > 0 && !beneath(member, under[len(under)-1]) {
				under = under[:len(under)-1]
			}
			if kept[member] {
				under = append(under, member)
			}
			if len(under) == 0 {
				return nil // not a node the filter kept, nor anything under one
			}
		}
		p, err := s.pathOfMember(member)
		if err != nil {
			return err
		}
		if rest == nil && p.Access == "" && slices.Equal(p.Ref.Segs(), prefix) {
			return nil // a path naming one node does not list that node
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, reached, nil
}

// beneath reports whether a store path stands under another: the longer one carries on with a
// separator, so `a.bc` is not beneath `a.b`.
func beneath(member, under string) bool {
	if !strings.HasPrefix(member, under) || len(member) == len(under) {
		return false
	}
	switch member[len(under)] {
	case '.', '[', '{', '(':
		return true
	}
	return false
}

// ListAtErr is ListAt, answering with the error THIS read failed with. See ListErr.
func (s *LogdStore) ListAtErr(at []string, depth int, pred func(Ref) bool) ([]Entity, error) {
	out, _, err := s.ListAtErrCommit(at, depth, pred)
	return out, err
}

// ListAtErrCommit is ListAtErr, and the commit the listing was served at — zero where the store
// said none (GetCommit).
func (s *LogdStore) ListAtErrCommit(at []string, depth int, pred func(Ref) bool) ([]Entity, int64, error) {
	if depth < len(at) {
		return nil, 0, nil
	}
	ctx, cancel := s.reqCtx()
	defer cancel()
	path := s.pathAt(at)
	node, commit, err := s.sess.MatchCommit(ctx, path)
	// NOT_FOUND IS AN ANSWER, not an outage: it says nothing stands at the path, and a read that
	// FAILS says the store could not be read (libctl's own distinction). They are only ever
	// confused upward: `unreadable` turns a recorded read error into a 503, so a read at a path
	// nothing stands at reported the verse as unavailable.
	//
	// IT ARRIVES AS AN EMPTY ANSWER, deliberately. Listing or watching a slice before anything
	// is written to it is what an engine, a source and an install all do, and none of them wants
	// an error for it. The one caller that must tell "nothing stands here" from "this holds
	// nothing" is the read a person types an address into, and it reads the node (ReadAt).
	if isNotFound(err) {
		s.noteRead(nil)
		return nil, 0, nil
	}
	s.noteRead(err)
	if err != nil || node == nil {
		return nil, commit, err
	}
	// Match answers the content AT the path it was given — not a document wrapped in it — so the
	// walk starts here and the remaining depth is what is left to descend. (List asks at the
	// root and walks the system keys directly, which is the same fact one level up.)
	var out []Entity
	nodesAt(node, depth-len(at),
		func(segs []string, v *ir.Node) {
			ref := RefAt(append(append([]string{}, at...), segs...)...)
			if pred == nil || pred(ref) {
				out = append(out, Entity{Ref: ref, Payload: nodeToPayload(v)})
			}
		})
	return out, commit, nil
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
// structure: logd owns the delta encoding and is free to change it — a scoped watch does
// not even deliver the patch that was committed — so verse can't and shouldn't decode it
// by hand, and decoding it is how the first appearance came to be silently dropped. A
// burst of events is applied then diffed once. unsub (or closing the store) stops it.
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
func (s *LogdStore) WatchSubtree(depth int, prefix ...string) (<-chan Commit, func(), error) {
	prefix = trimEmpty(prefix)
	return s.WatchPattern(prefix, PrefixPattern(prefix, depth), true, false)
}

// trimEmpty drops a trailing empty segment, which names no level: the two-argument
// (system, kind) shape still speaking, where `WatchSubtree("demo", "")` is a whole-system watch.
func trimEmpty(prefix []string) []string {
	for len(prefix) > 0 && prefix[len(prefix)-1] == "" {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}

// WatchSubtreeAt is WatchSubtree for a caller that did NOT choose the path — a person naming one
// at a command line — and so wants a path holding nothing refused rather than waited on.
//
// The refusal is the substrate's: since go-tony v0.0.201 a watch on such a path is refused unless
// the client asks to wait, which is the same answer a read of it gives. So this costs no extra
// read; it is one flag on the request verse already makes, and the alternative — reading the path
// first to decide whether to open the stream — would be verse re-implementing what the store
// does at the point it is asked.
// NO INIT EVENT: a subtree watch normally opens with the full state at the starting commit, and
// this one joins the stream at its next change, as `Observe` does. A caller that wants what
// already stands there asks WatchPattern for it, which is what `verse watch --init` does.
func (s *LogdStore) WatchSubtreeAt(depth int, waitIfAbsent bool, prefix ...string) (<-chan Commit, func(), error) {
	prefix = trimEmpty(prefix)
	return s.WatchPattern(prefix, PrefixPattern(prefix, depth), waitIfAbsent, true)
}

// WatchPattern is the watch a PATTERN opens: it watches the subtree at `prefix` and reports the
// nodes the pattern reaches whose node differs, commit by commit. The two above are this with
// the pattern a prefix and a depth spell; a pattern that DESCENDS (`demo..`) has no one depth
// and is exactly why the watch takes the question rather than a number.
//
// THE PLACE AND THE QUESTION ARE TWO ARGUMENTS, because they are two things: a rule about
// `git.head.verse` watches `git.head` — a watch AT the node would see that node's own changes
// and never its appearance — and reports the one node its pattern names.
func (s *LogdStore) WatchPattern(prefix []string, at *kpath.KPath, waitIfAbsent, noInit bool) (<-chan Commit, func(), error) {
	ch, unsub, _, err := s.watchPattern(prefix, at, waitIfAbsent, noInit)
	return ch, unsub, err
}

// WatchSubtreeProgress is WatchSubtree for a consumer that keeps a copy of the slice and has to
// know how far it is current (Attribution.ensure). progress asks the watch standing now to say,
// in its stream, when it is current through the head, and answers the head: the stream then
// carries a commit marked as progress (Commit.progress) at that head or later, after every
// commit at or below it. A watch that ends before it answers carries none; the stream goes on
// with the re-established watch's State, marked as a resync. The error is the watch not being
// asked — being re-established, or a store that cannot say (`unsupported`).
func (s *LogdStore) WatchSubtreeProgress(depth int, prefix ...string) (<-chan Commit, func(), func(context.Context) (int64, error), error) {
	prefix = trimEmpty(prefix)
	return s.watchPattern(prefix, PrefixPattern(prefix, depth), true, false)
}

func (s *LogdStore) watchPattern(prefix []string, at *kpath.KPath, waitIfAbsent, noInit bool) (<-chan Commit, func(), func(context.Context) (int64, error), error) {
	prefix = trimEmpty(prefix)
	// The path as segments as well as a kpath: every event is rooted at the watched path
	// and the document is rooted at the store root, so the segments are what bridges the two
	// (see nestAt).
	segs := append([]string{s.root}, prefix...)
	path := seg(s.root)
	for _, p := range prefix {
		path += "." + seg(p)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	// THE STATE IS ALWAYS ASKED FOR, and noInit says only whether it is SAID. Every commit this
	// watch emits carries an entity's whole state (Event.To), and that state is this watch's copy
	// of the slice with each delta applied: a copy that starts from nothing holds, for an entity
	// that stood before the watch opened, only the fields written since. So the watch opens on
	// the state the substrate offers (as a re-established one does), and a caller that wanted to
	// join at the next change gets that state taken in silently below rather than reported.
	w, err := s.establish(ctx, path, &libctl.WatchOptions{WaitIfAbsent: waitIfAbsent})
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	// standing is the watch progress is asked of: this one, then each that re-establishes it,
	// and none while it is being re-established.
	var standingMu sync.Mutex
	standing := w
	stand := func(nw *libctl.Watch) {
		standingMu.Lock()
		standing = nw
		standingMu.Unlock()
	}
	progress := func(ctx context.Context) (int64, error) {
		standingMu.Lock()
		cur := standing
		standingMu.Unlock()
		if cur == nil {
			return 0, fmt.Errorf("watching %s: the watch is being re-established", path)
		}
		return cur.Progress(ctx)
	}
	out := make(chan Commit, 256)
	go func() {
		defer close(out)
		// doc is the watched subtree as a root-rooted document, maintained by APPLYING
		// each watch event: a State (initial or reconnect) replaces it; a Patch is
		// applied with tony.Patch. We never interpret the patch's structure — which
		// operations a delta is spelled with, and in what order, is logd's to choose —
		// we apply it opaquely and read entities out of the reconstructed object.
		//
		// prev is the last emitted snapshot, and it starts EMPTY — including under the
		// opening State, so the first diff reports the whole slice and the backlog fires.
		// Nothing here distinguishes the opening State from a reconnect's, and nothing has
		// to: after the first diff prev holds the slice, so a reconnect's State diffs to
		// the gap alone.
		doc := ir.Null()
		// prev is the document the last diff was taken against. It starts EMPTY, so the
		// first diff reports everything standing at the pattern.
		var prev *ir.Node
		pattern := []*kpath.KPath{at}
		// rev is the commit the reconstructed doc now stands at, stamped on every event the
		// next diff emits. Without it a subtree event carried no revision at all while a root
		// event carried its commit — the same stream, one half of it unable to say WHEN.
		// Nothing downstream could then ask a question about a change at the commit it
		// happened at, which is the whole affordance AsOf exists to offer.
		var rev int64
		// sent is the commit of the last commit sent, which a consumer that applied it holds the
		// place as of — and as of every commit after it that changed nothing at the pattern — and
		// resync says the event just applied was a State: the diff it makes is the net of every
		// commit since, not the changes of the one it is stamped with. Both ride each commit as
		// since and resync, for a consumer that keeps a copy of its own beside other reads of the
		// same place (Attribution.follow).
		var sent int64
		var resync bool
		// seeded is whether the opening State has been taken in: under noInit it is taken
		// into the copy and the snapshot alike, so the first diff says nothing about what
		// already stood, and the first change carries the whole of what it changed.
		seeded := !noInit
		send := func(c Commit) bool {
			select {
			case out <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}
		apply := func(ev *logdapi.WatchEvent) {
			resync = ev.Patch == nil
			if ev.Commit > rev {
				rev = ev.Commit
			}
			switch {
			case ev.Patch != nil:
				// A patch that fails to apply leaves doc unchanged and is reported; a
				// later State (e.g. on reconnect) resyncs it. We never decode the patch
				// to recover by hand (applyDelta). A patch that TOOK THE PATH AWAY is
				// applied like any other — that is how this copy comes to hold nothing —
				// so the Absent riding on it needs no branch of its own.
				doc = s.applyDelta(doc, nestAt(ev.Patch, segs))
			case ev.State != nil || ev.Absent:
				doc = nestAt(stateOf(ev), segs) // full current state of the subtree
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
			// One change walk (changesAt), with no delta to prune it: a State replaces the
			// document wholesale, so only the two documents can say what differs. It compares
			// with ir.Node.DeepEqual, not reflect.DeepEqual — a node carries Parent links that
			// say where it sits rather than what it holds, and a document rebuilt from a fresh
			// read wires them anew, which reflect would count as every node changing.
			changes := changesAt(pattern, childByKey(prev, s.root), childByKey(doc, s.root), nil, rev)
			prev = doc
			if len(changes) == 0 {
				return true
			}
			c := Commit{Rev: rev, Changes: changes, since: sent, resync: resync}
			sent = rev
			return send(c)
		}
		backoff := 100 * time.Millisecond
		// A watch can die WITHOUT SAYING SO, and then this loop would wait on a channel
		// nothing will ever send to again. See subtreeHealAfter: the heal timer is what
		// bounds how long that can last.
		heal := time.NewTimer(subtreeHealAfter)
		defer heal.Stop()
		healAfter := subtreeHealAfter
		// probe is a heal's ask in flight on w, nil while there is none; swallow counts the
		// progress events those asks draw, which are the pump's and not the consumer's.
		var probe <-chan error
		swallow := 0
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
					// Quiet for a whole interval. Either the slice is idle or the watch is
					// dead, and the watch itself is asked which: one request, off this loop so
					// the stream keeps draining meanwhile. The timer stands still until it
					// answers, or does not, below.
					if probe == nil {
						probe = askProgress(ctx, w)
						// Counted as sent, not as answered: the progress event an ask draws
						// can reach the stream before the ask's own answer is read here.
						swallow++
					}
				case err := <-probe:
					probe = nil
					// Back off while nothing changes either way, so an idle slice is asked
					// less often and a dark one is not re-established every interval.
					healAfter = min(healAfter*2, subtreeHealMax)
					heal.Reset(healAfter)
					if err != nil {
						break pump // no answer: dead, or as good as; re-establish
					}
				case ev, ok := <-w.Events():
					if !ok {
						break pump // watch ended; re-establish
					}
					if ev.Progress && swallow > 0 {
						// A heal's ask answered, which the answer itself already said. Not an
						// event about the slice, so neither the heal nor the consumer hears it.
						swallow--
						if ev.Commit > rev {
							rev = ev.Commit
						}
						continue
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
					if ev.Progress {
						// Current through its commit: nothing is applied, and the consumer that
						// asked is told, in order with every commit before it.
						if ev.Commit > rev {
							rev = ev.Commit
						}
						c := Commit{Rev: ev.Commit, since: sent, progress: true}
						sent = ev.Commit
						if !send(c) {
							w.Close()
							return
						}
						continue
					}
					if !seeded && ev.Patch == nil {
						apply(ev)
						prev = doc
						seeded = true
						continue
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
			stand(nil)
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
				nw, err := s.establish(ctx, path, &libctl.WatchOptions{WaitIfAbsent: true})
				if err == nil {
					w = nw
					stand(nw)
					// An ask still out on the watch that ended answers for nobody, and the
					// progress event it would have drawn never reaches this stream.
					probe, swallow = nil, 0
					subtreeReopens.Add(1)
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
	return out, cancel, progress, nil
}

// nestAt puts a watch event's node under the segments of the path it is rooted at, which is
// what makes it part of a document rooted further up.
//
// ONE ROOTING (session protocol 2). Every event of a watch — the State it opens on and each
// Patch after it — is rooted AT THE WATCHED PATH, with that path's own segments stripped: a
// watch on verse.demo.pr sees top-level fields that are ids. The documents this store reads
// entities out of are rooted at the store root, because a Ref is addressed from there and
// nothing below the root is fixed for the store as a whole. Nesting is the difference
// between the two rootings, and it is the same for both event shapes.
//
// A nil node is a path holding nothing and nests as a null, which reads out as no entities.
func nestAt(node *ir.Node, segs []string) *ir.Node {
	if node == nil {
		node = ir.Null()
	}
	for i := len(segs) - 1; i >= 0; i-- {
		node = ir.FromMap(map[string]*ir.Node{segs[i]: node})
	}
	return node
}

// stateOf is the value a state event opens on: what it carries, or — when the path holds
// nothing — an empty object. ABSENCE IS SAID BY THE WIRE, not spelled as a null: null is a
// value a path can hold, so a document seeded with one where nothing stands could not tell
// an entity holding null from a slice that is empty.
func stateOf(ev *logdapi.WatchEvent) *ir.Node {
	if ev.Absent {
		return ir.FromMap(map[string]*ir.Node{})
	}
	return ev.State
}

// nodesAt calls fn for every node exactly depth object levels below node — whatever stands there:
// an object, a list, a scalar, a null, an empty object — and for nothing shallower. It is what a
// LISTING answers: the nodes a pattern of that depth reaches. A
// value met above the depth is not at it; a read of its own path answers it.
func nodesAt(node *ir.Node, depth int, fn func(segs []string, v *ir.Node)) {
	var rec func(n *ir.Node, segs []string)
	rec = func(n *ir.Node, segs []string) {
		if len(segs) == depth {
			fn(segs, n)
			return
		}
		n = ir.Uncomment(n) // a comment is not a container
		if n == nil || n.Type != ir.ObjectType {
			return
		}
		for i, f := range n.Fields {
			rec(n.Values[i], append(append([]string{}, segs...), fieldName(f)))
		}
	}
	if depth >= 0 {
		rec(node, nil)
	}
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
	// Its scratch too, which logd's delete knows nothing of: scratch has no scopes, and the
	// scope's stands under a root of its own (scratchPath). Cleared first, so a scope whose
	// scratch could not be cleared still stands to be deleted again.
	if _, err := s.sess.ScratchWrite(ctx, scopeScratchRoot(s.root, name), deleteOp()); err != nil {
		return fmt.Errorf("clearing scope %s's scratch: %w", name, err)
	}
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
func (s *LogdStore) Observe(at ...*kpath.KPath) (<-chan Commit, func()) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	o := &observer{ch: make(chan Commit, 256), at: at}
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
	w, err := s.establish(wctx, s.root, &libctl.WatchOptions{WaitIfAbsent: true})
	if err != nil {
		wcancel()
		return // no observers get events until a later Observe retries the open
	}
	s.watchCancel = wcancel
	go s.runWatch(wctx, w)
}

// commonPrefix is the literal path every pattern starts with — where a replay of all of them can
// be read — and the root when they share none.
func commonPrefix(patterns []*kpath.KPath) []string {
	var out []string
	for i, p := range patterns {
		lit, _ := LiteralPrefix(p)
		if i == 0 {
			out = lit
			continue
		}
		n := 0
		for n < len(out) && n < len(lit) && out[n] == lit[n] {
			n++
		}
		out = out[:n]
	}
	return out
}

// ErrReplayTooOld is the store saying the cursor predates what it still retains: logd refuses
// a FromCommit watch under its own replay floor with a terminal `replay_compacted` event, and
// there is no replay to be had at that cursor by any means. It is the ONE failure a caller
// answers by resyncing to current state rather than by asking again.
//
// IT CARRIES THE FLOOR, in the wrapped error's own words: the refusal says "delta history is
// retained only from commit N", which is the one fact that lets a caller pick a cursor that
// works or tell a person what happened. It is prose rather than a field because that is how the
// substrate sends it — a reason code a caller switches on, and a message it reports — so verse
// reports it and does not parse it. A caller that would rather not name a number asks for a
// RELATIVE cursor, the last N commits, which the store resolves against its watermark and
// CLAMPS to the floor rather than refusing.
var ErrReplayTooOld = errors.New("the cursor is older than what the store retains")

// Since replays commits with Rev > from using a native logd FromCommit watch (a
// query by revision), in commit order, over the slice at `at` — the path segments under the
// root, or the whole verse when none are given. It is a pure read — it derives each event's Op
// from the delta alone (a tombstone is Delete, any value is Set) and mutates no store state, so
// repeated or concurrent calls are consistent. This is what keeps a hold costing a cursor
// rather than a growing buffer.
//
// One replayed delta is one Commit, so a catch-up delivers the same framing the live stream
// does and a consumer cannot tell which it is reading — which is the point: the two paths
// exist to recover each other.
//
// IT ANSWERS WITH THE ERROR IT FAILED WITH, and that is the whole of what it can say about
// itself. A replay is a WATCH, so everything that can refuse a watch refuses a catch-up: the
// cursor below the floor (ErrReplayTooOld), a consumer that cannot keep up with its own replay,
// a mount arriving or leaving, an establish that never completes inside the budget below, a
// store that cannot be reached at all. Reported as one bit, every one of those read as "your
// cursor is too old" — so a store that was DOWN was described to a person as a cursor problem,
// and the handler above it went looking for a retained window that was never the question
// (git-issue 3qf2z59dh12ksdazg9n0).
//
// PARTIAL COMMITS COME BACK WITH THE ERROR rather than instead of it. A replay that stops
// halfway has delivered real commits, and a cursor-keeping caller can advance over them and
// resume; what it must not be told is that it caught up. So the caller gets both, and decides.
//
// NARROW IT WHEN THE CALLER IS NARROW. The replay reads the document as it stood at the
// starting commit (docAt) and logd reads every patch in the range that touches the watched
// path, so both halves cost what the PATH holds rather than what the verse holds: a whole-verse
// catch-up on a large store is gigabytes of document and log, and it is what evicted staging's
// substrate four times in fifteen minutes. logd narrows before it reads (its index descends by
// path, and only the segments recorded at or under it are fetched), so the prefix is not a
// filter applied after the cost is paid.
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
func (s *LogdStore) Since(from int64, patterns ...*kpath.KPath) ([]Commit, error) {
	at := commonPrefix(patterns)
	s.ensureSynced()
	// No rev>=from early-out: without a standing watch rev may be stale, so the FromCommit
	// replay is authoritative — it streams commits after `from` and signals ReplayComplete
	// when caught up (empty when there is nothing new), and it is also what reports a range
	// logd can no longer replay.

	// Concurrent Since calls need no serialization: each replay is its own id-keyed watch,
	// so several may be in flight on this session at once, on this same path (see
	// LogdStore.sess). Since mutates no store state, so they stay independent.
	ctx, cancel := context.WithTimeout(s.ctx, replayBudget)
	defer cancel()
	path := s.pathAt(at)
	fc := from
	w, err := s.sess.Watch(ctx, path, &libctl.WatchOptions{FromCommit: &fc, NoInit: true, WaitIfAbsent: true})
	if err != nil {
		return nil, fmt.Errorf("replaying %s from %d: %w", path, from, err)
	}
	defer w.Close()

	// THE SERVER'S ANSWER, not the cursor we asked with. A relative cursor (`-N`, the last N
	// commits) is resolved by the store — against its watermark, clamped to its retained floor
	// — and neither of those is knowable here. `docAt(-2)` is not a revision; reading the
	// document at the resolved commit is the whole point of the server reporting it.
	start := from
	if rf := w.ReplayingFrom(); rf != nil {
		start = *rf
	}

	// TAKE THE REPLAY FIRST, LEVEL IT AFTER — and it is not a refactor, it is what makes a
	// long catch-up possible at all.
	//
	// A watch has a bounded buffer and logd FAILS a consumer that does not keep up (slow
	// consumer, mirroring the server's own behaviour). Levelling inside the receive put the
	// most expensive work in the verse — a patch applied to the whole document, then the
	// entity walk over the result — between one event and the next, so the buffer filled and
	// the store killed the replay partway. Measured on a docd-backed store, this loop with the
	// work inline was cut off after ~1000 events; draining first carries 2000 in 297ms. What
	// came back was a truncated catch-up reported as a failure the consumer could do nothing
	// about, at a cursor that was perfectly good.
	//
	// It costs no more than it did: `out` already holds every commit in the range, so the
	// deltas are the same order of memory, held for the length of the call rather than
	// released one at a time. The base read moves down here too, for the same reason — it is a
	// round trip, and doing it before the first receive spends the buffer's headroom on the
	// network.
	type delta struct {
		commit int64
		patch  *ir.Node
		author string
	}
	var replayed []delta
	drain := func() error {
		for {
			select {
			case <-ctx.Done():
				// WHAT REPLAYED, AND THAT IT DID NOT FINISH. Answering the partial catch-up
				// with no error said the consumer was caught up when it was cut off at the
				// budget, which is the silent under-delivery a cursor exists to make
				// impossible.
				return fmt.Errorf("replaying %s from %d: %w", path, from, ctx.Err())
			case ev, ok := <-w.Events():
				if !ok {
					return replayEnded(path, from, w.Err())
				}
				if ev.ReplayComplete {
					return nil // caught up to current
				}
				if ev.Patch != nil {
					replayed = append(replayed, delta{commit: ev.Commit, patch: ev.Patch, author: ev.Author})
				}
			}
		}
	}
	replayErr := drain()

	// A replay carries deltas, and an event owes its consumer the whole entity as of its
	// commit — so level it the way the live watch does: read the document as it stood AT the
	// commit the replay starts from, then advance it one commit at a time. That is one read
	// for the whole catch-up rather than one per event, and it makes a replayed event
	// indistinguishable from the live one it may be recovering.
	//
	// A FAILED REPLAY IS STILL LEVELLED, because the commits it did carry happened: a
	// cursor-keeping caller advances over them and asks again from where they end.
	doc := s.docAt(ctx, start, at...)
	if n := len(replayed); n > 0 {
		s.mu.Lock()
		if replayed[n-1].commit > s.rev {
			s.rev = replayed[n-1].commit
		}
		s.mu.Unlock()
	}
	out := make([]Commit, 0, len(replayed))
	for _, d := range replayed {
		// The replay watches AT the path, so its deltas are rooted there while a Ref is
		// addressed from the store root. They are nested back to it once, and everything
		// below reads them from there.
		patch := nestAt(d.patch, at)
		before := doc
		doc = s.applyDelta(doc, nestAt(patch, []string{s.root}))
		var changes []Event
		if len(patterns) > 0 {
			changes = changesAt(patterns, childByKey(before, s.root), childByKey(doc, s.root), patch, d.commit)
		}
		out = append(out, Commit{Rev: d.commit, Author: d.author, Changes: changes}.WithDelta(patch))
	}
	return out, replayErr
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
// A revision the store cannot answer at — out of range, beyond current, or older than
// compaction keeps exactly (matchAt) — is an error,
// unlike docAt's replay base, which degrades to an empty document because the replay
// carries every commit it needs. Here there is nothing to make up the difference: a
// snapshot that quietly read current state instead would be indistinguishable from one that
// worked, at exactly the moment being at the right commit is the whole requirement.
func (s *LogdStore) AsOf(rev int64, within ...[]string) (Snapshot, error) {
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
	// IT IS WHAT MAKES A READ AFFORDABLE, and the difference grows with the store. Measured on
	// an in-process store, small entities, narrowed to two three-segment places against the
	// whole root:
	//
	//	commits     root    narrowed
	//	   1000    161ms       ~1ms
	//	   2000   1262ms       ~2ms
	//	   3000   2421ms      ~37ms
	//	   6000   6259ms      ~40ms
	//
	// The narrowed read is flat and the whole-root read grows about 1.2ms per commit past 2000,
	// crossing sessionBudget on its own at around 9,500 — with no contention at all. So a caller
	// that reads at every commit narrows to what it reads, or it pays for every entity it cannot
	// see, on a store that only grows (scratchpad probe on git-issue sfwhh9sv).
	//
	// What makes a read cheaper still is not asking for one — see the roll-forward note in
	// git-issue x2wzvn8q.
	verse := map[string]*ir.Node{}
	for _, st := range within {
		if len(st) == 0 {
			continue
		}
		path := seg(s.root)
		for _, sg := range st {
			path += "." + seg(sg)
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
//
// AT THE REVISION ASKED, OR NOT AT ALL. Beyond compaction's cutoff logd answers a read at the
// first snapshot after the revision, which holds the revision's write and whatever came
// between, and says which commit it answered (tony-format cpqj2tf6h12kr5jxqxn0). That is
// history the store no longer retains exactly, which is an error here (Store.AsOf): a rule
// held at a gate would otherwise act on a world it was not evaluated against, and say nothing.
// "Nothing there" is answered the same way, carrying the commit it was found absent at, and
// absent later is not absent at rev. An older logd echoes rev, so against one this changes nothing.
func (s *LogdStore) matchAt(ctx context.Context, path string, rev int64) (*ir.Node, error) {
	node, at, err := s.sess.MatchAtCommit(ctx, path, rev)
	if err != nil {
		if !isNotFound(err) {
			return nil, fmt.Errorf("read %s at rev %d: %w", path, rev, err)
		}
		var se *logdapi.SessionError
		if errors.As(err, &se) && se.Commit != nil && *se.Commit != rev {
			return nil, notRetainedAt(path, rev, *se.Commit)
		}
		return nil, nil
	}
	if at != rev {
		return nil, notRetainedAt(path, rev, at)
	}
	return node, nil
}

func notRetainedAt(path string, rev, at int64) error {
	return fmt.Errorf("read %s at rev %d: compaction answers it at rev %d: %w", path, rev, at, ErrReplayTooOld)
}

func nodeOrEmpty(n *ir.Node) *ir.Node {
	if n == nil {
		return ir.FromMap(map[string]*ir.Node{})
	}
	return n
}

// graftAt places a subtree read at a PREFIX into the verse map being assembled, merging with a
// sibling already grafted beside it — a second kind under one system, or the world's half
// beside verse's own.
func graftAt(verse map[string]*ir.Node, prefix []string, node *ir.Node) {
	if len(prefix) == 1 {
		verse[prefix[0]] = node
		return
	}
	under := map[string]*ir.Node{}
	if cur, ok := verse[prefix[0]]; ok && cur != nil && cur.Type == ir.ObjectType {
		for i, f := range cur.Fields {
			under[f.String] = cur.Values[i]
		}
	}
	graftAt(under, prefix[1:], node)
	verse[prefix[0]] = ir.FromMap(under)
}

// docAt reads the whole watched document as it stood at commit, root key and all — the base
// a replay levels its deltas against.
//
// An unreadable base is an EMPTY document, not nil: the read fails when nothing has ever
// been written under the root, and there the replay itself carries every commit an entity
// has, so applying the deltas onto nothing reconstructs it exactly. (A base that fails for
// some other reason leaves the replay levelling only what it can see, which is what it had
// before any of this existed.)
func (s *LogdStore) docAt(ctx context.Context, commit int64, at ...string) *ir.Node {
	// AT THE PATH THE REPLAY WATCHES, so the base costs what that slice holds rather than what
	// the verse holds — the half of a catch-up that grows with the whole store. The document is
	// still ROOT-ROOTED, because everything that reads entities out of it (payloadIn, changesAt)
	// addresses them from the root; the replay's deltas are nested onto it.
	path := seg(s.root)
	for _, a := range at {
		path += "." + seg(a)
	}
	node, err := s.sess.MatchAt(ctx, path, commit)
	if err != nil || node == nil {
		return ir.FromMap(map[string]*ir.Node{})
	}
	return nestAt(node, append([]string{s.root}, at...))
}

// replayEnded says what ended a replay's watch, in the vocabulary a caller can act on: the one
// terminal reason that means the history is gone (ErrReplayTooOld) and everything else, which
// means ask again. libctl reports both through the closed channel's Err, and the ending's
// MESSAGE rides along in it — the floor a compacted replay left behind, the mount whose failure
// ended a composed one — so wrapping the error is what carries the detail to whoever reports it.
func replayEnded(path string, from int64, err error) error {
	if err == nil {
		return nil // a clean end: the replay delivered what it had
	}
	var ended *libctl.WatchEndedError
	if errors.As(err, &ended) && ended.Reason == logdapi.ErrCodeReplayCompacted {
		return fmt.Errorf("replaying %s from %d: %w (%v)", path, from, ErrReplayTooOld, err)
	}
	return fmt.Errorf("replaying %s from %d: %w", path, from, err)
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
		nw, err := s.establish(ctx, s.root, &libctl.WatchOptions{WaitIfAbsent: true})
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
	case ev.Patch != nil:
		s.mu.Lock()
		before := s.doc
		s.doc = s.applyDelta(s.doc, nestAt(ev.Patch, []string{s.root}))
		after := s.doc
		if ev.Commit > s.rev {
			s.rev = ev.Commit
		}
		s.mu.Unlock()
		// The delta rides along, so a consumer can ask Touches about a path it did not observe
		// at. This watch is ON the root, so what arrives is already rooted where both read from.
		s.emit(Commit{Rev: ev.Commit, Author: ev.Author}.WithDelta(ev.Patch),
			childByKey(before, s.root), childByKey(after, s.root))
	case ev.State != nil || ev.Absent:
		s.mu.Lock()
		if ev.Commit > s.rev {
			s.rev = ev.Commit
		}
		s.doc = nestAt(stateOf(ev), []string{s.root})
		s.mu.Unlock()
	}
}

// payloadIn reads one entity's whole payload out of a root-rooted document, or nil if it does
// not stand there — which is a MISSING KEY, not a null value: the document is materialized, so
// an entity holding null is present and answers with null.
//
// It walks the ref's OWN segments. Naming three of them descended one level too few in verse's
// own half, so an entity there was never found and its change went out as the delta it was
// written as rather than as the level it stands at — which reads, to anything asking a change
// what the entity now says, as an entity that suddenly holds only the field somebody touched.
func payloadIn(doc *ir.Node, root string, ref Ref) Payload {
	n := childByKey(doc, root)
	for _, seg := range ref.Segs() {
		n = childByKey(n, seg)
	}
	if n == nil {
		return nil
	}
	return nodeToPayload(n)
}

// applyDelta advances a watched document by one commit's patch. A patch that fails to apply
// leaves the document unchanged — a later State (a reconnect) resyncs it — because logd owns
// the diff encoding and decoding it by hand is how a delta gets silently dropped.
//
// THE FAILURE IS SAID, not only absorbed. This document is what every event's level payload is
// read out of (emit, payloadIn), so a delta that did not land leaves each event after it
// reporting a state its entity has already left — and it keeps doing so until a reconnect
// reseeds the document, which may be a long time on a healthy connection. Recovering is the
// right behaviour and being quiet about it is not: the symptom is a consumer acting on stale
// values, and nothing else in the process would name where they came from.
//
// With no document there is nothing to advance: a delta before the first State, or a base
// read that could not be served. It stays nil, and levelling falls back to the delta.
// (tony.Patch dereferences a nil document rather than erroring, so this is also the guard.)
func (s *LogdStore) applyDelta(doc, patch *ir.Node) *ir.Node {
	if doc == nil {
		return nil
	}
	nd, err := tony.Patch(doc, patch)
	if err != nil {
		s.logger.Warn("a watch delta did not apply; the watched document is behind, and the "+
			"level payload of every event it answers is stale until the watch reseeds",
			"root", s.root, "scope", s.cfg.Scope, "err", err)
		return doc
	}
	return nd
}

// emit broadcasts one commit to observers, non-blocking with drop-and-gap accounting — the
// contract Observe documents (drops are recovered by consumers via Since). Each observer's
// Changes are answered at its own patterns, from the verse before and after the commit.
//
// A commit is dropped or delivered WHOLE. Dropping part of one would hand a consumer a
// transaction with entities missing and no way to know it, which is the one shape the
// framing exists to prevent; the gap count is therefore in commits, not entities.
func (s *LogdStore) emit(base Commit, before, after *ir.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.subs {
		c := base
		if len(o.at) > 0 {
			c.Changes = changesAt(o.at, before, after, base.delta, base.Rev)
		}
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

// writeCtx is reqCtx for a write — see writeBudget for why the two differ.
func (s *LogdStore) writeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.ctx, writeBudget)
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
// EVERY WATCH VERSE OPENS WAITS. A watch on a path holding nothing is refused by default since
// go-tony v0.0.201 — the same answer a read of it gives — and `WaitIfAbsent` is how a client says
// it meant to wait. Verse always means to wait: the engine watches the slice a rule selects over
// before any subject exists in it, a source watches the slice it is about to reflect into, and the
// root watch is opened on a store that has never been written. A refusal there is not a typo
// caught, it is the verse failing to start.
//
// The one place that must NOT wait is a person naming a path at a command line, where a refusal
// is the answer they want. That surface does not exist yet: `verse watch` observes the ROOT and
// filters, so it never asks the store about the path a person typed and never gets the refusal
// this option is the other half of (git-issue 7e5gv3n9h12krd10jhn0).
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
	node = ir.Uncomment(node) // a comment wraps the value it precedes; a walk is about structure
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

// commentValueField names the `!comment` operand's field holding a patch for the value the
// node carries, so one node states both what its comments are and what its value is.
const commentValueField = "value"

// commentOperand reads a `!comment` node: it answers what the operation says the node's VALUE
// is — the operand's `value:`, or nil when the operand names none — and whether the node is a
// `!comment` at all.
//
// It exists because that operation is not a comment WRAPPER and ir.Uncomment does not see it. A
// wrapper is a CommentType node standing around the value it precedes; `!comment` is an
// ordinary tagged object, `{head: […], line: […], value: …}`, whose fields name the operand's
// parts rather than the document's. A structural walk that treats it as a container therefore
// descends `head`, `line` and `value` as if they were levels of the verse, and reports whatever
// stands under them as entities that do not exist.
//
// A nil answer with ok is a comment stated on its own: the comments at that node changed and
// the value did not, so nothing below it changed either.
func commentOperand(n *ir.Node) (*ir.Node, bool) {
	if n == nil || n.Type != ir.ObjectType || opIn(n.Tag) != mergeop.CommentTag {
		return nil, false
	}
	return ir.Get(n, commentValueField), true
}

// walkEntityDoc reads List's entities out of a MATERIALIZED verse subtree: the nodes three levels
// down in the world's half and four in verse's own.
//
// IT IS THE ONE FIXED ARITY LEFT, and the reason is that List names no address, so there is
// nothing to count from — every other walk was able to say how far BELOW its own place its
// records stand (entity.Below) once it had a place to say it about. This one has to guess, and
// three is the guess. It is tracked as a residual; a caller that knows its depth asks ListAt or
// Below and does not come through here.
const listWalkArity = 3

func walkEntityDoc(node *ir.Node) []Entity {
	var out []Entity
	for _, name := range fieldNames(node) {
		depth := listWalkArity - 1 // the top level is this segment; the rest are below it
		if name == VerseRoot {
			depth = listWalkArity
		}
		nodesAt(childByKey(node, name), depth, func(segs []string, v *ir.Node) {
			if ref, ok := RefOfAny(append([]string{name}, segs...)); ok {
				out = append(out, Entity{Ref: ref, Payload: nodeToPayload(v)})
			}
		})
	}
	return out
}

// compile-time check: LogdStore satisfies Store.
var _ Store = (*LogdStore)(nil)
