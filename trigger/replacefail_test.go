package trigger

import (
	"errors"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// failsAt refuses the next n writes at one address, as a store under load refuses a write now
// and then.
type failsAt struct {
	entity.Store
	at entity.Ref
	n  int
}

func (f *failsAt) Write(w entity.Write) (bool, int64, error) {
	if f.n > 0 && w.At.Ref == f.at && w.Value != nil {
		f.n--
		return false, 0, errors.New("storage_error: the store refused the write")
	}
	return f.Store.Write(w)
}

// A REPLACEMENT THAT CANNOT BE WRITTEN LEAVES THE RULE THAT STOOD, record and all. A fresh
// install that cannot be made durable is taken out whole; run on a replacement, that cleanup
// deletes the standing rule and its owner record, and what is put back is then nobody's — a rule
// that acts as nobody, under a name that is anybody's, because a store refused one write.
func TestAReplacementThatCannotBeWrittenLeavesTheRuleAndItsOwner(t *testing.T) {
	alice, mallory := "user:alice@example.com", "user:mallory@example.com"
	rule := CharterRefAt("", "review")
	inner := &failsAt{Store: entitytest.OpenStore(t), at: rule}
	store := entity.As(inner, entity.SystemDefault)
	// Anybody installs a rule, as the seed says.
	if err := entity.SetGrants(store, entity.VerseRef(CharterSystem, CharterKind), []entity.Grant{{
		At: entity.VerseRef(CharterSystem, CharterKind).Segs(), Below: true,
		To: map[entity.Level][]string{entity.OwnerLevel: {entity.Everyone}},
	}}); err != nil {
		t.Fatal(err)
	}
	eng := New(store, task.NewRegistry())
	if err := eng.InstallTriggerAs(alice, reviewRule("review", entity.Obj{"stage": "reviewed"})); err != nil {
		t.Fatal(err)
	}
	owner := func() string {
		rec, ok, err := store.Get(entity.PermsRef(rule))
		if err != nil || !ok {
			return ""
		}
		return entity.Str(rec.Payload, entity.OwnerField)
	}
	if owner() != alice {
		t.Fatalf("the fixture is wrong: the rule is %q's", owner())
	}
	// In this order: after the second the rule is out of the engine and its record still
	// stands, and the third is an install over a record with no rule behind it — which, taken
	// for a fresh one, would clean up its own failure by removing what stands.
	for _, c := range []struct {
		name     string
		failures int
	}{
		{"the replacement alone", 1},
		{"the replacement and the restore", 2},
		{"an install over a record the engine no longer holds", 1},
	} {
		name, failures := c.name, c.failures
		inner.n = failures
		if err := eng.InstallTriggerAs(alice, reviewRule("review", entity.Obj{"stage": "approved"})); err == nil {
			t.Fatalf("%s: the edit landed through a store that refused it", name)
		}
		inner.n = 0
		if _, ok, _ := store.Get(rule); !ok {
			t.Errorf("%s: the rule is gone from the store", name)
		}
		if got := owner(); got != alice {
			t.Errorf("%s: the rule is %q's, want alice's still", name, got)
		}
		if ok, _ := entity.MayCreate(store, mallory, rule); ok {
			t.Errorf("%s: a stranger holds owner on alice's rule", name)
		}
		if ok, _ := entity.MayCreate(store, entity.Via(rule), entity.RefAt("alices", "place")); !ok {
			// alice may create in the open world; her rule acts as her.
			t.Errorf("%s: the rule no longer acts as alice", name)
		}
	}
	// With one failure the old rule is back in the engine; with two it is not, and the record
	// that stands is what a restart restores.
	e, _, _ := store.Get(rule)
	if spec := entity.Get(e.Payload, "spec"); entity.Str(entity.Get(entity.Get(spec, "action"), "patch"), "stage") != "reviewed" {
		t.Errorf("the record that stands is not the rule that stood: %v", spec)
	}
}
