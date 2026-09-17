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
	"github.com/signadot/verse/schedule"
	"github.com/signadot/verse/task"
)

// A tick is a subject the store consumes: the schedule replaces it, nobody else writes it, and
// its address never recurs. So a rule about ticks needs no latch, every rule reading a schedule
// fires once per tick, and a spawn about a tick is once-ever rather than only while live.

const (
	t1 = "2026-09-18T03:00:00Z"
	t2 = "2026-09-18T04:00:00Z"
)

func tickRule(name string) Trigger {
	return Trigger{
		Name:      name,
		Condition: Condition{Subject: mm("verse.schedule.hourly.tick.*")},
		Action:    Action{Spawn: &launch.Spec{Prompt: name + " for $subject"}},
	}
}

// writeTick stands one tick under the schedule, replacing whatever stood: the one write the
// daemon makes, as far as a rule can tell.
func writeTick(t *testing.T, store entity.Store, key string, replacing ...string) {
	t.Helper()
	ticks := entity.Obj{key: schedule.Tick(mustKey(t, key), 0, time.UTC)}
	for _, old := range replacing {
		ticks[old] = entity.Removed()
	}
	if _, _, err := store.Write(entity.Write{
		At:    schedule.Ref("hourly").At(),
		Value: entity.Obj{schedule.NextField: t2, schedule.TickField: ticks}.Payload(),
	}); err != nil {
		t.Fatalf("writing tick %s: %v", key, err)
	}
}

func mustKey(t *testing.T, key string) time.Time {
	t.Helper()
	at, err := schedule.ParseKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// tickSpawns collects the ids a spawner was asked for, and waits for a count.
func tickSpawns(t *testing.T, ids chan string, n int, within time.Duration) []string {
	t.Helper()
	var got []string
	deadline := time.After(within)
	for len(got) < n {
		select {
		case id := <-ids:
			got = append(got, id)
		case <-deadline:
			t.Fatalf("spawned %v, want %d within %s", got, n, within)
		}
	}
	return got
}

func noSpawn(t *testing.T, ids chan string, within time.Duration) {
	t.Helper()
	select {
	case id := <-ids:
		t.Fatalf("spawned %s, want nothing", id)
	case <-time.After(within):
	}
}

func TestATickRuleNeedsNoLatch(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	if err := eng.AddTrigger(tickRule("a")); err != nil {
		t.Fatalf("a spawn about a tick was refused: %v", err)
	}
	world := tickRule("b")
	world.Condition.Subject = mm("demo.pr.*")
	if err := eng.AddTrigger(world); err == nil {
		t.Fatal("a spawn about a world subject with no latch was accepted")
	}
}

// Two rules on one schedule each fire once per tick, and a replacement fires each once again
// for the new tick and never again for the old.
func TestTwoRulesOnOneScheduleEachFireOncePerTick(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	ids := make(chan string, 8)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		ids <- id
		return nil
	})
	for _, name := range []string{"a", "b"} {
		if err := eng.AddTrigger(tickRule(name)); err != nil {
			t.Fatalf("AddTrigger %s: %v", name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	writeTick(t, store, t1)
	got := tickSpawns(t, ids, 2, 3*time.Second)
	for _, id := range got {
		if !strings.Contains(id, t1) {
			t.Fatalf("spawn %s is not about the tick %s", id, t1)
		}
	}
	if got[0] == got[1] {
		t.Fatalf("two rules, one id: %v", got)
	}
	noSpawn(t, ids, 100*time.Millisecond)

	writeTick(t, store, t2, t1)
	got = tickSpawns(t, ids, 2, 3*time.Second)
	for _, id := range got {
		if !strings.Contains(id, t2) {
			t.Fatalf("spawn %s is not about the new tick %s", id, t2)
		}
	}
	noSpawn(t, ids, 100*time.Millisecond)
}

// A restart re-fires the tick standing under the schedule; a task record for the spawn, done
// or live, means it already ran, and nothing starts again. A new tick starts work as usual.
func TestARestartDoesNotReRunAFinishedTick(t *testing.T) {
	store := entitytest.Store(t)
	ids := make(chan string, 8)
	spawner := func(id string, spec launch.Spec) task.Handle {
		ids <- id
		return nil
	}

	first := New(store, task.AutoResolver{})
	first.SetSpawner(spawner)
	if err := first.AddTrigger(tickRule("a")); err != nil {
		t.Fatal(err)
	}
	ctx1, stop1 := context.WithCancel(context.Background())
	first.Start(ctx1)
	writeTick(t, store, t1)
	id := tickSpawns(t, ids, 1, 3*time.Second)[0]
	// The run finished: the mirror wrote its record and the task is not live any more.
	entitytest.Set(t, store, mirror.Ref(id), entity.Obj{"status": "done"}.Payload())
	stop1()

	second := New(store, task.AutoResolver{})
	second.SetSpawner(spawner)
	if err := second.AddTrigger(tickRule("a")); err != nil {
		t.Fatal(err)
	}
	ctx2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	second.Start(ctx2)
	noSpawn(t, ids, 300*time.Millisecond)

	writeTick(t, store, t2, t1)
	if got := tickSpawns(t, ids, 1, 3*time.Second)[0]; !strings.Contains(got, t2) {
		t.Fatalf("the new tick spawned %s", got)
	}
}
