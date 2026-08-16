package trigger

import (
	"context"
	"reflect"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// ParseExpr is the address grammar behind `flow pause <addr>`: three segments, `*` as a
// wildcard per segment, trailing segments optional. An id may contain colons.
func TestParseExpr(t *testing.T) {
	cases := []struct {
		in   string
		want Match
	}{
		{"demo:pr:1", Match{"system": "demo", "kind": "pr", "id": "1"}},
		{"demo:pr:*", Match{"system": "demo", "kind": "pr"}},
		{"demo:*:*", Match{"system": "demo"}},
		{"demo:pr", Match{"system": "demo", "kind": "pr"}},
		{"demo", Match{"system": "demo"}},
		{"*", Match{}},
		{"local:file:a/b:c", Match{"system": "local", "kind": "file", "id": "a/b:c"}},
	}
	for _, c := range cases {
		got, err := ParseExpr(c.in)
		if err != nil {
			t.Errorf("ParseExpr(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(map[string]any(got), map[string]any(c.want)) {
			t.Errorf("ParseExpr(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "   ", "demo::1", ":pr:1"} {
		if m, err := ParseExpr(bad); err == nil {
			t.Errorf("ParseExpr(%q) accepted, got %v", bad, m)
		}
	}
}

// spawnFP registers a live task under a subject, as Spawn does, and returns its handle.
func spawnFP(t *testing.T, eng *Engine, id, subject string) *task.Task {
	t.Helper()
	sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
	p := task.NewTask(id, sup, blockForever{}, noExec{})
	eng.track(flowTask{id: id, subject: subject, h: p})
	return p
}

// blockForever keeps a task alive so it stays in the live registry for the duration of a
// steering test (an `immediate` driver would complete and be reaped mid-test).
type blockForever struct{}

func (blockForever) Next(ctx context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	<-ctx.Done()
	return task.ToolUse{}, false, ctx.Err()
}

// A flow is a QUERY, so one address can steer a whole kind or a whole system — "relational
// flows" without a second primitive. This is the capability step 4 exists to unlock.
func TestFlowAddressingSelectsSets(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	spawnFP(t, eng, "p1", "demo:pr:1")
	spawnFP(t, eng, "p2", "demo:pr:1") // two tasks on one subject
	spawnFP(t, eng, "p3", "demo:pr:2")
	spawnFP(t, eng, "p4", "demo:issue:9")
	spawnFP(t, eng, "p5", "other:pr:1")

	cases := map[string]int{
		"demo:pr:1":    2, // one subject, both its tasks
		"demo:pr:2":    1,
		"demo:pr:*":    3, // the whole kind — the relational case
		"demo:*:*":     4, // the whole system
		"*":            5, // everything
		"demo:pr:404":  0, // no live work
		"nothing:x:y":  0,
		"other:pr:*":   1,
		"demo:issue:9": 1,
	}
	for addr, want := range cases {
		if got := len(eng.matchFlow(mustExpr(t, addr))); got != want {
			t.Errorf("address %q selected %d live tasks, want %d", addr, got, want)
		}
	}

	// Flows() reports the distinct subjects, sorted — a query over the same registry.
	want := []string{"demo:issue:9", "demo:pr:1", "demo:pr:2", "other:pr:1"}
	if got := eng.Flows(); !reflect.DeepEqual(got, want) {
		t.Errorf("Flows() = %v, want %v", got, want)
	}
}

// Steering by a set address reaches every selected task and reports the count — the
// {subject, verb, affected} contract the tutorial documents (api.Steered).
func TestSteerWholeKind(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	spawnFP(t, eng, "p1", "demo:pr:1")
	spawnFP(t, eng, "p2", "demo:pr:2")
	spawnFP(t, eng, "p3", "demo:issue:9")

	if n := eng.PauseFlow(mustExpr(t, "demo:pr:*")); n != 2 {
		t.Fatalf("PauseFlow(demo:pr:*) signalled %d, want 2", n)
	}
	if n := eng.ResumeFlow(mustExpr(t, "demo:pr:*")); n != 2 {
		t.Fatalf("ResumeFlow(demo:pr:*) signalled %d, want 2", n)
	}
	if n := eng.SteerFlow(mustExpr(t, "demo:*:*"), "hurry", "human:scott"); n != 3 {
		t.Fatalf("SteerFlow(demo:*:*) said to %d, want 3", n)
	}
	if n := eng.CancelFlow(mustExpr(t, "demo:issue:9")); n != 1 {
		t.Fatalf("CancelFlow signalled %d, want 1", n)
	}
}

// A payload predicate addresses flows by what their subject IS, not just by identity — so
// "hold the work on every open PR" is the same selector at a wider setting. It reads the
// subject entity, so it only applies to subjects that exist in the store.
func TestFlowAddressingByPayload(t *testing.T) {
	store := entitytest.Store(t)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "2"), entity.Obj{"stage": "merged"}.Payload())
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "3"), entity.Obj{"stage": "open"}.Payload())

	eng := New(store, task.AutoResolver{})
	spawnFP(t, eng, "p1", "demo:pr:1")
	spawnFP(t, eng, "p2", "demo:pr:2")
	spawnFP(t, eng, "p3", "demo:pr:3")

	open := Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}
	if n := eng.PauseFlow(open); n != 2 {
		t.Fatalf("pausing every open PR's work signalled %d, want 2", n)
	}
}

// A re-fire about the same subject must NOT start a second task: spawnID is deterministic
// per (trigger, subject), and the runtime would happily run duplicates while clobbering its
// own registry entry, so the engine dedupes by id.
func TestSpawnIsIdempotentPerID(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	spawned := 0
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		spawned++
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		return task.NewTask(id, sup, blockForever{}, noExec{})
	})

	spec := launch.Spec{Subject: "demo:pr:1", Prompt: "review"}
	h1 := eng.Spawn("rev-1", spec)
	h2 := eng.Spawn("rev-1", spec) // the same firing again
	if spawned != 1 {
		t.Fatalf("spawner invoked %d times for one id, want 1", spawned)
	}
	if h1 != h2 {
		t.Fatal("a duplicate spawn must return the live handle, not nil or a new one")
	}
	if n := len(eng.matchFlow(mustExpr(t, "demo:pr:1"))); n != 1 {
		t.Fatalf("live registry holds %d tasks for one id, want 1", n)
	}

	// Once it is reaped, the id is free again — a later, genuinely new firing can run.
	eng.reap(entity.Event{
		Ref: mirror.Ref("rev-1"), Op: entity.Set,
		To: entity.Obj{"status": string(task.Done)}.Payload(),
	})
	if eng.Spawn("rev-1", spec) == nil || spawned != 2 {
		t.Fatalf("after reap a fresh spawn should run; spawner invoked %d times", spawned)
	}
}

// A distinct subject gets a distinct id and its own task, so dedupe is per subject rather
// than per trigger.
func TestSpawnDedupeIsPerSubject(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		return task.NewTask(id, sup, blockForever{}, noExec{})
	})
	a := entity.RefAt("demo", "pr", "1")
	b := entity.RefAt("demo", "pr", "2")
	eng.Spawn(spawnID("review", a), launch.Spec{Subject: a.String()})
	eng.Spawn(spawnID("review", b), launch.Spec{Subject: b.String()})

	if n := len(eng.matchFlow(mustExpr(t, "demo:pr:*"))); n != 2 {
		t.Fatalf("two subjects yielded %d live tasks, want 2", n)
	}
}
