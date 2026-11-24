package viffd

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
)

// queryPattern reads the PAYLOAD half of the selector a read-plane route was given: ?if is a
// tony match over the payload, rooted at the field path in ?if-at (one parameter per segment,
// so a field name is never re-split here).
//
// Which nodes a read is about is the PATH (?at) — see querySelector, which pairs the two halves.
//
// ?if arrives as TONY whatever the operator typed, because a GET carries no Content-Type to
// declare a dialect in; the client parses in --ifmt and re-renders (see payloadFilter.apply).
func queryPattern(q url.Values) (entity.Pattern, error) {
	pred, err := codec.ParseNodeOrNil([]byte(q.Get("if")), codec.Tony)
	if err != nil {
		return entity.Pattern{}, fmt.Errorf("?if: %w", err)
	}
	return entity.Pattern{Predicate: pred, At: q["if-at"]}, nil
}

// queryPaths reads the listing half of a read's query: whether the caller wants ADDRESSES rather
// than the document, and how far to walk. depth is counted FROM THE ADDRESS the path names — 0
// is that address alone, 1 its children — and absent is everything beneath it, so the same
// number means the same thing at every level.
//
// A DEPTH WITHOUT A LISTING IS REFUSED rather than ignored: it bounds a walk, a document read
// does not walk, and a parameter that quietly does nothing is how a caller comes to believe it
// asked for something it did not.
func queryPaths(q url.Values) (listing bool, depth int, err error) {
	d := q.Get("depth")
	if q.Get("paths") == "" {
		if d != "" {
			return false, 0, fmt.Errorf("?depth=%s without ?paths: a depth bounds a listing, "+
				"and this read answers a document", d)
		}
		return false, entity.Unbounded, nil
	}
	depth = entity.Unbounded
	if d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 {
			return false, 0, fmt.Errorf("?depth=%s: a depth is how far BENEATH the address to "+
				"walk — 0 is the address alone — and it is never negative", d)
		}
		depth = n
	}
	return true, depth, nil
}

// selector is what a read-plane route was asked for: a path reaching nodes, and a predicate over
// each node it reached.
//
// It is ONE type because the streaming face and the listing face must name the SAME set —
// `viff watch --if X demo pr '*'` and `viff data --if X demo pr '*'` differ in when they
// answer, not in what they pick. That is not visible by eye, so TestStateAndWatchNameTheSameSet
// says it instead.
type selector struct {
	at  *kpath.KPath
	pat entity.Pattern
}

// querySelector reads both halves.
func querySelector(q url.Values) (selector, error) {
	pat, err := queryPattern(q)
	if err != nil {
		return selector{}, err
	}
	at, err := entity.ParseAt(q.Get("at"))
	if err != nil {
		return selector{}, fmt.Errorf("?at: %w", err)
	}
	return selector{at: at, pat: pat}, nil
}

// matchRef is whether a change's path is one the selector's path reaches.
func (s selector) matchRef(ref entity.Ref) bool { return entity.Reaches(s.at, ref) }

// matchNode is the whole question about a node a read reached, and matchEvent the whole question
// about a change.
func (s selector) matchNode(n *ir.Node) (*ir.Node, bool, error) {
	return n, s.pat.MatchesPayload(n), nil
}

func (s selector) matchEvent(ev entity.Event) bool {
	return s.matchRef(ev.Ref) && s.pat.MatchesChange(ev)
}

// storeUnreadable reports the store's last failed read, or nil.
//
// A LISTING CANNOT SAY THIS ITSELF: `Store.List` answers a slice, so a store that cannot be read
// answers the empty one, indistinguishable from a viff instance holding nothing. That is how a store
// with an unreplayable patch in its log presents — `viff data` empty, `viff charter` a
// charter of no rules, every write still reporting success. Asking the store turns it into an
// answer somebody can act on.
func storeUnreadable(st entity.Reader) error {
	if h, ok := st.(interface{ ReadError() error }); ok {
		return h.ReadError()
	}
	return nil
}

// unreadable answers 503 when THIS request's read did not happen, and reports whether it did.
//
// 503 rather than 500: the request is fine and the viff instance may be fine again in a moment — what
// failed is this server's ability to read its own store, which is what Service Unavailable
// says. The body names the cause, because "cannot read the store" with no cause sends an
// operator back to the log-reading this exists to replace.
//
// IT TAKES THE READ'S OWN ERROR, where it used to ask the store for the last one it remembered.
// That flag is one field shared by every reader — the engine, the sources, and each request in
// flight — so whether a request noticed its own failure depended on who else read in between:
// a neighbour's success hid it and the handler answered 200 with an empty document, and a
// neighbour's failure refused a request whose read was fine. Both directions were wrong at the
// same line (git-issue tyr7f6dhh12ksdd4gnn0), and the window is microseconds, which is why it
// read as `--paths` disagreeing with the document form rather than as a race.
// notFoundAt says nothing stands at the address asked for, in the address the caller wrote —
// which is the whole point of answering it rather than an empty document: a reader learns their
// path is wrong instead of that the viff instance is empty.
//
// IN THE ASKER'S VIEW, and it says so: what a read answers is what its reader may read, so
// "nothing stands" is a statement about that and not about the store (view.go).
func notFoundAt(w http.ResponseWriter, r *http.Request, at *kpath.KPath) {
	spelled := r.URL.Query().Get("at")
	if spelled == "" {
		spelled = at.String()
	}
	if spelled == "" {
		// The whole viff instance, on a viff instance nobody has written to. Naming the address would name
		// nothing, and "the viff instance is empty" is the fact a reader wants on their first read.
		codec.HTTPError(w, r, http.StatusNotFound, "in your view, the viff instance is empty: nothing has been written yet")
		return
	}
	codec.HTTPError(w, r, http.StatusNotFound, nothingStands(spelled))
}

func unreadable(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	// A read the check turned away is not a store that could not be read: it is answered,
	// with why, and nothing about the store is wrong.
	if refused(w, err) {
		return true
	}
	codec.HTTPError(w, r, http.StatusServiceUnavailable, fmt.Sprintf(
		"the store could not be read, so this answer would be an empty viff rather than the "+
			"viff: %v — nothing is missing until it reads again, and writes may still be "+
			"appending", err))
	return true
}

// state answers the viff instance AT A PATH: the node the path reaches in the store, with everything
// beneath it. It is narrowed by the same selector /watch
// streams, so a read and a stream never disagree about what a filter names.
//
// `--if` is asked of each node the path reached — `demo.pr` reaches one, `demo.pr.*` each pr —
// and never prunes inside one it keeps. (`--if-at` says where inside the node the predicate is
// evaluated, which is a different question again.)
//
// A path that reaches nothing is a 404 at every length; a path whose nodes `--if` kept none of is
// `{}`, which is an answer about the filter rather than the path.
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	sel, err := querySelector(r.URL.Query())
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	store := s.scopedStore(r)
	if refused(w, readableAt(store, principalOf(r), sel)) {
		return
	}
	// AT THE LITERAL PREFIX, which is what the substrate routes on: a read of one registration
	// costs what that registration holds, not what the viff instance does. The rest of the path — from
	// its first wildcard, index or key — is answered over the node that came back.
	prefix, rest := entity.LiteralPrefix(sel.at)
	// A LISTING IS ANSWERED BY THE STORE, which knows the names without reading the nodes: it
	// walks its own levels and answers addresses, so listing a wide container costs the walk
	// rather than the container — and answers one no read could return at all, being past the
	// read budget. That is every listing: a place, a path with a wildcard or a descent, and one
	// with a predicate, each handed over with the depth as it was given. The depth is the
	// store's own, so nothing here counts segments, and a path with more than one `..` means
	// what logd says it means.
	if listing, depth, err := queryPaths(r.URL.Query()); err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	} else if listing && rest == nil && sel.pat.Predicate == nil {
		out, err := store.PathsAt(prefix, depth)
		if unreadable(w, r, err) {
			return
		}
		// Asked without reading it: the place listed is as often as not a container.
		if ok, err := store.StandsAt(prefix); unreadable(w, r, err) {
			return
		} else if !ok {
			// A listing of a path nothing stands at is not an empty listing: `--paths | wc -l`
			// answering 0 must never be how a reader learns the address was wrong.
			notFoundAt(w, r, sel.at)
			return
		}
		writeOut(w, r, http.StatusOK, entity.PathsDoc(out))
		return
	} else if listing {
		out, reached, err := store.PathsMatching(sel.at, depth, sel.pat.StorePattern(), nil)
		if unreadable(w, r, err) {
			return
		}
		// A PATH THAT REACHES NOTHING NAMES NOTHING, and one that reached nodes `--if` kept none
		// of is an answer about the filter: the empty listing.
		if !reached {
			notFoundAt(w, r, sel.at)
			return
		}
		writeOut(w, r, http.StatusOK, entity.PathsDoc(out))
		return
	}
	// What is left reads the node: a read that wants the document.
	node, ok, err := store.ReadAt(prefix)
	if unreadable(w, r, err) {
		return
	}
	if !ok {
		notFoundAt(w, r, sel.at)
		return
	}
	var keep func(*ir.Node) (*ir.Node, bool, error)
	if sel.pat.Predicate != nil {
		keep = sel.matchNode
	}
	out, reached, err := entity.Reach(node, rest, keep)
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	// A PATH THAT REACHES NOTHING NAMES NOTHING — a wildcard over a node with no children
	// included — and a path that reached nodes `--if` kept none of is an answer about the
	// filter: the empty document.
	if !reached {
		notFoundAt(w, r, sel.at)
		return
	}
	if out == nil {
		out = ir.FromMap(map[string]*ir.Node{})
	}
	writeOut(w, r, http.StatusOK, entity.Doc{Node: out})
}

func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	ref, err := addressAt(r.URL.Query().Get("at"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e, ok, err := s.scopedStore(r).Get(ref)
	if refused(w, err) {
		return // may not be read, which is neither "absent" nor "the store is down"
	}
	if err != nil {
		// A read that could not be performed is NOT a 404. Reporting it as one tells the
		// caller the entity does not exist, which is a different — and actionable —
		// statement from "the store is unreachable", and the caller acts on it: an agent
		// creates the file it was told is missing. 502: viff is up, its store is not.
		http.Error(w, "entity read failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !ok {
		codec.HTTPError(w, r, http.StatusNotFound, nothingStands(ref.String()))
		return
	}
	// The entity itself. Its Ref carries MarshalText, so it still renders as the address
	// string this always sent — and the route table's `viff-entity` now names the type
	// actually written rather than a map that happened to match it.
	writeOut(w, r, http.StatusOK, e)
}

// deleteEntity removes whatever stands at `?at=` — an entity, or a place inside one, the
// address saying which by how deep it goes. Removing what is not there is not an error: the
// request says what should stand there afterwards, and nothing already does.
// reservedWrite refuses a client write into viff's own half of the tree.
//
// The entity routes are the GENERIC write, and before the boundary had a name anyone who could
// reach them could write a rule, stamp a task record, or answer a question on viff's behalf —
// with no route saying no, because there was nothing in an address to say no about. Now there
// is: everything under `mirror` is what it writes about its own running, and it is written by the engine,
// not through here.
//
// It is not a lock on the DATA. A reader still reads it (that is the point of it being
// entities), the engine still writes it, and a scope's own copy still layers over it. What is
// refused is one path — the generic write — because every legitimate change to viff's own
// state has a verb: `viff c -a`, `viff resolver add`, `viff question approve`. A ballot
// is not among them, deliberately: `vote.*` is the world's half, since casting one is an act
// somebody takes rather than a record viff keeps.
func reservedWrite(ref entity.Ref) error {
	if !ref.Reserved() {
		return nil
	}
	return fmt.Errorf("%s is viff's own: the entity write is for what the world is about, and "+
		"what viff writes about its own running is written by the verbs that own it "+
		"(charter -a, resolver add, runner add, iod add, question approve)", ref)
}

func (s *Server) deleteEntity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref, err := addressAt(q.Get("at"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := reservedWrite(ref); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if _, _, err := s.scopedStore(r).Write(entity.Write{
		At: entity.Path{Ref: ref, Access: q.Get("sub")},
	}); refused(w, err) {
		return
	} else if err != nil {
		http.Error(w, "entity remove failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// putEntity is the write surface: a value MERGES in at a place — an entity, or a field
// inside one — iff every stated condition holds.
//
// A refused condition is 409, not an error: the request was well-formed and the store was
// reachable, and the answer is "not while that is true". The success body is the entity as
// it now stands, read back, because a write that merges into a place is precisely the case
// where the caller cannot compute the result from what it sent.
func (s *Server) putEntity(w http.ResponseWriter, r *http.Request) {
	var body api.PutEntity
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ref, err := addressAt(body.At)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := reservedWrite(ref); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	when, err := conditions(body.When)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var grants []entity.Grant
	if body.Grants != nil {
		if grants, err = grantsAt(ref, body.Grants); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	store := s.scopedStore(r)
	committed, _, err := store.Write(entity.Write{
		At: entity.Path{Ref: ref, Access: body.Sub}, Value: body.Payload, When: when, Grants: grants,
	})
	// GRANTS ARE GIVEN AT A CREATION: a node that stands is 409 beside a refused condition, and
	// grants nobody could give there are the caller's to fix.
	if errors.Is(err, entity.ErrStands) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if errors.Is(err, entity.ErrBadGrants) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A WRITE THAT WOULD REPLACE WHAT IT DESCENDS THROUGH IS THE CALLER'S, not the gateway's:
	// nothing failed downstream and a retry changes nothing, so it answers 409 beside the
	// refused-precondition case rather than 502 (git-issue mbjpdr6sh12ksa5zgsn0).
	if errors.Is(err, entity.ErrNotDescendable) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if refused(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "entity write failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !committed {
		http.Error(w, "write refused: a condition did not hold", http.StatusConflict)
		return
	}
	// The ENTITY as it now stands, which is what makes a merge visible: a write that named one
	// field answers with the whole thing, so a caller sees what it did not send. The address it
	// answers about is the one the write named — a path of any depth, now that a client can say
	// one — rather than a three-segment ancestor of it.
	e, ok, err := store.Get(ref)
	if err != nil || !ok {
		w.WriteHeader(http.StatusNoContent) // it landed; we just cannot show it back
		return
	}
	writeOut(w, r, http.StatusOK, e)
}
