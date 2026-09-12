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

// Under the open verse's default what anybody creates is readable by everyone — the root's
// default, `read: ["*"]`, is copied onto the record at creation — and the mirror is the
// system's alone.
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

// With nothing held out by default a read is admitted by co-ownership or a `read` grant and by
// nothing else — and the same principal sees the same set whichever way they ask: a Get, a
// listing, a replay, a live watch and a snapshot.
func TestAReaderSeesTheSameSetEveryWay(t *testing.T) {
	inner := entitytest.Store(t)
	system := entity.As(inner, entity.SystemPrincipal)
	entity.PolicyOf(system).SetRootDefault(nil)
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

// What a node holds out is decided where its record is written, from the nearest default up
// the tree: a default of nothing makes what is created beneath its creator's alone, a default
// set on a place is inherited beneath it until another says otherwise, and a record written
// before defaults existed reads as holding out the root's.
func TestADefaultDecidesWhatACreationHoldsOut(t *testing.T) {
	inner := entitytest.Store(t)
	system := entity.As(inner, entity.SystemPrincipal)
	alice, bob := entity.As(system, "alice@example.com"), entity.As(system, "bob@example.com")

	// alice takes her space, and says nothing created beneath it is held out.
	space := entity.RefAt("spaces", "alice")
	if err := entity.Claim(alice, space, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if o, ok := ownerOf(t, inner, space); !ok || o != "alice@example.com" {
		t.Fatalf("the claimed place is owned by %q (%v)", o, ok)
	}
	if err := entity.SetDefault(alice, space, entity.Levels{}); err != nil {
		t.Fatal(err)
	}
	// A place already somebody's is not bob's to claim.
	if err := entity.Claim(bob, space, "bob@example.com"); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("bob claimed alice's place: %v", err)
	}

	// The space's own record was written under the root's default, so what stood there is
	// still readable; what alice creates beneath it now is not — and being alice's already, it
	// carries no record of its own, so the space's grants are what decide, and they hold out
	// what the root said when the space was claimed.
	private := entity.RefAt("spaces", "alice", "private")
	if _, _, err := alice.Write(entity.Write{At: private.At(), Value: entity.Obj{"n": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownerOf(t, inner, private); ok {
		t.Fatal("alice's node under alice's space got a record of its own")
	}
	if _, ok, err := bob.Get(private); err != nil || !ok {
		t.Fatalf("the space still holds out the root's read: ok=%v err=%v", ok, err)
	}
	// So a private space is a default of nothing AND its own grants of nothing.
	if err := entity.SetGrants(alice, space, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob.Get(private); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("bob reads alice's private node: %v", err)
	}
	// What bob creates beneath alice's space, were he granted it, would be alice's and carry
	// no record; what carol creates elsewhere carries the root's default.
	pub := entity.RefAt("demo", "pr", "1")
	if _, _, err := alice.Write(entity.Write{At: pub.At(), Value: entity.Obj{"n": 2}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := bob.Get(pub); err != nil || !ok {
		t.Fatalf("a creation under the root's default is not readable: ok=%v err=%v", ok, err)
	}
	rec, _, _ := inner.Get(entity.PermsRef(pub))
	if gs, _ := entity.ParseGrants(rec.Payload); len(gs) != 1 || gs[0].To[entity.Read][0] != entity.Everyone {
		t.Fatalf("the record does not carry the root's default as its grant: %v", rec.Payload)
	}

	// A default set beneath the root reaches a creation that gets a record of its own: bob,
	// holding out `owner` under his space, sees carol's creation there hold out nothing.
	bobs := entity.RefAt("spaces", "bob")
	if err := entity.Claim(bob, bobs, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetDefault(bob, bobs, entity.Levels{}); err != nil {
		t.Fatal(err)
	}
	if err := entity.AddGrant(bob, bobs, entity.Grant{At: bobs.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {"carol@example.com"}}}); err != nil {
		t.Fatal(err)
	}
	carol := entity.As(system, "carol@example.com")
	theirs := entity.RefAt("spaces", "bob", "carols")
	if _, _, err := carol.Write(entity.Write{At: theirs.At(), Value: entity.Obj{"n": 3}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, theirs); o != "carol@example.com" {
		t.Fatalf("carol's creation under bob's space is owned by %q", o)
	}
	if _, _, err := alice.Get(theirs); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("carol's creation under bob's default of nothing is readable by alice: %v", err)
	}
	// Removing the default makes bob's space inherit the root's again.
	if err := entity.SetDefault(bob, bobs, nil); err != nil {
		t.Fatal(err)
	}
	later := entity.RefAt("spaces", "bob", "later")
	if _, _, err := carol.Write(entity.Write{At: later.At(), Value: entity.Obj{"n": 4}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := alice.Get(later); err != nil || !ok {
		t.Fatalf("after the default is removed, a creation does not hold out the root's: ok=%v err=%v", ok, err)
	}

	// A record from before defaults existed — an owner and no grants field — reads as the
	// root's default.
	old := entity.RefAt("demo", "pr", "old")
	if _, _, err := system.Write(entity.Write{At: old.At(), Value: entity.Obj{"n": 5}.Payload()}); err != nil {
		t.Fatal(err)
	}
	seedRecordRaw(t, system, old, entity.Obj{entity.OwnerField: "alice@example.com"})
	if _, ok, err := bob.Get(old); err != nil || !ok {
		t.Fatalf("a pre-default record is not read as the root's default: ok=%v err=%v", ok, err)
	}
}

// seedRecordRaw writes a perms record exactly as given, through the view so it learns of it.
func seedRecordRaw(t *testing.T, store entity.Store, ref entity.Ref, rec entity.Obj) {
	t.Helper()
	if _, _, err := store.Write(entity.Write{At: entity.PermsRef(ref).At(), Value: rec.Payload(), Principal: entity.SystemPrincipal}); err != nil {
		t.Fatal(err)
	}
}
