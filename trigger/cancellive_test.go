package trigger

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// endsOnCancel is a task that runs until it is cancelled, and then ends cancelled, as a real one
// does at its next boundary.
type endsOnCancel struct {
	ch   chan task.Frame
	once sync.Once
	done atomic.Bool
}

func (h *endsOnCancel) Subscribe() (<-chan task.Frame, func())           { return h.ch, func() {} }
func (h *endsOnCancel) SubscribeSince(int64) (<-chan task.Frame, func()) { return h.ch, func() {} }
func (h *endsOnCancel) Pause() error                                     { return nil }
func (h *endsOnCancel) Resume() error                                    { return nil }
func (h *endsOnCancel) Steer(string, string) error                       { return nil }
func (h *endsOnCancel) SetVector(*task.Vector, string) error             { return nil }
func (h *endsOnCancel) Result() string                                   { return "" }
func (h *endsOnCancel) Err() string                                      { return "" }
func (h *endsOnCancel) Cancel(string) error {
	h.once.Do(func() {
		h.done.Store(true)
		h.ch <- task.Frame{Kind: task.EvStatus, Text: string(task.Cancelled)}
	})
	return nil
}

func (h *endsOnCancel) cancelled() bool { return h.done.Load() }

// DROPPING AN ENGINE'S LIVE WORK CANCELS IT WITHOUT ITS HOOKS. It is what deleting a scope does
// to the scope's engine (git-issue y51f9ce5h12kr2zvmxn0), and an onCancelled run then would act
// inside the scope being deleted — a patch into a store that is going, or a spawn into it that
// nothing would own. A cancel through one task's handle still runs its hook, which is what makes
// the difference this test's to see; and a task tracked after the drop is cancelled as it arrives.
func TestCancelLiveCancelsWithoutHooks(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	handles := map[string]*endsOnCancel{}
	var mu sync.Mutex
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		h := &endsOnCancel{ch: make(chan task.Frame, 1)}
		mu.Lock()
		handles[spec.Subject] = h
		mu.Unlock()
		return h
	})
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:       entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn:       &launch.Spec{Prompt: "review $subject"},
			OnCancelled: &Action{Patch: entity.Obj{"stage": "hooked"}.Payload()},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	for _, id := range []string{"1", "2"} {
		entitytest.Set(t, store, prRef(id), entity.Obj{"stage": "open"}.Payload())
	}
	handleOf := func(ref entity.Ref) *endsOnCancel {
		mu.Lock()
		defer mu.Unlock()
		return handles[ref.String()]
	}
	eventuallyN(t, "both reviews to be spawned", func() bool { return handleOf(prRef("1")) != nil && handleOf(prRef("2")) != nil })
	eventuallyN(t, "both reviews to be live", func() bool { return len(eng.Flows()) == 2 })

	one := eng.SelectFlow(mustSelect(t, "*", prRef("1").String()))
	if len(one) != 1 {
		t.Fatalf("the selection of demo.pr.1 reached %d tasks, want 1", len(one))
	}
	h, _ := eng.Handle(one[0].ID)
	h.Cancel("by hand")
	waitField(t, store, prRef("1"), "stage", "hooked")

	ids := eng.CancelLive("scope me was deleted")
	if len(ids) != 1 || !handleOf(prRef("2")).cancelled() {
		t.Fatalf("CancelLive cancelled %v, want the one task still live, through its handle", ids)
	}
	time.Sleep(300 * time.Millisecond)
	if e, _, _ := store.Get(prRef("2")); entity.Str(e.Payload, "stage") != "reviewing" {
		t.Fatalf("demo.pr.2 = %+v after CancelLive, want its onCancelled not run", e.Payload)
	}
	if len(eng.Flows()) != 0 {
		t.Fatalf("flows after CancelLive = %v, want none", eng.Flows())
	}

	late := &endsOnCancel{ch: make(chan task.Frame, 1)}
	eng.Adopt(prRef("3").String(), "late", late)
	if !late.cancelled() || len(eng.Flows()) != 0 {
		t.Fatal("a task tracked after CancelLive was left running")
	}
}
