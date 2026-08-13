package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// The spawn carries WHICH RUN it is: the commit that woke the firing.
//
// A spawn id is derived from the chain and the subject, so a re-fire about one subject reuses
// it — deliberately, since that is what makes a re-fire idempotent and what `verse flow
// demo:pr:1` addresses. Nothing named the occurrence, so two runs wrote one record and their
// fields interleaved: staging held a task record whose `ended` preceded its `started` by fifty
// minutes, carrying one run's result beside another's status (git-issue gjv063rrh12ksvgnfsn0).
//
// The engine has the causing commit in hand and stamps it, the way it already stamps Subject
// and Chain — engine-owned fields that ride the spec so the spawner can put them on the
// record. Asserted HERE, on what the engine hands the spawner, because that is where the fact
// is known; that it reaches the entity is versed's test.
func TestASpawnCarriesTheCommitThatCausedIt(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		specs <- spec
		return nil
	})

	pr := entity.Ref{System: "demo", Kind: "pr", ID: "7"}
	entitytest.Set(t, store, pr, entity.Obj{"stage": "reviewing"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name: "review",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "ci", "kind": "status"}),
			Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "reviewing"}},
		},
		Action: Action{
			Spawn:  &launch.Spec{Prompt: "review $subject"},
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.Ref{System: "ci", Kind: "status", ID: "build"},
		entity.Obj{"state": "green"}.Payload())

	spec := waitSpec(t, specs)
	if spec.Occurrence == 0 {
		t.Fatal("the spawn names no occurrence: a second run of this id would overwrite the " +
			"first with nothing to tell them apart")
	}
	// It is the commit that CAUSED it, not merely some revision — so it is at most the
	// revision the store stood at when the firing was made, and above the one before it.
	if rev := store.Rev(); spec.Occurrence > rev {
		t.Fatalf("occurrence %d is above the store's own revision %d", spec.Occurrence, rev)
	}

	// A second firing about the same subject: same id, later commit.
	entitytest.Set(t, store, entity.Ref{System: "ci", Kind: "status", ID: "build"},
		entity.Obj{"state": "green", "again": 1}.Payload())
	next := waitSpec(t, specs)
	if next.Occurrence <= spec.Occurrence {
		t.Fatalf("the second firing's occurrence is %d, not later than the first's %d",
			next.Occurrence, spec.Occurrence)
	}
}

func waitSpec(t *testing.T, specs chan launch.Spec) launch.Spec {
	t.Helper()
	select {
	case spec := <-specs:
		return spec
	case <-time.After(3 * time.Second):
		t.Fatal("no spawn")
	}
	return launch.Spec{}
}
