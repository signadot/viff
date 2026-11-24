package trigger

import (
	"context"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/task"
)

// A TASK ENDS WHEN ITS RECORD SAYS SO, AND ONLY THEN. A task's event stream closing with no end
// on it is a connection that dropped, which says nothing about the task: it stays live, its rule
// goes on holding the subject, and no hook runs. Its record reaching an end is what retires it,
// runs its hook and lets go of the subject.
func TestATaskEndsOnItsRecordAndNotOnItsStream(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	sp := &spawned{by: map[string]*heldHandle{}}
	eng.SetSpawner(sp.spawn)
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:  entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "viff"}, Prompt: "review"},
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	const id = "review@demo.pr.1"
	h := sp.wait(t, id)
	eventuallyN(t, "the review to be live", func() bool { return len(eng.Flows()) == 1 })
	claimed := func() bool {
		_, ok, err := store.Get(mirror.ClaimRef(id))
		if err != nil {
			t.Fatalf("reading the claim: %v", err)
		}
		return ok
	}
	if !claimed() {
		t.Fatal("the review's rule holds no claim on its subject while its task runs")
	}

	close(h.ch)
	time.Sleep(300 * time.Millisecond)
	if n := len(eng.Flows()); n != 1 {
		t.Fatalf("%d tasks live after the stream dropped, want the task still live", n)
	}
	if !claimed() {
		t.Fatal("the subject was let go when the stream dropped")
	}
	stays(t, store, prRef("1"), "stage", "reviewing")

	entitytest.Set(t, store, mirror.Ref(id), entity.Obj{"status": string(task.Done)}.Payload())
	waitStage(t, store, prRef("1"), "reviewed")
	eventuallyN(t, "the task to be retired and its claim released", func() bool { return len(eng.Flows()) == 0 && !claimed() })
}
