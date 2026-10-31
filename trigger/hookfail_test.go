package trigger

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/signadot/verse/systems"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// structured completes with a tony object as its final answer, so the mirror projects `data`
// beside `result` and a hook can deref into it.
type structured struct{ r string }

func (structured) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	return task.ToolUse{}, false, nil
}
func (d structured) Result() string { return d.r }

// erroring fails on its first turn, the way the fail tool makes an llm driver fail.
type erroring struct{ msg string }

func (d erroring) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	return task.ToolUse{}, false, errors.New(d.msg)
}

func structuredEngine(t *testing.T, d task.Driver) (*Engine, entity.Store) {
	t.Helper()
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		p := task.NewTask(id, sup, d, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{
			Subject: spec.Subject, Runner: "local", Chain: spec.Chain, Prompt: spec.Prompt,
		})
		go p.Run(context.Background())
		return p
	})
	eng.ObserveScoped(t.Context())
	return eng, store
}

// A structured answer reaches the subject through a deref: the agent's own finding becomes an
// ordinary field, at the depth the charter names.
func TestDerefIntoTaskData(t *testing.T) {
	eng, store := structuredEngine(t, structured{r: `{severity: high, findings: [{id: "f1"}]}`})

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $subject"},
			OnDone: &Action{Patch: entity.Obj{
				"stage":    "reviewed",
				"severity": "$data.severity",
				"first":    "$data.findings[0].id",
			}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "severity", "high")
	waitField(t, store, prRef("1"), "first", "f1")
}

// The agent that reports failure in prose is the case that motivated all of this. Its answer
// is not structure, so `data` is absent, so a hook naming $data.severity fails — the subject
// does not advance on an unverified claim, without anything having to detect the lie.
func TestProseAnswerCannotSatisfyADeref(t *testing.T) {
	eng, store := structuredEngine(t, structured{r: "Error could not open file"})

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:  entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $subject"},
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed", "severity": "$data.severity"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	// the failure is persisted against the task whose completion fired the hook
	pr := mirror.Ref("review@demo.pr.1")
	waitPath(t, store, pr, "hook.status", "failed")

	e, _, _ := store.Get(pr)
	if got, _ := e.Payload.GetKPath("hook.chain"); got == nil || got.String != "review.onDone" {
		t.Errorf("hook.chain = %v, want review.onDone", got)
	}
	if got, _ := e.Payload.GetKPath("hook.err"); got == nil || got.String == "" {
		t.Error("hook.err must say which reference did not resolve")
	}
	// the task's own text survives beside it, and status is still the mirror's
	if r := entity.Str(e.Payload, "result"); r != "Error could not open file" {
		t.Errorf("result = %q, want the agent's text", r)
	}
	if s := entity.Str(e.Payload, "status"); s != "done" {
		t.Errorf("status = %q: the engine must not overwrite the mirror's field", s)
	}
	// and the subject did NOT advance
	pe, _, _ := store.Get(prRef("1"))
	if s := entity.Str(pe.Payload, "stage"); s != "reviewing" {
		t.Errorf("stage = %q, want reviewing — the hook must not have advanced it", s)
	}
}

// A driver error reaches the durable entity as `err`. status says only THAT a task ended.
func TestDriverErrorIsProjectedAsErr(t *testing.T) {
	eng, store := structuredEngine(t, erroring{msg: "driver error: no credentials"})

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $subject"},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	pr := mirror.Ref("review@demo.pr.1")
	waitPath(t, store, pr, "err", "driver error: no credentials")

	e, _, _ := store.Get(pr)
	if s := entity.Str(e.Payload, "status"); s != "failed" {
		t.Errorf("status = %q, want failed", s)
	}
}

// waitPath polls until the kpath on ref reads want.
func waitPath(t *testing.T, store entity.Store, ref entity.Ref, kp, want string) {
	t.Helper()
	for {
		if e, ok, _ := store.Get(ref); ok {
			if n, err := e.Payload.GetKPath(kp); err == nil && n != nil && n.String == want {
				return
			}
		}
		select {
		case <-t.Context().Done():
			e, _, _ := store.Get(ref)
			t.Fatalf("%s %s never became %q; payload=%v", ref, kp, want, entity.ToObj(e.Payload))
		default:
		}
	}
}

// The observed failure, three firings out of three: an agent that has read nine files
// narrates and fences before it answers. The answer inside the fence is the answer, so the
// deref resolves and the subject advances — and it is the same document a spawn's `answer`
// contract validates, because both go through answer.Extract.
func TestDerefReadsAFencedAnswer(t *testing.T) {
	eng, store := structuredEngine(t, structured{r: "I read the handler and both callers.\n\n" +
		"```tony\n{severity: high, findings: [{id: \"f1\"}]}\n```\n\nHappy to write the patch."})

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $subject"},
			OnDone: &Action{Patch: entity.Obj{
				"stage":    "reviewed",
				"severity": "$data.severity",
				"first":    "$data.findings[0].id",
			}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "severity", "high")
	waitField(t, store, prRef("1"), "first", "f1")

	// the narration is still there verbatim: data and result are two faces of one value
	e, _, _ := store.Get(mirror.Ref("review@demo.pr.1"))
	if r := entity.Str(e.Payload, "result"); !strings.Contains(r, "nine files") && !strings.Contains(r, "I read the handler") {
		t.Errorf("result = %q, want the agent's full text", r)
	}
}
