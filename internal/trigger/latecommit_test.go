package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// loadCharter is a train loading what its proposals say: its subject is the train, and the
// proposals are what its condition reads. merge.tony's train-load has this shape.
const loadCharter = `
- name: load
  condition:
    subject:
      path: "train.*"
      value: !let
        let: [{told: !list-paths {at: "prop.*", path: says}}]
        in: !not {offered: .[told]}
  action:
    patch:
      offered: !list-paths {at: "prop.*", path: says}
`

// A COMMIT A WATCH DELIVERS LATE STILL WAKES THE RULE. A scoped engine watches each subtree a
// rule reads on its own, and nothing orders one watch's deliveries against another's. Here the
// rule's subject is written (Y) while a firing about it is in flight, which drops it; the commit
// before it (W), which moved what the condition reads, then arrives on the other watch. W is
// the only news the rule gets that a proposal now says something, so it must not be taken for a
// replay of Y.
func TestACommitDeliveredLateStillWakesTheRule(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	train := entity.RefAt("train", "t1")
	prop := entity.RefAt("prop", "a")
	setPayload(t, store, train, `{offered: []}`)
	setPayload(t, store, prop, `{branch: a}`)
	if err := eng.AddTrigger(mustParseCharter(t, loadCharter)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	rule, _ := eng.triggerNamed("load")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	commits, unsub := store.Observe()
	defer unsub()
	next := func() entity.Commit {
		t.Helper()
		select {
		case c := <-commits:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("no commit")
			return entity.Commit{}
		}
	}

	if _, ok := eng.claimFlight("load", train); !ok {
		t.Fatal("the flight was not free")
	}
	setPayload(t, store, prop, `{branch: a, says: x}`) // W: what the condition reads
	w := next()
	setPayload(t, store, train, `{offered: [], touched: 1}`) // Y: the subject, during the flight
	y := next()

	eng.evaluate(ctx, rule, y)
	time.Sleep(100 * time.Millisecond) // its firing reaches the held flight and is dropped
	eng.releaseFlight("load", train)
	eng.evaluate(ctx, rule, w)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := field(t, store, train, "offered").([]any); len(got) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the train never loaded what the proposal says: %v", field(t, store, train, "offered"))
}

// markCharter latches: it marks a train once a proposal says something it has not offered, and
// a marked train is not selected again.
const markCharter = `
- name: mark
  condition:
    subject:
      path: "train.*"
      value: !and
      - !not.has-path marked
      - !let
        let: [{told: !list-paths {at: "prop.*", path: says}}]
        in: !not {offered: .[told]}
  action:
    patch: {marked: fired}
`

// …AND IS ANSWERED AS OF NOW. The late commit's snapshot predates a latch written since; a
// subject the rule no longer selects is not fired on again from it.
func TestALateCommitDoesNotFireWhatNoLongerSelects(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	train := entity.RefAt("train", "t1")
	prop := entity.RefAt("prop", "a")
	setPayload(t, store, train, `{offered: []}`)
	setPayload(t, store, prop, `{branch: a}`)
	if err := eng.AddTrigger(mustParseCharter(t, markCharter)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	rule, _ := eng.triggerNamed("mark")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	commits, unsub := store.Observe()
	defer unsub()
	setPayload(t, store, prop, `{branch: a, says: x}`) // W
	w := <-commits
	setPayload(t, store, train, `{offered: [], marked: by-hand}`) // Y: the latch, written since
	y := <-commits

	eng.evaluate(ctx, rule, y)
	eng.evaluate(ctx, rule, w)
	time.Sleep(200 * time.Millisecond)
	if got := field(t, store, train, "marked"); got != "by-hand" {
		t.Fatalf("a late commit fired on a subject the rule no longer selects: marked = %v", got)
	}
}
