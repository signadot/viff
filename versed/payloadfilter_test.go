package versed

import (
	"bufio"
	"context"
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
// match, /entities lists the state that does. They are one selector (queryPattern) asked at
// two times, and the tests below are deliberately paired so that a divergence shows up as one
// of them going red on its own.

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
var sentinel = entity.Ref{System: "demo", Kind: "pr", ID: "sentinel"}

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

// listRefs writes the fixture, lists /entities with the given filters, and returns the refs it
// answered with, sorted — the listing route promises a set, not an order.
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

	resp, err := http.Get(ts.URL + "/entities?" + q.Encode())
	if err != nil {
		t.Fatalf("entities: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("entities: %s", resp.Status)
	}
	var listed []struct {
		Ref string `tony:"field=ref"`
	}
	decodeResp(t, resp, &listed)
	refs := make([]string, 0, len(listed))
	for _, e := range listed {
		refs = append(refs, e.Ref)
	}
	sort.Strings(refs)
	return refs
}

// The listing takes the same filter the stream does, and picks the same entities by it. What
// differs is only WHEN the question is asked — of the state as it stands here, of each change
// as it happens there — which is why both faces are pinned against one fixture.
func TestListEntitiesFiltersOnThePayload(t *testing.T) {
	all := []string{"demo:pr:1", "demo:pr:2", "demo:pr:3", sentinel.String()}
	if got := listRefs(t, url.Values{}); !sameRefs(got, all...) {
		t.Fatalf("an unfiltered listing gave %v, want the whole fixture", got)
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
	// The ref dimensions still narrow, and they compose with the predicate rather than
	// replacing it — the store applies them as List's prefilter, the payload is answered here,
	// and the answer must be the conjunction either way.
	if got := listRefs(t, url.Values{"system": {"other"}, "if": {"{stage: open}"}}); len(got) != 0 {
		t.Errorf("?system other listed %v, want nothing — no entity of that system exists", got)
	}
	if got := listRefs(t, url.Values{"kind": {"pr"}, "if": {"{sentinel: true}"}}); !sameRefs(got, sentinel.String()) {
		t.Errorf("?kind + ?if listed %v, want the one entity satisfying both", got)
	}
}

// Rooting works the same on the listing as on the stream, including the part that is easy to
// get wrong: an entity the path does not reach (demo:pr:3 has no labels) is not listed.
func TestListEntitiesRootsThePredicateAtAPath(t *testing.T) {
	q := url.Values{"if": {"{urgent: true}"}, "if-at": {"labels"}}
	if got := listRefs(t, q); !sameRefs(got, "demo:pr:1", sentinel.String()) {
		t.Errorf("?if-at labels listed %v, want the urgent ones", got)
	}
	deep := url.Values{"if": {"false"}, "if-at": {"labels", "urgent"}}
	if got := listRefs(t, deep); !sameRefs(got, "demo:pr:2") {
		t.Errorf("?if-at labels&if-at=urgent listed %v, want the one that is not urgent", got)
	}
}

// The listing rejects a malformed predicate rather than answering the unfiltered store, which
// would be a filter that failed OPEN — the operator sees more than they asked for and has no
// way to know the filter never ran.
func TestListEntitiesRejectsAMalformedPredicate(t *testing.T) {
	ts := httptest.NewServer(testServer(t).Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/entities?" + url.Values{"if": {"{stage: "}}.Encode())
	if err != nil {
		t.Fatalf("entities: %v", err)
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
