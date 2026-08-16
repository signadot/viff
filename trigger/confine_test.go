package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A confined engine refuses, at install, a rule that would write outside it — where "write" is
// the subject a patch advances and the address a derive lands at.
//
// The message names the address, because the author is who can fix it and the address is what
// they got wrong.
func TestAConfinedCharterRefusesWhatItCannotWrite(t *testing.T) {
	eng := New(entitytest.Store(t), denyResolver{})
	eng.Confine(Subtree{"", "demo", ""})

	err := eng.AddTrigger(Trigger{
		Name:      "reach",
		Condition: Condition{Subject: Match{"system": "other", "kind": "thing"}},
		Action:    Action{Patch: entity.Obj{"touched": true}.Payload()},
	})
	if err == nil {
		t.Fatal("a rule acting on another charter's subtree installed")
	}
	if !strings.Contains(err.Error(), "other:thing") || !strings.Contains(err.Error(), "demo:*") {
		t.Errorf("error %q names neither the address nor the confinement", err)
	}

	// A derive is the other way a rule writes, and `stratum.go` already knows how to find them
	// wherever they hide — a sequence element, a completion hook.
	err = eng.AddTrigger(mustParseCharter(t, `
- name: derive-out
  stratum: low
  condition:
    subject: {system: demo, kind: pr}
  action:
    derive:
      other:
        finding: {value: {seen: true}}
`))
	if err == nil || !strings.Contains(err.Error(), "other:finding") {
		t.Fatalf("a derive into another charter's subtree installed, or said the wrong thing: %v", err)
	}
}

// What it may write, it writes. The guard is not a general refusal of anything that mentions
// another address: a rule may READ anywhere — "react to what happened over there, record what
// it means over here" is the shape most real rules have.
func TestAConfinedCharterStillReadsAnywhere(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.Confine(Subtree{"", "drift", ""})

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: note-the-head
  condition:
    when: {system: git, kind: head, value: {sha: !glob "*"}}
    subject: {system: drift, kind: finding, id: latest}
  action:
    patch: {saw: "yes"}
`)); err != nil {
		t.Fatalf("a rule reading elsewhere and writing inside was refused: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitField(t, store, entity.RefAt("drift", "finding", "latest"), "saw", "yes")
}

// AND THE RUN-TIME HALF, which is the one that makes it a guarantee: a derive whose address is
// TEMPLATED cannot be checked at install, so the write itself is refused.
//
// The rule below installs — `derive` names `drift:finding`, which is inside — and its key is
// built from the subject at fire time. The engine writing it is what asks the question again.
func TestAWriteOutsideTheConfinementIsRefusedAtTheWrite(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	eng.Confine(Subtree{"", "drift", ""})

	// The direct proof, at the level the guarantee lives: whatever a template resolves to, the
	// write goes through here.
	inside := entity.RefAt("drift", "finding", "x")
	if err := eng.patchEntity("t", 0, inside, entity.Obj{"ok": true}.Payload()); err != nil {
		t.Fatalf("a write inside the confinement was refused: %v", err)
	}
	if _, ok, _ := store.Get(inside); !ok {
		t.Fatal("the write inside did not land")
	}
	outside := entity.RefAt("other", "thing", "x")
	err := eng.patchEntity("t", 0, outside, entity.Obj{"ok": true}.Payload())
	if err == nil {
		t.Fatal("a write outside the confinement landed")
	}
	if !strings.Contains(err.Error(), "other:thing:x") {
		t.Errorf("the refusal does not name what was refused: %v", err)
	}
	if _, ok, _ := store.Get(outside); ok {
		t.Fatal("the refused write landed anyway")
	}
}

// A stratum is about the charter's own addresses. One reaching outside is refused rather than
// left standing as an ordering no rule inside the confinement could ever satisfy.
func TestAConfinedCharterRefusesAStratumReachingOutside(t *testing.T) {
	eng := New(entitytest.Store(t), denyResolver{})
	eng.Confine(Subtree{"", "drift", ""})

	if err := eng.AddStratum(Stratum{Name: "mine", Level: 10,
		Match: []Match{{"system": "drift", "kind": "finding"}}}); err != nil {
		t.Fatalf("a stratum inside the confinement was refused: %v", err)
	}
	err := eng.AddStratum(Stratum{Name: "theirs", Level: 20,
		Match: []Match{{"system": "other", "kind": "thing"}}})
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("a stratum owning another charter's addresses installed: %v", err)
	}
}

// Two charters at disjoint addresses do not see each other's writes — the isolation the whole
// mechanism is for, end to end rather than by construction.
func TestTwoConfinedChartersDoNotWriteIntoEachOther(t *testing.T) {
	store := entitytest.Store(t)
	mine := New(store, task.AutoResolver{})
	mine.Confine(Subtree{"", "mine", ""})
	theirs := New(store, task.AutoResolver{})
	theirs.Confine(Subtree{"", "theirs", ""})

	// Both rules say the same thing about the same cause, and each writes in its own half.
	for _, c := range []struct {
		eng *Engine
		src string
	}{
		{mine, `
- name: mine
  condition: {when: {system: git, kind: head}, subject: {system: mine, kind: note, id: n}}
  action: {patch: {saw: "yes"}}`},
		{theirs, `
- name: theirs
  condition: {when: {system: git, kind: head}, subject: {system: theirs, kind: note, id: n}}
  action: {patch: {saw: "yes"}}`},
	} {
		if err := c.eng.AddTrigger(mustParseCharter(t, c.src)); err != nil {
			t.Fatalf("install: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c.eng.Start(ctx)
	}

	entitytest.Set(t, store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitField(t, store, entity.RefAt("mine", "note", "n"), "saw", "yes")
	waitField(t, store, entity.RefAt("theirs", "note", "n"), "saw", "yes")

	// And neither wrote where the other lives — which is the claim. A rule cannot reach the
	// other half even by naming it, because the install would have refused it.
	if err := mine.AddTrigger(mustParseCharter(t, `
- name: reach
  condition: {subject: {system: theirs, kind: note, id: n}}
  action: {patch: {tampered: true}}`)); err == nil {
		t.Fatal("one charter installed a rule that writes into the other's subtree")
	}
	for range 20 {
		if e, ok, _ := store.Get(entity.RefAt("theirs", "note", "n")); ok {
			if entity.ToObj(e.Payload)["tampered"] != nil {
				t.Fatal("the other charter's entity was tampered with")
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}
