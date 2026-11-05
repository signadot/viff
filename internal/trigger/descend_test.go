package trigger

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// A SUBJECT MAY DESCEND. `verse-dev..finding` selects every finding beneath verse-dev, at
// whatever depth the rule that derived it put one — which is the question a reader of derived
// entities has and could not ask while a subject selected at one depth.
func TestASubjectThatDescendsSelectsEveryDepth(t *testing.T) {
	store := entitytest.Store(t)
	for _, c := range []struct {
		at   entity.Ref
		body entity.Obj
	}{
		{entity.RefAt("verse-dev", "finding"), entity.Obj{"stage": "open"}},
		{entity.RefAt("verse-dev", "drift", "finding"), entity.Obj{"stage": "open"}},
		{entity.RefAt("verse-dev", "drift", "head", "abc", "finding"), entity.Obj{"stage": "open"}},
		{entity.RefAt("verse-dev", "drift", "head", "abc", "other"), entity.Obj{"stage": "open"}},
		{entity.RefAt("elsewhere", "finding"), entity.Obj{"stage": "open"}},
	} {
		entitytest.Set(t, store, c.at, c.body.Payload())
	}

	got := mm("verse-dev..finding").Select(store)
	var names []string
	for _, r := range got {
		names = append(names, r.String())
	}
	sort.Strings(names)
	want := []string{"verse-dev.drift.finding", "verse-dev.drift.head.abc.finding", "verse-dev.finding"}
	if len(names) != len(want) {
		t.Fatalf("selected %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("selected %v, want %v", names, want)
		}
	}
	// A descent names a SET, so it is not one entity named: a rule with this subject selects
	// rather than addressing.
	if _, names := mm("verse-dev..finding").Names(); names {
		t.Error("a descending subject reads as naming one entity")
	}
}

// …AND A RULE OVER ONE FIRES ON WHAT IT SELECTS, at whatever depth the write lands — which is
// the watch being opened with the pattern rather than with a depth.
func TestARuleWithADescendingSubjectFires(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "seen",
		Condition: Condition{Subject: Match{"path": mm("verse-dev..finding").SegNodes(), "value": map[string]any{"stage": "open"}}},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	deep := entity.RefAt("verse-dev", "drift", "head", "abc", "finding")
	entitytest.Set(t, store, deep, entity.Obj{"stage": "open"}.Payload())
	shallow := entity.RefAt("verse-dev", "finding")
	entitytest.Set(t, store, shallow, entity.Obj{"stage": "open"}.Payload())

	for _, ref := range []entity.Ref{deep, shallow} {
		deadline := time.After(5 * time.Second)
		for {
			e, _, _ := store.Get(ref)
			if entity.Get(e.Payload, "seen") != nil {
				break
			}
			select {
			case <-deadline:
				t.Fatalf("the rule never fired on %s: %v", ref, e.Payload)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
}
