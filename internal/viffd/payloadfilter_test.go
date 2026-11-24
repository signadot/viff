package viffd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// The read plane's payload filter, on both faces it wears: /watch streams the changes that
// match, /state answers the state that does. They are one selector (querySelector) asked at
// two times, and the tests below are deliberately paired so that a divergence shows up as one
// of them going red on its own.
//
// The pairing used to be visible by eye — both faces called MatchEvent/MatchEntity on one
// Pattern. It is not any more: one answers a DOCUMENT and the other a stream of events, and the
// narrowing is a path as well as a predicate. TestStateAndWatchNameTheSameSet is what says it
// instead of the reader having to.

// sentinel is written LAST, so a test knows the stream has delivered everything it is going
// to the moment it sees it. Reading "until the sentinel" rather than "until nothing arrives
// for a while" is what keeps these tests both fast and non-flaky: a filter that wrongly drops
// a frame fails on the frames it delivered, not on a clock that has to be generous enough for
// the slowest machine.
//
// Every predicate here must therefore MATCH the sentinel, or the test waits out its timeout
// on a stream that is behaving correctly. It carries a `sentinel` field of its own so that
// "this filter delivers nothing" is still expressible: filter on that field, and the only
// frame is the terminator.
var sentinel = entity.RefAt("demo", "pr", "sentinel")

// fixture is the set of changes every test here watches, in write order.
var fixture = []struct {
	ref     string
	payload entity.Payload
}{
	{`demo.pr."1"`, entity.Obj{"stage": "open", "labels": entity.Obj{"urgent": true}}.Payload()},
	{`demo.pr."2"`, entity.Obj{"stage": "merged", "labels": entity.Obj{"urgent": false}}.Payload()},
	{`demo.pr."3"`, entity.Obj{"stage": "open"}.Payload()}, // no labels at all
	{sentinel.String(), entity.Obj{
		"stage": "open", "labels": entity.Obj{"urgent": true}, "sentinel": true,
	}.Payload()},
}

// watchRefs opens the watch route with the given filters, writes the fixture, and returns the
// refs the stream delivered before the sentinel, in order.
//
// The writes happen AFTER the response headers arrive, which is not an ordering nicety: the
// handler flushes them once it has subscribed and finished its catch-up, so a write that
// lands afterwards is unambiguously live. Writing first would test nothing — a store only
// learns its Since low-watermark when something starts observing it, so a change made before
// that is older than the cursor can reach and never replays.
func watchRefs(t *testing.T, q url.Values) []string {
	t.Helper()
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// WAITING, and saying so. This opens the watch BEFORE writing the fixture below — which is
	// the whole shape of the harness, since a watch that started after the writes would have
	// nothing to report — so every path it names holds nothing at the moment it asks. A watch on
	// such a path is refused unless the client says it meant to wait, which this one does.
	wq := url.Values{"wait-if-absent": {"true"}}
	for k, v := range q {
		wq[k] = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/watch?"+wq.Encode(), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watch: %s", resp.Status)
	}
	f, err := codec.Parse(resp.Header.Get(codec.HeaderFormat))
	if err != nil {
		t.Fatalf("stream format %q: %v", resp.Header.Get(codec.HeaderFormat), err)
	}

	for _, e := range fixture {
		ref, err := entity.ParseAddr(e.ref)
		if err != nil {
			t.Fatalf("%s: %v", e.ref, err)
		}
		entitytest.Set(t, srv.Store, ref, e.payload)
	}

	var refs []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var frame struct {
			Ref string `tony:"field=at"`
		}
		if err := codec.Unmarshal([]byte(data), &frame, f); err != nil {
			t.Fatalf("decoding a frame: %v\n%s", err, data)
		}
		if frame.Ref == sentinel.String() {
			return worldsHalf(refs)
		}
		refs = append(refs, frame.Ref)
	}
	t.Fatalf("the stream ended before the sentinel; delivered %v", refs)
	return nil
}

// ?if narrows the stream by what an entity SAYS, not only by where it lives — the dimension
// system/kind could never express. It is a tony match, so it carries operators, and it is the
// same predicate a charter's `when` carries: an operator can watch exactly what a rule fires
// on.
func TestWatchFiltersOnThePayload(t *testing.T) {
	// AT THE PRS, one each: a watch answers at the depth its path names, as a read does.
	with := func(kv ...string) url.Values {
		q := url.Values{"at": {"demo.pr.*"}}
		for i := 0; i < len(kv); i += 2 {
			q[kv[i]] = []string{kv[i+1]}
		}
		return q
	}
	if got := watchRefs(t, with("if", "{stage: open}")); !sameRefs(got, `demo.pr."1"`, `demo.pr."3"`) {
		t.Errorf("?if {stage: open} delivered %v, want the two open PRs", got)
	}
	// An operator inside the predicate, which is the point of it being a match rather than a
	// bag of equalities.
	if got := watchRefs(t, with("if", "{stage: !or [open, merged]}")); len(got) != 3 {
		t.Errorf("?if with !or delivered %v, want all three PRs", got)
	}
	// A predicate only the terminator satisfies: nothing of the fixture reaches the stream.
	if got := watchRefs(t, with("if", "{sentinel: true}")); len(got) != 0 {
		t.Errorf("?if {sentinel: true} delivered %v, want nothing but the sentinel", got)
	}
	// Unfiltered, everything arrives — the predicate is the only thing being tested here, so
	// this is what says the fixture reaches the stream at all.
	if got := watchRefs(t, with()); len(got) != 3 {
		t.Errorf("an unfiltered watch delivered %v, want all three PRs", got)
	}
}

// ?if-at roots the predicate inside the payload, so it can be written about a subtree without
// restating the shape above it. An entity the path does not reach is not delivered: a
// predicate that could not be evaluated has not been satisfied.
func TestWatchRootsThePredicateAtAPath(t *testing.T) {
	q := url.Values{"at": {"demo.pr.*"}, "if": {"{urgent: true}"}, "if-at": {"labels"}}
	if got := watchRefs(t, q); !sameRefs(got, `demo.pr."1"`) {
		t.Errorf("?if-at labels delivered %v, want only the urgent PR", got)
	}
	// The same question asked at the payload's root, to pin that rooting is sugar rather than a
	// second semantics.
	if got := watchRefs(t, url.Values{"at": {"demo.pr.*"}, "if": {"{labels: {urgent: true}}"}}); !sameRefs(got, `demo.pr."1"`) {
		t.Errorf("the nested spelling delivered %v, want only the urgent PR", got)
	}
	// A path arrives as one parameter PER SEGMENT, so a field name is never re-split at this
	// end — the wire takes the address as a list for the same reason a deep write does.
	deep := url.Values{"at": {"demo.pr.*"}, "if": {"true"}, "if-at": {"labels", "urgent"}}
	if got := watchRefs(t, deep); !sameRefs(got, `demo.pr."1"`) {
		t.Errorf("?if-at labels&if-at=urgent delivered %v, want only the urgent PR", got)
	}
}

// listStatus is listRefs for a read whose ANSWER is its status code.
func listStatus(t *testing.T, q url.Values) int {
	t.Helper()
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, e := range fixture {
		ref, err := entity.ParseAddr(e.ref)
		if err != nil {
			t.Fatalf("%s: %v", e.ref, err)
		}
		entitytest.Set(t, srv.Store, ref, e.payload)
	}
	resp, err := http.Get(ts.URL + "/state?" + q.Encode())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// listRefs writes the fixture, reads /state with the given filters, and returns the addresses of
// the nodes the path reached and the filter kept, sorted — the read promises a set, not an order.
// They are read back OUT of the document: the answer is the tree itself, so an address is
// assembled from the path and the keys the wildcards kept.
func listRefs(t *testing.T, q url.Values) []string {
	t.Helper()
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, e := range fixture {
		ref, err := entity.ParseAddr(e.ref)
		if err != nil {
			t.Fatalf("%s: %v", e.ref, err)
		}
		entitytest.Set(t, srv.Store, ref, e.payload)
	}

	resp, err := http.Get(ts.URL + "/state?" + q.Encode())
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("state: %s", resp.Status)
	}
	var doc entity.Doc
	decodeResp(t, resp, &doc)
	at, err := entity.ParseAt(q.Get("at"))
	if err != nil {
		t.Fatalf("?at %q: %v", q.Get("at"), err)
	}
	var refs []string
	for _, e := range (entity.Doc{Node: doc.Node}).Reached(at) {
		refs = append(refs, e.Ref.String())
	}
	refs = worldsHalf(refs)
	sort.Strings(refs)
	return refs
}

// worldsHalf drops viff's own records from a listing. The fixture is written as the system,
// so every entity in it earns an owner record under `mirror.perms`, and these tests are about
// the payload filter over the world's entities and not about what viff keeps beside them.
func worldsHalf(refs []string) []string {
	out := refs[:0]
	for _, r := range refs {
		if !strings.HasPrefix(r, entity.ViffRoot+".") {
			out = append(out, r)
		}
	}
	return out
}

// The read takes the same filter the stream does, and picks the same nodes by it. What differs
// is only WHEN the question is asked — of the state as it stands here, of each change as it
// happens there — which is why both faces are pinned against one fixture.
//
// THE FILTER IS ASKED OF WHAT THE PATH REACHED: `demo.pr.*` reaches each pr, and `demo.pr` the
// one node holding them.
func TestStateFiltersOnThePayload(t *testing.T) {
	prs := url.Values{"at": {"demo.pr.*"}}
	with := func(kv ...string) url.Values {
		q := url.Values{"at": prs["at"]}
		for i := 0; i < len(kv); i += 2 {
			q[kv[i]] = []string{kv[i+1]}
		}
		return q
	}
	all := []string{`demo.pr."1"`, `demo.pr."2"`, `demo.pr."3"`, sentinel.String()}
	if got := listRefs(t, prs); !sameRefs(got, all...) {
		t.Fatalf("an unfiltered read gave %v, want the whole fixture", got)
	}
	if got := listRefs(t, with("if", "{stage: open}")); !sameRefs(got,
		`demo.pr."1"`, `demo.pr."3"`, sentinel.String()) {
		t.Errorf("?if {stage: open} listed %v, want the open ones", got)
	}
	if got := listRefs(t, with("if", "{stage: !or [open, merged]}")); !sameRefs(got, all...) {
		t.Errorf("?if with !or listed %v, want all of them", got)
	}
	if got := listRefs(t, with("if", "{stage: closed}")); len(got) != 0 {
		t.Errorf("?if {stage: closed} listed %v, want nothing", got)
	}
	if got := listRefs(t, with("if", "{sentinel: true}")); !sameRefs(got, sentinel.String()) {
		t.Errorf("?at + ?if listed %v, want the one node satisfying both", got)
	}
	// A path NOTHING STANDS AT is a 404 rather than an empty document, which is the distinction
	// git-issue 7e5gv3n9h12krd10jhn0 is about: `other` is not a node holding no matches, it is
	// not a node. An empty document is what a path that DOES stand answers when the predicate
	// keeps none of what it reached.
	if code := listStatus(t, url.Values{"at": {"other"}, "if": {"{stage: open}"}}); code != http.StatusNotFound {
		t.Errorf("?at other -> %d, want 404: no such path", code)
	}
}

// A predicate over a path naming ONE node asks that node, and a wildcard reaching nothing names
// nothing: `demo.pr` holds prs and is not itself open, `demo.pr.1` is, and `demo.pr.1.stage.*`
// reaches no node at all.
func TestStateAsksThePredicateOfWhatThePathReached(t *testing.T) {
	for _, tc := range []struct {
		q    url.Values
		code int
		body string
	}{
		{url.Values{"at": {"demo.pr"}, "if": {"{stage: open}"}}, http.StatusOK, "{}"},
		{url.Values{"at": {"demo.pr.*"}, "if": {"{stage: closed}"}}, http.StatusOK, "{}"},
		{url.Values{"at": {"demo.pr.1"}, "if": {"{stage: open}"}}, http.StatusOK, "stage: open"},
		{url.Values{"at": {"demo.pr.1.stage.*"}}, http.StatusNotFound, "nothing stands at"},
		{url.Values{"at": {"demo.pr.*.nosuch"}}, http.StatusNotFound, "nothing stands at"},
	} {
		srv := testServer(t)
		ts := httptest.NewServer(srv.Handler())
		for _, e := range fixture {
			ref, err := entity.ParseAddr(e.ref)
			if err != nil {
				t.Fatalf("%s: %v", e.ref, err)
			}
			entitytest.Set(t, srv.Store, ref, e.payload)
		}
		resp, err := http.Get(ts.URL + "/state?" + tc.q.Encode())
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ts.Close()
		if resp.StatusCode != tc.code || !strings.Contains(string(body), tc.body) {
			t.Errorf("%v -> %d %q, want %d holding %q", tc.q, resp.StatusCode, body, tc.code, tc.body)
		}
	}
}

// Rooting works the same on the read as on the stream, including the part that is easy to
// get wrong: a node the path does not reach (demo.pr.3 has no labels) is not listed.
func TestStateRootsThePredicateAtAPath(t *testing.T) {
	q := url.Values{"at": {"demo.pr.*"}, "if": {"{urgent: true}"}, "if-at": {"labels"}}
	if got := listRefs(t, q); !sameRefs(got, `demo.pr."1"`, sentinel.String()) {
		t.Errorf("?if-at labels listed %v, want the urgent ones", got)
	}
	deep := url.Values{"at": {"demo.pr.*"}, "if": {"false"}, "if-at": {"labels", "urgent"}}
	if got := listRefs(t, deep); !sameRefs(got, `demo.pr."2"`) {
		t.Errorf("?if-at labels&if-at=urgent listed %v, want the one that is not urgent", got)
	}
}

// The read rejects a malformed predicate rather than answering the unfiltered store, which
// would be a filter that failed OPEN — the operator sees more than they asked for and has no
// way to know the filter never ran.
func TestStateRejectsAMalformedPredicate(t *testing.T) {
	ts := httptest.NewServer(testServer(t).Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/state?" + url.Values{"if": {"{stage: "}}.Encode())
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a malformed ?if answered %s, want 400", resp.Status)
	}
}

// A predicate the server cannot parse is a 400, and it is answered BEFORE the stream opens.
// Otherwise the response is already a text/event-stream that then reports nothing, which the
// client reads as a quiet viff — a rejected watch and an idle one must not look alike.
func TestWatchRejectsAMalformedPredicate(t *testing.T) {
	ts := httptest.NewServer(testServer(t).Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/watch?" + url.Values{"if": {"{stage: "}}.Encode())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a malformed ?if answered %s, want 400", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("the rejection went out as a stream (%s) — the client cannot tell it from silence", ct)
	}
}

func sameRefs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// A cursor below the retained window is REFUSED, not answered with silence.
//
// `Since` answers with the error it failed with, and this is the failure that means the history
// is gone. Watch used to skip the catch-up on a failure and stream live anyway, so a client
// asking for history got an open stream, no history and no notice — indistinguishable from a
// viff where nothing had happened. From the outside it looks like flakiness: the same
// `--since 700` prints three commits, then nothing, then only new ones, as the window slides
// past the cursor between runs.
//
// The refusal has to name what to ask for instead, because the caller wanted history and now has
// to choose: take what is retained, or stream from now.
func TestWatchRefusesACursorItCannotAnswer(t *testing.T) {
	srv := testServer(t)
	// A STORE THAT CANNOT REPLAY, said outright. This used to be reached by asking for
	// `since=-1` — which logd refused as a cursor — and that stopped being a way to ask the
	// question when a negative became a RELATIVE one (`-1` is the last commit now). The
	// premise was never the number: it is a store whose retained window no longer covers the
	// cursor, which is what compaction leaves behind and what no test store can be made to do.
	srv.Store = cannotReplay{srv.Store}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/watch?" + url.Values{"since": {"1"}}.Encode())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("an unanswerable cursor answered 200 — a client cannot tell that from a quiet viff")
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("the refusal went out as a stream (%s)", ct)
	}
	// A CONFLICT, because the cursor really is the problem here.
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("an expired cursor answered %d, want 409 — the cursor is what cannot be "+
			"satisfied", resp.StatusCode)
	}
	// WHAT TO DO INSTEAD is what the refusal owes; WHY is only claimed when it can be
	// established. The store does not send the floor — logd keeps that in its own log — so the
	// advice names a RELATIVE cursor, which the store clamps to what it kept, rather than an
	// absolute one nobody here can name. It used to advise asking for exactly the cursor it had
	// just refused.
	msg := string(body)
	if !strings.Contains(msg, "since") {
		t.Errorf("the refusal does not name the cursor it refused: %q", msg)
	}
	if !strings.Contains(msg, "--ago") && !strings.Contains(msg, "stream from now") {
		t.Errorf("the refusal does not say what to ask for instead: %q", msg)
	}
	if strings.Contains(msg, "Ask for --since 1") {
		t.Errorf("the refusal advises asking for the cursor it just refused: %q", msg)
	}
}

// A STORE THAT CANNOT BE REACHED IS NOT A CURSOR PROBLEM, and the two answers differ.
//
// Both arrived as one bit for a while, and the handler read that bit as "your cursor is too
// old" — so a person whose viff instance was unreachable was told to change their cursor, and the
// retry made it worse (the diagnosis cost a full-history replay). The store says which failure
// it had now, so this one is a 503 about the store, and repeating the same request is the right
// advice rather than the wrong one (git-issue 3qf2z59dh12ksdazg9n0).
func TestWatchSaysTheStoreIsDownRatherThanBlamingTheCursor(t *testing.T) {
	srv := testServer(t)
	srv.Store = storeDown{srv.Store}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/watch?" + url.Values{"since": {"1"}}.Encode())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := string(body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("an unreachable store answered %d (%q), want 503 — the cursor is fine and the "+
			"store is not", resp.StatusCode, msg)
	}
	if strings.Contains(msg, "below") || strings.Contains(msg, "retains") {
		t.Errorf("the refusal blames the cursor for the store being unreachable: %q", msg)
	}
	if !strings.Contains(msg, "context deadline exceeded") {
		t.Errorf("the refusal does not carry what actually failed: %q", msg)
	}
}

// THE TWO FACES NAME THE SAME SET, which is the invariant the pairing above rests on and the
// one nothing pinned until now.
//
// It used to be visible by eye: both handlers filtered with one entity.Pattern, so a divergence
// would have been a diff you could see. It is not any more — the read answers a DOCUMENT and
// the watch a stream of events, and the narrowing is a path as well as a predicate — so this
// asks the question directly, across the selectors that have somewhere to diverge.
//
// The watch's answer is per-CHANGE and the read's per node reached, which is why the fixture is
// written once with no updates: over it, "the nodes that changed" and "the nodes that stand" are
// the same set, and any difference is the selector's.
func TestStateAndWatchNameTheSameSet(t *testing.T) {
	for _, q := range []url.Values{
		{"at": {"*.*.*"}},
		{"at": {"*.*.*"}, "if": {"{stage: open}"}},
		{"at": {"demo.*.*"}, "if": {"{stage: !or [open, merged]}"}},
		{"at": {"demo.pr.*"}},
		// Narrowing to ONE id, spelled at the sentinel's own address so the stream still
		// terminates — see below.
		{"at": {"demo.pr.sentinel"}},
		{"at": {"demo.*.sentinel"}},
		{"at": {"demo.pr.*"}, "if": {"{stage: open}"}},
		{"at": {"demo.pr.*"}, "if": {"{urgent: true}"}, "if-at": {"labels"}},
	} {
		// The sentinel terminates the stream, so every selector here has to reach it. That rules
		// out the empty cases (`at=other`), which is a limit of the harness rather than a gap in
		// the invariant — TestStateFiltersOnThePayload covers those on the read's side.
		read := listRefs(t, q)
		if !contains(read, sentinel.String()) {
			t.Fatalf("%v: the fixture's sentinel is not in the read's own answer — the case is unaskable", q)
		}
		streamed := append(watchRefs(t, q), sentinel.String())
		sort.Strings(streamed)
		if !sameRefs(read, streamed...) {
			t.Errorf("%v: the read named %v and the watch named %v", q, read, streamed)
		}
	}
}

func contains(refs []string, want string) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}

// AN ABSENT CURSOR IS NOT `since=0`, and the difference is what somebody asked for.
//
// A watch with no cursor says "show me what happens". The CLI defaulted it to 0 — "replay the
// whole retained log" — so every plain `viff watch <path>` asked for history nobody wanted, and
// on a store that could not replay from 0 it was REFUSED, with advice to "drop --since" the
// caller had never passed. Unfollowable advice about a flag they did not type.
//
// So: absent streams from now; `since=0` still means from the beginning, for a caller who means
// it. This asks the first half — that a plain watch is not refused and carries no replay.
func TestAWatchWithNoCursorStreamsFromNow(t *testing.T) {
	srv := testServer(t)
	entitytest.Set(t, srv.Store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/watch?at=demo.pr")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		t.Fatalf("a watch with no cursor was refused: %s: %s", resp.Status, body)
	}

	// Nothing has happened SINCE the watch opened, so the stream is quiet — where a replay
	// would have delivered demo.pr.1 immediately.
	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		done <- buf[:n]
	}()
	select {
	case got := <-done:
		if strings.Contains(string(got), `demo.pr."1"`) {
			t.Errorf("a watch with no cursor replayed history: %s", got)
		}
	case <-time.After(300 * time.Millisecond):
		// Quiet, which is the answer.
	}
}

// cannotReplay is a store whose HISTORY IS GONE — the cursor is below what it retains, which is
// what compaction leaves behind and what no test store can be made to do. Every other question
// it answers as itself.
type cannotReplay struct{ entity.Store }

func (cannotReplay) Since(from int64, _ ...*kpath.KPath) ([]entity.Commit, error) {
	return nil, fmt.Errorf("replaying viff from %d: %w", from, entity.ErrReplayTooOld)
}

// storeDown is a store that CANNOT BE REACHED: its history is presumably fine and nobody can
// see it. The pair exists because these two were one bit for a while and the handler above them
// guessed which — always wrongly, in the direction of blaming the cursor.
type storeDown struct{ entity.Store }

func (storeDown) Since(from int64, _ ...*kpath.KPath) ([]entity.Commit, error) {
	return nil, fmt.Errorf("replaying viff from %d: context deadline exceeded", from)
}
