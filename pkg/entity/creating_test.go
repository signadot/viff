package entity_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// A `child-owner` line is the right to CREATE, and these are about the other half of that: a
// node that stands has an owner already — the nearest record above it — and the line does not
// make it anybody else's, however wide it was written.

const (
	mallory = "user:mallory@example.com"
	carol   = "user:carol@example.com"
)

// standing is an open verse with one node in it that the system made, which leaves no record:
// it is the root owner's.
func standing(t *testing.T, at entity.Ref, v entity.Obj) entity.Store {
	t.Helper()
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	entitytest.Set(t, store, at, v.Payload())
	if _, recorded := ownerOf(t, store, at); recorded {
		t.Fatalf("the fixture is wrong: %s carries a record", at)
	}
	return store
}

func writeAs(store entity.Store, who string, at entity.Path, v entity.Payload, when ...entity.Require) (bool, error) {
	ok, _, err := entity.As(store, who).Write(entity.Write{At: at, Value: v, When: when, Principal: who})
	return ok, err
}

// The write, the delete, the record: none of them is a stranger's at a node that stands, and
// the refusal says whose the node is. Beside it the same stranger creates, and owns what they
// made — the line still means what it says.
func TestANodeThatStandsIsNotAnybodysWhoMayCreateThere(t *testing.T) {
	doc := entity.RefAt("shared", "doc")
	store := standing(t, doc, entity.Obj{"title": "the system's"})

	// The line holds `owner` out at this very address, which is why the check alone cannot
	// decide it and the write carries the condition.
	if ok, _ := entity.MayCreate(store, mallory, doc); !ok {
		t.Fatal("the fixture is wrong: the open verse does not hold shared.doc out to be created")
	}
	_, err := writeAs(store, mallory, doc.At(), entity.Obj{"title": "mallory's"}.Payload())
	if !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("mallory wrote into a node that stands: %v", err)
	}
	if !strings.Contains(err.Error(), "nearest owner") {
		t.Errorf("the refusal does not say whose it is: %v", err)
	}
	if _, err := writeAs(store, mallory, doc.At(), nil); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("mallory deleted a node that stands: %v", err)
	}
	if _, err := writeAs(store, mallory, doc.At("title"), entity.Obj{"t": "x"}.Payload()); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("mallory wrote a field of a node that stands: %v", err)
	}
	if err := entity.Claim(store, doc, mallory); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("mallory took a node that stands: %v", err)
	}
	if err := entity.CheckTake(store, mallory, doc, "installing over it"); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("mallory may install over a node that stands: %v", err)
	}
	if ok, _ := entity.Holds(store, mallory, entity.Control, doc); ok {
		t.Error("mallory holds control over a node that stands")
	}
	if who, recorded := ownerOf(t, store, doc); recorded {
		t.Errorf("a record was written at %s, for %q", doc, who)
	}
	e, _, _ := store.Get(doc)
	if entity.Str(e.Payload, "title") != "the system's" {
		t.Errorf("the node was written: %v", entity.ToObj(e.Payload))
	}

	beside := entity.RefAt("shared", "mallorys")
	if ok, err := writeAs(store, mallory, beside.At(), entity.Obj{"title": "new"}.Payload()); err != nil || !ok {
		t.Fatalf("mallory could not create beside it: %v %v", ok, err)
	}
	if who, _ := ownerOf(t, store, beside); who != mallory {
		t.Errorf("%s is %q's, want mallory's: she made it", beside, who)
	}
}

// A VALUE REACHES NODES TOO. Written at the parent, a value that lands in a node standing
// beneath it is a write into that node, asked there as one addressed to it would be: what
// admits it is what she holds besides the right to create — and having written into it does
// not make it hers.
func TestAValueReachingANodeThatStandsDoesNotTakeIt(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	team := entity.RefAt("team")
	if err := entity.Claim(store, team, carol); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, team, []entity.Grant{
		{At: team.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {mallory}}},
		{At: team.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {mallory}}},
	}); err != nil {
		t.Fatal(err)
	}
	// Carol's own node, standing beneath, with no record of its own: it is hers by `team`'s.
	notes := entity.RefAt("team", "notes")
	if _, err := writeAs(store, carol, notes.At(), entity.Obj{"body": "carol's"}.Payload()); err != nil {
		t.Fatal(err)
	}
	// Mallory holds read-write AT team and may create beneath it. Through the parent, her value
	// lands in `team.notes`, where read-write reaches: admitted, and it is not hers afterwards.
	if ok, err := writeAs(store, mallory, team.At(), entity.Obj{"notes": entity.Obj{"body": "edited"}}.Payload()); err != nil || !ok {
		t.Fatalf("mallory, who holds read-write at team, could not write through it: %v %v", ok, err)
	}
	if who, recorded := ownerOf(t, store, notes); recorded {
		t.Errorf("writing through the parent recorded %s as %q's", notes, who)
	}
	if ok, _ := entity.Holds(store, mallory, entity.OwnerLevel, notes); ok {
		t.Error("mallory owns carol's node for having written into it")
	}
	// And a node she makes through the parent is made: the line still means what it says.
	if ok, err := writeAs(store, mallory, team.At(), entity.Obj{"fresh": entity.Obj{"body": "hers"}}.Payload()); err != nil || !ok {
		t.Fatalf("mallory could not create a node beneath team through it: %v %v", ok, err)
	}
}

// before runs `then` once, as the write at `on` reaches the store and before it lands: the
// moment between a writer's check and their commit.
type before struct {
	entity.Store
	on   entity.Ref
	then func()
	done bool
}

func (b *before) Write(w entity.Write) (bool, int64, error) {
	if !b.done && w.At.Ref == b.on {
		b.done = true
		b.then()
	}
	return b.Store.Write(w)
}

// TWO PEOPLE MAKING ONE NODE AT ONCE: one of them made it and owns it, and the other wrote into
// a node that stands — refused, and never a second owner. The check admitted both, since nothing
// stood when it was asked; the store decides, at the commit.
func TestOfTwoCreatorsOfOneNodeOneOwnsItAndTheOtherIsRefused(t *testing.T) {
	raw := entitytest.OpenStore(t)
	store := entity.As(raw, entity.SystemDefault)
	node := entity.RefAt("probe", "thing")
	racing := &before{Store: raw, on: node, then: func() {
		if ok, err := writeAs(store, carol, node.At(), entity.Obj{"by": "carol"}.Payload()); err != nil || !ok {
			t.Errorf("carol, who got there first, could not create it: %v %v", ok, err)
		}
	}}
	view := entity.As(racing, entity.SystemDefault)
	_, err := writeAs(view, mallory, node.At(), entity.Obj{"by": "mallory"}.Payload())
	if !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("mallory's write, which lost the race, was not refused: %v", err)
	}
	e, _, _ := store.Get(node)
	if entity.Str(e.Payload, "by") != "carol" {
		t.Errorf("the node is %v, want carol's write alone", entity.ToObj(e.Payload))
	}
	if ok, _ := entity.Holds(store, mallory, entity.ReadWrite, node); ok {
		t.Error("mallory holds read-write on the node carol made")
	}
	if ok, _ := entity.Holds(store, carol, entity.OwnerLevel, node); !ok {
		t.Error("carol does not own the node she made")
	}
}

// THE WRITER'S OWN CONDITION IS STILL THEIRS. A creation whose own precondition does not hold
// is an uncommitted write, as it always was, and not a refusal of the writer: nothing stands at
// the node, so nobody's node was in the way.
func TestACreationWhoseOwnConditionFailsIsUncommittedAndNotRefused(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	node := entity.RefAt("probe", "thing")
	ok, err := writeAs(store, mallory, node.At(), entity.Obj{"n": 1}.Payload(), entity.Updated())
	if err != nil || ok {
		t.Fatalf("an update of nothing = committed %v, err %v; want uncommitted and no error", ok, err)
	}
	if _, stands, _ := store.Get(node); stands {
		t.Fatal("the write landed")
	}
	// A delete is never a creation, so it is not asked twice, and its refusal is a refusal.
	if _, err := writeAs(store, mallory, node.At(), nil); !errors.Is(err, entity.ErrForbidden) {
		t.Errorf("a delete by somebody who owns nothing there = %v, want refused", err)
	}
	// And the one a creation usually carries holds, beside the one the check added.
	if ok, err := writeAs(store, mallory, node.At(), entity.Obj{"n": 1}.Payload(), entity.Created()); err != nil || !ok {
		t.Fatalf("a --create of a fresh node = committed %v, err %v", ok, err)
	}
	if ok, err := writeAs(store, mallory, node.At(), entity.Obj{"n": 2}.Payload(), entity.Created()); err != nil || ok {
		t.Fatalf("a --create over her own node = committed %v, err %v; want uncommitted", ok, err)
	}
}

// AND WHAT CAME TO STAND ABOVE IT IS NOT HERS EITHER. What a write makes is decided by asking
// what stands, before the write, and its record goes at the topmost node it made. An ancestor
// the system brings into being in between leaves no record, and with the write conditional on
// its own node alone that ancestor was recorded as the writer's, though it stood by the time
// she wrote. The condition sits at the topmost node; refused, she is asked again as a creation
// and owns what she did make, beneath it.
func TestAnAncestorThatCameToStandIsNotRecordedAsTheWriters(t *testing.T) {
	raw := entitytest.OpenStore(t)
	store := entity.As(raw, entity.SystemDefault)
	top, mid, leaf := entity.RefAt("probe"), entity.RefAt("probe", "a"), entity.RefAt("probe", "a", "1")
	racing := &before{Store: raw, on: leaf, then: func() {
		entitytest.Set(t, store, top, entity.Obj{"about": "the system's"}.Payload())
	}}
	view := entity.As(racing, entity.SystemDefault)
	if ok, err := writeAs(view, mallory, leaf.At(), entity.Obj{"n": 1}.Payload()); err != nil || !ok {
		t.Fatalf("mallory's write beneath a place that came to stand: committed %v, err %v", ok, err)
	}
	if who, recorded := ownerOf(t, store, top); recorded {
		t.Errorf("%s, which the system made, is recorded as %q's", top, who)
	}
	if who, _ := ownerOf(t, store, mid); who != mallory {
		t.Errorf("%s is %q's, want mallory's: it is the topmost node she made", mid, who)
	}
	if ok, _ := entity.Holds(store, mallory, entity.ReadWrite, top); ok {
		t.Error("mallory writes the system's node above what she made")
	}
}
