package entity

import "testing"

func TestParseRefRoundTrip(t *testing.T) {
	cases := []string{"linear:issue:1090", "github:pr:signadot/hack#12", "process:instance:p1"}
	for _, s := range cases {
		r, err := ParseRef(s)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", s, err)
		}
		if r.String() != s {
			t.Fatalf("round trip: %q -> %q", s, r.String())
		}
	}
	for _, bad := range []string{"", "a:b", "a::c", ":b:c", "nope"} {
		if _, err := ParseRef(bad); err == nil {
			t.Fatalf("ParseRef(%q) should fail", bad)
		}
	}
}

func TestPutCreateThenUpdate(t *testing.T) {
	s := NewMemStore()
	ref := Ref{"linear", "issue", "1090"}

	ev := s.Put(ref, Payload{"stage": "open"})
	if ev.Op != Set || ev.From != nil || ev.To["stage"] != "open" {
		t.Fatalf("create event wrong: %+v", ev)
	}
	ev = s.Put(ref, Payload{"stage": "reviewed"})
	if ev.Op != Set || ev.From["stage"] != "open" || ev.To["stage"] != "reviewed" {
		t.Fatalf("update event wrong: %+v", ev)
	}
	got, ok := s.Get(ref)
	if !ok || got.Payload["stage"] != "reviewed" {
		t.Fatalf("get after update: %+v ok=%v", got, ok)
	}
}

func TestPutClonesPayload(t *testing.T) {
	s := NewMemStore()
	ref := Ref{"x", "y", "z"}
	p := Payload{"n": 1}
	s.Put(ref, p)
	p["n"] = 999 // mutate caller's map after Put
	got, _ := s.Get(ref)
	if got.Payload["n"] != 1 {
		t.Fatalf("store aliased caller payload: %+v", got.Payload)
	}
}

func TestDeleteAndMissing(t *testing.T) {
	s := NewMemStore()
	ref := Ref{"x", "y", "z"}
	if _, ok := s.Delete(ref); ok {
		t.Fatal("delete of missing should report false")
	}
	s.Put(ref, Payload{"a": 1})
	ev, ok := s.Delete(ref)
	if !ok || ev.Op != Delete || ev.From["a"] != 1 || ev.To != nil {
		t.Fatalf("delete event wrong: %+v ok=%v", ev, ok)
	}
	if _, ok := s.Get(ref); ok {
		t.Fatal("entity still present after delete")
	}
}

func TestObserveDeliversChanges(t *testing.T) {
	s := NewMemStore()
	ch, unsub := s.Observe()
	defer unsub()

	ref := Ref{"linear", "issue", "1"}
	s.Put(ref, Payload{"stage": "open"})
	s.Put(ref, Payload{"stage": "reviewed"})
	s.Delete(ref)

	want := []Op{Set, Set, Delete}
	for i, w := range want {
		ev := <-ch
		if ev.Op != w || ev.Ref != ref {
			t.Fatalf("event %d = %+v, want op %s ref %s", i, ev, w, ref)
		}
	}
}

func TestListPredicate(t *testing.T) {
	s := NewMemStore()
	s.Put(Ref{"linear", "issue", "1"}, nil)
	s.Put(Ref{"github", "pr", "2"}, nil)
	s.Put(Ref{"linear", "issue", "3"}, nil)

	linear := s.List(func(r Ref) bool { return r.System == "linear" })
	if len(linear) != 2 {
		t.Fatalf("want 2 linear entities, got %d", len(linear))
	}
	all := s.List(nil)
	if len(all) != 3 {
		t.Fatalf("want 3 entities, got %d", len(all))
	}
}

func TestRevAndSince(t *testing.T) {
	s := NewMemStore()
	if s.Rev() != 0 {
		t.Fatalf("fresh Rev = %d, want 0", s.Rev())
	}
	ref := Ref{"demo", "pr", "1"}
	e1 := s.Put(ref, Payload{"stage": "open"})     // rev 1
	e2 := s.Put(ref, Payload{"stage": "reviewed"}) // rev 2
	if e1.Rev != 1 || e2.Rev != 2 || s.Rev() != 2 {
		t.Fatalf("revs wrong: e1=%d e2=%d cur=%d", e1.Rev, e2.Rev, s.Rev())
	}

	// Since(0) replays both, in order; Since(1) only the second; Since(2) nothing.
	if evs, ok := s.Since(0); !ok || len(evs) != 2 || evs[0].Rev != 1 || evs[1].Rev != 2 {
		t.Fatalf("Since(0) = %+v ok=%v", evs, ok)
	}
	if evs, ok := s.Since(1); !ok || len(evs) != 1 || evs[0].Rev != 2 {
		t.Fatalf("Since(1) = %+v ok=%v", evs, ok)
	}
	if evs, ok := s.Since(2); !ok || len(evs) != 0 {
		t.Fatalf("Since(2) = %+v ok=%v, want empty", evs, ok)
	}
}

// A cursor older than the retained window returns ok=false — the caller resyncs.
func TestSinceTooOld(t *testing.T) {
	s := NewMemStore()
	s.histCap = 3 // retain only the last 3 changes
	ref := Ref{"demo", "x", "1"}
	for i := 0; i < 5; i++ { // revs 1..5; only 3..5 retained
		s.Put(ref, Payload{"n": i})
	}
	if _, ok := s.Since(0); ok {
		t.Fatal("Since(0) should be too-old (revs 1,2 dropped)")
	}
	if evs, ok := s.Since(3); !ok || len(evs) != 2 || evs[0].Rev != 4 {
		t.Fatalf("Since(3) within window = %+v ok=%v", evs, ok)
	}
}

func TestObserveGapMarkerUnderBackpressure(t *testing.T) {
	s := NewMemStore()
	ch, unsub := s.Observe()
	defer unsub()

	ref := Ref{"x", "y", "z"}
	for i := 0; i < 300; i++ { // overflow the 256 buffer without draining
		s.Put(ref, Payload{"n": i})
	}
	for i := 0; i < 10; i++ { // free slots, then emit → gap marker flushes
		<-ch
	}
	s.Put(ref, Payload{"n": 999})

	sawGap := false
	for {
		select {
		case ev := <-ch:
			if ev.Gap > 0 {
				sawGap = true
			}
		default:
			if !sawGap {
				t.Fatal("expected a gap marker after backpressure drops")
			}
			return
		}
	}
}

// A namespace-as-query returns the same shape List gives, so cross-cutting
// selection needs no separate construct in v0.
func TestNamespaceIsAQuery(t *testing.T) {
	s := NewMemStore()
	s.Put(Ref{"github", "pr", "1"}, Payload{"env": "prod"})
	s.Put(Ref{"github", "pr", "2"}, Payload{"env": "staging"})
	prodTouching := s.List(func(r Ref) bool {
		e, _ := s.Get(r)
		return e.Payload["env"] == "prod"
	})
	if len(prodTouching) != 1 || prodTouching[0].Ref.ID != "1" {
		t.Fatalf("prod-touching query wrong: %+v", prodTouching)
	}
}
