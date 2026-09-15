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

// A hold that outlasts the live observe buffer drops live commits — but none are LOST: on
// resume the engine replays from its cursor (Since), so every one is still dispatched.
//
// The property is about the CURSOR, and asserting it there is what makes this test robust
// against how fast the store is. Asserting it through firings does not work at this size and
// the reason is worth knowing: evaluating a rule at a commit costs one read, a read costs the
// log replay to that commit — ~73ms at 300 commits, whatever path it asks for — and 300 of
// those is half a minute before any rule does anything. So this half uses a trigger that reads
// a slice the burst never touches, which the commit skip declines to evaluate at all; the
// firing half is TestReplayedCommitsFireTriggers below, at a size where the reads are free.
func TestDropRecoveryViaReplay(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	// Reads `audit`, and the burst writes `demo` — so no commit in the burst is evaluated and
	// the walk is not paying a read per commit. What is under test is that the walk HAPPENS.
	eng.AddTrigger(Trigger{
		Name:      "elsewhere",
		Condition: Condition{Subject: mm("audit.record.1")},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	eng.Pause()

	// Well past the live observe buffer (256): live delivery drops, but the changes are
	// committed, so the replay watch still has every one of them.
	const n = 300
	for i := 0; i < n; i++ {
		entitytest.Set(t, store, entity.RefAt("demo", "x", fmt.Sprint(i)),
			entity.Obj{"stage": "new"}.Payload())
	}
	last := store.Rev()

	eng.Resume() // catchUp replays the whole window from the cursor

	// The cursor advances commit by commit as catchUp dispatches each, so reaching the last
	// revision IS "every commit was dispatched". Read it on the control plane, which runs on
	// the loop goroutine — the cursor belongs to that goroutine and racing it would be reading
	// a different question.
	deadline := time.After(30 * time.Second)
	for {
		var cur int64
		eng.Do(func() { cur = eng.cursor })
		if cur >= last {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("cursor reached %d of %d — the replay skipped commits", cur, last)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// ...and a replayed commit really does fire a trigger, rather than merely advancing a cursor
// past it. Small on purpose: every evaluated commit costs a read, so this asserts the
// behaviour where it is cheap and leaves the volume to the test above.
func TestReplayedCommitsFireTriggers(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.AddTrigger(Trigger{
		Name: "advance",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "x", "*"),
			"value": map[string]any{"stage": "new"}}},
		Action: Action{Patch: entity.Obj{"stage": "seen"}.Payload()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	eng.Pause() // nothing is dispatched live; these are recovered by the replay alone
	const n = 5
	for i := range n {
		entitytest.Set(t, store, entity.RefAt("demo", "x", fmt.Sprint(i)),
			entity.Obj{"stage": "new"}.Payload())
	}
	eng.Resume()

	deadline := time.After(30 * time.Second)
	for {
		seen := 0
		for _, e := range store.List(func(r entity.Ref) bool { return r.ChildOf("demo", "x") }) {
			if entity.Str(e.Payload, "stage") == "seen" {
				seen++
			}
		}
		if seen == n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%d of %d entities advanced — a replayed commit fired nothing", seen, n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
