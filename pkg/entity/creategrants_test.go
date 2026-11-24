package entity_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// A node created with grants (Write.Grants) holds out what its creator said from its first
// commit: the node and its record land together, and the check has the record before the
// commit does.

const victor = "user:victor@example.com"

// createWith is a create at ref carrying grants, as who.
func createWith(store entity.Store, who string, ref entity.Ref, v entity.Obj, grants ...entity.Grant) (bool, error) {
	if grants == nil {
		grants = []entity.Grant{}
	}
	ok, _, err := entity.As(store, who).Write(entity.Write{At: ref.At(), Value: v.Payload(), Principal: who, Grants: grants})
	return ok, err
}

// landed runs then after each write anchored at the root has committed, and before its writer
// goes on: the moment between the commit and whatever the writer does next.
type landed struct {
	entity.Store
	then func(rev int64)
}

func (l *landed) Write(w entity.Write) (bool, int64, error) {
	ok, rev, err := l.Store.Write(w)
	if ok && err == nil && w.At.Ref == entity.RefAt() {
		l.then(rev)
	}
	return ok, rev, err
}

// A STRANGER NEVER SEES IT. In an open viff, with the mirror loaded and watched, a node made
// with `grants: []` is refused to a stranger from the moment it commits — before the watch
// that keeps the check's view has delivered its record, and before its writer has done anything
// after the commit — and a live watch as the stranger never carries it, while the node made
// after it with the default does. So in a scope opened through the view.
func TestANodeCreatedWithGrantsIsHiddenFromItsFirstCommit(t *testing.T) {
	raw := entitytest.OpenStore(t)
	window := &landed{Store: raw}
	system := entity.As(window, entity.SystemDefault)
	entitytest.Set(t, system, entity.RefAt("ask"), entity.Obj{}.Payload())
	if err := entity.Track(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	// And a scope opened through it, whose view of the mirror is its own and kept the same way.
	side := system.Scope("side")
	if err := entity.Track(t.Context(), side); err != nil {
		t.Fatal(err)
	}
	bob := entity.As(system, "user:bob@example.com")
	bobSide := entity.As(side, "user:bob@example.com")
	ch, unsub, err := bob.WatchSubtree(2, "ask")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	private := map[entity.Ref]bool{}
	var node entity.Ref
	seen := map[entity.Ref]bool{}
	watched := func(until int64) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case c := <-ch:
				for _, ev := range c.Changes {
					if private[ev.Ref] {
						t.Fatalf("bob's watch carried %s, created holding out nothing", ev.Ref)
					}
					seen[ev.Ref] = true
				}
				if c.Rev >= until {
					return
				}
			case <-deadline:
				t.Fatalf("bob's watch never reached rev %d", until)
			}
		}
	}
	window.then = func(rev int64) {
		if _, found, err := bob.Get(node); found || err == nil {
			t.Errorf("bob read %s at its first commit, created holding out nothing: found=%v err=%v", node, found, err)
		}
		if _, found, err := bobSide.Get(node); found || err == nil {
			t.Errorf("bob read %s in a scope at its first commit: found=%v err=%v", node, found, err)
		}
		watched(rev)
	}
	for i := range 5 {
		node = entity.RefAt("ask", fmt.Sprintf("q%d", i))
		private[node] = true
		if ok, err := createWith(system, victor, node, entity.Obj{"question": "private?"}); err != nil || !ok {
			t.Fatalf("creating %s with grants: %v %v", node, ok, err)
		}
		if _, found, err := bob.Get(node); found || err == nil {
			t.Fatalf("bob read %s after it was created holding out nothing: found=%v err=%v", node, found, err)
		}
		if _, found, err := bobSide.Get(node); found || err == nil {
			t.Fatalf("bob read %s in a scope after it was created: found=%v err=%v", node, found, err)
		}
		if _, found, err := entity.As(system, victor).Get(node); err != nil || !found {
			t.Fatalf("victor cannot read the node he created: %v %v", found, err)
		}
	}
	open := entity.RefAt("ask", "open")
	_, rev, err := entity.As(system, victor).Write(entity.Write{At: open.At(), Value: entity.Obj{"question": "public?"}.Payload()})
	if err != nil {
		t.Fatalf("creating %s: %v", open, err)
	}
	watched(rev)
	if !seen[open] {
		t.Fatalf("bob's watch did not carry %s, held out to him", open)
	}
}

// A NODE THAT STANDS IS NOT GIVEN GRANTS BY A WRITE. The write is refused, saying so, and
// nothing about the node changes: not its value, and not what it holds out — bob, whom the
// default admits, still reads it.
func TestGrantsOnANodeThatStandsAreRefused(t *testing.T) {
	doc := entity.RefAt("shared", "doc")
	store := standing(t, doc, entity.Obj{"title": "the system's"})
	_, err := createWith(store, victor, doc, entity.Obj{"title": "victor's"})
	if !errors.Is(err, entity.ErrStands) {
		t.Fatalf("grants on a node that stands were not refused as such: %v", err)
	}
	e, _, err := store.Get(doc)
	if err != nil || entity.Str(e.Payload, "title") != "the system's" {
		t.Fatalf("the node changed: %v %v", entity.ToObj(e.Payload), err)
	}
	if _, recorded := ownerOf(t, store, doc); recorded {
		t.Error("a record was written for a node that stood")
	}
	if _, found, err := entity.As(store, "user:bob@example.com").Get(doc); err != nil || !found {
		t.Errorf("bob no longer reads the node the refused write named: %v %v", found, err)
	}
}

// A RACE WITH A PLAIN CREATE IS REFUSED, NOT MERGED. victor owns the place, so nothing about his
// right to create asks for the node's absence; the grants do, at the commit. The plain create
// that got there first stands as it was written.
func TestACreateWithGrantsThatLosesARaceIsRefused(t *testing.T) {
	raw := entitytest.OpenStore(t)
	store := entity.As(raw, entity.SystemDefault)
	if ok, err := writeAs(store, victor, entity.RefAt("mine").At(), entity.Obj{}.Payload()); err != nil || !ok {
		t.Fatalf("victor taking mine: %v %v", ok, err)
	}
	node := entity.RefAt("mine", "q")
	racing := &before{Store: raw, on: entity.RefAt(), then: func() {
		if ok, err := writeAs(store, victor, node.At(), entity.Obj{"by": "plain"}.Payload()); err != nil || !ok {
			t.Errorf("the plain create could not land: %v %v", ok, err)
		}
	}}
	_, err := createWith(entity.As(racing, entity.SystemDefault), victor, node, entity.Obj{"question": "private?"})
	if !errors.Is(err, entity.ErrStands) {
		t.Fatalf("the create with grants that lost the race was not refused: %v", err)
	}
	e, _, err := store.Get(node)
	if err != nil || entity.Get(e.Payload, "question") != nil || entity.Str(e.Payload, "by") != "plain" {
		t.Fatalf("the losing write merged into the node: %v %v", entity.ToObj(e.Payload), err)
	}
	if _, recorded := ownerOf(t, store, node); recorded {
		t.Error("the losing write left a record")
	}
}

// THE RECORD IS WRITTEN WHERE THE WRITER OWNS THE PLACE ABOVE, where a plain create writes none:
// it changes what the node holds out, if not its owner.
func TestACreatorWhoOwnsThePlaceAboveGetsTheRecord(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	if ok, err := writeAs(store, victor, entity.RefAt("mine").At(), entity.Obj{}.Payload()); err != nil || !ok {
		t.Fatalf("victor taking mine: %v %v", ok, err)
	}
	node := entity.RefAt("mine", "p")
	if ok, err := createWith(store, victor, node, entity.Obj{"n": 1}); err != nil || !ok {
		t.Fatalf("creating with grants: %v %v", ok, err)
	}
	if owner, recorded := ownerOf(t, store, node); !recorded || owner != victor {
		t.Fatalf("the record is %q (recorded %v), want victor's", owner, recorded)
	}
	bob := entity.As(store, "user:bob@example.com")
	if _, found, err := bob.Get(node); found || err == nil {
		t.Errorf("bob reads %s, created holding out nothing: %v %v", node, found, err)
	}
	if _, found, err := bob.Get(entity.RefAt("mine")); err != nil || !found {
		t.Errorf("bob no longer reads the place above, which holds out the default: %v %v", found, err)
	}
}

// WHAT A CREATOR MAY SAY IS WHAT AN OWNER MAY SAY, and no more: `owner` held out at the node is
// refused as `viff perms --grants` refuses it, and nothing is created. So are grants where no
// node is created to carry them.
func TestAGrantNoOwnerCouldGiveIsRefused(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	node := entity.RefAt("mine", "x")
	_, err := createWith(store, victor, node, entity.Obj{"n": 1},
		entity.Grant{At: node.Segs(), To: map[entity.Level][]string{entity.OwnerLevel: {"user:bob@example.com"}}})
	if !errors.Is(err, entity.ErrBadGrants) {
		t.Fatalf("`owner` held out at the node was not refused: %v", err)
	}
	if _, found, _ := store.Get(node); found {
		t.Error("the node was created")
	}
	for name, w := range map[string]entity.Write{
		"a delete":   {At: node.At()},
		"an element": {At: node.Into("[0]"), Value: entity.Obj{}.Payload()},
		"the mirror": {At: entity.PermsRef(node).At(), Value: entity.Obj{}.Payload()},
	} {
		w.Principal, w.Grants = victor, []entity.Grant{}
		if _, _, err := entity.As(store, victor).Write(w); !errors.Is(err, entity.ErrBadGrants) {
			t.Errorf("grants on %s were not refused: %v", name, err)
		}
	}
}
