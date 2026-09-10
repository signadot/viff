package trigger

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// posed writes a selection's question by hand — one the previous verse left standing.
func posed(t *testing.T, s entity.Store, id string, payload entity.Obj) entity.Ref {
	t.Helper()
	ref := mirror.QuestionRef(id)
	entitytest.Set(t, s, ref, payload.Payload())
	return ref
}

func statusOf(t *testing.T, s entity.Store, ref entity.Ref) entity.Obj {
	t.Helper()
	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		t.Fatalf("reading %s: ok=%v err=%v", ref, ok, err)
	}
	return entity.ToObj(e.Payload)
}

// A selection is resumed through the chain it recorded: the engine navigates back to the
// action the question is holding up and re-enters the selection, which re-poses the same
// question, so a pick cast after the restart still decides who runs.
func TestResumeReEntersASelection(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	spawned := make(chan launch.Spec, 2)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		spawned <- spec
		return nil
	})
	spawn := &launch.Spec{
		Prompt:   "review $subject",
		Grant:    "read-only",
		Provider: &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "ask"},
	}
	subj := entity.RefAt("demo", "pr", "1")
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    Action{Patch: entity.Obj{"stage": "reviewing"}.Payload(), Spawn: spawn},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	// The subject as the interrupted firing left it: past its latch, so nothing re-fires the
	// rule and the question below is the only thing standing between the answer and the work.
	entitytest.Set(t, store, subj, entity.Obj{"stage": "reviewing"}.Payload())
	id := selectID("review", subj, spawn.Provider)
	posed(t, store, id, entity.Obj{
		"id": id, "status": mirror.QuestionPending, "subject": subj.String(), "chain": "review",
		"options": []any{
			map[string]any{"id": "claude", "need": 1},
			map[string]any{"id": "gpt", "need": 1},
		},
	})

	if n := eng.ResumeSelections(); n != 1 {
		t.Fatalf("resumed %d selections, want 1", n)
	}
	deadline := time.After(3 * time.Second)
	for {
		if p := reg.Pending(); len(p) == 1 && p[0].Question.ID == id {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the resumed selection did not re-pose its question: %+v", reg.Pending())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// The pick, as an operator answering the docket would cast it.
	if !reg.Answer(id, task.Choose("claude", "scott")) {
		t.Fatal("the re-posed question was not there to answer")
	}
	select {
	case spec := <-spawned:
		if spec.Doer != "claude" || spec.Driver != "claude" {
			t.Fatalf("spawned %+v, want the doer the answer named", spec)
		}
		if spec.Chain != "review" || spec.Subject != subj.String() {
			t.Fatalf("the resumed spawn lost its provenance: %+v", spec)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the answer decided nothing: a resumed selection did not spawn")
	}
}

// A selection whose rule left the charter is SETTLED rather than offered. Nothing will act on
// the answer, so a docket that kept listing it would be asking the room to decide something
// no charter reads.
func TestResumeOrphansASelectionWhoseRuleIsGone(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	ref := posed(t, store, "verse.trigger.removed.provider~1", entity.Obj{
		"status": mirror.QuestionPending, "subject": `demo.pr."1"`, "chain": "removed",
	})
	if n := eng.ResumeSelections(); n != 0 {
		t.Fatalf("resumed %d selections, want 0 — it has no rule to resume into", n)
	}
	got := statusOf(t, store, ref)
	if got["status"] != mirror.QuestionOrphaned {
		t.Fatalf("status = %v, want orphaned: %+v", got["status"], got)
	}
	if got["why"] == "" || got["orphanedAt"] == "" {
		t.Fatalf("settled without saying why or when: %+v", got)
	}
	// Settled, not decided: a question nobody answered must not read as though somebody had.
	if got["verdict"] != nil {
		t.Fatalf("given a verdict nobody chose: %+v", got)
	}
}

// A selection that already settled is not resumed and not touched: the decision stands, and
// re-entering its continuation would be a second run of decided work.
func TestResumeLeavesASettledSelectionAlone(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	ref := posed(t, store, "verse.trigger.gone.provider~2", entity.Obj{
		"status": mirror.QuestionAnswered, "verdict": "claude", "subject": `demo.pr."1"`,
		"chain": "gone", // the rule is gone too, and it still must not be touched
	})
	if n := eng.ResumeSelections(); n != 0 {
		t.Fatalf("resumed %d selections, want 0", n)
	}
	got := statusOf(t, store, ref)
	if got["status"] != mirror.QuestionAnswered || got["verdict"] != "claude" {
		t.Fatalf("a settled selection was disturbed: %+v", got)
	}
}

// A PACKAGE'S SELECTION IS NOT BASELINE'S TO SETTLE. A verse runs several engines — baseline,
// and one per installed package — and all of them read the same questions out of the same
// store. Baseline once swept a package's motions, looked the rule up in its own charter, did
// not find it, and settled a live gate as gone.
func TestBaselineDoesNotSettleAPackagesSelection(t *testing.T) {
	store := entitytest.Store(t)
	pkg := New(store, denyResolver{})
	pkg.Confine(SubtreeAt("verse-dev", "merge"))
	if err := pkg.AddTrigger(Trigger{
		Name:      "train-run",
		Condition: Condition{Subject: Match{"system": "verse-dev", "kind": "merge"}},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	base := New(store, denyResolver{})

	// Stamped, as a question that engine asks now is.
	mine := posed(t, store, "verse.trigger.train-run.provider~p1", entity.Obj{
		"status": mirror.QuestionPending, "subject": "verse-dev.merge.1",
		"chain": "train-run", "at": "verse-dev.merge",
	})
	// And unstamped: baseline can only tell by asking the engines it runs.
	older := posed(t, store, "verse.trigger.train-run.provider~p2", entity.Obj{
		"status": mirror.QuestionPending, "subject": "verse-dev.merge.2", "chain": "train-run",
	})
	base.KnowsElsewhere(pkg.Knows)

	base.ResumeSelections()
	for _, ref := range []entity.Ref{mine, older} {
		if got := statusOf(t, store, ref); got["status"] != mirror.QuestionPending {
			t.Fatalf("baseline settled %s, a rule it does not hold: %+v", ref, got)
		}
	}
}

// …and with nobody else running, the old reading stands: a rule that is gone is gone.
func TestBaselineAloneStillSettlesAGoneRule(t *testing.T) {
	store := entitytest.Store(t)
	base := New(store, denyResolver{})
	ref := posed(t, store, "verse.trigger.removed.provider~g1", entity.Obj{
		"status": mirror.QuestionPending, "subject": `demo.pr."1"`, "chain": "removed",
	})
	base.ResumeSelections()
	if got := statusOf(t, store, ref); got["status"] != mirror.QuestionOrphaned {
		t.Fatalf("status = %v, want orphaned: %+v", got["status"], got)
	}
}

// captureResolver records the questions a firing asks, so a test can read what was published
// about them rather than only whether they were answered.
type captureResolver struct {
	mu   sync.Mutex
	seen []task.GateQuestion
}

func (c *captureResolver) Resolve(_ context.Context, q task.GateQuestion, _ task.ToolUse) (task.Resolution, error) {
	c.mu.Lock()
	c.seen = append(c.seen, q)
	c.mu.Unlock()
	return task.Resolution{Proceed: false, By: "deny"}, nil
}

// A FIRING'S GATE NAMES ITS CHARTER: `gate` and `origin` are names, and a name is unique
// inside a charter rather than across them, so a docket entry saying `merge-to-main` on
// `train-run` left a reader to work out which package that was.
func TestAFiringGateNamesItsCharter(t *testing.T) {
	store := entitytest.Store(t)
	res := &captureResolver{}
	eng := New(store, res)
	eng.Confine(SubtreeAt("verse-dev", "merge"))
	if err := eng.AddTrigger(Trigger{
		Name:      "train-run",
		Condition: Condition{Subject: Match{"system": "verse-dev", "kind": "merge"}},
		Gates:     []Gate{{Name: "merge-to-main", Mode: "ask"}},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("verse-dev", "merge", "1"), entity.Obj{"stage": "cut"}.Payload())
	if q := firstQuestion(t, res); q.At != "verse-dev.merge" {
		t.Fatalf("gate question At = %q, want the charter that asked: %+v", q.At, q)
	}
}
