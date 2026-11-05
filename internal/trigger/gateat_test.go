package trigger

import (
	"context"
	"sync"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// captureResolver records the questions a firing asks, so a test can read what was published
// about them rather than only whether they were answered.
type captureResolver struct {
	mu   sync.Mutex
	seen []task.GateQuestion
}

func (c *captureResolver) Resolve(_ context.Context, q task.GateQuestion, _ task.ToolUse) (task.Resolution, error) {
	c.mu.Lock()
	c.seen = append(c.seen, q)
	c.mu.Unlock()
	return task.Resolution{Proceed: false, By: "deny"}, nil
}

// A FIRING'S GATE NAMES ITS CHARTER: `gate` and `origin` are names, and a name is unique
// inside a charter rather than across them, so a docket entry saying `merge-to-main` on
// `train-run` left a reader to work out which package that was.
func TestAFiringGateNamesItsCharter(t *testing.T) {
	store := entitytest.Store(t)
	res := &captureResolver{}
	eng := New(store, res)
	eng.Confine(SubtreeAt("verse-dev", "merge"))
	if err := eng.AddTrigger(Trigger{
		Name:      "train-run",
		Condition: Condition{Subject: mm("verse-dev.merge.*")},
		Gates:     []Gate{{Name: "merge-to-main", Mode: "ask"}},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("verse-dev", "merge", "1"), entity.Obj{"stage": "cut"}.Payload())
	if q := firstQuestion(t, res); q.At != "verse-dev.merge" {
		t.Fatalf("gate question At = %q, want the charter that asked: %+v", q.At, q)
	}
}

// statusOf is an entity's payload as an object, for a test that reads what a firing wrote.
func statusOf(t *testing.T, s entity.Store, ref entity.Ref) entity.Obj {
	t.Helper()
	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		t.Fatalf("reading %s: ok=%v err=%v", ref, ok, err)
	}
	return entity.ToObj(e.Payload)
}
