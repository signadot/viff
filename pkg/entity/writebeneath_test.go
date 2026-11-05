package entity_test

import (
	"errors"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// A WRITE IS CHECKED AT EVERY NODE ITS VALUE REACHES, not only the one it names. A value is a
// tree, so `{c: {secret: x}}` written AT `team` lands in `team.c` — which bob was refused when he
// addressed it, and admitted when it arrived through its parent. A record governs its node
// whichever ancestor the write was asked at; a read already promised that.

// theTeam is x's place, with `read-write` held out to bob on it and beneath, and carol's own
// place inside it by record.
func theTeam(t *testing.T) (store entity.Store, team, carols entity.Ref) {
	t.Helper()
	store = entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	team, carols = entity.RefAt("team"), entity.RefAt("team", "c")
	if err := entity.Claim(store, team, "user:x@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, team, []entity.Grant{
		{At: team.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {"user:bob@example.com"}}},
		{At: team.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {"user:carol@example.com"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := entity.As(store, "user:carol@example.com").Write(entity.Write{
		At: carols.At(), Value: entity.Obj{"secret": "carols"}.Payload(), Principal: "user:carol@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, store, carols); o != "user:carol@example.com" {
		t.Fatalf("the fixture is wrong: team.c is %q's", o)
	}
	return store, team, carols
}

func secret(t *testing.T, store entity.Store, at entity.Ref) string {
	t.Helper()
	e, _, err := store.Get(at)
	if err != nil {
		t.Fatal(err)
	}
	return entity.Str(e.Payload, "secret")
}

func TestAWriteDoesNotReachThroughItsValueIntoSomebodyElsesPlace(t *testing.T) {
	store, team, carols := theTeam(t)
	bob := entity.As(store, "user:bob@example.com")
	// Addressed directly, he is refused — which was always so.
	if _, _, err := bob.Write(entity.Write{At: carols.At(), Value: entity.Obj{"secret": "pwned"}.Payload()}); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("bob wrote carol's place, addressed: %v", err)
	}
	// Through the parent he holds `read-write` at, with a value that carries her place in it.
	_, _, err := bob.Write(entity.Write{At: team.At(), Value: entity.Obj{"c": entity.Obj{"secret": "pwned"}}.Payload()})
	if !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("bob wrote carol's place through its parent: %v", err)
	}
	if got := secret(t, store, carols); got != "carols" {
		t.Errorf("team.c.secret = %q, want it left as carol wrote it", got)
	}
}

// What he may write, he still writes: a value that touches only what is his to touch is admitted
// as it was, beside a place that is not.
func TestAWriteBesideSomebodyElsesPlaceIsStillAdmitted(t *testing.T) {
	store, team, carols := theTeam(t)
	bob := entity.As(store, "user:bob@example.com")
	if _, _, err := bob.Write(entity.Write{At: team.At(), Value: entity.Obj{"note": "hi", "b": entity.Obj{"n": 1}}.Payload()}); err != nil {
		t.Fatalf("bob could not write beside carol's place: %v", err)
	}
	if got := secret(t, store, carols); got != "carols" {
		t.Errorf("team.c.secret = %q after a write beside it", got)
	}
}

// Removing her place through the parent is the same act as removing it addressed, and is the
// owner's: `{c: !delete}` written at `team` is a delete of `team.c`.
func TestADeleteInsideAValueIsADeleteOfThatNode(t *testing.T) {
	store, team, carols := theTeam(t)
	bob := entity.As(store, "user:bob@example.com")
	if _, _, err := bob.Write(entity.Write{At: team.At(), Value: entity.Obj{"c": entity.Removed()}.Payload()}); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("bob deleted carol's place through its parent: %v", err)
	}
	if got := secret(t, store, carols); got != "carols" {
		t.Errorf("team.c.secret = %q, want carol's place still standing", got)
	}
}

// A value that is not an object replaces the node whole, and what stands beneath goes with it:
// a scalar over `team` takes carol's place as surely as a delete does, so it is the owner's act.
// The owner's own — by record, which descends — is admitted.
func TestReplacingANodeWholeIsItsOwnersWhereSomethingStandsBeneath(t *testing.T) {
	store, team, carols := theTeam(t)
	bob := entity.As(store, "user:bob@example.com")
	if _, _, err := bob.Write(entity.Write{At: team.At(), Value: entity.Val("flattened")}); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("bob replaced team whole, carol's place with it: %v", err)
	}
	if got := secret(t, store, carols); got != "carols" {
		t.Errorf("team.c.secret = %q, want carol's place still standing", got)
	}
	// x owns `team` by record, and so co-owns what is beneath it.
	x := entity.As(store, "user:x@example.com")
	if _, _, err := x.Write(entity.Write{At: team.At(), Value: entity.Obj{"c": entity.Obj{"secret": "x was here"}}.Payload()}); err != nil {
		t.Errorf("the owner of team could not write into what is beneath it: %v", err)
	}
	if got := secret(t, store, carols); got != "x was here" {
		t.Errorf("team.c.secret = %q after its co-owner wrote it", got)
	}
}

// And the squat's last door: in an open verse everybody holds `read` at `github` and nothing
// more, since the fence holds ownership back above alice's place. A value written at the top
// that carried her place in it was the same reach, from further up.
func TestAValueWrittenFromTheTopDoesNotReachAPlaceBeneath(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	place := entity.RefAt("github", "signadot", "verse")
	if err := entity.Claim(store, place, "user:alice@example.com"); err != nil {
		t.Fatal(err)
	}
	mallory := entity.As(store, "user:mallory@example.com")
	reach := entity.Obj{"signadot": entity.Obj{"verse": entity.Obj{"pr": entity.Obj{"1": entity.Obj{"title": "pwned"}}}}}
	if _, _, err := mallory.Write(entity.Write{At: entity.RefAt("github").At(), Value: reach.Payload()}); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("mallory wrote alice's place from `github`: %v", err)
	}
}

// A NAME IN THE ACCESS IS A NAME IN THE ADDRESS. The api takes a write as an address and a `sub`
// beneath it, and a `sub` that spells names — `c.secret` — names the node `team.c.secret` in two
// pieces. Checked at the piece in the ref, it is a write into somebody else's place by whoever
// writes the place above it, and a delete of one without being its owner, which leaves the owner
// record standing over nothing.
func TestAWriteSpelledThroughItsAccessIsCheckedWhereItLands(t *testing.T) {
	system := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	bob, carol, alice := "user:bob@example.com", "user:carol@example.com", "user:alice@example.com"
	team, place := entity.RefAt("team"), entity.RefAt("team", "c")
	if err := entity.Claim(system, team, bob); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(system, team, []entity.Grant{
		{At: team.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {carol}}},
		{At: team.Segs(), Below: true, To: map[entity.Level][]string{entity.ReadWrite: {carol}, entity.OwnerLevel: {alice}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := entity.As(system, alice).Write(entity.Write{At: place.At(), Value: entity.Obj{"secret": "hers"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(system, place, nil); err != nil {
		t.Fatal(err)
	}
	asCarol := entity.As(system, carol)
	if _, _, err := asCarol.Write(entity.Write{At: place.At("secret"), Value: entity.Val("x")}); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("the fixture is wrong: carol writes alice's place when she addresses it: %v", err)
	}
	for name, w := range map[string]entity.Write{
		"a write":  {At: entity.Path{Ref: team, Access: "c.secret"}, Value: entity.Val("x")},
		"a delete": {At: entity.Path{Ref: team, Access: "c"}},
	} {
		if _, _, err := asCarol.Write(w); !errors.Is(err, entity.ErrForbidden) {
			t.Errorf("%s spelled through the access was admitted: %v", name, err)
		}
	}
	e, ok, _ := system.Get(place)
	if !ok || entity.Str(e.Payload, "secret") != "hers" {
		t.Errorf("alice's place reads %v (stands: %v)", e.Payload, ok)
	}
	// An element is still an access, and still written by whoever may write the node.
	if _, _, err := entity.As(system, bob).Write(entity.Write{At: team.At("votes"), Value: entity.Get(entity.Obj{"v": []any{"a", "b"}}.Payload(), "v")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := asCarol.Write(entity.Write{At: entity.Path{Ref: team, Access: "votes[0]"}, Value: entity.Val("z")}); err != nil {
		t.Errorf("carol, who writes team, may not write an element of it: %v", err)
	}
}

// A PRECONDITION IS A READ. The store answers one with committed-or-not, so placed on a node its
// writer may not read, it reads that node a guess at a time, and says whether a withheld node —
// or its perms record — stands at all.
func TestAPreconditionAsksOnlyAboutWhatItsWriterMayRead(t *testing.T) {
	system := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	alice, mallory := "user:alice@example.com", "user:mallory@example.com"
	vault := entity.RefAt("vault")
	if _, _, err := entity.As(system, alice).Write(entity.Write{At: vault.At("k"), Value: entity.Obj{"secret": "hunter2"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(system, vault, nil); err != nil {
		t.Fatal(err)
	}
	// …and a place she may read, with one inside it that she may not.
	pub := entity.RefAt("pub")
	if _, _, err := entity.As(system, alice).Write(entity.Write{At: pub.At(), Value: entity.Obj{"open": 1, "inner": entity.Obj{"secret": "hunter2"}}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.Claim(system, entity.RefAt("pub", "inner"), alice); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(system, entity.RefAt("pub", "inner"), nil); err != nil {
		t.Fatal(err)
	}
	asMallory, probe := entity.As(system, mallory), entity.RefAt("mallory", "probe")
	guess := func(when entity.Require) error {
		_, _, err := asMallory.Write(entity.Write{At: probe.At(), Value: entity.Obj{"n": 1}.Payload(), When: []entity.Require{when}})
		return err
	}
	for name, when := range map[string]entity.Require{
		"the right guess":      {At: vault.At("k"), Pattern: entity.Obj{"secret": "hunter2"}.Payload()},
		"a wrong guess":        {At: vault.At("k"), Pattern: entity.Obj{"secret": "wrong"}.Payload()},
		"that it stands":       {At: vault.At("k"), Pattern: entity.Present()},
		"that nothing stands":  {At: vault.At("nothing"), Absent: true},
		"its perms record":     {At: entity.PermsRef(vault).At(), Pattern: entity.Present()},
		"through the readable": {At: pub.At(), Pattern: entity.Obj{"inner": entity.Obj{"secret": "hunter2"}}.Payload()},
	} {
		if err := guess(when); !errors.Is(err, entity.ErrForbidden) {
			t.Errorf("%s: %v, want it refused as a read of what mallory may not read", name, err)
		}
	}
	if _, ok, _ := system.Get(probe); ok {
		t.Error("a refused write landed")
	}
	// What she may read she may ask about, her own place included.
	if err := guess(entity.Require{At: pub.At("open"), Pattern: entity.Present()}); err != nil {
		t.Errorf("a precondition on a field she reads: %v", err)
	}
	if err := guess(entity.Require{At: probe.At(), Absent: true}); err != nil {
		t.Errorf("a precondition on her own place: %v", err)
	}
}

// …AND ONLY ABOUT WHAT THE PATTERN NAMES. A closed place inside a node does not stop whoever
// writes the node from guarding their write: a compare-and-swap's pattern is what its writer
// read, which is the node without the place, and refusing it for what it does not mention would
// leave bob unable to Modify a node he holds read-write on because carol keeps something in it.
func TestAPreconditionThatDoesNotReachAClosedPlaceIsAdmitted(t *testing.T) {
	system := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	alice, bob, carol := "user:alice@example.com", "user:bob@example.com", "user:carol@example.com"
	team, closed := entity.RefAt("team"), entity.RefAt("team", "c")
	if _, _, err := entity.As(system, alice).Write(entity.Write{At: team.At(), Value: entity.Obj{"note": "x"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(system, team, []entity.Grant{
		{At: team.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {bob}}},
		{At: team.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {carol}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := entity.As(system, carol).Write(entity.Write{At: closed.At(), Value: entity.Obj{"secret": "hers"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(system, closed, nil); err != nil {
		t.Fatal(err)
	}
	asBob := entity.As(system, bob)
	if err := entity.Modify(t.Context(), asBob, team, func(cur entity.Payload) entity.Payload {
		o := entity.ToObj(cur) // nil the first time: Modify asks what a create would write
		if o == nil {
			o = entity.Obj{}
		}
		if _, leaked := o["c"]; leaked {
			t.Error("bob's read of team carries carol's place")
		}
		o["note"] = "y"
		return o.Payload()
	}); err != nil {
		t.Fatalf("bob's compare-and-swap on a node he writes: %v", err)
	}
	if e, _, _ := system.Get(closed); entity.Str(e.Payload, "secret") != "hers" {
		t.Errorf("carol's place did not survive bob's write: %v", e.Payload)
	}
	// What reaches it is still refused: by name, by a literal, and by negation.
	for name, pat := range map[string]entity.Payload{
		"naming it":  entity.Obj{"c": entity.Obj{"secret": "hers"}}.Payload(),
		"a literal":  entity.Exactly(entity.Obj{"note": "y"}.Payload()),
		"a negation": entity.Obj{"c": entity.Obj{"secret": "hers"}}.Payload().WithTag("!not"),
	} {
		_, _, err := asBob.Write(entity.Write{At: team.At("note"), Value: entity.Val("z"), When: []entity.Require{{At: team.At(), Pattern: pat}}})
		if !errors.Is(err, entity.ErrForbidden) {
			t.Errorf("a precondition reaching carol's place by %s: %v", name, err)
		}
	}
}
