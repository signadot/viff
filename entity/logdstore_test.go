package entity

import (
	"sync"
	"testing"
	"time"

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
// reach n. Exercises MemStore's lock and the logd store's PatchIf CAS loop.
func concurrentIncrement(t *testing.T, store Store) {
	t.Helper()
	ref := Ref{System: "demo", Kind: "counter", ID: "1"}
	store.Put(ref, Payload{})
	const n = 25

	var wg sync.WaitGroup
	for _, field := range []string{"a", "b"} {
		wg.Add(1)
		go func(f string) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				store.Modify(ref, func(cur Payload) Payload {
					p := Payload{}
					for k, v := range cur {
						p[k] = v
					}
					p[f] = asInt(p[f]) + 1
					return p
				})
			}
		}(field)
	}
	wg.Wait()

	e, ok := store.Get(ref)
	if !ok {
		t.Fatal("entity missing after increments")
	}
	if a := asInt(e.Payload["a"]); a != n {
		t.Fatalf("a = %d, want %d (lost update)", a, n)
	}
	if b := asInt(e.Payload["b"]); b != n {
		t.Fatalf("b = %d, want %d (lost update)", b, n)
	}
}

func TestMemStore_ModifyNoLostUpdate(t *testing.T)  { concurrentIncrement(t, NewMemStore()) }
func TestLogdStore_ModifyNoLostUpdate(t *testing.T) { concurrentIncrement(t, openStore(t)) }

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

func openStore(t *testing.T) *LogdStore {
	t.Helper()
	s, err := OpenLogdStore(LogdConfig{Addr: startLogd(t), ClientID: "verse-test"})
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	s.Put(ref, Payload{"stage": "open", "num": int64(42), "ok": true})

	e, ok := s.Get(ref)
	if !ok {
		t.Fatal("Get after Put: not found")
	}
	if e.Payload["stage"] != "open" || e.Payload["num"] != int64(42) || e.Payload["ok"] != true {
		t.Fatalf("payload not preserved: %#v", e.Payload)
	}
	if _, ok := s.Get(Ref{System: "demo", Kind: "pr", ID: "nope"}); ok {
		t.Fatal("Get of absent ref returned found")
	}
}

// An id with characters that are significant to kinded paths ('/', '#') must still
// round-trip as a single segment.
func TestLogdStore_QuotedID(t *testing.T) {
	s := openStore(t)
	ref := Ref{System: "github", Kind: "pr", ID: "signadot/x#42"}
	s.Put(ref, Payload{"stage": "review"})
	e, ok := s.Get(ref)
	if !ok || e.Payload["stage"] != "review" {
		t.Fatalf("quoted-id round-trip failed: ok=%v payload=%#v", ok, e.Payload)
	}
}

// Put has replace (not merge) semantics: a field dropped from the payload is gone
// on read-back, matching MemStore.
func TestLogdStore_PutReplaces(t *testing.T) {
	s := openStore(t)
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	s.Put(ref, Payload{"stage": "open", "extra": "x"})
	s.Put(ref, Payload{"stage": "reviewing"}) // drops "extra"

	e, _ := s.Get(ref)
	if e.Payload["stage"] != "reviewing" {
		t.Fatalf("stage not updated: %#v", e.Payload)
	}
	if _, present := e.Payload["extra"]; present {
		t.Fatalf("Put did not replace: stale field remains: %#v", e.Payload)
	}
}

// Delete reports prior existence and makes the entity absent.
func TestLogdStore_Delete(t *testing.T) {
	s := openStore(t)
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	if _, ok := s.Delete(ref); ok {
		t.Fatal("Delete of absent ref reported existed")
	}
	s.Put(ref, Payload{"stage": "open"})
	ev, ok := s.Delete(ref)
	if !ok {
		t.Fatal("Delete of present ref reported absent")
	}
	if ev.Op != Delete || ev.From["stage"] != "open" {
		t.Fatalf("delete event wrong: %#v", ev)
	}
	if _, ok := s.Get(ref); ok {
		t.Fatal("entity still present after Delete")
	}
}

// A scope is a copy-on-write overlay: it reads baseline through, its writes are
// isolated from baseline, and DeleteScope tears the overlay down.
func TestLogdStore_ScopeIsolation(t *testing.T) {
	base := openStore(t)
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	base.Put(ref, Payload{"stage": "open"})

	scoped, ok := base.Scope("me").(*LogdStore)
	if !ok {
		t.Fatal("Scope did not return an isolating *LogdStore")
	}

	// Reads see baseline through the overlay.
	if e, ok := scoped.Get(ref); !ok || e.Payload["stage"] != "open" {
		t.Fatalf("scope did not read baseline through: ok=%v payload=%#v", ok, e.Payload)
	}

	// A scope write is isolated: the scope sees it, baseline does not.
	scoped.Put(ref, Payload{"stage": "reviewing"})
	if e, _ := scoped.Get(ref); e.Payload["stage"] != "reviewing" {
		t.Fatalf("scope write not visible in scope: %#v", e.Payload)
	}
	if e, _ := base.Get(ref); e.Payload["stage"] != "open" {
		t.Fatalf("scope write leaked into baseline: %#v", e.Payload)
	}

	// A scope-only entity does not exist in baseline.
	only := Ref{System: "demo", Kind: "pr", ID: "scope-only"}
	scoped.Put(only, Payload{"x": "1"})
	if _, ok := base.Get(only); ok {
		t.Fatal("scope-only entity leaked into baseline")
	}
	if _, ok := scoped.Get(only); !ok {
		t.Fatal("scope-only entity missing from scope")
	}

	// DeleteScope removes the overlay: a fresh scope view reads baseline again.
	if err := base.DeleteScope("me"); err != nil {
		t.Fatalf("DeleteScope: %v", err)
	}
	fresh := base.Scope("me")
	if e, _ := fresh.Get(ref); e.Payload["stage"] != "open" {
		t.Fatalf("after DeleteScope, scope still overlaid: %#v", e.Payload)
	}
	if _, ok := fresh.Get(only); ok {
		t.Fatal("after DeleteScope, scope-only entity survived")
	}

	// A scoped store may not delete scopes.
	if err := scoped.DeleteScope("me"); err == nil {
		t.Fatal("DeleteScope from a scoped store should error")
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
	ref := Ref{System: "demo", Kind: "pr", ID: "1"}
	s.Put(ref, Payload{"stage": "open"})      // would have been "create"
	s.Put(ref, Payload{"stage": "reviewing"}) // would have been "update"

	var first []Event
	eventually(t, "Since to replay both writes", func() bool {
		ev, ok := s.Since(start)
		if !ok || len(ev) < 2 {
			return false
		}
		first = ev
		return true
	})
	for _, e := range first {
		if e.Op != Set {
			t.Fatalf("expected Op=Set (create/update not distinguished), got %q for %s", e.Op, e.Ref)
		}
	}
	// idempotent: a second identical query yields the same events, unchanged Op.
	second, ok := s.Since(start)
	if !ok || len(second) != len(first) {
		t.Fatalf("Since not idempotent: first=%d second=%d", len(first), len(second))
	}
	for i := range first {
		if first[i].Ref != second[i].Ref || first[i].Op != second[i].Op || first[i].Rev != second[i].Rev {
			t.Fatalf("Since drifted at %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// A cursor older than a store instance's sync point can't be served (logd may have
// compacted it, and under-delivers silently) — Since returns ok=false so the
// consumer resyncs from current state.
func TestLogdStore_SinceBelowWatermarkResyncs(t *testing.T) {
	addr := startLogd(t)
	s1, err := OpenLogdStore(LogdConfig{Addr: addr, ClientID: "hist"})
	if err != nil {
		t.Fatal(err)
	}
	s1.Put(Ref{System: "demo", Kind: "pr", ID: "1"}, Payload{"stage": "open"})
	s1.Put(Ref{System: "demo", Kind: "pr", ID: "2"}, Payload{"stage": "open"})
	eventually(t, "history to land", func() bool { return s1.Rev() >= 2 })
	s1.Close()

	// s2 opens at the current (high) commit; a cursor from before it synced is below
	// its watermark.
	s2, err := OpenLogdStore(LogdConfig{Addr: addr, ClientID: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, ok := s2.Since(0); ok {
		t.Fatal("Since(0) below the store's open watermark should return ok=false (resync)")
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
		{System: "demo", Kind: "pr", ID: "1"},
		{System: "demo", Kind: "pr", ID: "2"},
		{System: "demo", Kind: "pr", ID: "3"},
	}
	for i, r := range refs {
		s.Put(r, Payload{"stage": "open", "i": int64(i)})
	}

	// Rev advances past the start as the watch observes the writes.
	eventually(t, "Rev to advance", func() bool { return s.Rev() > start })

	// The observer sees the live changes (allowing for gap/drop accounting).
	seen := map[string]bool{}
	timeout := time.After(3 * time.Second)
	for len(seen) < len(refs) {
		select {
		case ev := <-ch:
			if ev.Ref.ID != "" {
				seen[ev.Ref.String()] = true
			}
		case <-timeout:
			t.Fatalf("observer saw %d/%d changes: %v", len(seen), len(refs), seen)
		}
	}

	// Since replays the whole window from before the writes.
	var evs []Event
	eventually(t, "Since to replay all writes", func() bool {
		got, ok := s.Since(start)
		if !ok {
			return false
		}
		evs = got
		return len(got) >= len(refs)
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
