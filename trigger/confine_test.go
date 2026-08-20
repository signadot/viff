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
	eng.Confine(SubtreeAt("demo"))

	err := eng.AddTrigger(Trigger{
		Name:      "reach",
		Condition: Condition{Subject: Match{"system": "other", "kind": "thing"}},
		Action:    Action{Patch: entity.Obj{"touched": true}.Payload()},
	})
	if err == nil {
		t.Fatal("a rule acting on another charter's subtree installed")
	}
	if !strings.Contains(err.Error(), "other.thing") || !strings.Contains(err.Error(), "demo.*") {
		t.Errorf("error %q names neither the address nor the confinement", err)
	}

	// A derive is the other way a rule writes, and `stratum.go` already knows how to find them
	// wherever they hide — a sequence element, a completion hook.
	err = eng.AddTrigger(mustParseCharter(t, `
- name: derive-out
  stratum: low
  condition:
    subject: {path: [demo, pr, *]}
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
// another address: "react to what happened over there, record what it means over here" is the
// shape most real rules have — so a package reads what it DECLARES (reads.go), and the
// declaration is what makes the dependency visible rather than what makes it possible.
func TestAConfinedCharterReadsWhatItDeclares(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.Confine(SubtreeAt("drift"))
	eng.SetReads([]Subtree{SubtreeAt("git")}) // …declared, which is the half reads.go adds

	if err := eng.AddTrigger(mustParseCharter(t, `
- name: note-the-head
  condition:
    when: {path: [git, head, *], value: {sha: !glob "*"}}
    subject: {path: [drift, finding, latest]}
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
	eng.Confine(SubtreeAt("drift"))

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
	if !strings.Contains(err.Error(), "other.thing.x") {
		t.Errorf("the refusal does not name what was refused: %v", err)
	}
	if _, ok, _ := store.Get(outside); ok {
		t.Fatal("the refused write landed anyway")
	}
}

// Two charters at disjoint addresses do not see each other's writes — the isolation the whole
// mechanism is for, end to end rather than by construction.
func TestTwoConfinedChartersDoNotWriteIntoEachOther(t *testing.T) {
	store := entitytest.Store(t)
	mine := New(store, task.AutoResolver{})
	mine.Confine(SubtreeAt("mine"))
	mine.SetReads([]Subtree{SubtreeAt("git")})
	theirs := New(store, task.AutoResolver{})
	theirs.Confine(SubtreeAt("theirs"))
	theirs.SetReads([]Subtree{SubtreeAt("git")}) // both read the source; neither declares a read of the other

	// Both rules say the same thing about the same cause, and each writes in its own half.
	for _, c := range []struct {
		eng *Engine
		src string
	}{
		{mine, `
- name: mine
  condition: {when: {path: [git, head, *]}, subject: {path: [mine, note, n]}}
  action: {patch: {saw: "yes"}}`},
		{theirs, `
- name: theirs
  condition: {when: {path: [git, head, *]}, subject: {path: [theirs, note, n]}}
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
  condition: {subject: {path: [theirs, note, n]}}
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

// A PACKAGE READS A SOURCE AS ITS SUBJECT, which is the case that decides whether packages are
// usable at all: two charters both reacting to `git:*` is the ordinary arrangement, not an
// exotic one, and a source is nobody's package to be inside.
//
// comment-drift's own `head-moved` is the shape, reduced: select `git.head.verse`, derive into
// `drift:head`. It reads its subject and writes somewhere else, so the subject's address is a
// READ and only the derive is confined.
func TestAPackageMaySubjectOnASourceItOnlyReads(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.Confine(SubtreeAt("drift"))
	eng.SetReads([]Subtree{SubtreeAt("git")})

	// The levels as the real charter writes them: the SOURCE slice at the bottom — which is
	// what a package's lowest level owns, since its rules select from what they read — and the
	// package's own above it, because that is the only direction a derive may go.
	if err := eng.AddStratum(Stratum{Name: "reflected", Level: 0,
		Match: []Match{{"system": "git"}}}); err != nil {
		t.Fatalf("stratum: %v", err)
	}
	if err := eng.AddStratum(Stratum{Name: "ours", Level: 10,
		Match: []Match{{"system": "drift"}}}); err != nil {
		t.Fatalf("stratum: %v", err)
	}
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: head-moved
  stratum: reflected
  condition:
    subject: git.head.verse
  action:
    derive:
      drift:
        head:
          repo: $subject.value.repo
`)); err != nil {
		t.Fatalf("a rule deriving from a source it reads was refused: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("git", "head", "verse"),
		entity.Obj{"repo": "verse", "sha": "abc"}.Payload())
	// The derived entity lands under the package's own address, keyed by the derive's own id.
	deadline := time.After(3 * time.Second)
	for {
		found := false
		for _, e := range store.List(func(r entity.Ref) bool {
			return r.System() == "drift" && r.Kind() == "head"
		}) {
			if entity.Str(e.Payload, "repo") == "verse" {
				found = true
			}
		}
		if found {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the derive never landed in the package's own address")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// And the same subject with a PATCH is still refused: that one writes the source.
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: touch-the-source
  condition:
    subject: git.head.verse
  action:
    patch: {touched: "yes"}
`)); err == nil {
		t.Fatal("a rule patching a source outside the package installed")
	}
}
