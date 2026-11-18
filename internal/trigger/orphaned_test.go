package trigger

import (
	"context"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/task"
)

// AN ORPHANED TASK IS A FAILED ONE THAT SAYS SO. A task that ended because verse and its runner
// lost each other reaches `onFailed` as any failure does, and the hook reads `.[orphaned]` to
// tell which kind it was: true for that end, and false, not absent, for a task that failed for
// a reason of its own.
func TestOnFailedReadsWhetherTheTaskWasOrphaned(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	sp := &spawned{by: map[string]*heldHandle{}, store: store}
	eng.SetSpawner(sp.spawn)
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:    entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn:    &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review"},
			OnFailed: &Action{Patch: entity.Obj{"stage": "failed", "lost": ".[orphaned]", "why": ".[err]"}.Payload()},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	for id, end := range map[string]task.Event{
		"1": {Kind: task.EventStatus, Text: string(task.Failed), Err: "the runner has no such task", Orphaned: true},
		"2": {Kind: task.EventStatus, Text: string(task.Failed), Err: "it could not"},
	} {
		entitytest.Set(t, store, prRef(id), entity.Obj{"stage": "open"}.Payload())
		sp.wait(t, "review@demo.pr."+id).ch <- end
		waitStage(t, store, prRef(id), "failed")
		ent, _, err := store.Get(prRef(id))
		if err != nil {
			t.Fatal(err)
		}
		got := entity.ToObj(ent.Payload)
		if lost, ok := got["lost"].(bool); !ok || lost != end.Orphaned || got["why"] != end.Err {
			t.Fatalf("demo.pr.%s after onFailed = %v, want lost: %v and why: %q", id, got, end.Orphaned, end.Err)
		}
	}
}
