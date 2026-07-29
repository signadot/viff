package trigger

import (
	"context"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// answering is a driver that completes immediately with a final answer — fast enough that
// the bus-driven reapOnDone reliably beats mirror.Reflect to the retire, which is the
// condition the terminal bindings used to be wrong under.
type answering struct{ r string }

func (answering) Next(context.Context, process.StepResult) (process.ToolUse, bool, error) {
	return process.ToolUse{}, false, nil
}
func (d answering) Result() string { return d.r }

func hookResultEngine(t *testing.T, answer string) (*Engine, entity.Store) {
	t.Helper()
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) process.Handle {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		p := process.NewProcess(id, sup, answering{r: answer}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{
			Subject: spec.Subject, Runner: "local", Chain: spec.Chain, Task: spec.Task,
		})
		go p.Run(context.Background())
		return p
	})
	eng.ObserveScoped(t.Context())
	return eng, store
}

// A completion hook binds the TERMINAL payload. reapOnDone and mirror.Reflect are two
// independent readers of the same process bus, so the hook used to see the pre-terminal
// projection instead: $status bound "running" and $result was absent — the payload a
// completion hook exists to read.
func TestHookBindsTerminalStatusAndResult(t *testing.T) {
	eng, store := hookResultEngine(t, "SHIPIT")

	var hookTask string
	done := make(chan struct{})
	eng.SetSpawner(func(id string, spec launch.Spec) process.Handle {
		if spec.Chain == "review.onDone" {
			hookTask = spec.Task
			close(done)
		}
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		p := process.NewProcess(id, sup, answering{r: "SHIPIT"}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{
			Subject: spec.Subject, Runner: "local", Chain: spec.Chain, Task: spec.Task,
		})
		go p.Run(context.Background())
		return p
	})

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Task: "review $subject"},
			OnDone: &Action{
				Spawn: &launch.Spec{Task: "s=$status r=$result f=$flow"},
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("hook never spawned")
	}
	if want := "s=done r=SHIPIT f=demo:pr:1"; hookTask != want {
		t.Errorf("hook task = %q, want %q", hookTask, want)
	}
}

// The loop closes: a process's own output is written onto the subject by the hook's patch,
// where it becomes an ordinary fact — and a second trigger fires on it. Before patch
// expansion a result could only reach the next PROMPT, never the next `when`.
func TestHookResultPatchesSubjectAndDrivesNextTrigger(t *testing.T) {
	eng, store := hookResultEngine(t, "high")

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Task: "review $subject"},
			OnDone: &Action{
				// the process's finding becomes a field on the subject
				Patch: entity.Obj{"stage": "reviewed", "severity": "$result"}.Payload(),
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger review: %v", err)
	}
	// an ordinary trigger matching that finding — no knowledge of processes at all
	if err := eng.AddTrigger(Trigger{
		Name: "escalate",
		Condition: Condition{Subject: Match{
			"system": "demo", "kind": "pr",
			"value": map[string]any{"stage": "reviewed", "severity": "high"},
		}},
		Action: Action{Patch: entity.Obj{"escalated": true}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger escalate: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "severity", "high")
	waitBool(t, store, prRef("1"), "escalated", true)
}

// A patch naming something the payload does not carry fails the firing rather than writing a
// partial state: an absent key and a null one are different facts in the store, so dropping
// the reference would launder "the process never said" into "the process said nothing".
func TestHookUnresolvedReferenceFailsTheFiring(t *testing.T) {
	eng, store := hookResultEngine(t, "SHIPIT")

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Task: "review $subject"},
			OnDone: &Action{
				Patch: entity.Obj{"stage": "reviewed", "sev": "$nosuchfield"}.Payload(),
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	// the spawn happened, so the subject reached "reviewing"; the hook's patch must not land
	waitField(t, store, prRef("1"), "stage", "reviewing")
	e, _, _ := store.Get(prRef("1"))
	if entity.Get(e.Payload, "sev") != nil {
		t.Errorf("an unresolved reference was written: %v", entity.ToObj(e.Payload))
	}
	if s := entity.Str(e.Payload, "stage"); s != "reviewing" {
		t.Errorf("stage = %q: the failed hook patch must not partially apply", s)
	}
}

func waitBool(t *testing.T, store entity.Store, ref entity.Ref, key string, want bool) {
	t.Helper()
	for {
		if e, ok, _ := store.Get(ref); ok {
			if b := entity.Get(e.Payload, key); b != nil && b.Bool == want {
				return
			}
		}
		select {
		case <-t.Context().Done():
			e, _, _ := store.Get(ref)
			t.Fatalf("%s.%s never became %v; payload=%v", ref, key, want, entity.ToObj(e.Payload))
		default:
		}
	}
}
