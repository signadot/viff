package versed

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
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
	{"demo:pr:1", entity.Obj{"stage": "open", "labels": entity.Obj{"urgent": true}}.Payload()},
	{"demo:pr:2", entity.Obj{"stage": "merged", "labels": entity.Obj{"urgent": false}}.Payload()},
	{"demo:pr:3", entity.Obj{"stage": "open"}.Payload()}, // no labels at all
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/watch?"+q.Encode(), nil)
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
		ref, err := entity.ParseRef(e.ref)
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
			Ref string `tony:"field=ref"`
		}
		if err := codec.Unmarshal([]byte(data), &frame, f); err != nil {
			t.Fatalf("decoding a frame: %v\n%s", err, data)
		}
		if frame.Ref == sentinel.String() {
			return refs
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
	if got := watchRefs(t, url.Values{"if": {"{stage: open}"}}); !sameRefs(got, "demo:pr:1", "demo:pr:3") {
		t.Errorf("?if {stage: open} delivered %v, want the two open PRs", got)
	}
	// An operator inside the predicate, which is the point of it being a match rather than a
	// bag of equalities.
	if got := watchRefs(t, url.Values{"if": {"{stage: !or [open, merged]}"}}); len(got) != 3 {
		t.Errorf("?if with !or delivered %v, want all three PRs", got)
	}
	// A predicate only the terminator satisfies: nothing of the fixture reaches the stream.
	if got := watchRefs(t, url.Values{"if": {"{sentinel: true}"}}); len(got) != 0 {
		t.Errorf("?if {sentinel: true} delivered %v, want nothing but the sentinel", got)
	}
	// Unfiltered, everything arrives — the predicate is the only thing being tested here, so
	// this is what says the fixture reaches the stream at all.
	if got := watchRefs(t, url.Values{}); len(got) != 3 {
		t.Errorf("an unfiltered watch delivered %v, want all three PRs", got)
	}
}

// ?if-at roots the predicate inside the payload, so it can be written about a subtree without
// restating the shape above it. An entity the path does not reach is not delivered: a
// predicate that could not be evaluated has not been satisfied.
func TestWatchRootsThePredicateAtAPath(t *testing.T) {
	q := url.Values{"if": {"{urgent: true}"}, "if-at": {"labels"}}
	if got := watchRefs(t, q); !sameRefs(got, "demo:pr:1") {
		t.Errorf("?if-at labels delivered %v, want only the urgent PR", got)
	}
	// The same question asked at the root, to pin that rooting is sugar rather than a second
	// semantics.
	if got := watchRefs(t, url.Values{"if": {"{labels: {urgent: true}}"}}); !sameRefs(got, "demo:pr:1") {
		t.Errorf("the nested spelling delivered %v, want only the urgent PR", got)
	}
	// A path arrives as one parameter PER SEGMENT, so a field name is never re-split at this
	// end — the wire takes the address as a list for the same reason a deep write does.
	deep := url.Values{"if": {"true"}, "if-at": {"labels", "urgent"}}
	if got := watchRefs(t, deep); !sameRefs(got, "demo:pr:1") {
		t.Errorf("?if-at labels&if-at=urgent delivered %v, want only the urgent PR", got)
	}
}

// listRefs writes the fixture, reads /state with the given filters, and returns the refs it
// answered with, sorted — the read promises a set, not an order.
//
// The refs are read back OUT of the document, which is the change in one line: the answer is
// `{system: {kind: {id: payload}}}` now, so an address is assembled from the keys rather than
// lifted from a `ref:` field. Every assertion below is unchanged, because they are all about
// the set and the set is the same one.
func listRefs(t *testing.T, q url.Values) []string {
	t.Helper()
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, e := range fixture {
		ref, err := entity.ParseRef(e.ref)
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
	refs := doc.Paths(at)
	sort.Strings(refs)
	return refs
}

// The read takes the same filter the stream does, and picks the same entities by it. What
// differs is only WHEN the question is asked — of the state as it stands here, of each change
// as it happens there — which is why both faces are pinned against one fixture.
func TestStateFiltersOnThePayload(t *testing.T) {
	all := []string{"demo:pr:1", "demo:pr:2", "demo:pr:3", sentinel.String()}
	if got := listRefs(t, url.Values{}); !sameRefs(got, all...) {
		t.Fatalf("an unfiltered read gave %v, want the whole fixture", got)
	}
	if got := listRefs(t, url.Values{"if": {"{stage: open}"}}); !sameRefs(got,
		"demo:pr:1", "demo:pr:3", sentinel.String()) {
		t.Errorf("?if {stage: open} listed %v, want the open ones", got)
	}
	if got := listRefs(t, url.Values{"if": {"{stage: !or [open, merged]}"}}); !sameRefs(got, all...) {
		t.Errorf("?if with !or listed %v, want all of them", got)
	}
	if got := listRefs(t, url.Values{"if": {"{stage: closed}"}}); len(got) != 0 {
		t.Errorf("?if {stage: closed} listed %v, want nothing", got)
	}
	// The PATH still narrows, and it composes with the predicate rather than replacing it — the
	// store applies the address dimensions as List's prefilter, the payload is answered at the
	// handler, and the answer must be the conjunction either way.
	if got := listRefs(t, url.Values{"at": {"other"}, "if": {"{stage: open}"}}); len(got) != 0 {
		t.Errorf("?at other listed %v, want nothing — no entity of that system exists", got)
	}
	if got := listRefs(t, url.Values{"at": {"demo.pr"}, "if": {"{sentinel: true}"}}); !sameRefs(got, sentinel.String()) {
		t.Errorf("?at + ?if listed %v, want the one entity satisfying both", got)
	}
}

// Rooting works the same on the read as on the stream, including the part that is easy to
// get wrong: an entity the path does not reach (demo:pr:3 has no labels) is not listed.
func TestStateRootsThePredicateAtAPath(t *testing.T) {
	q := url.Values{"if": {"{urgent: true}"}, "if-at": {"labels"}}
	if got := listRefs(t, q); !sameRefs(got, "demo:pr:1", sentinel.String()) {
		t.Errorf("?if-at labels listed %v, want the urgent ones", got)
	}
	deep := url.Values{"if": {"false"}, "if-at": {"labels", "urgent"}}
	if got := listRefs(t, deep); !sameRefs(got, "demo:pr:2") {
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
// client reads as a quiet verse — a rejected watch and an idle one must not look alike.
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
// `Since` reports ok=false when it cannot answer from `from`, deliberately rather than
// returning what it happens to still have. Watch used to skip the catch-up on that answer and
// stream live anyway, so a client asking for history got an open stream, no history and no
// notice — indistinguishable from a verse where nothing had happened. From the outside it looks
// like flakiness: the same `--since 700` prints three commits, then nothing, then only new ones,
// as the window slides past the cursor between runs.
//
// The refusal has to name what to ask for instead, because the caller wanted history and now has
// to choose: take what is retained, or stream from now.
func TestWatchRefusesACursorItCannotAnswer(t *testing.T) {
	ts := httptest.NewServer(testServer(t).Handler())
	defer ts.Close()

	// Far beyond anything this store has retained — the same shape as a cursor left behind by
	// compaction, which is how it is reached in the world.
	resp, err := http.Get(ts.URL + "/watch?" + url.Values{"since": {"-1"}}.Encode())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("an unanswerable cursor answered 200 — a client cannot tell that from a quiet verse")
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("the refusal went out as a stream (%s)", ct)
	}
	if msg := string(body); !strings.Contains(msg, "retained window") || !strings.Contains(msg, "since") {
		t.Errorf("the refusal does not say what to ask for instead: %q", msg)
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
// The watch's answer is per-CHANGE and the read's per-entity, which is why the fixture is
// written once with no updates: over it, "the entities that changed" and "the entities that
// stand" are the same set, and any difference is the selector's.
func TestStateAndWatchNameTheSameSet(t *testing.T) {
	for _, q := range []url.Values{
		{},
		{"if": {"{stage: open}"}},
		{"if": {"{stage: !or [open, merged]}"}},
		{"at": {"demo"}},
		{"at": {"demo.pr"}},
		// Narrowing to ONE id, spelled at the sentinel's own address so the stream still
		// terminates — see below.
		{"at": {"demo.pr.sentinel"}},
		{"at": {"demo.*.sentinel"}},
		{"at": {"demo.pr"}, "if": {"{stage: open}"}},
		{"at": {"demo.pr"}, "if": {"{urgent: true}"}, "if-at": {"labels"}},
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
// whole retained log" — so every plain `verse watch <path>` asked for history nobody wanted, and
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
	// would have delivered demo:pr:1 immediately.
	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		done <- buf[:n]
	}()
	select {
	case got := <-done:
		if strings.Contains(string(got), "demo:pr:1") {
			t.Errorf("a watch with no cursor replayed history: %s", got)
		}
	case <-time.After(300 * time.Millisecond):
		// Quiet, which is the answer.
	}
}
