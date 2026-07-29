package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/process"
)

// The latch check, over the shapes a charter actually comes in. It is advisory today (see
// AddTrigger) — what is pinned here is WHICH shapes it names, since that is the judgement.
func TestLatchCheck(t *testing.T) {
	pr := func(pred map[string]any) Match {
		m := Match{"system": "demo", "kind": "pr"}
		if pred != nil {
			m["value"] = pred
		}
		return m
	}
	spawn := &launch.Spec{Task: "review $subject"}

	ok := map[string]Trigger{
		// The canonical shape: spawn the work, let its completion advance the subject past
		// the state that asked for it.
		"spawn with an onDone that advances the subject": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action:    Action{Spawn: spawn, OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()}},
		},
		"spawn with a patch beside it": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action:    Action{Spawn: spawn, Patch: entity.Obj{"stage": "reviewing"}.Payload()},
		},
		"a sequence whose last element latches": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action: Action{Seq: []Action{
				{Spawn: spawn},
				{Patch: entity.Obj{"stage": "done"}.Payload()},
			}},
		},
		// Nothing to diverge: a write is inert on repetition.
		"no spawn at all": {
			Condition: Condition{Subject: pr(nil)},
			Action:    Action{Patch: entity.Obj{"stage": "seen"}.Payload()},
		},
	}
	for name, tr := range ok {
		t.Run("ok/"+name, func(t *testing.T) {
			if err := checkLatch(tr.Condition, tr.Action); err != nil {
				t.Fatalf("flagged a converging charter: %v", err)
			}
		})
	}

	bad := map[string]Trigger{
		"spawn with nothing written at all": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action:    Action{Spawn: spawn},
		},
		"spawn writing a field the subject does not read": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action:    Action{Spawn: spawn, OnDone: &Action{Patch: entity.Obj{"note": "x"}.Payload()}},
		},
		"spawn on a subject with no predicate to latch": {
			Condition: Condition{Subject: pr(nil)},
			Action:    Action{Spawn: spawn, OnDone: &Action{Patch: entity.Obj{"stage": "done"}.Payload()}},
		},
	}
	for name, tr := range bad {
		t.Run("bad/"+name, func(t *testing.T) {
			err := checkLatch(tr.Condition, tr.Action)
			if err == nil {
				t.Fatal("a charter that re-spawns forever was not flagged")
			}
			t.Log(err)
		})
	}
}

// `verse connect`'s validator is the charter this check was written for: it spawned per file
// with nothing to stop it. Its replacement latches on the content hash the dir-status entity
// already carries, so a file is validated once per version rather than once per commit.
func TestConnectValidatorShapeLatches(t *testing.T) {
	cond := Condition{Subject: Match{
		"system": "local", "kind": "dir-status-abc",
		"value": map[string]any{"validated": map[string]any{"not": nil}},
	}}
	act := Action{
		Spawn:  &launch.Spec{Task: `Read the file at path "$path" and report any problems.`},
		OnDone: &Action{Patch: entity.Obj{"validated": "$hash"}.Payload()},
	}
	if err := checkLatch(cond, act); err != nil {
		t.Fatalf("the connect validator still re-spawns: %v", err)
	}
	// And it installs.
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	tr := Trigger{Name: "validate-abc", Condition: cond, Action: act}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
}

// A firing whose work is already running does not ask again. The subject stays selected for as
// long as the process runs — the write that would release it is what the process has not
// finished doing — so every commit meanwhile re-selects it.
//
// spawnHooked has always refused the second process; what this pins is that the refusal now
// happens BEFORE the gates, so a firing for work already under way does not put a question on
// the docket or spend budget on its way to being told no.
func TestAFiringDoesNotAskAgainWhileItsWorkRuns(t *testing.T) {
	store := entitytest.Store(t)
	reg := process.NewRegistry()
	eng := New(store, reg)
	eng.SetSpawner(func(id string, spec launch.Spec) process.Handle {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(id, sup, blockForever{}, noExec{})
	})

	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	if err := eng.AddTrigger(Trigger{
		Name: "review",
		// Latches through its onDone — but the process blocks forever, so the latch never
		// lands and the subject stays selected the whole time.
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr",
			"value": map[string]any{"stage": "open"}}},
		Gates: []Gate{{Name: "go", Mode: "ask"}},
		Action: Action{
			Spawn:  &launch.Spec{Task: "review $subject"},
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())

	// The first firing asks; approve it so the process starts.
	var id string
	deadline := time.After(3 * time.Second)
	for id == "" {
		if p := reg.Pending(); len(p) == 1 {
			id = p[0].Question.ID
			break
		}
		select {
		case <-deadline:
			t.Fatal("no gate question appeared")
		case <-time.After(10 * time.Millisecond):
		}
	}
	reg.Answer(id, process.Resolution{Proceed: true, By: "alice"})
	waitFlow(t, eng, ref, 1)

	fired := eng.Fired()
	// Now stir the subtree while the process runs. The subject still matches — the latch is
	// waiting on a completion that never comes — so every one of these re-selects it.
	for i := range 4 {
		entitytest.Set(t, store, entity.Ref{System: "demo", Kind: "pr", ID: "other"},
			entity.Obj{"stage": "closed", "n": i}.Payload())
	}
	time.Sleep(500 * time.Millisecond)

	if p := reg.Pending(); len(p) != 0 {
		t.Fatalf("asked again for work already running: %+v", p)
	}
	if now := eng.Fired(); now != fired {
		t.Fatalf("budget spent %d more times on work already running", now-fired)
	}
	if n := len(eng.matchFlow(mustExpr(t, "demo:pr:1"))); n != 1 {
		t.Fatalf("%d processes for one subject, want 1", n)
	}
}

// waitFlow blocks until the subject has n live processes.
func waitFlow(t *testing.T, eng *Engine, ref entity.Ref, n int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if len(eng.matchFlow(Match{"system": ref.System, "kind": ref.Kind, "id": ref.ID})) == n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d live process(es) about %s", n, ref)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
