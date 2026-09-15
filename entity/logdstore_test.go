package entity

import (
	"sync"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"
)

func asInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// concurrentIncrement has two goroutines read-modify-write the same entity,
// incrementing disjoint fields n times each. Without atomic Modify (compare-and-
// swap + retry), interleaved read→write would lose increments; with it, both fields
// reach n. Exercises the store's PatchIf CAS loop.
func concurrentIncrement(t *testing.T, store Store) {
	t.Helper()
	ref := RefAt("demo", "counter", "1")
	set(t, store, ref, Obj{}.Payload())
	const n = 25

	var wg sync.WaitGroup
	for _, field := range []string{"a", "b"} {
		wg.Add(1)
		go func(f string) {
			defer wg.Done()
			for range n {
				if err := Modify(t.Context(), store, ref, func(cur Payload) Payload {
					p := ToObj(cur)
					if p == nil {
						p = Obj{}
					}
					p[f] = asInt(p[f]) + 1
					return p.Payload()
				}); err != nil {
					t.Errorf("modify: %v", err)
					return
				}
			}
		}(field)
	}
	wg.Wait()

	e, ok, _ := store.Get(ref)
	if !ok {
		t.Fatal("entity missing after increments")
	}
	if a := asInt(ToObj(e.Payload)["a"]); a != n {
		t.Fatalf("a = %d, want %d (lost update)", a, n)
	}
	if b := asInt(ToObj(e.Payload)["b"]); b != n {
		t.Fatalf("b = %d, want %d (lost update)", b, n)
	}
}

// startLogd brings up an in-process logd on an ephemeral port, backed by a temp
// dir, and returns its address. Mirrors go-tony's own test harness.
func startLogd(t *testing.T) string {
	t.Helper()
	store, err := storage.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := logdserver.New(&logdserver.Spec{Storage: store})
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start logd: %v", err)
	}
	t.Cleanup(func() { srv.StopTCP() })
	return srv.TCPAddr()
}

// openStore opens a store the way a person runs one: THROUGH DOCD. `verse up` brings up
// logd and docd and points the store at docd's client face, so a store spoken straight to
// logd is a configuration nobody deploys — and the two do not accept the same writes. docd
// decomposes a patch by mount, statically, over the path's FIELD names, so it refuses a
// path holding an array index that logd takes (tony-format: docd non-field segment). These
// tests said an array element could be written; against every real verse it could not.
//
// A test that needs the logd underneath — one restarting it, or reading its commits — takes
// startLogd directly.
func openStore(t *testing.T) *LogdStore {
	return openStoreAt(t, startDocd(t, startLogd(t)).ClientTCPAddr())
}

// openStoreAt opens a store on an already-running logd — for a test that needs two
// clients (or two opened at different commits) over one substrate.
func openStoreAt(t *testing.T, addr string) *LogdStore {
	t.Helper()
	s, err := OpenLogdStore(LogdConfig{Addr: addr, ClientID: "verse-test"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// eventually polls cond until true or the deadline, failing otherwise.
func eventually(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", why)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A round-trip through a real logd: Put is readable via Get, with the payload
// preserved across the ir.Node encoding.
func TestLogdStore_PutGet(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open", "num": int64(42), "ok": true}.Payload())

	e, ok, _ := s.Get(ref)
	if !ok {
		t.Fatal("Get after Put: not found")
	}
	if ToObj(e.Payload)["stage"] != "open" || ToObj(e.Payload)["num"] != int64(42) || ToObj(e.Payload)["ok"] != true {
		t.Fatalf("payload not preserved: %#v", e.Payload)
	}
	if _, ok, _ := s.Get(RefAt("demo", "pr", "nope")); ok {
		t.Fatal("Get of absent ref returned found")
	}
}

// An id with characters that are significant to kinded paths ('/', '#') must still
// round-trip as a single segment.
func TestLogdStore_QuotedID(t *testing.T) {
	s := openStore(t)
	ref := RefAt("github", "pr", "signadot/x#42")
	set(t, s, ref, Obj{"stage": "review"}.Payload())
	e, ok, _ := s.Get(ref)
	if !ok || ToObj(e.Payload)["stage"] != "review" {
		t.Fatalf("quoted-id round-trip failed: ok=%v payload=%#v", ok, e.Payload)
	}
}

// Removing an entity makes it absent, and observers see a Delete for it.
func TestLogdStore_Remove(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())

	ch, unsub := s.Observe()
	defer unsub()
	eventually(t, "the watch to be live", func() bool { return s.Rev() > 0 })

	remove(t, s, ref)
	if _, ok, _ := s.Get(ref); ok {
		t.Fatal("entity still present after being removed")
	}
	if ev := oneChange(t, ch, "the removal"); ev.Op != Delete || ev.Ref != ref {
		t.Fatalf("event = %#v, want a Delete for %s", ev, ref)
	}
}

// A scope is a copy-on-write overlay: it reads baseline through, its writes are
// isolated from baseline, and DeleteScope tears the overlay down.
func TestLogdStore_ScopeIsolation(t *testing.T) {
	base := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, base, ref, Obj{"stage": "open"}.Payload())

	scoped, ok := base.Scope("me").(*LogdStore)
	if !ok {
		t.Fatal("Scope did not return an isolating *LogdStore")
	}

	// Reads see baseline through the overlay.
	if e, ok, _ := scoped.Get(ref); !ok || ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("scope did not read baseline through: ok=%v payload=%#v", ok, e.Payload)
	}

	// A scope write is isolated: the scope sees it, baseline does not.
	set(t, scoped, ref, Obj{"stage": "reviewing"}.Payload())
	if e, _, _ := scoped.Get(ref); ToObj(e.Payload)["stage"] != "reviewing" {
		t.Fatalf("scope write not visible in scope: %#v", e.Payload)
	}
	if e, _, _ := base.Get(ref); ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("scope write leaked into baseline: %#v", e.Payload)
	}

	// A scope-only entity does not exist in baseline.
	only := RefAt("demo", "pr", "scope-only")
	set(t, scoped, only, Obj{"x": "1"}.Payload())
	if _, ok, _ := base.Get(only); ok {
		t.Fatal("scope-only entity leaked into baseline")
	}
	if _, ok, _ := scoped.Get(only); !ok {
		t.Fatal("scope-only entity missing from scope")
	}

	// DeleteScope removes the overlay: a fresh scope view reads baseline again.
	if err := base.DeleteScope("me"); err != nil {
		t.Fatalf("DeleteScope: %v", err)
	}
	fresh := base.Scope("me")
	if e, _, _ := fresh.Get(ref); ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("after DeleteScope, scope still overlaid: %#v", e.Payload)
	}
	if _, ok, _ := fresh.Get(only); ok {
		t.Fatal("after DeleteScope, scope-only entity survived")
	}

	// A scoped store may not delete scopes.
	if err := scoped.DeleteScope("me"); err == nil {
		t.Fatal("DeleteScope from a scoped store should error")
	}
}

// The same rule one level down: a scope can hide a SLOT it does not own.
//
// This was the open question a shared entity raised. A slot-claiming source converges inside a
// scope exactly as a whole-entity one does — it writes what external truth has at its own path
// and tombstones what it does not — and inside a scope most of what it finds standing is
// inherited. If a scoped delete could only reach a scope-local slot, a scoped source could not
// say "in this sandbox that team does not own this project", and the answer would be stuck with
// baseline's owner set as a floor. It can, and neither direction leaks: the scoped write does
// not reach baseline's value, and the scoped hide does not remove baseline's key.
func TestLogdStore_ScopeHidesAnInheritedSlot(t *testing.T) {
	base := openStore(t)
	ref := RefAt("demo", "project", "shared")
	set(t, base, ref, Obj{"owners": map[string]any{
		"ENG": map[string]any{"name": "baseline-eng"},
		"OPS": map[string]any{"name": "baseline-ops"},
	}}.Payload())
	scoped := base.Scope("me")

	owners := func(s Store) map[string]*ir.Node {
		e, ok, err := s.Get(ref)
		if err != nil || !ok {
			t.Fatalf("get %s: ok=%v err=%v", ref, ok, err)
		}
		return Fields(Get(e.Payload, "owners"))
	}
	if len(owners(scoped)) != 2 {
		t.Fatal("precondition: the scope should read both inherited slots through")
	}

	// The scope writes its own value at one slot. Baseline keeps its own.
	if _, _, err := scoped.Write(Write{At: ref.At("owners", "ENG"),
		Value: Obj{"name": "scoped-eng"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if got := Str(owners(scoped)["ENG"], "name"); got != "scoped-eng" {
		t.Fatalf("the scoped slot write did not land: %q", got)
	}
	if got := Str(owners(base)["ENG"], "name"); got != "baseline-eng" {
		t.Fatalf("a scoped slot write reached baseline: %q", got)
	}

	// And it hides a slot it never wrote, leaving baseline holding it.
	if _, _, err := scoped.Write(Write{At: ref.At("owners", "OPS")}); err != nil {
		t.Fatal(err)
	}
	if _, still := owners(scoped)["OPS"]; still {
		t.Fatal("scoped delete of an inherited slot did not hide it")
	}
	if _, gone := owners(base)["OPS"]; !gone {
		t.Fatal("a scoped slot hide reached baseline")
	}
}

// A scope can delete an entity it does not own: removing an INHERITED baseline entity
// hides it in the scope and leaves baseline holding it. The overlay's read-through is
// therefore not one-way — a scope can say "not here" about something it never wrote.
//
// This is what lets a scoped reflection be level-triggered. A converge writes what the
// external truth has and tombstones what it does not, and inside a scope most of what it
// finds standing is inherited; if a scoped delete could only remove scope-local entities,
// a scoped source could not say "that branch does not exist in this sandbox" and every
// scope would be stuck with baseline's set as a floor. See docs/sources.md.
func TestLogdStore_ScopeHidesAnInheritedEntity(t *testing.T) {
	base := openStore(t)
	ref := RefAt("demo", "pr", "inherited")
	set(t, base, ref, Obj{"stage": "open"}.Payload())

	scoped := base.Scope("me")
	inDemo := func(r Ref) bool { return r.Under("demo") }
	if _, ok, _ := scoped.Get(ref); !ok {
		t.Fatal("precondition: the scope should read the baseline entity through")
	}

	remove(t, scoped, ref)

	// Hidden in the scope, by both faces of a read — Get and List. List is the one the
	// converge path uses to decide what to tombstone, so a hide it did not honour would
	// leave a scoped reflection re-tombstoning the same entity on every pass.
	if _, ok, _ := scoped.Get(ref); ok {
		t.Fatal("scoped delete of an inherited entity did not hide it from Get")
	}
	if n := len(scoped.List(inDemo)); n != 0 {
		t.Fatalf("scoped delete did not hide the entity from List: %d entities remain", n)
	}

	// And baseline still has it: the hide is an overlay fact, not a delete.
	if e, ok, _ := base.Get(ref); !ok || ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("a scoped delete reached baseline: ok=%v payload=%#v", ok, e.Payload)
	}

	// The scope can bring it back by writing its own, which is what a reflection does when
	// the branch reappears.
	set(t, scoped, ref, Obj{"stage": "reopened"}.Payload())
	if e, ok, _ := scoped.Get(ref); !ok || ToObj(e.Payload)["stage"] != "reopened" {
		t.Fatalf("scope could not rewrite what it had hidden: ok=%v payload=%#v", ok, e.Payload)
	}

	// Dropping the overlay drops the hide with it.
	remove(t, scoped, ref)
	if err := base.DeleteScope("me"); err != nil {
		t.Fatalf("DeleteScope: %v", err)
	}
	if _, ok, _ := base.Scope("me").Get(ref); !ok {
		t.Fatal("after DeleteScope the hide survived: a fresh scope should read baseline again")
	}
}

// Scope caches its children: asking twice yields the same live store.
func TestLogdStore_ScopeCaching(t *testing.T) {
	base := openStore(t)
	if base.Scope("me") != base.Scope("me") {
		t.Fatal("Scope did not cache the scoped child")
	}
	if base.Scope("") != Store(base) {
		t.Fatal("empty scope should return the baseline store")
	}
}

// Since is a pure read: it derives Op from the delta (all Set here, create/update
// not distinguished) and returns identical events on repeated calls — no shared
// state mutated, so it is idempotent.
func TestLogdStore_SinceIsPureSet(t *testing.T) {
	s := openStore(t)
	start := s.Rev()
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())      // would have been "create"
	set(t, s, ref, Obj{"stage": "reviewing"}.Payload()) // would have been "update"

	var first []Event
	eventually(t, "Since to replay both writes", func() bool {
		commits, err := s.Since(start)
		if err != nil || len(changes(commits)) < 2 {
			return false
		}
		first = changes(commits)
		return true
	})
	for _, e := range first {
		if e.Op != Set {
			t.Fatalf("expected Op=Set (create/update not distinguished), got %q for %s", e.Op, e.Ref)
		}
	}
	// idempotent: a second identical query yields the same events, unchanged Op.
	secondCommits, err := s.Since(start)
	second := changes(secondCommits)
	if err != nil || len(second) != len(first) {
		t.Fatalf("Since not idempotent: first=%d second=%d", len(first), len(second))
	}
	for i := range first {
		if first[i].Ref != second[i].Ref || first[i].Op != second[i].Op || first[i].Rev != second[i].Rev {
			t.Fatalf("Since drifted at %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// Concurrent Since calls must all replay, on one session, with no serialization. This is
// what let the store drop its second "-replay" session and the mutex that serialized
// replays against it: a session may hold several watches on the SAME path, each keyed by
// its request id (go-tony ≥ v0.0.85). Run under -race, since removing that mutex is
// exactly the kind of change a data race would punish.
//
// Both faces are covered because the admission rule lives in two places: logd's session
// (direct) and docd's per-watch bookkeeping (which is what verse actually talks to).
func TestLogdStore_ConcurrentSince(t *testing.T) {
	run := func(t *testing.T, s *LogdStore) {
		t.Helper()
		start := s.Rev()
		refs := []Ref{
			RefAt("demo", "pr", "1"),
			RefAt("demo", "pr", "2"),
			RefAt("demo", "pr", "3"),
		}
		for i, r := range refs {
			set(t, s, r, Obj{"stage": "open", "i": int64(i)}.Payload())
		}
		// Let the writes land, so every concurrent replay has the same window to see.
		eventually(t, "writes to be replayable", func() bool {
			evs, err := s.Since(start)
			return err == nil && len(evs) >= len(refs)
		})

		const n = 8
		type result struct {
			evs []Event
			ok  bool
		}
		results := make([]result, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				commits, err := s.Since(start)
				results[i] = result{changes(commits), err == nil}
			}(i)
		}
		wg.Wait()

		for i, got := range results {
			if !got.ok {
				t.Fatalf("concurrent Since #%d returned ok=false — replays are interfering "+
					"(a session must admit several id-keyed watches on one path)", i)
			}
			for _, r := range refs {
				if !hasRef(got.evs, r) {
					t.Fatalf("concurrent Since #%d missed %s (got %d events) — a concurrent "+
						"replay on the same path lost events", i, r, len(got.evs))
				}
			}
		}
	}

	t.Run("direct logd", func(t *testing.T) { run(t, openStore(t)) })
	t.Run("through docd", func(t *testing.T) {
		s, _ := storeOverDocd(t)
		run(t, s)
	})
}

// A store answers Since for commits that landed before it opened: the cursor names a STORE
// revision, not a mark in this process's memory, so a restarted verse still replays what its
// predecessor wrote.
//
// This asserted the opposite, and called itself deliberately conservative — logd may well still
// hold that range, but a store that has not observed it "cannot say so". It can: it asks. The
// watermark was added when logd under-delivered a compacted range SILENTLY and was kept after
// that was fixed, by which time the only thing it still did was truncate readable history at
// every restart. What is genuinely unreplayable is logd's to report, and it reports it
// (TestSinceBelowReplayFloorResyncs).
func TestLogdStore_SinceReadsBeforeThisStoreOpened(t *testing.T) {
	addr := startLogd(t)
	s1, err := OpenLogdStore(LogdConfig{Addr: addr, ClientID: "hist"})
	if err != nil {
		t.Fatal(err)
	}
	set(t, s1, RefAt("demo", "pr", "1"), Obj{"stage": "open"}.Payload())
	set(t, s1, RefAt("demo", "pr", "2"), Obj{"stage": "open"}.Payload())
	eventually(t, "history to land", func() bool { return s1.Rev() >= 2 })
	s1.Close()

	// s2 opens at the current (high) commit — a restart, in miniature.
	s2, err := OpenLogdStore(LogdConfig{Addr: addr, ClientID: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	commits, err := s2.Since(0)
	if err != nil {
		t.Fatal("a fresh store refused history logd still holds — this is the case that made " +
			"every restart truncate what a cursor could read")
	}
	var refs []string
	for _, c := range commits {
		for _, ev := range c.Changes {
			refs = append(refs, ev.Ref.String())
		}
	}
	if len(refs) < 2 {
		t.Errorf("replayed %v, want the two commits written before this store opened", refs)
	}
}

// The live watch drives Rev forward and delivers change events to observers, and
// Since replays the window from a cursor natively from logd.
func TestLogdStore_ObserveSinceRev(t *testing.T) {
	s := openStore(t)
	ch, unsub := s.Observe()
	defer unsub()

	start := s.Rev()
	refs := []Ref{
		RefAt("demo", "pr", "1"),
		RefAt("demo", "pr", "2"),
		RefAt("demo", "pr", "3"),
	}
	for i, r := range refs {
		set(t, s, r, Obj{"stage": "open", "i": int64(i)}.Payload())
	}

	// Rev advances past the start as the watch observes the writes.
	eventually(t, "Rev to advance", func() bool { return s.Rev() > start })

	// The observer sees the live changes (allowing for gap/drop accounting).
	seen := map[string]bool{}
	timeout := time.After(3 * time.Second)
	for len(seen) < len(refs) {
		select {
		case c := <-ch:
			for _, ev := range c.Changes {
				for _, r := range refs {
					if ev.Ref == r {
						seen[ev.Ref.String()] = true
					}
				}
			}
		case <-timeout:
			t.Fatalf("observer saw %d/%d changes: %v", len(seen), len(refs), seen)
		}
	}

	// Since replays the whole window from before the writes.
	var evs []Event
	eventually(t, "Since to replay all writes", func() bool {
		commits, err := s.Since(start)
		if err != nil {
			return false
		}
		evs = changes(commits)
		return len(evs) >= len(refs)
	})
	// Every write is present and in ascending commit order.
	last := int64(-1)
	found := map[string]bool{}
	for _, ev := range evs {
		if ev.Rev < last {
			t.Fatalf("Since out of order: %d after %d", ev.Rev, last)
		}
		last = ev.Rev
		found[ev.Ref.String()] = true
	}
	for _, r := range refs {
		if !found[r.String()] {
			t.Fatalf("Since missing %s; got %v", r, found)
		}
	}
}
