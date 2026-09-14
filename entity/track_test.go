package entity_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// reads counts the reads of the perms mirror that reach the substrate, and the writes to it.
type reads struct {
	entity.Store
	mirror atomic.Int64
	writes atomic.Int64
}

func (r *reads) Get(ref entity.Ref) (entity.Entity, bool, error) {
	if ref.Reserved() && ref.System() == entity.PermsSystem {
		r.mirror.Add(1)
	}
	return r.Store.Get(ref)
}

func (r *reads) Write(w entity.Write) (bool, int64, error) {
	if w.At.Reserved() && w.At.System() == entity.PermsSystem {
		r.writes.Add(1)
	}
	return r.Store.Write(w)
}

// A node written again in a later life of the process — a source re-reflecting an entity it
// reflected before the restart — has its owner record in the loaded mirror, and neither writes
// it again nor reads for it: the record written again would be refused by its own absence
// precondition, and a refused write is explained by reading everything it descended through.
func TestARecordInTheLoadedMirrorIsNotWrittenAgain(t *testing.T) {
	raw := entitytest.Store(t)
	inner := &reads{Store: raw}
	pr := entity.RefAt("demo", "pr", "1")
	first := entity.As(entity.As(inner, entity.SystemDefault), "user:alice@example.com")
	if _, _, err := first.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if got := inner.writes.Load(); got != 1 {
		t.Fatalf("%d mirror writes for a first write, want the owner record alone", got)
	}

	// The next life: a fresh view over the same store, the mirror loaded.
	system := entity.As(inner, entity.SystemDefault)
	if err := entity.Track(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	alice := entity.As(system, "user:alice@example.com")
	reads, writes := inner.mirror.Load(), inner.writes.Load()
	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "reviewed"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if got := inner.writes.Load() - writes; got != 0 {
		t.Fatalf("%d mirror writes for a node whose record the loaded mirror holds, want none", got)
	}
	if got := inner.mirror.Load() - reads; got != 0 {
		t.Fatalf("%d mirror reads for a node whose record the loaded mirror holds, want none", got)
	}
	// And the record stands as it was: alice's, so bob is still refused.
	bob := entity.As(system, "user:bob@example.com")
	if ok, _ := entity.Holds(bob, "user:bob@example.com", entity.ReadWrite, pr); ok {
		t.Fatal("bob writes alice's pr")
	}
}

// With the mirror loaded and watched, a check makes no round trip: an absent record is known
// absent, a present one is in hand, what this process writes is read back the moment it is
// written, and what another process writes arrives on the watch.
func TestATrackedMirrorCostsNoReadPerCheck(t *testing.T) {
	raw := entitytest.Store(t)
	inner := &reads{Store: raw}
	system := entity.As(inner, entity.SystemDefault)
	alice, bob := entity.As(system, "user:alice@example.com"), entity.As(system, "user:bob@example.com")
	pr := entity.RefAt("demo", "pr", "1")
	if _, _, err := alice.Write(entity.Write{At: pr.At(), Value: entity.Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.Track(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	if !entity.PolicyOf(system).Tracked() {
		t.Fatal("not tracked")
	}

	before := inner.mirror.Load()
	// A fresh address beneath a fresh system: every prefix is a miss, and none is read.
	if ok, err := entity.Holds(bob, "user:bob@example.com", entity.OwnerLevel, entity.RefAt("spaces", "bob", "x")); err != nil || !ok {
		t.Fatalf("bob holds owner at a fresh world address: %v %v", ok, err)
	}
	if ok, _ := entity.Holds(bob, "user:bob@example.com", entity.ReadWrite, pr); ok {
		t.Fatal("bob writes alice's pr")
	}
	if _, _, err := bob.Write(entity.Write{At: entity.RefAt("spaces", "bob", "x").At(), Value: entity.Obj{"n": 1}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if got := inner.mirror.Load() - before; got != 0 {
		t.Fatalf("%d mirror reads for checks over a loaded mirror, want none", got)
	}

	// What this process writes to the mirror is what it checks against next.
	if err := entity.AddGrant(alice, pr, entity.Grant{At: pr.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {"user:bob@example.com"}}}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(bob, "user:bob@example.com", entity.ReadWrite, pr); !ok {
		t.Fatal("a grant this process wrote is not what it checks against")
	}

	// What ANOTHER process writes — a second view over the same store, sharing nothing —
	// arrives on the watch.
	other := entity.As(raw, entity.SystemDefault)
	if err := entity.SetGrants(other, pr, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _ := entity.Holds(bob, "user:bob@example.com", entity.ReadWrite, pr); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ok, _ := entity.Holds(bob, "user:bob@example.com", entity.ReadWrite, pr); ok {
		t.Fatal("a grant another process withdrew is still honoured")
	}
	// And a record another process establishes on a node this view had never seen.
	notes := entity.RefAt("spaces", "carol", "notes")
	carol := entity.As(other, "user:carol@example.com")
	if _, _, err := carol.Write(entity.Write{At: notes.At(), Value: entity.Obj{"n": 2}.Payload()}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _ := entity.Holds(bob, "user:bob@example.com", entity.ReadWrite, notes); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ok, _ := entity.Holds(bob, "user:bob@example.com", entity.ReadWrite, notes); ok {
		t.Fatal("carol's record, written by another process, is not seen: bob still holds the open world there")
	}

	// A whole delete sweeps the records beneath it from the cache, without listing the store.
	space := entity.RefAt("spaces", "alice")
	if err := entity.Claim(alice, space, "user:alice@example.com"); err != nil {
		t.Fatal(err)
	}
	child := entity.RefAt("spaces", "alice", "c")
	if _, _, err := alice.Write(entity.Write{At: child.At(), Value: entity.Obj{"n": 3}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if err := entity.AddGrant(alice, child, entity.Grant{At: child.Segs(), To: map[entity.Level][]string{entity.Read: {"user:bob@example.com"}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.Write(entity.Write{At: space.At()}); err != nil {
		t.Fatal(err)
	}
	if o, ok := ownerOf(t, raw, child); ok {
		t.Fatalf("the record beneath the deleted place stands, owned by %q", o)
	}
}
