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

// ParseExpr is the address grammar behind `verse flow --subject <addr>`: a kinded path of any
// length, `*` a whole segment left open. A segment may hold colons.
func TestParseExpr(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`demo.pr."1"`, []string{"demo", "pr", "1"}},
		{"demo.pr.*", []string{"demo", "pr", "*"}},
		{"demo.*.*", []string{"demo", "*", "*"}},
		{"demo.pr", []string{"demo", "pr"}}, // a short path names the node at it
		{"demo", []string{"demo"}},
		{"*", []string{"*"}},
		{"local.file.a/b:c", []string{"local", "file", "a/b:c"}},
	}
	for _, c := range cases {
		got, err := ParseExpr(c.in)
		if err != nil {
			t.Errorf("ParseExpr(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got.Segs(), c.want) {
			t.Errorf("ParseExpr(%q) = %q, want %q", c.in, got.Segs(), c.want)
		}
	}
	for _, bad := range []string{"", "   ", ".pr.1"} {
		if m, err := ParseExpr(bad); err == nil {
			t.Errorf("ParseExpr(%q) accepted, got %v", bad, m)
		}
	}
	// A DESCENT IS A QUESTION, and a subject asks one: `demo..1` selects every `1` beneath demo.
	if m, err := ParseExpr("demo..1"); err != nil || len(m.Segs()) != 3 || m.Segs()[1] != ".." {
		t.Errorf("ParseExpr(demo..1) = (%v, %v), want the descent as its own segment", m.Segs(), err)
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

// A flow is a QUERY, so one address can steer every node at a place, or everything under a first
// segment — "relational flows" without a second primitive. This is the capability step 4 exists to unlock.
func TestFlowAddressingSelectsSets(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	spawnFP(t, eng, "p1", `demo.pr."1"`)
	spawnFP(t, eng, "p2", `demo.pr."1"`) // two tasks on one subject
	spawnFP(t, eng, "p3", `demo.pr."2"`)
	spawnFP(t, eng, "p4", `demo.issue."9"`)
	spawnFP(t, eng, "p5", `other.pr."1"`)

	cases := map[string]int{
		`demo.pr."1"`:    2, // one subject, both its tasks
		`demo.pr."2"`:    1,
		"demo.pr.*":      3, // every pr — the relational case
		"demo.*.*":       4, // everything two levels under demo
		"*":              5, // everything
		`demo.pr."404"`:  0, // no live work
		"nothing.x.y":    0,
		"other.pr.*":     1,
		`demo.issue."9"`: 1,
	}
	for addr, want := range cases {
		if got := len(eng.matchFlow(mustExpr(t, addr))); got != want {
			t.Errorf("address %q selected %d live tasks, want %d", addr, got, want)
		}
	}

	// Flows() reports the distinct subjects, sorted — a query over the same registry.
	want := []string{`demo.issue."9"`, `demo.pr."1"`, `demo.pr."2"`, `other.pr."1"`}
	if got := eng.Flows(); !reflect.DeepEqual(got, want) {
		t.Errorf("Flows() = %v, want %v", got, want)
	}
}

// A selection is spelled as a suspension is, so one address reaches a subject or every node at a
// place, and a charter narrows it to the rules of one charter.
func TestSelectionReachesSets(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	spawnFP(t, eng, "p1", `demo.pr."1"`)
	spawnFP(t, eng, "p2", `demo.pr."2"`)
	spawnFP(t, eng, "p3", `demo.issue."9"`)
	eng.liveMu.Lock()
	fp := eng.live["p1"]
	fp.chain = "review"
	eng.live["p1"] = fp
	eng.liveMu.Unlock()

	for _, c := range []struct {
		charter, subject string
		want             int
	}{
		{"*", "demo.pr.*", 2},
		{"*", "demo.*.*", 3},
		{"*", "*.*.9", 1},
		{":review", "*", 1},
		{":triage", "*", 0},
		{"elsewhere", "*", 0}, // no task here is a package's
	} {
		if n := len(eng.SelectFlow(mustSelect(t, c.charter, c.subject))); n != c.want {
			t.Errorf("--charter %s --subject %s selected %d, want %d", c.charter, c.subject, n, c.want)
		}
	}
	if _, ok := eng.Handle("p3"); !ok {
		t.Fatal("Handle did not find the live task")
	}
	if _, ok := eng.Handle("gone"); ok {
		t.Fatal("Handle found a task that is not live")
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

	spec := launch.Spec{Subject: `demo.pr."1"`, Prompt: "review"}
	h1 := eng.Spawn("rev-1", spec)
	h2 := eng.Spawn("rev-1", spec) // the same firing again
	if spawned != 1 {
		t.Fatalf("spawner invoked %d times for one id, want 1", spawned)
	}
	if h1 != h2 {
		t.Fatal("a duplicate spawn must return the live handle, not nil or a new one")
	}
	if n := len(eng.matchFlow(mustExpr(t, `demo.pr."1"`))); n != 1 {
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
	eng.Spawn(spawnID("", "review", a), launch.Spec{Subject: a.String()})
	eng.Spawn(spawnID("", "review", b), launch.Spec{Subject: b.String()})

	if n := len(eng.matchFlow(mustExpr(t, "demo.pr.*"))); n != 2 {
		t.Fatalf("two subjects yielded %d live tasks, want 2", n)
	}
}
