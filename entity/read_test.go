package entity_test

import (
	"errors"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

func refs(ents []entity.Entity) map[string]bool {
	out := map[string]bool{}
	for _, e := range ents {
		out[e.Ref.String()] = true
	}
	return out
}

// Under the open verse's reading rule everyone reads everything but the perms mirror, whatever
// the records say; the mirror is the system's alone.
func TestTheOpenVerseReadsEverythingButTheMirror(t *testing.T) {
	inner := entitytest.Store(t)
	system := entity.As(inner, entity.SystemPrincipal)
	alice, bob := entity.As(system, "alice@example.com"), entity.As(system, "bob@example.com")
	pr := entity.RefAt("demo", "pr", "1")
	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := bob.Get(pr); err != nil || !ok {
		t.Fatalf("bob reads alice's pr in the open verse: ok=%v err=%v", ok, err)
	}
	if _, _, err := bob.Get(entity.PermsRef(pr)); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("bob reads the mirror: %v", err)
	}
	if _, ok, err := system.Get(entity.PermsRef(pr)); err != nil || !ok {
		t.Fatalf("the system reads the mirror: ok=%v err=%v", ok, err)
	}
	// And a listing across verse's half leaves the mirror out for bob.
	all := refs(bob.ListAt([]string{entity.VerseRoot}, entity.Depth+1, nil))
	if all[entity.PermsRef(pr).String()] {
		t.Fatal("bob's listing of verse's half holds a perms record")
	}
}

// With the open rule off a read is admitted by co-ownership or a `read` grant and by nothing
// else — and the same principal sees the same set whichever way they ask: a Get, a listing, a
// replay, a live watch and a snapshot.
func TestAReaderSeesTheSameSetEveryWay(t *testing.T) {
	inner := entitytest.Store(t)
	system := entity.As(inner, entity.SystemPrincipal)
	entity.PolicyOf(system).OpenReads(false)
	alice, bob, carol := entity.As(system, "alice@example.com"), entity.As(system, "bob@example.com"), entity.As(system, "carol@example.com")

	mine := entity.RefAt("demo", "pr", "1")
	from := system.Head()
	if _, _, err := alice.Write(entity.Write{At: mine.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	theirs := entity.RefAt("demo", "pr", "2")
	if _, _, err := bob.Write(entity.Write{At: theirs.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	shared := entity.RefAt("demo", "pr", "3")
	if _, _, err := carol.Write(entity.Write{At: shared.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	seedRecord(t, system, shared, "carol@example.com", entity.Grant{
		At: shared.Segs(), To: map[entity.Level][]string{entity.Read: {"bob@example.com"}},
	})

	// Get: refused for alice's, answered for bob's own and for the one held out to him.
	if _, _, err := bob.Get(mine); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("bob reads alice's pr: %v", err)
	}
	for _, ref := range []entity.Ref{theirs, shared} {
		if _, ok, err := bob.Get(ref); err != nil || !ok {
			t.Fatalf("bob cannot read %s: ok=%v err=%v", ref, ok, err)
		}
	}
	want := map[string]bool{theirs.String(): true, shared.String(): true}

	// A listing.
	if got := refs(bob.ListAt([]string{"demo", "pr"}, entity.Depth, nil)); len(got) != 2 || !got[theirs.String()] || !got[shared.String()] {
		t.Fatalf("bob's listing = %v, want %v", got, want)
	}
	// A replay.
	commits, err := bob.Since(from)
	if err != nil {
		t.Fatal(err)
	}
	replayed := map[string]bool{}
	for _, c := range commits {
		for _, ev := range c.Changes {
			if ev.Ref.Reserved() {
				continue
			}
			replayed[ev.Ref.String()] = true
		}
		for _, r := range c.ChangedAt(entity.Depth) {
			if r == mine {
				t.Fatalf("a replayed commit's ChangedAt names alice's pr: %v", c.Changes)
			}
		}
	}
	if len(replayed) != 2 || !replayed[theirs.String()] || !replayed[shared.String()] {
		t.Fatalf("bob's replay = %v, want %v", replayed, want)
	}
	// The live watch: an update to each, once the records stand. (A CREATION is visible for
	// the one commit before its owner record lands — the record follows the write — so a watch
	// open across a creation sees it once; see attributed.narrow.)
	watch, unsub := bob.Observe()
	defer unsub()
	for _, w := range []struct {
		as  entity.Store
		ref entity.Ref
	}{{alice, mine}, {bob, theirs}, {carol, shared}} {
		if _, _, err := w.as.Write(entity.Write{At: w.ref.At("stage"), Value: entity.Val("reviewed")}); err != nil {
			t.Fatal(err)
		}
	}
	live := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(live) < 2 {
		select {
		case c := <-watch:
			for _, ev := range c.Changes {
				if !ev.Ref.Reserved() {
					live[ev.Ref.String()] = true
				}
			}
		case <-deadline:
			t.Fatalf("bob's watch delivered %v, want %v", live, want)
		}
	}
	if live[mine.String()] {
		t.Fatal("bob's watch delivered alice's pr")
	}
	// A snapshot.
	snap, err := bob.AsOf(system.Head())
	if err != nil {
		t.Fatal(err)
	}
	if got := refs(snap.ListAt([]string{"demo", "pr"}, entity.Depth, nil)); len(got) != 2 || got[mine.String()] {
		t.Fatalf("bob's snapshot = %v, want %v", got, want)
	}
	if _, _, err := snap.Get(mine); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("bob's snapshot answers alice's pr: %v", err)
	}
	// alice and the system see everything of theirs.
	if got := refs(alice.ListAt([]string{"demo", "pr"}, entity.Depth, nil)); len(got) != 1 || !got[mine.String()] {
		t.Fatalf("alice's listing = %v", got)
	}
	if got := refs(system.ListAt([]string{"demo", "pr"}, entity.Depth, nil)); len(got) != 3 {
		t.Fatalf("the system's listing = %v", got)
	}
}
