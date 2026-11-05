package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// AN ASK SETTLES ONCE, AND AN OPEN ONE IS JOINED. Asked where nothing stands, a firing's question
// goes under the id it derives; where that ask stands open — pending, or orphaned by a restart or
// a withdrawal — the firing joins it; where it was answered, the firing asks a new question
// under an id of its own, the number after the highest standing.
func TestAnAskingJoinsTheOpenAskAndOtherwiseAsksANewOne(t *testing.T) {
	base := gateID("", "land", "ok", []entity.Ref{entity.RefAt("demo", "pr", "1")})
	answered := entity.Obj{"status": mirror.QuestionAnswered, "verdict": "proceed"}
	pending := entity.Obj{"status": mirror.QuestionPending}
	orphaned := entity.Obj{"status": mirror.QuestionOrphaned}
	for _, c := range []struct {
		name     string
		standing map[string]entity.Obj
		want     string
	}{
		{"nothing stands", nil, base},
		{"pending is joined", map[string]entity.Obj{base: pending}, base},
		{"orphaned is joined", map[string]entity.Obj{base: orphaned}, base},
		{"answered is asked anew", map[string]entity.Obj{base: answered}, base + "-2"},
		{"the open one after an answer is joined",
			map[string]entity.Obj{base: answered, base + "-2": orphaned}, base + "-2"},
		{"after two answers, the third",
			map[string]entity.Obj{base: answered, base + "-2": answered}, base + "-3"},
		{"the first gone after its keep is not reused",
			map[string]entity.Obj{base + "-2": answered}, base + "-3"},
		{"an open one past a gap is joined",
			map[string]entity.Obj{base + "-3": pending}, base + "-3"},
		{"names that are not its askings are not counted", map[string]entity.Obj{
			base + "-1": answered, base + "-02": answered, base + "-x": answered, base + "x": answered,
		}, base},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := entitytest.Store(t)
			for id, rec := range c.standing {
				entitytest.Set(t, store, mirror.QuestionRef(id), rec.Payload())
			}
			eng := New(store, task.AutoResolver{})
			got, err := eng.askingID(base)
			if err != nil {
				t.Fatalf("askingID: %v", err)
			}
			if got != c.want {
				t.Fatalf("askingID = %q, want %q", got, c.want)
			}
			// And a new asking's id still cuts once, at the owner, where an answer finds
			// what posed it.
			if _, rest, ok := strings.Cut(got, task.GateSep); !ok || strings.Contains(rest, task.GateSep) {
				t.Fatalf("%q does not cut once on %q", got, task.GateSep)
			}
		})
	}
}

// And the engine asks under it: a firing that finds its gate's question standing pending asks
// that question — the same id, so the registry joins it and the docket keeps one record — and a
// firing that finds it answered asks a new one.
func TestAFiringAsksTheOpenQuestionAndANewOneAfterAnAnswer(t *testing.T) {
	store := entitytest.Store(t)
	subj := entity.RefAt("demo", "pr", "1")
	base := gateID("", "ship", "ok", []entity.Ref{subj})
	entitytest.Set(t, store, mirror.QuestionRef(base), entity.Obj{"status": mirror.QuestionPending}.Payload())

	asked := make(chan task.GateQuestion, 8)
	eng := New(store, countingResolver{asked})
	if err := eng.AddTrigger(Trigger{
		Name:      "ship",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Gates:     []Gate{{Name: "ok", Mode: "ask"}},
		Action:    Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	next := func() string {
		t.Helper()
		select {
		case q := <-asked:
			return q.ID
		case <-time.After(3 * time.Second):
			t.Fatal("the rule never asked")
			return ""
		}
	}
	entitytest.Set(t, store, subj, entity.Obj{"stage": "open"}.Payload())
	if got := next(); got != base {
		t.Fatalf("a firing with its question pending asked %q, want the one standing, %q", got, base)
	}
	waitStage(t, store, subj, "shipped")

	entitytest.Set(t, store, mirror.QuestionRef(base),
		entity.Obj{"status": mirror.QuestionAnswered, "verdict": "proceed"}.Payload())
	entitytest.Set(t, store, subj, entity.Obj{"stage": "open"}.Payload())
	if got := next(); got != base+"-2" {
		t.Fatalf("a firing after its question was answered asked %q, want a new one, %q", got, base+"-2")
	}
}
