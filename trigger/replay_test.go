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

// A hold that outlasts the live observe buffer drops live events — but none are
// lost: on resume the engine replays from its cursor (Since), so every change is
// still dispatched. This is what makes a long-waiting gate cost a cursor, not a
// growing buffer.
func TestDropRecoveryViaReplay(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	eng.AddTrigger(Trigger{
		Name:      "count",
		Condition: Condition{Subject: Match{"system": "demo"}},
		Action:    Action{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	eng.Pause()

	// Inject well past the live observe buffer (256): live delivery drops, but the
	// changes are committed, so the replay watch still has every one of them.
	const n = 300
	for i := 0; i < n; i++ {
		entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "x", ID: fmt.Sprint(i)}, entity.Obj{}.Payload())
	}

	eng.Resume() // catchUp replays the whole window from the cursor

	// Generous: the replay is a FromCommit watch streaming 300 commits back from the
	// server, which takes seconds — this asserts that none are LOST, not how fast they
	// arrive.
	deadline := time.After(30 * time.Second)
	for eng.Fired() < n {
		select {
		case <-deadline:
			t.Fatalf("after resume, fired = %d, want %d — replay lost changes", eng.Fired(), n)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
