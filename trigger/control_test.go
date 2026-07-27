package trigger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/process"
)

// Steering outranks observation: a Pause (control) takes effect ahead of a burst of
// observations, so none of them fire while paused — control can't queue behind an
// observation backlog. On Resume the buffered burst dispatches.
func TestControlOutranksObservation(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	eng.AddTrigger(Trigger{
		Name:      "count",
		Condition: Condition{When: MatchWhen(Match{"system": "demo"})},
		Action:    Action{}, // firing is the only thing we count
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	eng.Pause() // synchronous: returns once the loop has applied it

	// A burst of changes arrives while paused; none may fire.
	for i := 0; i < 20; i++ {
		entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "x", ID: fmt.Sprint(i)}, entity.Obj{}.Payload())
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
