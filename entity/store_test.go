package entity

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

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

	ev := s.Put(ref, Obj{"stage": "open"}.Payload())
	if ev.Op != Set || ev.From != nil || Str(ev.To, "stage") != "open" {
		t.Fatalf("create event wrong: %+v", ev)
	}
	ev = s.Put(ref, Obj{"stage": "reviewed"}.Payload())
	if ev.Op != Set || Str(ev.From, "stage") != "open" || Str(ev.To, "stage") != "reviewed" {
		t.Fatalf("update event wrong: %+v", ev)
	}
	got, ok, _ := s.Get(ref)
	if !ok || ToObj(got.Payload)["stage"] != "reviewed" {
		t.Fatalf("get after update: %+v ok=%v", got, ok)
	}
}

func TestPutClonesPayload(t *testing.T) {
	s := NewMemStore()
	ref := Ref{"x", "y", "z"}
	p := Obj{"n": 1}.Payload()
	s.Put(ref, p)
	*ir.Get(p, "n") = *ir.FromInt(999) // mutate the caller's node after Put
	got, _, _ := s.Get(ref)
	if ToObj(got.Payload)["n"] != int64(1) {
		t.Fatalf("store aliased caller payload: %+v", got.Payload)
	}
}

func TestDeleteAndMissing(t *testing.T) {
	s := NewMemStore()
	ref := Ref{"x", "y", "z"}
	if _, ok := s.Delete(ref); ok {
		t.Fatal("delete of missing should report false")
	}
	s.Put(ref, Obj{"a": 1}.Payload())
	ev, ok := s.Delete(ref)
	if !ok || ev.Op != Delete || ToObj(ev.From)["a"] != int64(1) || ev.To != nil {
		t.Fatalf("delete event wrong: %+v ok=%v", ev, ok)
	}
	if _, ok, _ := s.Get(ref); ok {
		t.Fatal("entity still present after delete")
	}
}

func TestObserveDeliversChanges(t *testing.T) {
	s := NewMemStore()
	ch, unsub := s.Observe()
	defer unsub()

	ref := Ref{"linear", "issue", "1"}
	s.Put(ref, Obj{"stage": "open"}.Payload())
	s.Put(ref, Obj{"stage": "reviewed"}.Payload())
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
	e1 := s.Put(ref, Obj{"stage": "open"}.Payload())     // rev 1
	e2 := s.Put(ref, Obj{"stage": "reviewed"}.Payload()) // rev 2
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
		s.Put(ref, Obj{"n": i}.Payload())
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
		s.Put(ref, Obj{"n": i}.Payload())
	}
	for i := 0; i < 10; i++ { // free slots, then emit → gap marker flushes
		<-ch
	}
	s.Put(ref, Obj{"n": 999}.Payload())

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
	s.Put(Ref{"github", "pr", "1"}, Obj{"env": "prod"}.Payload())
	s.Put(Ref{"github", "pr", "2"}, Obj{"env": "staging"}.Payload())
	prodTouching := s.List(func(r Ref) bool {
		e, _, _ := s.Get(r)
		return ToObj(e.Payload)["env"] == "prod"
	})
	if len(prodTouching) != 1 || prodTouching[0].Ref.ID != "1" {
		t.Fatalf("prod-touching query wrong: %+v", prodTouching)
	}
}
