package entity_test

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// oldRecord writes a perms record as a binary from before `child-owner` wrote it, straight into
// the store: the grammar no longer reads this spelling, so nothing that goes through it can.
func oldRecord(t *testing.T, store entity.Store, at entity.Ref, tony string) {
	t.Helper()
	if _, _, err := store.Write(entity.Write{
		At: entity.PermsRef(at).At(), Value: mustParse(t, tony), Principal: entity.SystemPerms,
	}); err != nil {
		t.Fatal(err)
	}
}

// THE MIGRATION IS WHAT LETS A STORE WITH HISTORY KEEP WORKING. A seed row written before
// `child-owner` says `{below: true, owner: ["*"]}`, the grammar refuses that line, and a field
// the grammar refuses holds out nothing — so without it nobody spawns a task on a verse that was
// upgraded rather than wiped.
func TestTheMigrationRewritesWhatAnOlderBinaryWrote(t *testing.T) {
	raw := entitytest.OpenStore(t)
	system := entity.As(raw, entity.SystemDefault)
	tasks := entity.VerseRef("task", "instance")
	oldRecord(t, system, tasks, `{grants: [{below: true, owner: ["*"]}]}`)
	bob, task := "user:bob@example.com", entity.VerseRef("task", "instance", "run-1")

	// Before: the line is refused, the field with it, and the row holds out nothing.
	if ok, _ := entity.MayCreate(entity.As(raw, entity.SystemDefault), bob, task); ok {
		t.Fatal("the fixture is wrong: the old spelling still reads")
	}
	rewrote, narrowed, err := entity.MigrateChildOwner(system)
	if err != nil {
		t.Fatal(err)
	}
	if rewrote != 1 || len(narrowed) != 0 {
		t.Errorf("rewrote %d, narrowed %v; want one record and nothing narrowed", rewrote, narrowed)
	}
	if ok, err := entity.MayCreate(entity.As(raw, entity.SystemDefault), bob, task); err != nil || !ok {
		t.Errorf("bob cannot create a task after the migration: %v %v", ok, err)
	}
	// The line stands in the new spelling and ONLY the new one: the old key is removed from the
	// line it stood on, which a plain write would have left beside the new one.
	rec, _, _ := system.Get(entity.PermsRef(tasks))
	line := entity.Get(rec.Payload, entity.GrantsField).Values[0]
	if entity.Get(line, "owner") != nil || entity.Get(line, "below") != nil || entity.Get(line, entity.ChildOwnerKey) == nil {
		t.Errorf("the migrated line reads %v", line)
	}
	// And it is once: a second run finds nothing to do.
	if again, _, err := entity.MigrateChildOwner(system); err != nil || again != 0 {
		t.Errorf("a second migration rewrote %d (err %v)", again, err)
	}
}

// A line that held `owner` out AT a node as well as beneath it is NARROWED, and said: there is no
// such grant any more. What is left is what `child-owner` means.
func TestTheMigrationSaysWhatItNarrowed(t *testing.T) {
	raw := entitytest.OpenStore(t)
	system := entity.As(raw, entity.SystemDefault)
	team := entity.RefAt("team")
	oldRecord(t, system, team, `{owner: "user:x@example.com", grants: [{owner: [user:alice@example.com], read: ["*"]}]}`)
	_, narrowed, err := entity.MigrateChildOwner(system)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrowed) != 1 || narrowed[0] != "team" {
		t.Fatalf("narrowed = %v, want [team]", narrowed)
	}
	view := entity.As(raw, entity.SystemDefault)
	alice := "user:alice@example.com"
	if ok, _ := entity.MayCreate(view, alice, team); ok {
		t.Error("alice holds owner AT team after the migration")
	}
	if ok, err := entity.MayCreate(view, alice, entity.RefAt("team", "new")); err != nil || !ok {
		t.Errorf("alice cannot create beneath team: %v %v", ok, err)
	}
	if ok, err := entity.Holds(view, "user:bob@example.com", entity.Read, team); err != nil || !ok {
		t.Errorf("the level beside it did not survive: %v %v", ok, err)
	}
}

// THE WAY BACK, for a rollback past the migration: a binary from before cannot read the new key,
// so the mirror is rewritten as it reads it. It must never come back WIDER than it stood — a
// `child-owner` beside a level about the node goes back as two lines, since the old `owner`
// reached the node unless the whole line said `below`.
func TestUnmigratingNeverWidens(t *testing.T) {
	raw := entitytest.OpenStore(t)
	system := entity.As(raw, entity.SystemDefault)
	team := entity.RefAt("team")
	if err := entity.Claim(system, team, "user:x@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(system, team, []entity.Grant{
		{At: team.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {"user:alice@example.com"}}},
		{At: team.Segs(), To: map[entity.Level][]string{entity.Read: {entity.Everyone}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := entity.UnmigrateChildOwner(system); err != nil {
		t.Fatal(err)
	}
	rec, _, _ := system.Get(entity.PermsRef(team))
	for i, line := range entity.Get(rec.Payload, entity.GrantsField).Values {
		if entity.Get(line, entity.ChildOwnerKey) != nil {
			t.Errorf("line %d still says %s: %v", i, entity.ChildOwnerKey, line)
		}
		if entity.Get(line, "owner") != nil {
			if b := entity.Get(line, "below"); b == nil || !b.Bool {
				t.Errorf("line %d holds `owner` out at the node, which is wider than it stood: %v", i, line)
			}
		}
	}
	// And forward again is where it started.
	if _, narrowed, err := entity.MigrateChildOwner(system); err != nil || len(narrowed) != 0 {
		t.Errorf("the round trip narrowed %v (err %v)", narrowed, err)
	}
	view := entity.As(raw, entity.SystemDefault)
	if ok, _ := entity.MayCreate(view, "user:alice@example.com", team); ok {
		t.Error("alice holds owner AT team after the round trip")
	}
	if ok, err := entity.MayCreate(view, "user:alice@example.com", entity.RefAt("team", "new")); err != nil || !ok {
		t.Errorf("alice lost what she was held out: %v %v", ok, err)
	}
}

// A LINE THAT NAMES ITS PATH GOES BACK UNDER A TRAILING `*`. The old grammar read `below` only on
// a line about the record's own node; on a line with an `at` it was not read at all, so
// `{at: [team, sandbox], below: true, owner: [x]}` held `owner` out AT team.sandbox — x could
// delete the node a rollback was meant to leave as it stood.
func TestUnmigratingALineWithAPathSaysBeneathInThePath(t *testing.T) {
	raw := entitytest.OpenStore(t)
	system := entity.As(raw, entity.SystemDefault)
	team, sandbox := entity.RefAt("team"), entity.RefAt("team", "sandbox")
	if err := entity.Claim(system, team, "user:x@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(system, team, []entity.Grant{
		{At: sandbox.Segs(), Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {"user:alice@example.com"}}},
		{At: sandbox.Segs(), To: map[entity.Level][]string{entity.Read: {entity.Everyone}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := entity.UnmigrateChildOwner(system); err != nil {
		t.Fatal(err)
	}
	rec, _, _ := system.Get(entity.PermsRef(team))
	var owners int
	for i, line := range entity.Get(rec.Payload, entity.GrantsField).Values {
		if entity.Get(line, "owner") == nil {
			continue
		}
		owners++
		at := entity.Strs(line, "at")
		if len(at) != 3 || at[2] != "*" {
			t.Errorf("line %d holds `owner` out at %v, which the old grammar reads as the node itself: %v", i, at, line)
		}
		if entity.Get(line, "below") != nil {
			t.Errorf("line %d says `below` beside an `at`, where nothing read it: %v", i, line)
		}
		if entity.Get(line, "read") != nil {
			t.Errorf("line %d took the level about the node beneath with it: %v", i, line)
		}
	}
	if owners != 1 {
		t.Fatalf("%d lines hold `owner` out, want one", owners)
	}
	// And forward again is where it started, narrowing nothing.
	if _, narrowed, err := entity.MigrateChildOwner(system); err != nil || len(narrowed) != 0 {
		t.Errorf("the round trip narrowed %v (err %v)", narrowed, err)
	}
	view := entity.As(raw, entity.SystemDefault)
	if ok, _ := entity.MayCreate(view, "user:alice@example.com", sandbox); ok {
		t.Error("alice holds owner AT team.sandbox after the round trip")
	}
	if ok, err := entity.MayCreate(view, "user:alice@example.com", entity.RefAt("team", "sandbox", "new")); err != nil || !ok {
		t.Errorf("alice lost what she was held out: %v %v", ok, err)
	}
	if ok, err := entity.Holds(view, "user:bob@example.com", entity.Read, sandbox); err != nil || !ok {
		t.Errorf("the level beside it did not survive: %v %v", ok, err)
	}
}

// A NODE'S OWNER IS WHOEVER CREATED IT, and nobody names somebody else. Every record this package
// can be asked to write carries the writer's own name or the claimant's, which is what keeps a
// thing that acts — a rule, a source, a resolver — acting as whoever made it: if joe could put
// admin's name on the record of his rule, his rule would fire with admin's authority.
func TestNobodyIsMadeAnOwnerExceptByTheirOwnWrite(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	joe, admin := "user:joe@example.com", "user:admin@verse"
	rule := entity.RefAt("joes", "rule")
	if err := entity.Claim(store, rule, joe); err != nil {
		t.Fatal(err)
	}
	// Holding `owner` out AT the node to somebody is not a thing that can be said…
	for name, err := range map[string]error{
		"SetGrants": entity.SetGrants(store, rule, []entity.Grant{{At: rule.Segs(), To: map[entity.Level][]string{entity.OwnerLevel: {admin}}}}),
		"AddGrant":  entity.AddGrant(store, rule, entity.Grant{At: rule.Segs(), To: map[entity.Level][]string{entity.OwnerLevel: {admin}}}),
	} {
		if err == nil {
			t.Errorf("%s held `owner` out at a node", name)
		}
	}
	// …a claim in somebody else's name is made as the CLAIMANT and lands on them, not on the
	// name a caller would rather it carried…
	if err := entity.Claim(store, entity.RefAt("joes", "other"), joe); err != nil {
		t.Fatal(err)
	}
	if o, _ := ownerOf(t, store, entity.RefAt("joes", "other")); o != joe {
		t.Errorf("joes.other is %q's, want the claimant's", o)
	}
	// …and the record joe made still says joe, so whatever acts through it acts as him.
	if o, _ := ownerOf(t, store, rule); o != joe {
		t.Errorf("joes.rule is %q's", o)
	}
	vault := entity.RefAt("vault")
	if err := entity.Claim(store, vault, admin); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, vault, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(store, entity.Via(rule), entity.ReadWrite, entity.RefAt("vault", "secret")); ok {
		t.Error("joe's rule writes admin's vault")
	}
}

// A PLACE SET PRIVATE STAYS PRIVATE ACROSS THE RENAME. What a node created beneath holds out was
// written under `default`; under a name the check no longer reads, the record says nothing, and
// what is created beneath holds out the root's instead — everything its owner closed, readable
// by everyone. The field is renamed at start, together with the old level key inside it, and
// the way back puts both as they were.
func TestTheFieldAnOlderBinaryCalledDefaultIsRenamed(t *testing.T) {
	raw := entitytest.OpenStore(t)
	system := entity.As(raw, entity.SystemDefault)
	alice, bob := "user:alice@example.com", "user:bob@example.com"
	place := entity.RefAt("spaces", "alice")
	oldRecord(t, system, place, `{owner: "user:alice@example.com", grants: [], default: {}}`)
	shared := entity.RefAt("spaces", "shared")
	oldRecord(t, system, shared, `{owner: "user:alice@example.com", grants: [], default: {owner: ["user:bob@example.com"]}}`)

	renamed, err := entity.MigrateChildGrants(system)
	if err != nil || renamed != 2 {
		t.Fatalf("renamed %d records (%v), want the two that said `default`", renamed, err)
	}
	if _, _, err := entity.MigrateChildOwner(system); err != nil {
		t.Fatal(err)
	}
	for _, at := range []entity.Ref{place, shared} {
		rec, _, _ := system.Get(entity.PermsRef(at))
		if entity.Get(rec.Payload, "default") != nil || entity.Get(rec.Payload, entity.ChildGrantsField) == nil {
			t.Fatalf("%s after the migration: %v", at, entity.ToObj(rec.Payload))
		}
	}
	shd, _, _ := system.Get(entity.PermsRef(shared))
	if inner := entity.Get(shd.Payload, entity.ChildGrantsField); entity.Get(inner, entity.ChildOwnerKey) == nil || entity.Get(inner, "owner") != nil {
		t.Errorf("the level inside the renamed field was not respelled: %v", entity.ToObj(shd.Payload))
	}
	// What alice creates in her place holds out nothing, as she set it.
	fresh := entity.As(raw, entity.SystemDefault) // a view that has cached nothing
	note := entity.RefAt("spaces", "alice", "note")
	if _, _, err := entity.As(fresh, alice).Write(entity.Write{At: note.At(), Value: entity.Obj{"n": 1}.Payload(), Principal: alice}); err != nil {
		t.Fatal(err)
	}
	// her place is hers by record, so the note is hers through it; a node of its own shows the copy
	sub := entity.RefAt("spaces", "alice", "box")
	if err := entity.Claim(entity.As(fresh, alice), sub, alice); err != nil {
		t.Fatal(err)
	}
	// `box` has a record of its own, which copied what her place says a new node holds out:
	// nothing. Under the old name that copy would have been the root's, readable by everyone.
	for _, at := range []entity.Ref{note, sub} {
		if ok, _ := entity.Holds(fresh, bob, entity.Read, at); ok {
			t.Errorf("bob reads %s, made in a place alice set private", at)
		}
	}
	// A second run finds nothing to do.
	if again, err := entity.MigrateChildGrants(system); err != nil || again != 0 {
		t.Errorf("a second run renamed %d (%v)", again, err)
	}
	// And the way back, levels first.
	if _, err := entity.UnmigrateChildOwner(system); err != nil {
		t.Fatal(err)
	}
	if back, err := entity.UnmigrateChildGrants(system); err != nil || back != 2 {
		t.Fatalf("unmigrated %d (%v), want 2", back, err)
	}
	rec, _, _ := system.Get(entity.PermsRef(shared))
	if inner := entity.Get(rec.Payload, "default"); inner == nil || entity.Get(inner, "owner") == nil || entity.Get(rec.Payload, entity.ChildGrantsField) != nil {
		t.Errorf("after the way back: %v", entity.ToObj(rec.Payload))
	}
}
