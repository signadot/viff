package entity

import (
	"fmt"
	"testing"
)

// SCRATCH HOLDS WHAT IS WRITTEN AND NOTHING ELSE: a write merges as a patch does, a read answers
// what stands now and nil for nothing, and none of it reaches the document — no entity, no
// commit.
func TestScratchHoldsWhatIsWrittenAndNothingElse(t *testing.T) {
	s := openStore(t)
	ref := RefAt("probe", "iods", "x", "out")
	if n, err := s.ScratchRead(ref); err != nil || n != nil {
		t.Fatalf("a read before any write = %v, %v; want nothing", n, err)
	}
	before := s.Head()
	if err := s.ScratchWrite(ref, Obj{"lastPass": "t1", "delivered": 3}.Payload()); err != nil {
		t.Fatal(err)
	}
	if err := s.ScratchWrite(ref, Obj{"failed": 1}.Payload()); err != nil {
		t.Fatal(err)
	}
	n, err := s.ScratchRead(ref)
	if err != nil {
		t.Fatal(err)
	}
	got := ToObj(n)
	if Str(n, "lastPass") != "t1" || got["delivered"] == nil || got["failed"] == nil {
		t.Errorf("after two writes scratch reads %v, want both merged", got)
	}
	if h := s.Head(); h != before {
		t.Errorf("scratch writes committed to the document: head %d → %d", before, h)
	}
	if _, ok, _ := s.Get(ref); ok {
		t.Error("a scratch write landed in the document")
	}
	if err := s.ScratchWrite(ref, nil); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ScratchRead(ref); err != nil || n != nil {
		t.Errorf("after a nil write scratch still reads %v, %v", n, err)
	}
}

// SCRATCH IS CHECKED AT ITS ADDRESS, as the document is: whoever may read a place may read its
// scratch and nobody else, whoever may write it may write its scratch, and the system is
// unchecked. The place here is a node the system creates holding out `read` to one person and
// `read-write` to another, and nothing to anybody else.
func TestScratchIsCheckedAtItsAddress(t *testing.T) {
	s := openStore(t)
	const reader, writer, nobody = "user:reader@example.com", "user:writer@example.com", "user:nobody@example.com"
	place := RefAt("probe", "iods", "y")
	if ok, _, err := As(s, SystemDefault).Write(Write{At: place.At(), Value: Obj{"name": "y"}.Payload(),
		Grants: []Grant{{Below: true, To: map[Level][]string{Read: {reader}, ReadWrite: {writer}}}}}); err != nil || !ok {
		t.Fatalf("creating the place with its grants: ok=%v err=%v", ok, err)
	}
	ref := RefAt("probe", "iods", "y", "out")
	face := func(who string) Scratch {
		t.Helper()
		sc, ok := ScratchOf(As(s, who))
		if !ok {
			t.Fatal("an attributed view has no scratch face")
		}
		return sc
	}
	if err := face(nobody).ScratchWrite(ref, Obj{"a": 1}.Payload()); err == nil {
		t.Error("somebody holding nothing at the place wrote its scratch")
	}
	if err := face(reader).ScratchWrite(ref, Obj{"a": 1}.Payload()); err == nil {
		t.Error("somebody holding read at the place wrote its scratch")
	}
	if err := face(writer).ScratchWrite(ref, Obj{"a": 1}.Payload()); err != nil {
		t.Fatalf("somebody holding read-write at the place could not write its scratch: %v", err)
	}
	if _, err := face(nobody).ScratchRead(ref); err == nil {
		t.Error("somebody holding nothing at the place read its scratch")
	}
	if n, err := face(reader).ScratchRead(ref); err != nil || n == nil {
		t.Errorf("somebody holding read at the place could not read its scratch: %v, %v", n, err)
	}
	if n, err := face(SystemDefault).ScratchRead(ref); err != nil || n == nil {
		t.Errorf("the system could not read scratch: %v, %v", n, err)
	}
}

// SCRATCH FOLLOWS THE STORE'S SCOPE: a scoped store's scratch stands beside baseline's and not
// on it, since scratch has no scopes of its own, and it goes when the scope does.
func TestScratchFollowsTheStoresScope(t *testing.T) {
	s := openStore(t)
	ref := RefAt("probe", "iods", "x", "out")
	if err := s.ScratchWrite(ref, Obj{"delivered": 1}.Payload()); err != nil {
		t.Fatal(err)
	}
	scoped := func() Scratch {
		t.Helper()
		sc, ok := ScratchOf(s.Scope("sandbox"))
		if !ok {
			t.Fatal("a scoped store has no scratch face")
		}
		return sc
	}
	if n, err := scoped().ScratchRead(ref); err != nil || n != nil {
		t.Fatalf("the scope reads baseline's scratch: %v, %v", n, err)
	}
	// The scope holds a record of its own, as a scope with a registration in it does; a scope
	// with nothing in the document is not one logd will delete.
	if ok, _, err := s.Scope("sandbox").Write(Write{At: RefAt("probe", "iods", "x").At(), Value: Obj{"name": "x"}.Payload()}); err != nil || !ok {
		t.Fatalf("writing the scope's record: ok=%v err=%v", ok, err)
	}
	if err := scoped().ScratchWrite(ref, Obj{"delivered": 7}.Payload()); err != nil {
		t.Fatal(err)
	}
	delivered := func(sc Scratch) string {
		t.Helper()
		n, err := sc.ScratchRead(ref)
		if err != nil {
			t.Fatal(err)
		}
		if n == nil {
			return ""
		}
		return fmt.Sprint(ToObj(n)["delivered"])
	}
	if got := delivered(s); got != "1" {
		t.Errorf("a scoped write reached baseline's scratch: delivered %q, want 1", got)
	}
	if got := delivered(scoped()); got != "7" {
		t.Errorf("the scope's scratch reads delivered %q, want 7", got)
	}
	if err := s.DeleteScope("sandbox"); err != nil {
		t.Fatal(err)
	}
	if got := delivered(scoped()); got != "" {
		t.Errorf("the scope's scratch outlived the scope: delivered %q", got)
	}
	if got := delivered(s); got != "1" {
		t.Errorf("deleting the scope took baseline's scratch: delivered %q, want 1", got)
	}
}
