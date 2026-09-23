package entity_test

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A WRITE OF A GRANTS LIST REPLACES THE LIST. An array write merges by position and truncates,
// so a line written at position i lands ON the line that stood there and keeps its fields
// unless it says to remove them. Every writer of the field goes through GrantsOver, and these
// are the shapes that got it wrong.

func place(t *testing.T) (entity.Store, entity.Ref) {
	t.Helper()
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	at := entity.RefAt("place")
	if err := entity.Claim(store, at, "user:x@example.com"); err != nil {
		t.Fatal(err)
	}
	return store, at
}

// A revoke of one level on a line that carries two: the line is written back without the level,
// and the merge kept it — the revoke reported success and revoked nothing.
func TestARevokeTakesALevelOffALineThatCarriesTwo(t *testing.T) {
	store, at := place(t)
	ana, carol := "user:ana@example.com", "user:carol@example.com"
	if err := entity.SetGrants(store, at, []entity.Grant{{At: at.Segs(), To: map[entity.Level][]string{
		entity.Read: {ana}, entity.ReadWrite: {carol},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := entity.Revoke(store, at, entity.Levels{entity.Read: {ana}}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(store, ana, entity.Read, at); ok {
		t.Error("ana still reads after her read was revoked")
	}
	if ok, err := entity.Holds(store, carol, entity.ReadWrite, at); err != nil || !ok {
		t.Errorf("carol lost her read-write to ana's revoke: %v %v", ok, err)
	}
}

// A revoke that empties the FIRST line drops it, and the line beneath moves up into its place:
// merged onto the old first line, it carried that line's grantee off to its own path.
func TestARevokeThatDropsALineDoesNotMoveItsGrantee(t *testing.T) {
	store, at := place(t)
	ana, carol := "user:ana@example.com", "user:carol@example.com"
	sub := entity.RefAt("place", "sub")
	if err := entity.SetGrants(store, at, []entity.Grant{
		{At: at.Segs(), To: map[entity.Level][]string{entity.Read: {ana}}},
		{At: sub.Segs(), Below: true, To: map[entity.Level][]string{entity.ReadWrite: {carol}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := entity.Revoke(store, at, entity.Levels{entity.Read: {ana}}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []entity.Ref{at, entity.RefAt("place", "sub", "k")} {
		if ok, _ := entity.Holds(store, ana, entity.Read, ref); ok {
			t.Errorf("ana reads %s after her read was revoked", ref)
		}
	}
	if ok, err := entity.Holds(store, carol, entity.ReadWrite, entity.RefAt("place", "sub", "k")); err != nil || !ok {
		t.Errorf("carol's line did not survive the revoke above it: %v %v", ok, err)
	}
}

// SetLevel takes a level off the node's own line and drops the line when that empties it, which
// is how a resolver's grant on a question is taken back.
func TestSettingALevelToNobodyTakesItOff(t *testing.T) {
	store, at := place(t)
	ana := "user:ana@example.com"
	if err := entity.SetLevel(store, at, entity.ReadWrite, []string{ana}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(store, ana, entity.ReadWrite, at); !ok {
		t.Fatal("the level did not land")
	}
	if err := entity.SetLevel(store, at, entity.ReadWrite, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(store, ana, entity.ReadWrite, at); ok {
		t.Error("ana still writes after the level was set to nobody")
	}
}

// A PLACE THAT HOLDS OUT NOTHING NEVER HOLDS OUT EVERYTHING, not even for the moment a grants
// edit takes. A record with an owner and no grants field reads as holding out the root's
// default, so removing the field and then writing it was a window in which a private place was
// readable by everyone — seen by any other process, and permanent if the second write was lost.
func TestAGrantsEditNeverPassesThroughNoGrantsField(t *testing.T) {
	raw := entitytest.OpenStore(t)
	watching := &grantsFieldWatch{Store: raw, rec: entity.PermsRef(entity.RefAt("secret"))}
	store := entity.As(watching, entity.SystemDefault)
	at := entity.RefAt("secret")
	if err := entity.Claim(store, at, "user:x@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(store, at, nil); err != nil {
		t.Fatal(err)
	}
	watching.armed = true
	if err := entity.SetGrants(store, at, []entity.Grant{
		{At: at.Segs(), To: map[entity.Level][]string{entity.Read: {"user:ana@example.com"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if watching.missing {
		t.Error("the record stood with no grants field part way through the edit")
	}
	if ok, _ := entity.Holds(store, "user:carol@example.com", entity.Read, at); ok {
		t.Error("carol reads a place that holds out read to ana alone")
	}
}

// grantsFieldWatch reads the record back after every write to it, as another process's watch
// would see it, and notes a moment it stood with an owner and no grants field.
type grantsFieldWatch struct {
	entity.Store
	rec     entity.Ref
	armed   bool
	missing bool
}

func (g *grantsFieldWatch) Write(w entity.Write) (bool, int64, error) {
	ok, rev, err := g.Store.Write(w)
	if g.armed && w.At.Ref == g.rec {
		if e, stands, _ := g.Store.Get(g.rec); stands && entity.Get(e.Payload, entity.GrantsField) == nil {
			g.missing = true
		}
	}
	return ok, rev, err
}

// A LEVEL ADDED SINCE THE READ IS A CHANGE. The grants list is rewritten by position, under a
// precondition that the record is what was read — and a match is of what the pattern names, so
// a level somebody added to a line in between was not in it and the write went through, carrying
// that level onto whichever line stood at its position afterwards.
func TestAGrantsRewriteIsRefusedOverARecordThatGainedALevel(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	ref := entity.RefAt("team")
	if err := entity.Claim(store, ref, "user:x@example.com"); err != nil {
		t.Fatal(err)
	}
	rec := entity.PermsRef(ref)
	read, _, err := store.Get(rec)
	if err != nil {
		t.Fatal(err)
	}
	// Somebody else's write lands: bob gains read-write.
	if err := entity.AddGrant(store, ref, entity.Grant{At: ref.Segs(), To: map[entity.Level][]string{entity.ReadWrite: {"user:bob@example.com"}}}); err != nil {
		t.Fatal(err)
	}
	committed, _, err := store.Write(entity.Write{
		At: rec.At(), Value: entity.Obj{entity.GrantsField: entity.GrantsOver(entity.SegsOf(ref), nil, read.Payload)}.Payload(),
		When: []entity.Require{{Pattern: entity.Exactly(read.Payload)}}, Principal: entity.SystemPerms,
	})
	if err != nil || committed {
		t.Errorf("a rewrite from a stale read landed (committed=%v err=%v)", committed, err)
	}
	if ok, _ := entity.Holds(store, "user:bob@example.com", entity.ReadWrite, ref); !ok {
		t.Error("bob's grant was lost to a write that had not read it")
	}
	// …and over the record as it stands, it lands.
	now, _, _ := store.Get(rec)
	if committed, _, err := store.Write(entity.Write{
		At: rec.At(), Value: entity.Obj{"note": "x"}.Payload(),
		When: []entity.Require{{Pattern: entity.Exactly(now.Payload)}}, Principal: entity.SystemPerms,
	}); err != nil || !committed {
		t.Errorf("a write over the record as read was refused (committed=%v err=%v)", committed, err)
	}
}
