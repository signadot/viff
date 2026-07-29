package trigger

import (
	"testing"

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
