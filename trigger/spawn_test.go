package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// immediate is a driver that completes without taking any action.
type immediate struct{}

func (immediate) Next(context.Context, process.StepResult) (process.ToolUse, bool, error) {
	return process.ToolUse{}, false, nil
}

type noExec struct{}

func (noExec) Execute(context.Context, process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	return process.StepResult{}, nil, nil
}

// Engine.Spawn hands the (bound) launch.Spec to the Spawner and mirrors the
// resulting process into the verse — the wire-form action becoming a real process.
func TestSpawnRoutesBoundSpec(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})

	var got launch.Spec
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		got = spec
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		p := process.NewProcess(id, sup, immediate{}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{Policy: "local"}) // the Spawner now owns mirror + run
		go p.Run(context.Background())
		return p
	})

	spec := launch.Spec{Driver: "scripted", Task: "review $subject"}.
		Bind(launch.Binding{"subject": "demo:pr:1"})

	p := eng.Spawn("rev-1", spec)
	if p == nil {
		t.Fatal("Spawn returned nil")
	}
	if got.Driver != "scripted" || got.Task != "review demo:pr:1" {
		t.Fatalf("spawner received unbound/wrong spec: %+v", got)
	}

	// the process appears in the verse (mirror), then reaches a terminal status
	ref := mirror.Ref("rev-1")
	deadline := time.After(2 * time.Second)
	for {
		if e, ok, _ := store.Get(ref); ok && entity.ToObj(e.Payload)["status"] == "done" {
			return
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(ref)
			t.Fatalf("spawned process never completed in the verse: %+v", e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A ref id can be a file path (from local-dev federation) — full of slashes. The
// spawned process id must stay a single, route-safe URL segment (it routes through
// /processes/{id}), so slashes are sanitized while the real ref lives on as Subject.
func TestSpawnIDIsRouteSafe(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	ids := make(chan string, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		ids <- id
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		p := process.NewProcess(id, sup, immediate{}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{Subject: spec.Subject, Policy: "local"})
		go p.Run(context.Background())
		return p
	})
	if err := eng.AddTrigger(Trigger{
		Name:      "validate",
		Condition: Condition{When: MatchWhen(Match{"system": "local", "kind": "file"})},
		Action:    Action{Spawn: &launch.Spec{Task: "validate $path"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.Ref{System: "local", Kind: "file", ID: "process/processd/daemon.go"},
		entity.Obj{"path": "process/processd/daemon.go"}.Payload())

	select {
	case id := <-ids:
		if strings.ContainsAny(id, `/\#?% `) {
			t.Fatalf("spawn id is not a safe URL segment: %q", id)
		}
		// Chain, then the subject boundary, then the full ref (slashes sanitized), plus a
		// hash suffix behind its own marker (the ref had rewritten characters, so it is
		// disambiguated).
		if !strings.HasPrefix(id, "validate@local:file:process_processd_daemon.go+") {
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
				t.Fatalf("mirrored process not found under sanitized id %q", id)
			case <-time.After(5 * time.Millisecond):
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("trigger never spawned")
	}
}

// spawnID keys off the whole ref and disambiguates safeSeg's lossy rewrites, so
// distinct entities never share a process id (which would cross-wire control).
func TestSpawnIDDistinguishesRefs(t *testing.T) {
	// same id, different system: must differ (not just ref.ID keyed)
	if a, b := spawnID("r", entity.Ref{System: "demo", Kind: "pr", ID: "1"}),
		spawnID("r", entity.Ref{System: "other", Kind: "pr", ID: "1"}); a == b {
		t.Fatalf("same spawn id for different refs: %q", a)
	}
	// ids that collapse under safeSeg (a/b vs a_b) stay distinct via the hash
	if c, d := spawnID("r", entity.Ref{System: "local", Kind: "file", ID: "a/b"}),
		spawnID("r", entity.Ref{System: "local", Kind: "file", ID: "a_b"}); c == d {
		t.Fatalf("safeSeg collision not disambiguated: %q", c)
	}
}

// A spawnID becomes the OWNER half of every gate id its process poses, so it must never
// contain process.GateSep — an owner carrying one would make `gate resolve` cut the id at
// the wrong place and route the answer to a process that does not exist. A ref is arbitrary
// user data (a branch, a path, a title), so the guard belongs in safeSeg rather than in a
// convention nobody enforces. The hash still keeps the rewritten ids distinct.
func TestSpawnIDNeverContainsGateSep(t *testing.T) {
	dirty := entity.Ref{System: "local", Kind: "file", ID: "a" + process.GateSep + "b"}
	id := spawnID("r", dirty)
	if strings.Contains(id, process.GateSep) {
		t.Errorf("spawnID(%s) = %q contains GateSep %q — a gate id built on it would split "+
			"inside the owner", dirty, id, process.GateSep)
	}
	// and the rewrite stays lossless in the way that matters: a_b must not collide with a~b
	if other := spawnID("r", entity.Ref{System: "local", Kind: "file", ID: "a_b"}); id == other {
		t.Errorf("GateSep rewrite collided with a literal underscore: both %q", id)
	}
}

// With no Spawner configured, Spawn is a no-op that returns nil (traced), not a panic.
func TestSpawnWithoutSpawner(t *testing.T) {
	eng := New(entitytest.Store(t), process.AutoResolver{})
	if p := eng.Spawn("x", launch.Spec{Task: "t"}); p != nil {
		t.Fatal("expected nil process with no spawner")
	}
}
