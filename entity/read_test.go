package entity_test

import (
	"errors"
	"fmt"
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
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	alice, bob := entity.As(system, "user:alice@example.com"), entity.As(system, "user:bob@example.com")
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
	// The record for what alice created stands at the place she made, `demo`, since her write
	// brought that into being too.
	if _, ok, err := system.Get(entity.PermsRef(entity.RefAt("demo"))); err != nil || !ok {
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
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	entity.PolicyOf(system).SetRootChildGrants(nil)
	alice, bob, carol := entity.As(system, "user:alice@example.com"), entity.As(system, "user:bob@example.com"), entity.As(system, "user:carol@example.com")

	// THE PLACE FIRST, as the system, so the three of them write rows into one that is nobody's
	// rather than one the first writer made: a creation takes the place it brought into being
	// with it, and `demo.pr` standing already is what leaves each row its writer's alone.
	if _, _, err := system.Write(entity.Write{At: entity.RefAt("demo", "pr").At(), Value: entity.Obj{}.Payload()}); err != nil {
		t.Fatal(err)
	}
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
	seedRecord(t, system, shared, "user:carol@example.com", entity.Grant{
		At: shared.Segs(), To: map[entity.Level][]string{entity.Read: {"user:bob@example.com"}},
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
	commits, err := bob.Since(from, entity.PrefixPattern([]string{"demo", "pr"}, 3))
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
		if c.Changed(mine) {
			t.Fatalf("a replayed commit says alice's pr changed: %v", c.Changes)
		}
	}
	if len(replayed) != 2 || !replayed[theirs.String()] || !replayed[shared.String()] {
		t.Fatalf("bob's replay = %v, want %v", replayed, want)
	}
	// The live watch: an update to each, once the records stand. (A CREATION is visible for
	// the one commit before its owner record lands — the record follows the write — so a watch
	// open across a creation sees it once; see attributed.narrow.)
	watch, unsub := bob.Observe(entity.PrefixPattern([]string{"demo", "pr"}, 3))
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

// A RECORD BENEATH A ROW GOVERNS ITS NODE WHEREVER THE READ WAS ASKED: a node inside a readable
// payload that stands under a record the reader may not read is left out of it — by a Get of the
// row, a listing, a replay, a live watch and a snapshot alike — and its owner still sees it.
func TestARecordBeneathARowIsLeftOutEveryWay(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	scott, ana := entity.As(system, "user:walter@example.com"), entity.As(system, "user:ana@example.com")

	pr := entity.RefAt("demo", "pr", "1")
	secret := entity.RefAt("demo", "pr", "1", "secret")
	from := system.Head()
	if _, _, err := scott.Write(entity.Write{At: pr.At(), Value: entity.Obj{
		"open": "yes", "secret": entity.Obj{"x": 1},
	}.Payload()}); err != nil {
		t.Fatal(err)
	}
	// scott's, holding out nothing: an explicit empty list, since a record with no grants field
	// reads as holding out the root's default.
	seedRecordRaw(t, system, secret, entity.Obj{
		entity.OwnerField: "user:walter@example.com", entity.GrantsField: entity.GrantsPayload(secret.Segs(), nil),
	})

	holdsSecret := func(p entity.Payload) bool { return entity.Get(p, "secret") != nil }
	if _, _, err := ana.Get(secret); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("ana reads the secret at its own address: %v", err)
	}
	// Get of the row.
	if e, ok, err := ana.Get(pr); err != nil || !ok || holdsSecret(e.Payload) || entity.Get(e.Payload, "open") == nil {
		t.Fatalf("ana's Get of the row = %v ok=%v err=%v, want it without the secret", e.Payload, ok, err)
	}
	// A read of the row, of what holds it, and of the root.
	for _, at := range [][]string{pr.Segs(), {"demo", "pr"}, {"demo"}, nil} {
		node, ok, err := ana.ReadAt(at)
		if err != nil || !ok {
			t.Fatalf("ana's read at %v: ok=%v err=%v", at, ok, err)
		}
		row := node
		for _, seg := range pr.Segs()[len(at):] {
			row = entity.Get(row, seg)
		}
		if row == nil || entity.Get(row, "open") == nil || holdsSecret(row) {
			t.Fatalf("ana's read at %v = %v, want the row without the secret", at, row)
		}
	}
	// A listing, at the row's depth and from further up.
	for _, at := range [][]string{{"demo", "pr"}, {"demo"}} {
		for _, e := range ana.ListAt(at, len(pr.Segs()), nil) {
			if e.Ref == pr && holdsSecret(e.Payload) {
				t.Fatalf("ana's listing at %v carries the secret", at)
			}
		}
	}
	// A replay.
	commits, err := ana.Since(from, entity.PrefixPattern([]string{"demo", "pr"}, 3))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range commits {
		for _, ev := range c.Changes {
			if ev.Ref == pr && holdsSecret(ev.To) {
				t.Fatalf("ana's replay carries the secret: %v", ev.To)
			}
		}
	}
	// A live watch, over a change to the row once the record stands.
	watch, unsub := ana.Observe(entity.PrefixPattern([]string{"demo", "pr"}, 3))
	defer unsub()
	if _, _, err := scott.Write(entity.Write{At: pr.At("open"), Value: entity.Val("no")}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for seen := false; !seen; {
		select {
		case c := <-watch:
			for _, ev := range c.Changes {
				if ev.Ref != pr {
					continue
				}
				if holdsSecret(ev.To) {
					t.Fatalf("ana's watch carries the secret: %v", ev.To)
				}
				seen = true
			}
		case <-deadline:
			t.Fatal("ana's watch never delivered the change to the row")
		}
	}
	// A snapshot.
	snap, err := ana.AsOf(system.Head())
	if err != nil {
		t.Fatal(err)
	}
	if e, ok, err := snap.Get(pr); err != nil || !ok || holdsSecret(e.Payload) {
		t.Fatalf("ana's snapshot Get of the row = %v ok=%v err=%v", e.Payload, ok, err)
	}
	for _, e := range snap.ListAt([]string{"demo", "pr"}, len(pr.Segs()), nil) {
		if e.Ref == pr && holdsSecret(e.Payload) {
			t.Fatal("ana's snapshot listing carries the secret")
		}
	}
	if node, ok, err := snap.ReadAt([]string{"demo"}); err != nil || !ok || holdsSecret(entity.Get(entity.Get(node, "pr"), "1")) {
		t.Fatalf("ana's snapshot read of demo = %v ok=%v err=%v", node, ok, err)
	}
	// scott, who owns it, and the system see it.
	for name, s := range map[string]entity.Store{"scott": scott, "the system": system} {
		if e, ok, err := s.Get(pr); err != nil || !ok || !holdsSecret(e.Payload) {
			t.Fatalf("%s's Get of the row = %v ok=%v err=%v, want the secret", name, e.Payload, ok, err)
		}
	}
}

// A READ OF A PATH ANSWERS WHAT READS OF EACH NODE IN IT WOULD. Beneath a node the reader may
// not read, a node held out to them is still theirs, and comes back where it stands; what is
// neither theirs nor held out is not there, and neither is verse's own half, which nothing here
// holds out.
func TestAReadAnswersEachNodeAsItsOwnReadWould(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	entity.PolicyOf(system).SetRootChildGrants(nil)
	alice, bob := entity.As(system, "user:alice@example.com"), entity.As(system, "user:bob@example.com")

	// The place first, as the system, so each row beneath it is its own writer's (a creation
	// takes the place it made with it).
	if _, _, err := system.Write(entity.Write{At: entity.RefAt("demo", "pr").At(), Value: entity.Obj{}.Payload()}); err != nil {
		t.Fatal(err)
	}
	pr := entity.RefAt("demo", "pr", "1")
	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{
		"stage": "open", "shared": entity.Obj{"note": "hi"},
	}.Payload()}); err != nil {
		t.Fatal(err)
	}
	shared := entity.RefAt("demo", "pr", "1", "shared")
	seedRecord(t, system, shared, "user:alice@example.com", entity.Grant{
		At: shared.Segs(), To: map[entity.Level][]string{entity.Read: {"user:bob@example.com"}},
	})
	if _, _, err := bob.Write(entity.Write{At: entity.RefAt("demo", "pr", "2").At(), Value: entity.Obj{"stage": "draft"}.Payload()}); err != nil {
		t.Fatal(err)
	}

	for _, at := range [][]string{{"demo"}, nil} {
		node, ok, err := bob.ReadAt(at)
		if err != nil || !ok {
			t.Fatalf("bob's read at %v: ok=%v err=%v", at, ok, err)
		}
		if at == nil {
			if entity.Get(node, entity.VerseRoot) != nil {
				t.Fatalf("bob's read of the root holds verse's own half: %v", node)
			}
			node = entity.Get(node, "demo")
		}
		prs := entity.Get(node, "pr")
		one := entity.Get(prs, "1")
		if one == nil || entity.Get(one, "stage") != nil || entity.Str(entity.Get(one, "shared"), "note") != "hi" {
			t.Fatalf("bob's read at %v has pr 1 = %v, want only what is held out to him", at, one)
		}
		if entity.Str(entity.Get(prs, "2"), "stage") != "draft" {
			t.Fatalf("bob's read at %v lost his own pr: %v", at, prs)
		}
	}
	if node, _, err := alice.ReadAt([]string{"demo", "pr"}); err != nil || entity.Get(entity.Get(node, "1"), "stage") == nil || entity.Get(node, "2") != nil {
		t.Fatalf("alice's read of demo.pr = %v err=%v, want hers whole and not bob's", node, err)
	}
	if node, _, err := system.ReadAt(nil); err != nil || entity.Get(node, entity.VerseRoot) == nil {
		t.Fatalf("the system's read of the root = %v err=%v, want all of it", node, err)
	}
}

// ONE PLACE, HOWEVER IT IS SPELLED. A write at `demo.pr.1.stage` and a rule patching `stage` of
// `demo.pr.1` name one node, so they are checked at one node and recorded at one node — where
// the two spellings needed different levels, and a removal of one of them ran the eviction the
// other did not.
func TestAWriteIsCheckedAtThePathHoweverItIsSpelled(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	entity.PolicyOf(system).SetRootChildGrants(nil)
	alice, bob := entity.As(system, "user:alice@example.com"), entity.As(system, "user:bob@example.com")

	pr := entity.RefAt("demo", "pr", "1")
	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	seedRecord(t, system, pr, "user:alice@example.com", entity.Grant{
		At: pr.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {"user:bob@example.com"}},
	})
	deep := entity.RefAt("demo", "pr", "1", "stage")

	// Two spellings of the same write, admitted alike.
	for _, at := range []entity.Path{pr.At("stage"), deep.At()} {
		if ok, _, err := bob.Write(entity.Write{At: at, Value: entity.Val("reviewed")}); err != nil || !ok {
			t.Fatalf("bob's write at %s: ok=%v err=%v", at, ok, err)
		}
	}
	// And two spellings of the same removal, refused alike: removing a node is eviction, which
	// read-write is not.
	for _, at := range []entity.Path{pr.At("stage"), deep.At()} {
		if _, _, err := bob.Write(entity.Write{At: at}); !errors.Is(err, entity.ErrForbidden) {
			t.Fatalf("bob removed %s with read-write: %v", at, err)
		}
	}
	// Its owner takes it, at either spelling.
	if ok, _, err := alice.Write(entity.Write{At: deep.At()}); err != nil || !ok {
		t.Fatalf("alice removing what she owns: ok=%v err=%v", ok, err)
	}
	if node, _, err := system.ReadAt(pr.Segs()); err != nil || entity.Get(node, "stage") != nil {
		t.Fatalf("demo.pr.1 = %v (%v), want the stage gone", node, err)
	}
}

// What a node holds out is decided where its record is written, from the nearest default up
// the tree: a default of nothing makes what is created beneath its creator's alone, a default
// set on a place is inherited beneath it until another says otherwise, and a record written
// before defaults existed reads as holding out the root's.
func TestADefaultDecidesWhatACreationHoldsOut(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	alice, bob := entity.As(system, "user:alice@example.com"), entity.As(system, "user:bob@example.com")

	// alice takes her space, and says nothing created beneath it is held out.
	space := entity.RefAt("spaces", "alice")
	if err := entity.Claim(alice, space, "user:alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if o, ok := ownerOf(t, inner, space); !ok || o != "user:alice@example.com" {
		t.Fatalf("the claimed place is owned by %q (%v)", o, ok)
	}
	if err := entity.SetChildGrants(alice, space, entity.Levels{}); err != nil {
		t.Fatal(err)
	}
	// A place already somebody's is not bob's to claim.
	if err := entity.Claim(bob, space, "user:bob@example.com"); !errors.Is(err, entity.ErrForbidden) {
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
	// The default is copied onto the record that was written, which stands at the place the
	// creation made — `demo` — and covers what is beneath it.
	place := entity.RefAt("demo")
	rec, _, _ := inner.Get(entity.PermsRef(place))
	if gs, _ := entity.ParseGrants(entity.SegsOf(place), rec.Payload); len(gs) != 1 || gs[0].To[entity.Read][0] != entity.Everyone {
		t.Fatalf("the record does not carry the root's default as its grant: %v", rec.Payload)
	}

	// A default set beneath the root reaches a creation that gets a record of its own: bob,
	// holding out `owner` under his space, sees carol's creation there hold out nothing.
	bobs := entity.RefAt("spaces", "bob")
	if err := entity.Claim(bob, bobs, "user:bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetChildGrants(bob, bobs, entity.Levels{}); err != nil {
		t.Fatal(err)
	}
	if err := entity.AddGrant(bob, bobs, entity.Grant{At: bobs.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {"user:carol@example.com"}}}); err != nil {
		t.Fatal(err)
	}
	carol := entity.As(system, "user:carol@example.com")
	theirs := entity.RefAt("spaces", "bob", "carols")
	if _, _, err := carol.Write(entity.Write{At: theirs.At(), Value: entity.Obj{"n": 3}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, theirs); o != "user:carol@example.com" {
		t.Fatalf("carol's creation under bob's space is owned by %q", o)
	}
	if _, _, err := alice.Get(theirs); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("carol's creation under bob's default of nothing is readable by alice: %v", err)
	}
	// Removing the default makes bob's space inherit the root's again.
	if err := entity.SetChildGrants(bob, bobs, nil); err != nil {
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
	seedRecordRaw(t, system, old, entity.Obj{entity.OwnerField: "user:alice@example.com"})
	if _, ok, err := bob.Get(old); err != nil || !ok {
		t.Fatalf("a pre-default record is not read as the root's default: ok=%v err=%v", ok, err)
	}
}

// seedRecordRaw writes a perms record exactly as given, through the view so it learns of it.
func seedRecordRaw(t *testing.T, store entity.Store, ref entity.Ref, rec entity.Obj) {
	t.Helper()
	if _, _, err := store.Write(entity.Write{At: entity.PermsRef(ref).At(), Value: rec.Payload(), Principal: entity.SystemPerms}); err != nil {
		t.Fatal(err)
	}
}

// A whole delete takes the records beneath it with the subtree: a place deleted above a node
// that had a record of its own leaves no ghost owning an address nothing stands at, and the
// next creator there owns what they create.
func TestADeleteForgetsTheRecordsBeneathIt(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	alice, bob := entity.As(system, "user:alice@example.com"), entity.As(system, "user:bob@example.com")
	space := entity.RefAt("spaces", "alice")
	notes := entity.RefAt("spaces", "alice", "notes")
	if err := entity.Claim(alice, space, "user:alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.Write(entity.Write{At: notes.At(), Value: entity.Obj{"n": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.AddGrant(alice, notes, entity.Grant{At: notes.Segs(), To: map[entity.Level][]string{entity.Read: {"user:bob@example.com"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownerOf(t, inner, notes); !ok {
		t.Fatal("the grant gave notes no record of its own")
	}
	// alice deletes her place whole.
	if _, _, err := alice.Write(entity.Write{At: space.At()}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []entity.Ref{space, notes} {
		if o, ok := ownerOf(t, inner, ref); ok {
			t.Fatalf("the record of %s outlived the delete, owned by %q", ref, o)
		}
	}
	// bob takes the address: the place he re-creates is his, and alice's grant to bob is not
	// what admits him. The record stands where his creation began, which is the space itself.
	if _, _, err := bob.Write(entity.Write{At: notes.At(), Value: entity.Obj{"n": 2}.Payload()}); err != nil {
		t.Fatalf("bob cannot create where alice's place was: %v", err)
	}
	if o, _ := ownerOf(t, inner, space); o != "user:bob@example.com" {
		t.Fatalf("re-created by bob, owner of %s = %q", space, o)
	}
}

// A group admits its members: a principal, a kind, everyone, another group, a binding. A group
// of groups is walked exactly — five deep admits the leaf — and a cycle admits nobody because
// the walk ends.
func TestAGroupAdmitsItsMembers(t *testing.T) {
	inner := entitytest.OpenStore(t)
	system := entity.As(inner, entity.SystemDefault)
	entity.PolicyOf(system).SetRootChildGrants(nil)
	alice := entity.As(system, "user:alice@example.com")
	pr := entity.RefAt("demo", "pr", "1")
	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	group := func(name string, members ...string) {
		ms := make([]any, 0, len(members))
		for _, m := range members {
			ms = append(ms, m)
		}
		if _, _, err := system.Write(entity.Write{At: entity.GroupRef(name).At(), Value: entity.Obj{entity.MembersField: ms}.Payload()}); err != nil {
			t.Fatal(err)
		}
	}
	group("payments", "user:oscar@example.com", "group:security", "group:chain-1")
	group("security", "user:carol@example.com", "runner:*")
	group("loop-a", "group:loop-b", "user:erin@example.com")
	group("loop-b", "group:loop-a")
	for i := 1; i < 5; i++ {
		group(fmt.Sprintf("chain-%d", i), fmt.Sprintf("group:chain-%d", i+1))
	}
	group("chain-5", "user:frank@example.com")
	if err := entity.AddGrant(alice, pr, entity.Grant{At: pr.Segs(), To: map[entity.Level][]string{entity.Read: {"group:loop-a", "group:payments"}}}); err != nil {
		t.Fatal(err)
	}
	for who, want := range map[string]bool{
		"user:oscar@example.com": true,  // a member
		"user:carol@example.com":   true,  // a member of a member group
		"runner:bot":               true,  // of a kind a member group names
		"user:erin@example.com":    true,  // a member of a group in a cycle
		"user:frank@example.com":   true,  // five groups down
		"user:bob@example.com":     false, // nobody's member, walked through the cycle to the end
		"":                         false, // the unattributed request is not a member of anything
	} {
		if got, _ := entity.Holds(system, who, entity.Read, pr); got != want {
			t.Errorf("%q reads alice's pr = %v, want %v", who, got, want)
		}
	}
	// Membership moves: taking davido out takes his read.
	group("payments", "group:security")
	if got, _ := entity.Holds(system, "user:oscar@example.com", entity.Read, pr); got {
		t.Fatal("davido reads after being taken out of the group")
	}
}
