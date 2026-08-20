package trigger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// Steering outranks observation: a Pause (control) takes effect ahead of a burst of
// observations, so none of them fire while paused — control can't queue behind an
// observation backlog. On Resume the buffered burst dispatches.
func TestControlOutranksObservation(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	// Firing is the only thing this counts, and a rule must still have an action (git-issue
	// b2g9hsnfh12ks5vqg9n0). The pair below LATCHES — it selects what has no `seen` and writes
	// one — so the count stays exactly the number of entities in the burst rather than growing
	// on the rule's own writes.
	eng.AddTrigger(Trigger{
		Name:      "count",
		Condition: Condition{Subject: Match{"system": "demo", "value": map[string]any{"stage": "new"}}},
		Action:    Action{Patch: entity.Obj{"stage": "seen"}.Payload()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	eng.Pause() // synchronous: returns once the loop has applied it

	// A burst of changes arrives while paused; none may fire.
	for i := 0; i < 20; i++ {
		entitytest.Set(t, store, entity.RefAt("demo", "x", fmt.Sprint(i)), entity.Obj{"stage": "new"}.Payload())
	}
	time.Sleep(100 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("fired %d while paused — steering did not outrank observation", f)
	}

	eng.Resume() // buffered burst now dispatches

	deadline := time.After(2 * time.Second)
	for eng.Fired() < 20 {
		select {
		case <-deadline:
			t.Fatalf("after resume, fired = %d, want 20", eng.Fired())
		case <-time.After(5 * time.Millisecond):
		}
	}
}
