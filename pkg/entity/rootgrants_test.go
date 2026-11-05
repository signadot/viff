package entity_test

import (
	"errors"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// WHO COMES TO OWN A FRESH TOP-LEVEL NAME IS THE ROOT'S TO SAY. An owner record is written only
// for a writer who holds `owner` where they wrote, and at a name nothing stands under the only
// thing that can hold that out is the root's record — so "the first writer owns the name" is
// what ONE line of that record says, and the other lines say other things. These are the rows
// of the table in docs/perms.md.
func TestTheRootsGrantDecidesWhoseAFreshNameIs(t *testing.T) {
	alice, bob, carol := "user:alice@example.com", "user:bob@example.com", "user:carol@example.com"
	put := func(store entity.Store, who string, segs ...string) error {
		_, _, err := entity.As(store, who).Write(entity.Write{
			At: entity.RefAt(segs...).At(), Value: entity.Obj{"n": 1}.Payload(), Principal: who,
		})
		return err
	}
	rooted := func(t *testing.T, grants ...entity.Grant) entity.Store {
		t.Helper()
		store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
		if _, _, err := store.Write(entity.Write{At: entity.GroupRef("makers").At(),
			Value: entity.Obj{entity.MembersField: []any{alice}}.Payload()}); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, entity.Grant{To: map[entity.Level][]string{entity.Read: {entity.Everyone}}})
		if err := entity.SetGrants(store, entity.RefAt(), grants); err != nil {
			t.Fatal(err)
		}
		return store
	}
	below := func(level entity.Level, to ...string) entity.Grant {
		return entity.Grant{Below: true, To: map[entity.Level][]string{level: to}}
	}
	name := entity.RefAt("fresh")

	t.Run("owner to everyone: the first writer's, and the second is refused inside it", func(t *testing.T) {
		store := rooted(t, below(entity.OwnerLevel, entity.Everyone))
		if err := put(store, bob, "fresh", "pr", "1"); err != nil {
			t.Fatal(err)
		}
		if o, _ := ownerOf(t, store, name); o != bob {
			t.Errorf("`fresh` is %q's, want the first writer's", o)
		}
		if err := put(store, carol, "fresh", "pr", "2"); !errors.Is(err, entity.ErrForbidden) {
			t.Errorf("a second writer was admitted inside the first's place: %v", err)
		}
	})

	t.Run("owner to a group: the member's who wrote, never the group's", func(t *testing.T) {
		store := rooted(t, below(entity.OwnerLevel, "group:makers"))
		if err := put(store, bob, "fresh", "pr", "1"); !errors.Is(err, entity.ErrForbidden) {
			t.Errorf("somebody outside the group made a top-level place: %v", err)
		}
		if err := put(store, alice, "fresh", "pr", "1"); err != nil {
			t.Fatal(err)
		}
		if o, _ := ownerOf(t, store, name); o != alice {
			t.Errorf("`fresh` is %q's, want the member's who wrote", o)
		}
	})

	t.Run("read-write: the root's owner's, no record, and nobody takes it by arriving first", func(t *testing.T) {
		store := rooted(t, below(entity.ReadWrite, entity.Everyone))
		if err := put(store, bob, "fresh", "pr", "1"); err != nil {
			t.Fatal(err)
		}
		for _, at := range []entity.Ref{name, entity.RefAt("fresh", "pr", "1")} {
			if o, ok := ownerOf(t, store, at); ok {
				t.Errorf("a read-write creation recorded %q as the owner of %s", o, at)
			}
		}
		if err := put(store, carol, "fresh", "pr", "2"); err != nil {
			t.Errorf("the place did not stay everybody's the root admits: %v", err)
		}
	})

	t.Run("owner to makers and read-write to everyone compose", func(t *testing.T) {
		store := rooted(t, below(entity.OwnerLevel, "group:makers"), below(entity.ReadWrite, entity.Everyone))
		if err := put(store, alice, "made", "pr", "1"); err != nil {
			t.Fatal(err)
		}
		if err := put(store, bob, "made", "pr", "2"); !errors.Is(err, entity.ErrForbidden) {
			t.Errorf("bob wrote inside the place a maker made: %v", err)
		}
		if err := put(store, bob, "commons", "pr", "1"); err != nil {
			t.Errorf("bob could not write where nothing is taken: %v", err)
		}
	})

	t.Run("nothing: nobody but the root's owner", func(t *testing.T) {
		store := rooted(t)
		if err := put(store, bob, "fresh", "pr", "1"); !errors.Is(err, entity.ErrForbidden) {
			t.Errorf("a top-level place was made with nothing held out: %v", err)
		}
	})
}

// A CREATION IS RECORDED ONLY AT NODES ITS WRITER HOLDS `owner` AT. Holding it at the row says
// nothing about the node above: a grant of what is BENEATH `sandbox` is not a grant of `sandbox`,
// and recording the first writer beneath it as its owner gave them a space they were only
// invited into — and shut everybody else out, since a grant does not reach through a record.
func TestACreationIsNotRecordedAboveWhereItsWriterHoldsOwner(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	bob, carol := "user:bob@example.com", "user:carol@example.com"
	sandbox := entity.RefAt("sandbox")
	if err := entity.SetGrants(store, entity.RefAt(), []entity.Grant{
		{At: sandbox.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {entity.Everyone}}},
		{To: map[entity.Level][]string{entity.Read: {entity.Everyone}}},
	}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.MayCreate(store, bob, sandbox); ok {
		t.Fatal("the fixture is wrong: bob holds owner at sandbox itself")
	}
	put := func(who string, segs ...string) error {
		_, _, err := entity.As(store, who).Write(entity.Write{
			At: entity.RefAt(segs...).At(), Value: entity.Obj{"n": 1}.Payload(), Principal: who,
		})
		return err
	}
	if err := put(bob, "sandbox", "x", "1"); err != nil {
		t.Fatal(err)
	}
	if o, ok := ownerOf(t, store, sandbox); ok {
		t.Errorf("bob's write recorded %q as the owner of sandbox, which he was never held out", o)
	}
	// His own begins where his authority does: `sandbox.x`, which his write made.
	if o, _ := ownerOf(t, store, entity.RefAt("sandbox", "x")); o != bob {
		t.Errorf("sandbox.x is %q's, want bob's", o)
	}
	if err := put(carol, "sandbox", "y", "1"); err != nil {
		t.Errorf("carol is shut out of a space held out to everyone: %v", err)
	}
}

// TWO PEOPLE MAKING THE SAME PLACE AT ONCE both mean to record at it, and one wins. The other's
// record goes at the next node down that they made — never nowhere, which left them holding
// `read` on a row they had just created, inside somebody else's place.
func TestTheLoserOfARaceForAPlaceStillOwnsWhatTheyMade(t *testing.T) {
	raw := entitytest.OpenStore(t)
	store := entity.As(raw, entity.SystemDefault)
	bob, carol := "user:bob@example.com", "user:carol@example.com"
	// Carol's record lands at `probe` between bob's decision and bob's record: the race, made
	// to happen from inside bob's write. It is the record her own write of `probe.b.1` leaves
	// when both decided on `probe` while nothing stood there — written here as that write's
	// record is, since by the time this runs `probe` stands and is nobody's to take.
	racing := &raceAt{Store: raw, on: entity.RefAt("probe", "a", "1"), then: func() {
		if err := entity.Establish(entity.As(raw, entity.SystemDefault), entity.RefAt("probe"), carol, entity.Ref{}, nil); err != nil {
			t.Errorf("carol's record did not land at probe: %v", err)
		}
	}}
	view := entity.As(entity.As(racing, entity.SystemDefault), bob)
	row := entity.RefAt("probe", "a", "1")
	if _, _, err := view.Write(entity.Write{At: row.At(), Value: entity.Obj{"n": 1}.Payload(), Principal: bob}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, store, entity.RefAt("probe")); o != carol {
		t.Fatalf("the fixture is wrong: probe is %q's, want carol's", o)
	}
	if o, _ := ownerOf(t, store, entity.RefAt("probe", "a")); o != bob {
		t.Errorf("probe.a is %q's, want bob's: he made it", o)
	}
	fresh := entity.As(raw, entity.SystemDefault)
	if ok, err := entity.Holds(fresh, bob, entity.OwnerLevel, row); err != nil || !ok {
		t.Errorf("bob does not own the row he created: %v %v", ok, err)
	}
}

// raceAt runs `then` once, after the write at `on` has landed and before its writer records it.
type raceAt struct {
	entity.Store
	on   entity.Ref
	then func()
	done bool
}

func (r *raceAt) Write(w entity.Write) (bool, int64, error) {
	ok, rev, err := r.Store.Write(w)
	if !r.done && w.At.Ref == r.on {
		r.done = true
		r.then()
	}
	return ok, rev, err
}

// SOMEBODY TAKEN OUT OF A GROUP IS OUT OF IT IN EVERY SCOPE. Each scope has its own attribution
// and memoises the members it read through its overlay; the write dropped them only where it
// went, so with the root held out to a group, a removed admin kept an admin's authority in every
// scope already open until the process restarted.
func TestLeavingAGroupIsSeenInEveryScope(t *testing.T) {
	bob := "user:bob@example.com"
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	members := func(ms ...any) {
		t.Helper()
		if _, _, err := store.Write(entity.Write{At: entity.GroupRef("makers").At("members"), Value: entity.Get(entity.Obj{"m": ms}.Payload(), "m")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Write(entity.Write{At: entity.GroupRef("makers").At(),
		Value: entity.Obj{entity.MembersField: []any{bob}}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, entity.RefAt(), []entity.Grant{
		{Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {"group:makers"}}},
		{To: map[entity.Level][]string{entity.Read: {entity.Everyone}}},
	}); err != nil {
		t.Fatal(err)
	}
	dev, fresh := store.Scope("dev"), entity.RefAt("fresh")
	for name, view := range map[string]entity.Store{"baseline": store, "scope dev": dev} {
		if ok, err := entity.MayCreate(view, bob, fresh); err != nil || !ok {
			t.Fatalf("%s: bob, a maker, takes no place: %v %v", name, ok, err)
		}
	}
	// Written beneath the group rather than at it, which names the group no less.
	members("user:alice@example.com")
	for name, view := range map[string]entity.Store{"baseline": store, "scope dev": dev} {
		if ok, _ := entity.MayCreate(view, bob, fresh); ok {
			t.Errorf("%s: bob still takes a place after leaving the group", name)
		}
	}
}

// A READ WITHDRAWN IN BASELINE IS WITHDRAWN UNDER EVERY SCOPE NAME. A scope reads baseline's
// records through its overlay and memoises them; only a scope somebody opened an engine for is
// watched, so any other name — `?scope=zz`, which nobody has to have made — is a view that reads
// once, and would go on admitting what baseline has since refused. And the other way: a place
// taken after the scope first looked would be, in that scope, still nobody's.
func TestARecordThatMovesInBaselineMovesInEveryScope(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	alice, bob := "user:alice@example.com", "user:bob@example.com"
	team, late := entity.RefAt("team"), entity.RefAt("late")
	if err := entity.Claim(store, team, alice); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, team, []entity.Grant{{At: team.Segs(), To: map[entity.Level][]string{entity.Read: {bob}}}}); err != nil {
		t.Fatal(err)
	}
	zz := store.Scope("zz")
	if ok, err := entity.Holds(zz, bob, entity.Read, team); err != nil || !ok {
		t.Fatalf("the fixture is wrong: bob does not read team in the scope: %v %v", ok, err)
	}
	if ok, _ := entity.MayCreate(zz, bob, late); !ok {
		t.Fatal("the fixture is wrong: `late` is not bob's to take in the scope")
	}
	if err := entity.SetGrants(store, team, nil); err != nil {
		t.Fatal(err)
	}
	if err := entity.Claim(store, late, alice); err != nil {
		t.Fatal(err)
	}
	for name, view := range map[string]entity.Store{"baseline": store, "scope zz": zz} {
		if ok, _ := entity.Holds(view, bob, entity.Read, team); ok {
			t.Errorf("%s: bob still reads team after alice withdrew it", name)
		}
		if ok, _ := entity.MayCreate(view, bob, late); ok {
			t.Errorf("%s: bob may still take `late`, which is alice's now", name)
		}
	}
}

// deafScopes is a store whose scopes' watches open and never deliver: the time between a write
// to baseline's mirror and a scope's own watch reporting it, held open for as long as a test
// looks at it.
type deafScopes struct{ entity.Store }

func (d deafScopes) Scope(name string) entity.Store { return deaf{d.Store.Scope(name)} }

type deaf struct{ entity.Store }

func (d deaf) WatchSubtree(depth int, at ...string) (<-chan entity.Commit, func(), error) {
	return make(chan entity.Commit), func() {}, nil
}

func (d deaf) Scope(name string) entity.Store { return deaf{d.Store.Scope(name)} }

// AND IN A SCOPE THAT HOLDS THE WHOLE MIRROR, which is every scope with an engine: it cannot
// forget a key, since one missing from a loaded mirror reads as absent, and its own watch
// reports baseline's write when it reports it. Until then the scope checked against the record
// as it was — a read alice had withdrawn still honoured there, a place she had just taken still
// anybody's. The scope hears of it when baseline's record moves, and not when its watch says so.
func TestARecordThatMovesInBaselineMovesAtOnceInALoadedScope(t *testing.T) {
	store := entity.As(deafScopes{entitytest.OpenStore(t)}, entity.SystemDefault)
	alice, bob := "user:alice@example.com", "user:bob@example.com"
	team, late := entity.RefAt("team"), entity.RefAt("late")
	if err := entity.Claim(store, team, alice); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, team, []entity.Grant{{At: team.Segs(), To: map[entity.Level][]string{entity.Read: {bob}}}}); err != nil {
		t.Fatal(err)
	}
	zz := store.Scope("zz")
	if err := entity.Track(t.Context(), zz); err != nil {
		t.Fatal(err)
	}
	if !entity.PolicyOf(zz).Tracked() {
		t.Fatal("the fixture is wrong: the scope does not hold the mirror")
	}
	if ok, err := entity.Holds(zz, bob, entity.Read, team); err != nil || !ok {
		t.Fatalf("the fixture is wrong: bob does not read team in the scope: %v %v", ok, err)
	}
	if ok, _ := entity.MayCreate(zz, bob, late); !ok {
		t.Fatal("the fixture is wrong: `late` is not bob's to take in the scope")
	}
	if err := entity.SetGrants(store, team, nil); err != nil {
		t.Fatal(err)
	}
	if err := entity.Claim(store, late, alice); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(zz, bob, entity.Read, team); ok {
		t.Error("bob still reads team in the loaded scope after alice withdrew it")
	}
	if ok, _ := entity.MayCreate(zz, bob, late); ok {
		t.Error("bob may still take `late` in the loaded scope, which is alice's now")
	}
	// And the scope's own record still wins there: what it says over baseline's is what is read.
	if err := entity.SetGrants(zz, team, []entity.Grant{{At: team.Segs(), To: map[entity.Level][]string{entity.Read: {bob}}}}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(zz, bob, entity.Read, team); !ok {
		t.Error("the scope's own grant is not read in the scope")
	}
	if ok, _ := entity.Holds(store, bob, entity.Read, team); ok {
		t.Error("the scope's grant reached baseline")
	}
}
