package trigger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/process"
)

// A hold that outlasts the live observe buffer drops live events — but none are
// lost: on resume the engine replays from its cursor (Since), so every change is
// still dispatched. This is what makes a long-waiting gate cost a cursor, not a
// growing buffer.
func TestDropRecoveryViaReplay(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	eng.AddTrigger(Trigger{
		Name:    "count",
		Matches: []entity.Pattern{{System: "demo"}},
		Action:  Action{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	eng.Pause()

	// Inject well past the live observe buffer (256): live delivery drops, but the
	// store retains the changes (histCap 4096) for replay.
	const n = 300
	for i := 0; i < n; i++ {
		store.Put(entity.Ref{System: "demo", Kind: "x", ID: fmt.Sprint(i)}, nil)
	}

	eng.Resume() // catchUp replays the whole window from the cursor

	deadline := time.After(3 * time.Second)
	for eng.Fired() < n {
		select {
		case <-deadline:
			t.Fatalf("after resume, fired = %d, want %d — replay lost changes", eng.Fired(), n)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
