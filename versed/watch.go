package versed

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// watch streams store changes as SSE, catching up from ?since=<rev> first, then
// live — the cursor model surfaced at the wire. What it delivers is queryPattern's selector.
func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	q := r.URL.Query()
	// Before NewSSE, which commits the response: a malformed predicate has to reach the
	// client as a 400 it can report, not as a header'd stream that then says nothing.
	sel, err := querySelector(q)
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if refused(w, readableAt(store, principalOf(r), sel)) {
		return
	}
	// ABSENT MEANS FROM NOW, and it is not the same as `since=0`. A watch with no cursor is
	// somebody saying "show me what happens", and answering that with a replay of the whole
	// retained log — or, when the store cannot replay from 0, with a REFUSAL telling them to
	// "drop --since" they never passed — is answering a question nobody asked. `since=0` still
	// means from the beginning, for a caller that means it.
	// The client's answer to what a watch on a path holding nothing means: refuse it, as a read
	// of the same path does, or wait for a value to arrive. Refusing is the default because the
	// client here is a person who typed the address.
	waitIfAbsent := q.Get("wait-if-absent") == "true"
	since, catchUp, err := watchCursor(q.Get("since"))
	if err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	// `init` opens the stream with what stands at the path, as the substrate's opening state
	// diffed against nothing: a set per node, at the revision the watch opened at. Refused where
	// it cannot mean that rather than dropped — a cursor has already said what comes first, and
	// a watch with no literal prefix is the verse-wide stream, which has no opening state.
	withInit := q.Get("init") == "true"
	if withInit && catchUp {
		codec.HTTPError(w, r, http.StatusBadRequest,
			"init and since both say what comes first — what stands now, or history from a revision; ask one")
		return
	}
	match := sel.matchEvent

	// Subscribe BEFORE the catch-up snapshot, so an event landing between the two is
	// captured live rather than lost in the gap. The catch-up and the live stream
	// overlap; dedup by revision (send only strictly-ascending revs).
	// AT THE PATH when one was named, and at the root only when none was. A watch on a path
	// nothing stands at is refused by the substrate unless the client asked to wait, so a person
	// who typed an address wrong is told so instead of holding a stream that will never carry
	// anything. It also narrows what crosses: observing the root and filtering here means every
	// commit in the verse is delivered to a watch on one slice.
	ch, unsub, err := watchFor(store, sel, waitIfAbsent, withInit)
	if errors.Is(err, errNoOpeningState) {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		// The substrate refuses a watch on a path holding nothing unless the client asked to
		// wait, which is the same answer a read of it gives — so this needs no read of its own
		// to know, and says the same thing `verse data` would about the same address.
		notFoundAt(w, r, sel.at)
		return
	}
	defer unsub()

	// A cursor the store cannot answer from is REFUSED, before NewSSE commits the response.
	//
	// `Since` returns a non-nil error when `since` is below what the store still retains, and it does
	// that deliberately rather than returning what it happens to still have. Skipping the
	// catch-up on that answer — which is what this did — hands the client an open stream with no
	// history and no notice, indistinguishable from a verse where nothing has happened. Observed
	// from the outside as: the same `--since 700` printing three commits, then nothing, then only
	// new ones, as the window slid past the cursor between runs.
	//
	// A gap MARKER would be the wrong shape here even though the live path has one: `Gap` means
	// "commits were dropped, recover them with Since", and Since is exactly what just could not
	// answer. So this is a refusal that says what to ask for instead — the caller wanted history
	// and has to decide whether to take what is retained or to stream from now.
	// AT THE SAME PATH THE LIVE HALF WATCHES. A catch-up costs what the slice it reads holds —
	// the base document at the starting commit, and every patch in the range that touches it —
	// so a watch on one slice that replayed the WHOLE VERSE paid for the store's size on every
	// request. Both halves take `prefix` now, and a selector that names no path still asks at
	// the root, because that is what it is asking about.
	var replay []entity.Commit
	var replayErr error
	if catchUp {
		replay, replayErr = store.Since(since, sel.at)
	}
	if replayErr != nil {
		// WHAT ACTUALLY HAPPENED, said in the store's own words. This asked a second question
		// to guess with — `store.Since(0)`, whose answer was one int64, the oldest commit still
		// retained — and that probe is gone. It could not work: it is the LONGEST replay the
		// store can be asked for, so on any verse with a history it failed exactly as the
		// cheap one just had, and the handler then asserted "it is not the cursor's age"
		// whatever the cause. Before it learned to say it had failed at all, its zero value
		// went out as a measurement: `the store still has commits from 0`, which is what
		// git-issue 3qf2z59dh12ksdazg9n0 reports seeing. It was also expensive enough to
		// matter — on staging it materialised the whole log, and the substrate was evicted for
		// memory four times in the fifteen minutes somebody spent retrying a refused --since.
		//
		// `Since` answers with its own error now, so the guess is unnecessary: the one failure
		// that means the history is gone says so, and everything else names the store's
		// condition rather than the caller's cursor.
		if errors.Is(replayErr, entity.ErrReplayTooOld) {
			// THE STORE SAYS FROM WHICH COMMIT IT CAN STILL SERVE, in the refusal itself, so
			// the advice names a cursor that works rather than the one just refused. That is
			// what the deleted probe was reaching for and could not reach.
			codec.HTTPError(w, r, http.StatusConflict, fmt.Sprintf(
				"since=%d is below what the store still retains, so that history is gone: %v. "+
					"Ask from the commit it names, or with --ago N for the last N commits, "+
					"which the store clamps to what it kept, or drop --since to stream from now",
				since, replayErr))
			return
		}
		// NOT A CONFLICT. The cursor is fine and the store could not answer — a 409 told a
		// person their cursor was the problem while their verse was unreachable, and they
		// retried the one call that made it worse.
		codec.HTTPError(w, r, http.StatusServiceUnavailable, fmt.Sprintf(
			"the store could not replay from since=%d: %v — the cursor stands, so the same "+
				"request is worth repeating once the store answers again", since, replayErr))
		return
	}

	sse, err := codec.NewSSE(w, r)
	if err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	// The wire is a stream of EVENTS, not commits: a reader of /watch is watching entities,
	// and the commit framing is the store's way of saying which changes were atomic — a fact
	// this surface has never carried and nothing consuming it asks for. So a commit is
	// flattened here, in commit order, and its Rev rides each event as it always did.
	last := since
	for _, c := range replay {
		for _, ev := range c.Changes {
			if match(ev) {
				_ = sse.Send(ev)
			}
		}
		if c.Rev > last {
			last = c.Rev
		}
	}
	sse.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case c, ok := <-ch:
			if !ok {
				return
			}
			if c.Rev != 0 && c.Rev <= last {
				continue // already delivered by the catch-up (overlap window)
			}
			// A dropped commit is reported once, as a gap marker, not once per event it
			// held — the count is in commits and saying otherwise would overstate it.
			if c.Gap > 0 {
				_ = sse.Send(entity.Event{Rev: c.Rev, Gap: c.Gap})
			}
			for _, ev := range c.Changes {
				if match(ev) {
					_ = sse.Send(ev)
				}
			}
			if c.Rev > last {
				last = c.Rev
			}
		}
	}
}

// errNoOpeningState refuses `init` on a watch that has no opening state to give.
var errNoOpeningState = errors.New("init needs a path with a literal prefix: without one the " +
	"watch joins the verse-wide stream, which has no opening state")

// watchFor opens the narrowest watch the selector allows: at its literal prefix, reporting at the
// depth the path names, or at the root when it names no prefix. withInit opens it with what
// stands at the path, which only the watch at a prefix has.
func watchFor(store entity.Store, sel selector, waitIfAbsent, withInit bool) (<-chan entity.Commit, func(), error) {
	prefix, _ := entity.LiteralPrefix(sel.at)
	narrow, hasNarrow := store.(interface {
		WatchPattern([]string, *kpath.KPath, bool, bool) (<-chan entity.Commit, func(), error)
	})
	if len(prefix) == 0 || !hasNarrow {
		if withInit {
			return nil, nil, errNoOpeningState
		}
		ch, unsub := store.Observe(sel.at)
		return ch, unsub, nil
	}
	// WITH THE PATH ITSELF: what is reported is what the path reaches, whether that is one
	// depth or — under a `..` — every depth beneath it.
	return narrow.WatchPattern(prefix, sel.at, waitIfAbsent, !withInit)
}

// watchCursor reads the `since` parameter: the cursor to replay from, whether to replay at all,
// and why not if it cannot be read.
//
// A NEGATIVE CURSOR IS RELATIVE — `since=-20` is the last twenty commits — and it is PASSED
// THROUGH rather than resolved here. The store resolves it against its own watermark at the
// moment the watch is established, clamped to its retained floor (go-tony v0.0.176/177), and
// neither of those is knowable from this end: verse could subtract from the head it last saw,
// but it could not clamp to a floor it cannot see, so a deep `-N` on a compacted store would
// come back refused where the store would have answered from what it kept.
//
// It is also why this takes no head: resolving here meant reading one, and a head read a moment
// before the watch starts is a head something may have moved. The store reads its own.
//
// N COMMITS, NOT N EVENTS. A watch narrowed to a path sees only the changes under it, so `-20`
// asks for the span of the last twenty commits and delivers however many events live in it —
// which is usually fewer and never more. A cursor stays a bookmark; the relative form makes the
// bookmark relative rather than making it a count.
//
// ABSENT MEANS FROM NOW, and it is not the same as `since=0`. A watch with no cursor is
// somebody saying "show me what happens", and answering that with a replay of the whole
// retained log — or, when the store cannot replay from 0, with a REFUSAL telling them to "drop
// --since" they never passed — is answering a question nobody asked. `catchUp` is what says
// which, and it has to: the sentinel used to be `since = -1`, which is a real cursor now.
func watchCursor(raw string) (since int64, catchUp bool, err error) {
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("since=%q is not a revision: a cursor is a number, and an "+
			"absent one streams from now", raw)
	}
	return n, true, nil
}
