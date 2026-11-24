package entity

import (
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
)

// ordinary is what these tests observe at: the nodes `*.*.*` reaches in the world's half and
// `viff.*.*.*` in viff's own, which is where the fixtures write. A pattern is what an observer
// asks with — there is no depth the stream picks for everybody.
func ordinary() []*kpath.KPath {
	return []*kpath.KPath{PrefixPattern(nil, 3), PrefixPattern([]string{ViffRoot}, 4)}
}

func TestParseAddrRoundTrip(t *testing.T) {
	cases := []string{
		`linear.issue."1090"`,
		`github.pr."signadot/hack#12"`, // the quoting is the grammar's: a '#' is inside the name
		"mirror.task.instance.p1",
		"nope", // one segment is an address too — a whole system, which is a real thing to name
	}
	for _, s := range cases {
		r, err := ParseAddr(s)
		if err != nil {
			t.Fatalf("ParseAddr(%q): %v", s, err)
		}
		if r.String() != s {
			t.Fatalf("round trip: %q -> %q", s, r.String())
		}
	}
	// An empty segment names no place, at either end or in the middle.
	for _, bad := range []string{"", `a."".b`, `a.b.""`} {
		if _, err := ParseAddr(bad); err == nil {
			t.Fatalf("ParseAddr(%q) should fail", bad)
		}
	}
}

// A write is unconditional and last-write-wins unless it says otherwise: a create and an
// update are the same operation, and the store does not distinguish them. (Which it WAS is
// not in the change stream at all — see docs/change-events.md — but a writer that cares can
// insist, with Created() or Updated().)
func TestCreateThenUpdate(t *testing.T) {
	s := openStore(t)
	ref := RefAt("linear", "issue", "1090")

	mustWrite(t, s, Write{At: ref.At(), Value: Obj{"stage": "open"}.Payload()})
	if got, ok, _ := s.Get(ref); !ok || Str(got.Payload, "stage") != "open" {
		t.Fatalf("get after create: %+v ok=%v", got, ok)
	}
	mustWrite(t, s, Write{At: ref.At(), Value: Obj{"stage": "reviewed"}.Payload()})
	got, ok, _ := s.Get(ref)
	if !ok || ToObj(got.Payload)["stage"] != "reviewed" {
		t.Fatalf("get after update: %+v ok=%v", got, ok)
	}
}

func TestWriteClonesPayload(t *testing.T) {
	s := openStore(t)
	ref := RefAt("x", "y", "z")
	p := Obj{"n": 1}.Payload()
	mustWrite(t, s, Write{At: ref.At(), Value: p})
	*ir.Get(p, "n") = *ir.FromInt(999) // mutate the caller's node after the write
	got, _, _ := s.Get(ref)
	if ToObj(got.Payload)["n"] != int64(1) {
		t.Fatalf("store aliased caller payload: %+v", got.Payload)
	}
}

// Removal is a write of nothing. Removing what is not there is not an error: the write
// says what should stand at a path, and "nothing" is a thing it can say about a path that
// already holds nothing.
func TestRemoveAndMissing(t *testing.T) {
	s := openStore(t)
	ref := RefAt("x", "y", "z")
	mustWrite(t, s, Write{At: ref.At()}) // nothing is there; still not an error

	mustWrite(t, s, Write{At: ref.At(), Value: Obj{"a": 1}.Payload()})
	if _, ok, _ := s.Get(ref); !ok {
		t.Fatal("entity missing after it was written")
	}
	mustWrite(t, s, Write{At: ref.At()})
	if _, ok, _ := s.Get(ref); ok {
		t.Fatal("entity still present after being removed")
	}
}

func TestObserveDeliversChanges(t *testing.T) {
	s := openStore(t)
	ch, unsub := s.Observe(ordinary()...)
	defer unsub()

	ref := RefAt("linear", "issue", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())
	set(t, s, ref, Obj{"stage": "reviewed"}.Payload())
	remove(t, s, ref)

	want := []Op{Set, Set, Delete}
	for i, w := range want {
		ev := oneChange(t, ch, "the observed change")
		if ev.Op != w || ev.Ref != ref {
			t.Fatalf("event %d = %+v, want op %s ref %s", i, ev, w, ref)
		}
	}
}

func TestListPredicate(t *testing.T) {
	s := openStore(t)
	// An entity with no fields is an EMPTY OBJECT: a nil value means "nothing stands here",
	// which is a removal, not a blank entity.
	set(t, s, RefAt("linear", "issue", "1"), Obj{}.Payload())
	set(t, s, RefAt("github", "pr", "2"), Obj{}.Payload())
	set(t, s, RefAt("linear", "issue", "3"), Obj{}.Payload())

	linear := s.List(func(r Ref) bool { return r.Under("linear") })
	if len(linear) != 2 {
		t.Fatalf("want 2 linear entities, got %d", len(linear))
	}
	all := s.List(nil)
	if len(all) != 3 {
		t.Fatalf("want 3 entities, got %d", len(all))
	}
}

// The cursor is opaque and monotonic — commits are sparse, so a consumer asks "what is
// after this cursor", never "what is at cursor+1".
func TestRevAndSince(t *testing.T) {
	s := openStore(t)
	start := s.Rev()
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())
	mid := s.Rev()
	set(t, s, ref, Obj{"stage": "reviewed"}.Payload())
	end := s.Rev()
	if !(start < mid && mid < end) {
		t.Fatalf("revs did not advance: start=%d mid=%d end=%d", start, mid, end)
	}

	// From the start both replay, in order; from mid only the second; from current, nothing.
	commits, err := s.Since(start, ordinary()...)
	evs := changes(commits)
	if err != nil || len(evs) != 2 || Str(evs[0].To, "stage") != "open" || Str(evs[1].To, "stage") != "reviewed" {
		t.Fatalf("Since(start) = %+v err=%v", evs, err)
	}
	if evs[0].Rev >= evs[1].Rev {
		t.Fatalf("replay is not ordered: %d then %d", evs[0].Rev, evs[1].Rev)
	}
	// Two writes are two commits, not one holding both: a commit is a transaction, and
	// these were not written together.
	if len(commits) != 2 {
		t.Fatalf("Since(start) = %d commits, want 2 (one per write)", len(commits))
	}
	if evs, err := s.Since(mid, ordinary()...); err != nil || len(changes(evs)) != 1 || Str(changes(evs)[0].To, "stage") != "reviewed" {
		t.Fatalf("Since(mid) = %+v err=%v", evs, err)
	}
	if evs, err := s.Since(end, ordinary()...); err != nil || len(evs) != 0 {
		t.Fatalf("Since(end) = %+v err=%v, want empty", evs, err)
	}
}

// A cursor is about the STORE, not about this process — so a store that opens onto an
// already-written viff answers for the commits that preceded it.
//
// This asserted the opposite, and the opposite was a guard rather than a decision: a
// per-instance low-watermark added when logd under-delivered a compacted range silently, kept
// after that was fixed. What it cost is what a cursor is for. `--since` names a store revision
// — the CLI's own help says so — so a reader asking for one is asking about the log, and
// refusing on where THIS instance happened to open answers a question nobody asked. Every
// restart truncated readable history to "since the restart", which from outside looks like
// flakiness rather than truncation.
//
// A range logd genuinely cannot replay is still ok=false, reported by logd itself:
// TestSinceBelowReplayFloorResyncs compacts one away and asserts exactly that.
func TestSinceIsAboutTheStoreNotThisProcess(t *testing.T) {
	addr := startLogd(t)
	first := openStoreAt(t, addr)
	ref := RefAt("demo", "x", "1")
	for i := range 5 {
		set(t, first, ref, Obj{"n": i}.Payload())
	}

	late := openStoreAt(t, addr) // opens at commit 5; commits 1..5 are before its time
	evs, err := late.Since(0, ordinary()...)
	if err != nil {
		t.Fatal("a store that opened late cannot read history logd still holds — the cursor " +
			"is a store revision, not a mark in this process's memory")
	}
	if len(evs) == 0 {
		t.Fatal("Since(0) answered ok with nothing, over five commits that are still there")
	}
	cur := late.Rev()
	set(t, first, ref, Obj{"n": 99}.Payload())
	if evs, err := late.Since(cur, ordinary()...); err != nil || len(evs) != 1 || evs[0].Rev <= cur {
		t.Fatalf("Since(%d) within window = %+v err=%v", cur, evs, err)
	}
}

func TestObserveGapMarkerUnderBackpressure(t *testing.T) {
	s := openStore(t)
	ch, unsub := s.Observe(ordinary()...)
	defer unsub()

	ref := RefAt("x", "y", "z")
	for i := range 300 { // overflow the 256 buffer without draining
		set(t, s, ref, Obj{"n": i}.Payload())
	}
	// A full buffer is not yet a drop — the writes race the watch, and 256 delivered with
	// the rest still in flight looks the same from here. Wait for it to fill, then write
	// MORE without reading: those have nowhere to go, so they are dropped for certain.
	eventually(t, "the observer buffer to fill", func() bool { return len(ch) == 256 })
	for i := range 50 {
		set(t, s, ref, Obj{"n": 1000 + i}.Payload())
	}
	eventually(t, "the surplus to be delivered", func() bool { return s.Rev() >= int64(300) })

	for range 10 { // free slots, then emit → the gap marker flushes
		<-ch
	}
	set(t, s, ref, Obj{"n": 999}.Payload())

	deadline := time.After(10 * time.Second)
	for {
		select {
		case c := <-ch:
			if c.Gap > 0 {
				return
			}
		case <-deadline:
			t.Fatal("expected a gap marker after backpressure drops")
		}
	}
}

// A namespace-as-query returns the same shape List gives, so cross-cutting
// selection needs no separate construct in v0.
func TestNamespaceIsAQuery(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("github", "pr", "1"), Obj{"env": "prod"}.Payload())
	set(t, s, RefAt("github", "pr", "2"), Obj{"env": "staging"}.Payload())
	prodTouching := s.List(func(r Ref) bool {
		e, _, _ := s.Get(r)
		return ToObj(e.Payload)["env"] == "prod"
	})
	if len(prodTouching) != 1 || prodTouching[0].Ref != RefAt("github", "pr", "1") {
		t.Fatalf("prod-touching query wrong: %+v", prodTouching)
	}
}

// changes flattens commits to the per-entity changes they carry, in order. Most of this
// package's tests are about what a change SAYS — its op, its level payload, the revision it
// happened at — rather than about which commit carried it, so they read the stream flat and
// stay written the way they were.
func changes(commits []Commit) []Event {
	var out []Event
	for _, c := range commits {
		out = append(out, c.Changes...)
	}
	return out
}

// oneChange takes the next commit off a stream and returns its single change, failing if the
// commit carries any other number. The writes in these tests touch one entity each, so a
// commit holding two is a surprise worth stopping on rather than an event worth picking.
func oneChange(t *testing.T, ch <-chan Commit, why string) Event {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatalf("commit stream closed waiting for %s", why)
		}
		if len(c.Changes) != 1 {
			t.Fatalf("commit %d carries %d changes waiting for %s, want 1: %+v",
				c.Rev, len(c.Changes), why, c.Changes)
		}
		return c.Changes[0]
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", why)
	}
	return Event{}
}
