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
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// immediate is a driver that completes without taking any action.
type immediate struct{}

func (immediate) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	return task.ToolUse{}, false, nil
}

type noExec struct{}

func (noExec) Execute(context.Context, task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{}, nil, nil
}

// Engine.Spawn hands the (bound) launch.Spec to the Spawner and mirrors the
// resulting task into the verse — the wire-form action becoming a real task.
func TestSpawnRoutesBoundSpec(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	var got launch.Spec
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		got = spec
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		p := task.NewTask(id, sup, immediate{}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{Runner: "local"}) // the Spawner now owns mirror + run
		go p.Run(context.Background())
		return p
	})

	spec := launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $subject"}.
		Bind(launch.Binding{"subject": `demo.pr."1"`})

	p := eng.Spawn("rev-1", spec)
	if p == nil {
		t.Fatal("Spawn returned nil")
	}
	if got.Who() != "verse" || got.Prompt != `review demo.pr."1"` {
		t.Fatalf("spawner received unbound/wrong spec: %+v", got)
	}

	// the task appears in the verse (mirror), then reaches a terminal status
	ref := mirror.Ref("rev-1")
	deadline := time.After(2 * time.Second)
	for {
		if e, ok, _ := store.Get(ref); ok && entity.ToObj(e.Payload)["status"] == "done" {
			return
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(ref)
			t.Fatalf("spawned task never completed in the verse: %+v", e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A ref id can be a file path — full of slashes. The
// spawned task id must stay a single, route-safe URL segment (it routes through
// /tasks/{id}), so slashes are sanitized while the real ref lives on as Subject.
func TestSpawnIDIsRouteSafe(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	ids := make(chan string, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		ids <- id
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		p := task.NewTask(id, sup, immediate{}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{Subject: spec.Subject, Runner: "local"})
		go p.Run(context.Background())
		return p
	})
	if err := eng.AddTrigger(Trigger{
		Name: "validate",
		Condition: Condition{Subject: Match{"path": pathOf("local", "file", "*"),
			"value": map[string]any{"stage": "open"}}},
		Action: Action{
			Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "validate $path"},
			OnDone: &Action{Patch: entity.Obj{"stage": "validated"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("local", "file", "task/taskd/daemon.go"),
		entity.Obj{"path": "task/taskd/daemon.go", "stage": "open"}.Payload())

	select {
	case id := <-ids:
		if strings.ContainsAny(id, `/\#?% `) {
			t.Fatalf("spawn id is not a safe URL segment: %q", id)
		}
		// Chain, then the subject boundary, then the whole address as a token (slashes sanitized),
		// plus a hash behind its own marker — the address had rewritten characters, so it is
		// disambiguated).
		if !strings.HasPrefix(id, "validate@local.file.task_taskd_daemon.go+") {
			t.Fatalf("unexpected sanitized id: %q", id)
		}
		// and it is routable/findable in the verse under that id (mirror is async)
		found := false
		for deadline := time.After(2 * time.Second); !found; {
			if _, ok, _ := store.Get(mirror.Ref(id)); ok {
				found = true
				break
			}
			select {
			case <-deadline:
				t.Fatalf("mirrored task not found under sanitized id %q", id)
			case <-time.After(5 * time.Millisecond):
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("trigger never spawned")
	}
}

// spawnID keys off the whole ref and disambiguates safeSeg's lossy rewrites, so
// distinct entities never share a task id (which would cross-wire control).
func TestSpawnIDDistinguishesRefs(t *testing.T) {
	// same last segment, different first: must differ (not keyed by the name alone)
	if a, b := spawnID("", "r", entity.RefAt("demo", "pr", "1")),
		spawnID("", "r", entity.RefAt("other", "pr", "1")); a == b {
		t.Fatalf("same spawn id for different refs: %q", a)
	}
	// ids that collapse under safeSeg (a/b vs a_b) stay distinct via the hash
	if c, d := spawnID("", "r", entity.RefAt("local", "file", "a/b")),
		spawnID("", "r", entity.RefAt("local", "file", "a_b")); c == d {
		t.Fatalf("safeSeg collision not disambiguated: %q", c)
	}
}

// A spawnID becomes the OWNER half of every gate id its task poses, so it must never
// contain task.GateSep — an owner carrying one would make `gate resolve` cut the id at
// the wrong place and route the answer to a task that does not exist. A ref is arbitrary
// user data (a branch, a path, a title), so the guard belongs in safeSeg rather than in a
// convention nobody enforces. The hash still keeps the rewritten ids distinct.
func TestSpawnIDNeverContainsGateSep(t *testing.T) {
	dirty := entity.RefAt("local", "file", "a"+task.GateSep+"b")
	id := spawnID("", "r", dirty)
	if strings.Contains(id, task.GateSep) {
		t.Errorf("spawnID(%s) = %q contains GateSep %q — a gate id built on it would split "+
			"inside the owner", dirty, id, task.GateSep)
	}
	// and the rewrite stays lossless in the way that matters: a_b must not collide with a~b
	if other := spawnID("", "r", entity.RefAt("local", "file", "a_b")); id == other {
		t.Errorf("GateSep rewrite collided with a literal underscore: both %q", id)
	}
}

// With no Spawner configured, Spawn is a no-op that returns nil (traced), not a panic.
func TestSpawnWithoutSpawner(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	if p := eng.Spawn("x", launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "t"}); p != nil {
		t.Fatal("expected nil task with no spawner")
	}
}
