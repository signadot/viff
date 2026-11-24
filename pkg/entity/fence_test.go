package entity_test

import (
	"errors"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// TAKING A NODE TAKES EVERYTHING UNDER IT, since ownership descends — so a grant that reaches an
// untaken node would hand whoever it names every place already standing beneath it. That is what
// the fence holds back: ownership concluded from a grant does not apply at a node somebody
// else's place stands beneath. Ownership from a RECORD is untouched; an owner keeps what they
// hold, whatever is beneath it.

// alicesPlace is an open viff where alice has taken github.signadot.viff and written in it,
// which is the shape a source's install leaves.
func alicesPlace(t *testing.T) (entity.Store, entity.Ref, entity.Ref) {
	t.Helper()
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	place := entity.RefAt("github", "signadot", "viff")
	pr := entity.RefAt("github", "signadot", "viff", "pr", "1")
	if err := entity.Claim(store, place, "user:alice@example.com"); err != nil {
		t.Fatalf("alice takes her place: %v", err)
	}
	if _, _, err := entity.As(store, "user:alice@example.com").Write(entity.Write{
		At: pr.At(), Value: entity.Obj{"title": "x"}.Payload(),
		Principal: "user:alice@example.com",
	}); err != nil {
		t.Fatalf("alice writes in it: %v", err)
	}
	return store, place, pr
}

// The first squat: `viff perms github`, above the place alice took. The open viff holds
// `owner` out there, so admitting it without asking what stands beneath would make mallory a
// co-owner of everything alice has written.
func TestAPlaceAboveSomebodyElsesCannotBeTaken(t *testing.T) {
	store, place, pr := alicesPlace(t)
	mallory := "user:mallory@example.com"
	err := entity.Claim(store, entity.RefAt("github"), mallory)
	if !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("mallory took github above %s: %v", place, err)
	}
	if ok, _ := entity.MayCreate(store, mallory, pr); ok {
		t.Errorf("mallory owns alice's %s", pr)
	}
	if _, _, err := entity.As(store, mallory).Write(entity.Write{
		At: pr.At(), Value: entity.Obj{"title": "mallory"}.Payload(), Principal: mallory,
	}); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("mallory wrote alice's %s: %v", pr, err)
	}
}

// The second squat, and the reason the fence is in the check rather than in `Claim`: an ordinary
// write at the node ABOVE somebody's place was admitted by the same grant, and the owner record
// it stamped was the co-ownership by another name.
func TestAWriteAboveSomebodyElsesPlaceIsRefused(t *testing.T) {
	store, _, pr := alicesPlace(t)
	mallory := "user:mallory@example.com"
	above := entity.RefAt("github", "signadot")
	if _, _, err := entity.As(store, mallory).Write(entity.Write{
		At: above.At(), Value: entity.Obj{"note": "hi"}.Payload(), Principal: mallory,
	}); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("mallory wrote at %s: %v", above, err)
	}
	if _, ok := ownerOf(t, store, above); ok {
		t.Errorf("a record was stamped at %s", above)
	}
	if ok, _ := entity.MayCreate(store, mallory, pr); ok {
		t.Errorf("mallory owns alice's %s", pr)
	}
}

// The fence is about SOMEBODY ELSE's place, and it is not what stops the owner of everything
// beneath from taking the node above: that the node STANDS is. `github` came into being with
// what alice wrote beneath it, a node that stands has an owner — the nearest record above it,
// here the root's — and a `child-owner` line is the right to create one, not to take one from
// whoever it is already. Taken AHEAD of what goes in it, the same place is hers.
func TestANodeThatStandsIsNotTakenByTheOwnerOfWhatIsBeneath(t *testing.T) {
	store, _, _ := alicesPlace(t)
	alice := "user:alice@example.com"
	err := entity.Claim(store, entity.RefAt("github"), alice)
	if !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("alice took github, which stands and is the root owner's: %v", err)
	}
	if owner, ok := ownerOf(t, store, entity.RefAt("github")); ok {
		t.Errorf("a record was written at github, for %q", owner)
	}
	// What decides it is that the node stands, and the refusal says whose it is.
	if !strings.Contains(err.Error(), "nearest owner") {
		t.Errorf("the refusal does not say whose github is: %v", err)
	}
	if err := entity.Claim(store, entity.RefAt("gitlab"), alice); err != nil {
		t.Fatalf("alice takes a place nothing stands at: %v", err)
	}
	if owner, _ := ownerOf(t, store, entity.RefAt("gitlab")); owner != alice {
		t.Errorf("gitlab is %q's, want alice's", owner)
	}
}

// And a place BESIDE one somebody holds is nobody's business but the taker's: the fence asks
// about what stands beneath the node being taken, not about what stands near it.
func TestAPlaceBesideSomebodyElsesIsStillFree(t *testing.T) {
	store, _, _ := alicesPlace(t)
	bob := "user:bob@example.com"
	if err := entity.Claim(store, entity.RefAt("github", "signadot", "other"), bob); err != nil {
		t.Fatalf("bob takes a place beside alice's: %v", err)
	}
	// …and the one nobody has touched at all, which is the open viff working as it did.
	if _, _, err := entity.As(store, bob).Write(entity.Write{
		At:    entity.RefAt("spaces", "bob", "notes").At(),
		Value: entity.Obj{"todo": "milk"}.Payload(), Principal: bob,
	}); err != nil {
		t.Fatalf("bob writes where nobody stands: %v", err)
	}
}

// An owner keeps what they hold: the fence is on ownership CONCLUDED FROM A GRANT, and a record
// naming you is not a grant. Walter owning `spaces` still owns ana's place inside it, which is
// the parent-owns-the-subtree half of the model and the half a fence must not eat.
func TestAnOwnerAboveKeepsWhatIsBeneath(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	walter, ana := "user:walter@example.com", "user:ana@example.com"
	spaces := entity.RefAt("spaces")
	if err := entity.Claim(store, spaces, walter); err != nil {
		t.Fatal(err)
	}
	// Ana's place inside walter's, hers because he held `owner` out beneath it.
	if err := entity.AddGrant(store, spaces, entity.Grant{
		At: spaces.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {ana}},
	}); err != nil {
		t.Fatal(err)
	}
	anas := entity.RefAt("spaces", "ana")
	if err := entity.Claim(store, anas, ana); err != nil {
		t.Fatalf("ana takes her space: %v", err)
	}
	if ok, err := entity.Holds(store, walter, entity.OwnerLevel, anas); err != nil || !ok {
		t.Errorf("walter lost ana's space beneath his own: %v %v", ok, err)
	}
	if ok, err := entity.Holds(store, ana, entity.OwnerLevel, anas); err != nil || !ok {
		t.Errorf("ana does not own her own space: %v %v", ok, err)
	}
}

// A GRANTS EDIT REPLACES THE LIST. An array write merges by position and truncates, so setting
// line 0 to a line of another shape kept the fields of the one that stood there: a `--grants`
// that dropped an `owner` line left the owner held out, on a record that said it was taken back.
func TestSettingGrantsDropsWhatItLeavesOut(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	at := entity.RefAt("spaces", "walter")
	if err := entity.Claim(store, at, "user:walter@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, at, []entity.Grant{
		{At: at.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {"user:bob@example.com"}}},
	}); err != nil {
		t.Fatal(err)
	}
	beneath := entity.RefAt("spaces", "walter", "x")
	if ok, _ := entity.MayCreate(store, "user:bob@example.com", beneath); !ok {
		t.Fatal("bob was not given the place to begin with")
	}
	// One line of another shape, which is the `--grants` a person types to take that back.
	if err := entity.SetGrants(store, at, []entity.Grant{
		{At: at.Segs(), To: map[entity.Level][]string{entity.Read: {"user:ana@example.com"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.MayCreate(store, "user:bob@example.com", beneath); ok {
		t.Error("bob's owner grant survived the edit that dropped it")
	}
	if ok, err := entity.Holds(store, "user:ana@example.com", entity.Read, at); err != nil || !ok {
		t.Errorf("ana's read did not land: %v %v", ok, err)
	}
}

// NOBODY IS NOT THE SOLE OWNER OF SEVERAL PEOPLE'S PLACES. A node with more than one owner
// beneath it has no sole owner, and a request that names no principal has no name — two empty
// strings, which compared equal, so the one caller with no identity at all was the one the fence
// let through: a DELETE of `gh` with no `-as` took alice's place and bob's with it.
func TestTheFenceHoldsBackARequestThatNamesNobody(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	gh := entity.RefAt("gh")
	for who, at := range map[string]entity.Ref{
		"user:alice@example.com": entity.RefAt("gh", "alice"),
		"user:bob@example.com":   entity.RefAt("gh", "bob"),
	} {
		if err := entity.Claim(store, at, who); err != nil {
			t.Fatal(err)
		}
		if _, _, err := entity.As(store, who).Write(entity.Write{At: at.At("n"), Value: entity.Obj{"v": 1}.Payload()}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := entity.MayCreate(store, "", gh); ok {
		t.Error("a request naming nobody holds owner above two people's places")
	}
	if _, _, err := entity.As(store, "").Write(entity.Write{At: gh.At()}); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("a delete naming nobody took %s: %v", gh, err)
	}
	if _, ok, _ := store.Get(entity.RefAt("gh", "alice", "n")); !ok {
		t.Error("alice's place is gone")
	}
}

// deafMirror is a store whose perms mirror does not answer, as one under load does not.
type deafMirror struct {
	entity.Store
	deaf bool
}

func (d *deafMirror) Get(ref entity.Ref) (entity.Entity, bool, error) {
	if d.deaf && ref.Under(entity.ViffRoot, entity.PermsSystem) {
		return entity.Entity{}, false, errors.New("timeout: the store did not answer")
	}
	return d.Store.Get(ref)
}

// A RECORD THAT COULD NOT BE READ IS NOT A RECORD THAT IS NOT THERE. Where no record stands the
// owner above is the system, so an error read as an absence made a place acting through its
// record — a rule, `via:` its address — act as the system for that one write: the principal
// nothing checks, in somebody else's place, because the store was slow.
func TestAMirrorThatDoesNotAnswerAdmitsNobody(t *testing.T) {
	inner := &deafMirror{Store: entitytest.OpenStore(t)}
	setup := entity.As(inner, entity.SystemDefault)
	trent, rule, vault := "user:trent@example.com", entity.RefAt("joes", "rule"), entity.RefAt("vault")
	if err := entity.Claim(setup, rule, trent); err != nil {
		t.Fatal(err)
	}
	if err := entity.Claim(setup, vault, "user:admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(setup, vault, nil); err != nil {
		t.Fatal(err)
	}
	// A fresh view, which has read nothing yet, over a mirror that has stopped answering.
	inner.deaf = true
	view := entity.As(inner, entity.SystemDefault)
	ok, err := entity.Holds(view, entity.Via(rule), entity.ReadWrite, entity.RefAt("vault", "secret"))
	if ok {
		t.Error("trent's rule writes admin's vault while the mirror does not answer")
	}
	if err == nil {
		t.Error("a mirror that did not answer was read as a mirror with nothing in it")
	}
	if _, _, err := entity.As(view, entity.Via(rule)).Write(entity.Write{
		At: entity.RefAt("vault", "secret").At(), Value: entity.Obj{"k": "v"}.Payload(),
	}); err == nil {
		t.Error("the write was admitted")
	}
}
