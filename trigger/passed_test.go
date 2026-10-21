package trigger

import (
	"context"
	"github.com/signadot/verse/systems"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// settling answers every question the same way: as By says, which is a person's ballot, the
// clock, or nobody in particular.
type settling struct{ res task.Resolution }

func (s settling) Resolve(_ context.Context, q task.GateQuestion, _ task.ToolUse) (task.Resolution, error) {
	res := s.res
	res.Question = q
	return res, nil
}

// A rule's own spawn carries the gates its firing passed, in the rule's order, as the engine heard
// them settle: a gate that asks with its question and whether a ballot or the clock settled it,
// and a gate written `auto` as that. A completion hook's spawn, which starts after the answer was
// given, carries none.
func TestASpawnCarriesTheGatesItsFiringPassed(t *testing.T) {
	const ana = "user:ana@example.com"
	for _, c := range []struct {
		name string
		res  task.Resolution
		want launch.Passed
	}{
		{"a ballot", task.Resolution{Choice: task.Proceed, Proceed: true, By: ana, Authenticated: "corp"},
			launch.Passed{How: launch.PassedBallots, Choice: task.Proceed, By: ana, Authenticated: "corp"}},
		{"a ballot through a resolver", task.Resolution{Choice: task.Proceed, Proceed: true, By: ana, Via: "via:verse.resolvers.helper"},
			launch.Passed{How: launch.PassedBallots, Choice: task.Proceed, By: ana, Via: "via:verse.resolvers.helper"}},
		{"the clock", task.Resolution{Choice: task.Proceed, Proceed: true, By: task.ByDeadline},
			launch.Passed{How: launch.PassedDeadline, Choice: task.Proceed}},
		{"nobody in particular", task.Resolution{Choice: task.Proceed, Proceed: true, By: task.ByAuto},
			launch.Passed{Choice: task.Proceed}},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := entitytest.Store(t)
			eng := New(store, settling{c.res})
			specs := make(chan launch.Spec, 4)
			eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
				specs <- spec
				sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
				p := task.NewTask(id, sup, answering{r: "done"}, noExec{})
				go p.Run(context.Background())
				return p
			})
			eng.ObserveScoped(t.Context())
			if err := eng.AddTrigger(Trigger{
				Name:      "deploy",
				Condition: Condition{Subject: prSubject("open")},
				Gates:     []Gate{{Name: "free", Mode: "auto"}, {Name: "ok", Mode: "ask"}},
				Action: Action{
					Patch:  entity.Obj{"stage": "deploying"}.Payload(),
					Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "deploy $subject"},
					OnDone: &Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "after $subject"}},
				},
			}); err != nil {
				t.Fatalf("AddTrigger: %v", err)
			}
			entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

			next := func(what string) launch.Spec {
				t.Helper()
				select {
				case spec := <-specs:
					return spec
				case <-time.After(10 * time.Second):
					t.Fatalf("%s never spawned", what)
					return launch.Spec{}
				}
			}
			own := next("the rule's own spawn")
			want := c.want
			want.Gate, want.Question = "ok", gateID("", "deploy", "ok", []entity.Ref{prRef("1")})
			free := launch.Passed{Gate: "free", How: launch.PassedAuto}
			if len(own.Passed) != 2 || own.Passed[0] != free || own.Passed[1] != want {
				t.Fatalf("the rule's own spawn passed %+v, want %+v then %+v", own.Passed, free, want)
			}
			if hook := next("the hook's spawn"); hook.Chain != "deploy.onDone" || len(hook.Passed) != 0 {
				t.Errorf("the hook's spawn (%s) passed %+v, want none", hook.Chain, hook.Passed)
			}
		})
	}
}

// A sequence's steps start as the ones before them finish, which may be long after the gate was
// answered: they carry none of what the firing passed.
func TestASequencesStepsCarryNoGate(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, settling{task.Resolution{Choice: task.Proceed, Proceed: true, By: "user:ana@example.com"}})
	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		specs <- spec
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		p := task.NewTask(id, sup, answering{r: "done"}, noExec{})
		go p.Run(context.Background())
		return p
	})
	eng.ObserveScoped(t.Context())
	if err := eng.AddTrigger(Trigger{
		Name:      "deploy",
		Condition: Condition{Subject: prSubject("open")},
		Gates:     []Gate{{Name: "ok", Mode: "ask"}},
		Action: Action{
			Patch: entity.Obj{"stage": "deploying"}.Payload(),
			Seq:   []Action{{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "one $subject"}}, {Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "two $subject"}}},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	for _, step := range []string{"the first step", "the second step"} {
		select {
		case spec := <-specs:
			if len(spec.Passed) != 0 {
				t.Errorf("%s (%s) passed %+v, want none", step, spec.Chain, spec.Passed)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s never spawned", step)
		}
	}
}

// A charter that asks for a credential it could never be given is refused when it is installed:
// one with no audience or a derived one, one on a rule that writes no gate, one on a hook's or a
// sequence step's spawn, and one that writes what the engine alone says. A gate written `auto`
// is a gate written: unattended work that reaches another system says so in its charter.
func TestACredentialNobodyCouldMintIsRefusedAtInstall(t *testing.T) {
	cred := func(aud string) *launch.Credential { return &launch.Credential{Audience: aud} }
	ask := []Gate{{Name: "ok", Mode: "ask"}}
	for _, c := range []struct {
		name string
		t    Trigger
		want string
	}{
		{"no audience", Trigger{Gates: ask, Action: Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "p", Credential: cred(" ")}}}, "names no audience"},
		{"a derived audience", Trigger{Gates: ask, Action: Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "p", Credential: cred("$target")}}}, "declared and not derived"},
		{"no gate", Trigger{Action: Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "p", Credential: cred("sts")}}}, "the rule has no gate"},
		{"a hook's spawn", Trigger{Gates: ask, Action: Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "p"},
			OnDone: &Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "q", Credential: cred("sts")}}}}, "onDone asks for a credential"},
		{"a sequence step's spawn", Trigger{Gates: ask, Action: Action{
			Seq: []Action{{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "q", Credential: cred("sts")}}}}}, "seq[0] asks for a credential"},
		{"passed written by the charter", Trigger{Gates: ask, Action: Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "p",
			Passed: []launch.Passed{{Question: "q", How: launch.PassedBallots}}}}}, "the engine's to set"},
	} {
		eng := New(entitytest.Store(t), task.AutoResolver{})
		c.t.Name, c.t.Condition = "deploy", Condition{Subject: prSubject("open")}
		if c.t.Action.Patch == nil {
			c.t.Action.Patch = entity.Obj{"stage": "deploying"}.Payload()
		}
		err := eng.AddTrigger(c.t)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a refusal saying %q", c.name, err, c.want)
		}
	}
	// And the ones that could be minted install: a gate that asks, and a gate written `auto`.
	for name, gates := range map[string][]Gate{"a gate that asks": ask, "a gate written auto": {{Name: "ok", Mode: "auto"}}} {
		eng := New(entitytest.Store(t), task.AutoResolver{})
		if err := eng.AddTrigger(Trigger{
			Name: "deploy", Condition: Condition{Subject: prSubject("open")}, Gates: gates,
			Action: Action{Patch: entity.Obj{"stage": "deploying"}.Payload(), Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "p", Credential: cred("sts.example.com")}},
		}); err != nil {
			t.Errorf("%s, and its rule's own spawn asking for a credential: %v", name, err)
		}
	}
}
