package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/task"
)

// A charter gate poses a QUESTION, and what it says about how the question settles — a
// threshold, options of its own, a deadline — rides the question to whoever answers it. The
// tally and the clock are theirs; what the engine owes is to ask exactly what was written.

// firstQuestion waits for the first question a capturing resolver saw.
func firstQuestion(t *testing.T, res *captureResolver) task.GateQuestion {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		res.mu.Lock()
		n := len(res.seen)
		var q task.GateQuestion
		if n > 0 {
			q = res.seen[0]
		}
		res.mu.Unlock()
		if n > 0 {
			return q
		}
		select {
		case <-deadline:
			t.Fatal("the gate never asked")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func gated(t *testing.T, store entity.Store, res task.Resolver, g Gate) *Engine {
	t.Helper()
	eng := New(store, res)
	if err := eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Gates:     []Gate{g},
		Action:    Action{Patch: entity.Obj{"stage": "merged"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	return eng
}

// `need: 2` is the short spelling of proceed at two and deny at one, and it implies `ask`.
func TestAThresholdGatePosesItsOptions(t *testing.T) {
	store := entitytest.Store(t)
	res := &captureResolver{}
	eng := gated(t, store, res, Gate{Name: "approvals", Need: 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())

	q := firstQuestion(t, res)
	ids, needs := task.OptionIDs(q.Options), []int{}
	for _, o := range q.Options {
		needs = append(needs, o.Need)
	}
	if strings.Join(ids, ",") != "proceed,deny" || len(needs) != 2 || needs[0] != 2 || needs[1] != 1 {
		t.Fatalf("options = %+v, want proceed at two and deny at one", q.Options)
	}
	if q.Deadline != nil {
		t.Errorf("a gate with no deadline posed one: %+v", q.Deadline)
	}
}

// Options of the gate's own, and its deadline, reach the question as written and in order.
func TestAGateWithOptionsPosesThemVerbatim(t *testing.T) {
	store := entitytest.Store(t)
	res := &captureResolver{}
	eng := gated(t, store, res, Gate{Name: "route", Mode: "ask",
		Options:  []task.Option{{ID: "ship", Need: 1}, {ID: "hold", Need: 2}, {ID: "drop", Need: 1}},
		Deadline: &task.Deadline{Within: "1h", Default: "hold"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "2"), entity.Obj{"stage": "open"}.Payload())

	q := firstQuestion(t, res)
	ids := task.OptionIDs(q.Options)
	if strings.Join(ids, ",") != "ship,hold,drop" || q.Options[1].Need != 2 {
		t.Fatalf("options = %+v, want ship, hold at two, drop", q.Options)
	}
	if q.Deadline == nil || q.Deadline.Within != "1h" || q.Deadline.Default != "hold" {
		t.Fatalf("deadline = %+v, want 1h defaulting to hold", q.Deadline)
	}
}

// The answer the registry delivers is the settle: a threshold gate fires on it exactly as a
// yes-or-no gate does. How many ballots it took is the answerer's business, not the engine's.
func TestAThresholdGateFiresWhenAnswered(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := gated(t, store, reg, Gate{Name: "approvals", Need: 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	pr := entity.RefAt("demo", "pr", "3")
	entitytest.Set(t, store, pr, entity.Obj{"stage": "open"}.Payload())

	id := waitQuestionID(t, reg)
	if !reg.Answer(id, task.Choose("proceed", "human:bob")) {
		t.Fatalf("Answer(%q) found nothing", id)
	}
	waitStage(t, store, pr, "merged")
}

// A charter that still writes a formula is refused where it is read, and told the spelling —
// by name, ahead of the decoder's "unknown field", under either spelling of the gates.
func TestAFormulaGateIsRefusedByName(t *testing.T) {
	for _, doc := range []string{`
name: merge
condition: {subject: demo.pr.*}
gates: [{name: approvals, mode: ask, formula: !quorum 2}]
action: {patch: {ok: yes}}
`, `
name: merge
condition: {subject: demo.pr.*}
gating: {gates: [{name: approvals, formula: !until [!quorum 1, !deadline "24h"]}]}
action: {patch: {ok: yes}}
`} {
		node, err := codec.ParseNode([]byte(doc), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		err = CheckFields(node)
		if err == nil {
			t.Fatal("a formula gate passed the field check")
		}
		for _, want := range []string{"need", "deadline", "when:"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name %q: %v", want, err)
			}
		}
	}
}

// Every refusal names the spelling that would have worked.
func TestGateValidation(t *testing.T) {
	cases := []struct {
		name string
		gate Gate
		want string
	}{
		{"need beside options", Gate{Need: 2, Options: []task.Option{{ID: "a"}}}, "written together"},
		{"a default that is not an option", Gate{Need: 2, Deadline: &task.Deadline{Within: "1h", Default: "maybe"}}, "not one of this gate's options"},
		{"an unparseable duration", Gate{Mode: "ask", Deadline: &task.Deadline{Within: "soon"}}, "deadline"},
		{"auto with a threshold", Gate{Mode: "auto", Need: 2}, "asks nobody"},
		{"an option twice", Gate{Options: []task.Option{{ID: "a"}, {ID: "a"}}}, "written twice"},
		{"an option with no id", Gate{Options: []task.Option{{Need: 1}}}, "no id"},
	}
	for _, c := range cases {
		eng := New(entitytest.Store(t), denyResolver{})
		err := eng.AddTrigger(Trigger{
			Name:      "merge",
			Condition: Condition{Subject: mm("demo.pr.*")},
			Gates:     []Gate{c.gate},
			Action:    Action{Patch: entity.Obj{"ok": "yes"}.Payload()},
		})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", c.name, err, c.want)
		}
	}
}

// waitQuestionID waits for one question to stand on a registry and returns its id.
func waitQuestionID(t *testing.T, reg *task.Registry) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if p := reg.Pending(); len(p) > 0 {
			return p[0].Question.ID
		}
		select {
		case <-deadline:
			t.Fatal("no question was posed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
