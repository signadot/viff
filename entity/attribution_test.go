package entity_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// stamped wraps a store and keeps the last write it saw, so a test can read the principal
// that reached the substrate — the log does not hold one yet.
type stamped struct {
	entity.Store
	last   entity.Write
	writes []entity.Write
}

func (s *stamped) Write(w entity.Write) (bool, int64, error) {
	s.last = w
	s.writes = append(s.writes, w)
	return s.Store.Write(w)
}

// lastAt is the principal of the last write the substrate saw at ref's own address.
func (s *stamped) lastAt(ref entity.Ref) string {
	for i := len(s.writes) - 1; i >= 0; i-- {
		if s.writes[i].At.Ref == ref {
			return s.writes[i].Principal
		}
	}
	return ""
}

// seedRecord writes a perms record by hand: the owner of ref and what they hold out. The
// mirror is versed's to write, and a test standing in for versed writes it the same way —
// through an attributed view, so the view learns the record moved.
func seedRecord(t *testing.T, store entity.Store, ref entity.Ref, owner string, grants ...entity.Grant) {
	t.Helper()
	rec := entity.Obj{}
	if owner != "" {
		rec[entity.OwnerField] = owner
	}
	// A write merges, so a record re-written without grants says so, or the old ones stand.
	rec[entity.GrantsField] = entity.Removed()
	if len(grants) > 0 {
		rec[entity.GrantsField] = entity.GrantsPayload(ref.Segs(), grants)
	}
	if _, _, err := store.Write(entity.Write{
		At: entity.PermsRef(ref).At(), Value: rec.Payload(), Principal: entity.SystemPerms,
	}); err != nil {
		t.Fatal(err)
	}
}

// grantInstalls holds out `owner` under the rule slice to everyone, which is the grant versed
// seeds so that anyone may install a charter.
func grantInstalls(t *testing.T, store entity.Store) {
	t.Helper()
	seedRecord(t, store, entity.VerseRef("trigger", "rule"), "", entity.Grant{
		At: []string{entity.VerseRoot, "trigger", "rule"}, Below: true,
		To: map[entity.Level][]string{entity.OwnerLevel: {entity.Everyone}},
	})
}

func ownerOf(t *testing.T, store entity.Store, ref entity.Ref) (string, bool) {
	t.Helper()
	rec, ok, err := store.Get(entity.PermsRef(ref))
	if err != nil {
		t.Fatalf("reading the owner of %s: %v", ref, err)
	}
	if !ok {
		return "", false
	}
	return entity.Str(rec.Payload, entity.OwnerField), true
}

// A write through an attributed store carries the principal, and what it creates is owned by
// them: the record stands in the perms mirror, keyed by the topmost node the write brought into
// being — `demo` here, since the write made that too, and everything under it is its owner's.
func TestAttributedWriteStampsThePrincipalAndRecordsTheOwner(t *testing.T) {
	inner := &stamped{Store: entitytest.OpenStore(t)}
	alice := entity.As(inner, "user:alice@example.com")
	pr := entity.RefAt("demo", "pr", "1")

	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	// The owner record is the LAST write the substrate saw: it follows the entity write, as
	// the system, under an absence precondition.
	if inner.last.Principal != entity.SystemPerms || inner.last.At.Ref != entity.PermsRef(entity.RefAt("demo")) {
		t.Fatalf("the write after a creation should be the owner record of what it created, as the system; got %s as %q",
			inner.last.At, inner.last.Principal)
	}
	if o, ok := ownerOf(t, inner, entity.RefAt("demo")); !ok || o != "user:alice@example.com" {
		t.Fatalf("owner of %s = %q, %v; want alice", pr, o, ok)
	}

	// A LATER write by somebody else — admitted by alice holding it out to them — is theirs,
	// and moves nothing: ownership changes only by create and delete.
	bob := entity.As(alice, "user:bob@example.com") // one memory, two views, as versed derives them
	seedRecord(t, alice, pr, "user:alice@example.com", entity.Grant{
		At: pr.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {"user:bob@example.com"}},
	})
	if _, _, err := bob.Write(entity.Write{At: pr.At("stage"), Value: entity.Val("reviewed")}); err != nil {
		t.Fatal(err)
	}
	if inner.last.Principal != "user:bob@example.com" {
		t.Fatalf("bob's write reached the store as %q", inner.last.Principal)
	}
	if o, _ := ownerOf(t, inner, pr); o != "user:alice@example.com" {
		t.Fatalf("a field write by bob moved the owner to %q", o)
	}

	// Modify goes through the same stamp: a compare-and-swap made on alice's behalf is hers
	// without the caller saying so.
	if err := entity.Modify(context.Background(), alice, pr, func(cur entity.Payload) entity.Payload {
		return entity.Obj{"stage": "merged"}.Payload()
	}); err != nil {
		t.Fatal(err)
	}
	if got := inner.lastAt(pr); got != "user:alice@example.com" {
		t.Fatalf("Modify's write reached the store as %q", got)
	}

	// Deleting a row inside the place does NOT give the place away: alice made `demo`, and a
	// row leaving it changes nothing about who it belongs to.
	if _, _, err := alice.Write(entity.Write{At: pr.At()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Logf("bob is refused inside alice's place, as he should be: %v", err)
	} else {
		t.Fatal("bob wrote inside a place alice created")
	}
	// Deleting the PLACE forgets its record, and whoever creates it next owns it.
	demo := entity.RefAt("demo")
	if _, _, err := alice.Write(entity.Write{At: demo.At()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownerOf(t, inner, demo); ok {
		t.Fatal("the owner record outlived the place")
	}
	if _, _, err := bob.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, demo); o != "user:bob@example.com" {
		t.Fatalf("re-created by bob, owner = %q", o)
	}
}

// A place acting — via a rule's record — confers the owner of that record: what a rule brings
// into being belongs to whoever installed the rule, while the write itself still says the rule
// acted.
func TestAPlaceActingResolvesToItsOwner(t *testing.T) {
	inner := &stamped{Store: entitytest.OpenStore(t)}
	rule := entity.VerseRef("trigger", "rule", "review")
	walter := entity.As(inner, "user:walter@example.com")
	grantInstalls(t, walter)
	if _, _, err := walter.Write(entity.Write{At: rule.At(), Value: entity.Obj{"name": "review"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, rule); o != "user:walter@example.com" {
		t.Fatalf("the rule record's owner = %q", o)
	}

	firing := entity.As(walter, entity.Via(rule))
	pr := entity.RefAt("demo", "pr", "2")
	if _, _, err := firing.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	// Owned THROUGH what the firing created: the record stands at the topmost new node, and
	// ownership descends from it.
	if ok, err := entity.Holds(walter, "user:walter@example.com", entity.OwnerLevel, pr); err != nil || !ok {
		t.Fatalf("an entity the rule created is not the rule's owner's: %v %v", ok, err)
	}
}

// An unattributed view stamps nothing and records nothing: the node reads as its parent's.
// And a store attributed twice is one view over one memory, not a stamp over a stamp.
func TestAnEmptyPrincipalRecordsNoOwner(t *testing.T) {
	inner := &stamped{Store: entitytest.OpenStore(t)}
	nobody := entity.As(entity.As(inner, entity.SystemDefault), "")
	pr := entity.RefAt("demo", "pr", "3")
	if _, _, err := nobody.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if inner.last.Principal != "" {
		t.Fatalf("an unattributed request wrote as %q", inner.last.Principal)
	}
	if _, ok := ownerOf(t, inner, pr); ok {
		t.Fatal("an unattributed write recorded an owner")
	}
	if entity.PrincipalOf(nobody) != "" || entity.PrincipalOf(entity.As(inner, "x")) != "x" {
		t.Fatal("PrincipalOf does not report the view's principal")
	}
}

// Every method the logd store has beyond the Store interface is one a caller asks for by
// type assertion, and a wrapper that lacks it turns "the read failed" into "nothing there".
// So the attributed store must have all of them, and this is what says so when one is added.
func TestAttributedPassesEveryOptionalMethodThrough(t *testing.T) {
	logd := reflect.TypeOf((*entity.LogdStore)(nil))
	wrapped := reflect.TypeOf(entity.As(entitytest.OpenStore(t), "x"))
	iface := reflect.TypeOf((*entity.Store)(nil)).Elem()
	for i := 0; i < logd.NumMethod(); i++ {
		m := logd.Method(i)
		if _, inIface := iface.MethodByName(m.Name); inIface {
			continue
		}
		if _, ok := wrapped.MethodByName(m.Name); !ok {
			t.Errorf("LogdStore.%s is not passed through by the attributed store; add it to attribution.go", m.Name)
		}
	}
}

// The writer reaches the log and comes back on the commit: a replay of what alice wrote says
// alice wrote it, and a write that named nobody says so by saying nothing.
func TestTheAuthorRidesTheCommit(t *testing.T) {
	store := entitytest.OpenStore(t)
	alice := entity.As(store, "user:alice@example.com")
	pr := entity.RefAt("demo", "pr", "9")
	_, rev, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()})
	if err != nil {
		t.Fatal(err)
	}
	_, anon, err := store.Write(entity.Write{At: pr.At("stage"), Value: entity.Val("draft")})
	if err != nil {
		t.Fatal(err)
	}
	commits, err := store.Since(rev - 1)
	if err != nil {
		t.Fatal(err)
	}
	by := map[int64]string{}
	for _, c := range commits {
		by[c.Rev] = c.Author
	}
	if by[rev] != "user:alice@example.com" {
		t.Fatalf("commit %d replays as written by %q, want alice", rev, by[rev])
	}
	if by[anon] != "" {
		t.Fatalf("an unattributed write replays as written by %q", by[anon])
	}
}

// A record is written only where ownership changes. The system owns the root, so what the
// system creates under it records nothing; a record standing above a node covers every node
// beneath it with the same owner, and only a different owner below it earns one of its own.
func TestARecordIsWrittenOnlyWhereOwnershipChanges(t *testing.T) {
	inner := &stamped{Store: entitytest.OpenStore(t)}
	system := entity.As(inner, entity.SystemDefault)
	ref := entity.RefAt("git", "ref", "main")
	if _, _, err := system.Write(entity.Write{At: ref.At(), Value: entity.Obj{"sha": "abc"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownerOf(t, inner, ref); ok {
		t.Fatal("a system-created node under the system's root got a record of its own")
	}
	if inner.last.At.Ref != ref {
		t.Fatalf("something was written after the entity: %s", inner.last.At)
	}

	// A slice alice owns, holding out `owner` beneath it: the record stands at the prefix, so
	// what alice creates beneath it records nothing, and what bob creates there does.
	slice := entity.RefAt("spaces", "alice")
	seedRecord(t, system, slice, "user:alice@example.com", entity.Grant{
		At: []string{"spaces", "alice"}, Below: true,
		To: map[entity.Level][]string{entity.OwnerLevel: {entity.Everyone}},
	})
	alice := entity.As(system, "user:alice@example.com")
	mine := entity.RefAt("spaces", "alice", "notes")
	if _, _, err := alice.Write(entity.Write{At: mine.At(), Value: entity.Obj{"n": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownerOf(t, inner, mine); ok {
		t.Fatal("alice's node under alice's slice got a record of its own")
	}
	bob := entity.As(system, "user:bob@example.com")
	theirs := entity.RefAt("spaces", "alice", "guest")
	if _, _, err := bob.Write(entity.Write{At: theirs.At(), Value: entity.Obj{"n": 2}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, ok := ownerOf(t, inner, theirs); !ok || o != "user:bob@example.com" {
		t.Fatalf("bob's node under alice's slice: owner %q, %v; want bob on record", o, ok)
	}

	// A slice alice SHARES — read-write held out, not owner: what bob creates there is
	// alice's, and records nothing.
	shared := entity.RefAt("spaces", "alice", "shared")
	seedRecord(t, system, shared, "user:alice@example.com", entity.Grant{
		At: []string{"spaces", "alice", "shared"}, Below: true,
		To: map[entity.Level][]string{entity.ReadWrite: {entity.Everyone}},
	})
	file := entity.RefAt("spaces", "alice", "shared", "file")
	if _, _, err := bob.Write(entity.Write{At: file.At(), Value: entity.Obj{"n": 3}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, ok := ownerOf(t, inner, file); ok {
		t.Fatalf("a read-write grantee's creation got a record of its own, owned by %q", o)
	}
}

// A rule that has no owner on record acts as NOBODY — not as the system above it, which is what
// having no record would otherwise be worth, and not as its own address, which is no party — and
// starts acting as its owner the moment the record is written: a miss is not remembered.
func TestAResolutionMissIsNotRemembered(t *testing.T) {
	inner := &stamped{Store: entitytest.OpenStore(t)}
	rule := entity.VerseRef("trigger", "rule", "late")
	system := entity.As(inner, entity.SystemDefault)
	firing := entity.As(system, entity.Via(rule))
	first := entity.RefAt("demo", "pr", "1")
	if _, _, err := firing.Write(entity.Write{At: first.At(), Value: entity.Obj{"stage": "open"}.Payload()}); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("a rule nobody owns wrote in an open verse: %v", err)
	}
	if ok, _ := entity.Holds(system, entity.Via(rule), entity.Read, entity.PermsRef(entity.RefAt())); ok {
		t.Fatal("a rule nobody owns reads the root's perms record, which only the system may")
	}
	grantInstalls(t, system)
	walter := entity.As(system, "user:walter@example.com")
	if _, _, err := walter.Write(entity.Write{At: rule.At(), Value: entity.Obj{"name": "late"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	second := entity.RefAt("demo", "pr", "2")
	if _, _, err := firing.Write(entity.Write{At: second.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	// The record stands at the top of what the write made, which is `demo`.
	if o, _ := ownerOf(t, inner, entity.RefAt("demo")); o != "user:walter@example.com" {
		t.Fatalf("after the rule has an owner, its creation is owned by %q, want the installer", o)
	}
}

// A scope has an attribution of its own — one per name, the same one twice, baseline's for the
// empty name — and a view re-derived for another principal keeps it. What a scope's checks
// read is then the scope's mirror, layered as the store is, and never baseline's cache
// (docs/perms.md#scopes).
func TestAScopeHasAnAttributionOfItsOwn(t *testing.T) {
	base := entity.As(entitytest.OpenStore(t), "user:a")
	me := base.Scope("me")
	if entity.PolicyOf(me) == entity.PolicyOf(base) {
		t.Fatal("a scoped view shares baseline's attribution")
	}
	if entity.PolicyOf(base.Scope("me")) != entity.PolicyOf(me) {
		t.Fatal("opening the same scope twice gave two attributions")
	}
	if entity.PolicyOf(base.Scope("you")) == entity.PolicyOf(me) {
		t.Fatal("two scopes share one attribution")
	}
	if entity.PolicyOf(base.Scope("")) != entity.PolicyOf(base) {
		t.Fatal("the empty scope is not baseline")
	}
	if entity.PolicyOf(entity.As(me, "user:b")) != entity.PolicyOf(me) {
		t.Fatal("re-deriving a scoped view for another principal lost the scope's attribution")
	}
	entity.PolicyOf(base).DropScope("me")
	if entity.PolicyOf(base.Scope("me")) == entity.PolicyOf(me) {
		t.Fatal("a dropped scope's attribution came back")
	}
}
