package entity_test

import (
	"context"
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
		At: entity.PermsRef(ref).At(), Value: rec.Payload(), Principal: entity.SystemPrincipal,
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

// A write through an attributed store carries the principal, and the entity it creates is
// owned by them: the record stands in the perms mirror, keyed by the entity's address.
func TestAttributedWriteStampsThePrincipalAndRecordsTheOwner(t *testing.T) {
	inner := &stamped{Store: entitytest.Store(t)}
	alice := entity.As(inner, "alice@example.com")
	pr := entity.RefAt("demo", "pr", "1")

	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	// The owner record is the LAST write the substrate saw: it follows the entity write, as
	// the system, under an absence precondition.
	if inner.last.Principal != entity.SystemPrincipal || inner.last.At.Ref != entity.PermsRef(pr) {
		t.Fatalf("the write after an entity's creation should be its owner record, as the system; got %s as %q",
			inner.last.At, inner.last.Principal)
	}
	if o, ok := ownerOf(t, inner, pr); !ok || o != "alice@example.com" {
		t.Fatalf("owner of %s = %q, %v; want alice", pr, o, ok)
	}

	// A LATER write by somebody else — admitted by alice holding it out to them — is theirs,
	// and moves nothing: ownership changes only by create and delete.
	bob := entity.As(alice, "bob@example.com") // one memory, two views, as versed derives them
	seedRecord(t, alice, pr, "alice@example.com", entity.Grant{
		At: pr.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {"bob@example.com"}},
	})
	if _, _, err := bob.Write(entity.Write{At: pr.At("stage"), Value: entity.Val("reviewed")}); err != nil {
		t.Fatal(err)
	}
	if inner.last.Principal != "bob@example.com" {
		t.Fatalf("bob's write reached the store as %q", inner.last.Principal)
	}
	if o, _ := ownerOf(t, inner, pr); o != "alice@example.com" {
		t.Fatalf("a field write by bob moved the owner to %q", o)
	}

	// Modify goes through the same stamp: a compare-and-swap made on alice's behalf is hers
	// without the caller saying so.
	if err := entity.Modify(context.Background(), alice, pr, func(cur entity.Payload) entity.Payload {
		return entity.Obj{"stage": "merged"}.Payload()
	}); err != nil {
		t.Fatal(err)
	}
	if got := inner.lastAt(pr); got != "alice@example.com" {
		t.Fatalf("Modify's write reached the store as %q", got)
	}

	// Deleting the whole entity forgets its owner, so whoever creates it next owns it.
	if _, _, err := alice.Write(entity.Write{At: pr.At()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownerOf(t, inner, pr); ok {
		t.Fatal("the owner record outlived the entity")
	}
	if _, _, err := bob.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, pr); o != "bob@example.com" {
		t.Fatalf("re-created by bob, owner = %q", o)
	}
}

// A principal that is itself an address — a rule's record — confers the owner of that
// record: what a rule brings into being belongs to whoever installed the rule, while the
// write itself still says the rule acted.
func TestAnAddressPrincipalResolvesToItsOwner(t *testing.T) {
	inner := &stamped{Store: entitytest.Store(t)}
	rule := entity.VerseRef("trigger", "rule", "review")
	scott := entity.As(inner, "scott@example.com")
	grantInstalls(t, scott)
	if _, _, err := scott.Write(entity.Write{At: rule.At(), Value: entity.Obj{"name": "review"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, rule); o != "scott@example.com" {
		t.Fatalf("the rule record's owner = %q", o)
	}

	firing := entity.As(scott, rule.String())
	pr := entity.RefAt("demo", "pr", "2")
	if _, _, err := firing.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, pr); o != "scott@example.com" {
		t.Fatalf("an entity the rule created is owned by %q, want the rule's owner", o)
	}
}

// An unattributed view stamps nothing and records nothing: the node reads as its parent's.
// And a store attributed twice is one view over one memory, not a stamp over a stamp.
func TestAnEmptyPrincipalRecordsNoOwner(t *testing.T) {
	inner := &stamped{Store: entitytest.Store(t)}
	nobody := entity.As(entity.As(inner, entity.SystemPrincipal), "")
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
	wrapped := reflect.TypeOf(entity.As(entitytest.Store(t), "x"))
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
	store := entitytest.Store(t)
	alice := entity.As(store, "alice@example.com")
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
	if by[rev] != "alice@example.com" {
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
	inner := &stamped{Store: entitytest.Store(t)}
	system := entity.As(inner, entity.SystemPrincipal)
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
	seedRecord(t, system, slice, "alice@example.com", entity.Grant{
		At: []string{"spaces", "alice"}, Below: true,
		To: map[entity.Level][]string{entity.OwnerLevel: {entity.Everyone}},
	})
	alice := entity.As(system, "alice@example.com")
	mine := entity.RefAt("spaces", "alice", "notes")
	if _, _, err := alice.Write(entity.Write{At: mine.At(), Value: entity.Obj{"n": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownerOf(t, inner, mine); ok {
		t.Fatal("alice's node under alice's slice got a record of its own")
	}
	bob := entity.As(system, "bob@example.com")
	theirs := entity.RefAt("spaces", "alice", "guest")
	if _, _, err := bob.Write(entity.Write{At: theirs.At(), Value: entity.Obj{"n": 2}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, ok := ownerOf(t, inner, theirs); !ok || o != "bob@example.com" {
		t.Fatalf("bob's node under alice's slice: owner %q, %v; want bob on record", o, ok)
	}

	// A slice alice SHARES — read-write held out, not owner: what bob creates there is
	// alice's, and records nothing.
	shared := entity.RefAt("spaces", "alice", "shared")
	seedRecord(t, system, shared, "alice@example.com", entity.Grant{
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

// A rule that has no owner on record acts as the nearest owner above it, the system, and starts
// acting as its owner the moment the record is written — a miss is not remembered, and an
// address is never an owner.
func TestAResolutionMissIsNotRemembered(t *testing.T) {
	inner := &stamped{Store: entitytest.Store(t)}
	rule := entity.VerseRef("trigger", "rule", "late")
	system := entity.As(inner, entity.SystemPrincipal)
	firing := entity.As(system, rule.String())
	first := entity.RefAt("demo", "pr", "1")
	if _, _, err := firing.Write(entity.Write{At: first.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, ok := ownerOf(t, inner, first); ok {
		t.Fatalf("before the rule has an owner it acts as the system, and a system node under the "+
			"system's root records nothing; got a record owned by %q", o)
	}
	grantInstalls(t, system)
	scott := entity.As(system, "scott@example.com")
	if _, _, err := scott.Write(entity.Write{At: rule.At(), Value: entity.Obj{"name": "late"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	second := entity.RefAt("demo", "pr", "2")
	if _, _, err := firing.Write(entity.Write{At: second.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, inner, second); o != "scott@example.com" {
		t.Fatalf("after the rule has an owner, its creation is owned by %q, want the installer", o)
	}
}
