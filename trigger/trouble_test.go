package trigger

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// troubleOf waits for a rule to be in trouble of a kind, and answers it.
func troubleOf(t *testing.T, eng *Engine, rule, kind string) Trouble {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, tr := range eng.Troubles() {
			if tr.Rule == rule && tr.Kind == kind {
				return tr
			}
		}
		select {
		case <-deadline:
			t.Fatalf("rule %s was never in trouble (%s); the engine keeps %+v", rule, kind, eng.Troubles())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func ruleFrom(t *testing.T, doc string) Trigger {
	t.Helper()
	var tr Trigger
	if err := codec.Unmarshal([]byte(doc), &tr, codec.Tony); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return tr
}

// A RULE'S ACTION THAT FAILS IS KEPT ON THE RULE: where in it, about what, and why. The rule
// below copies a field its subject does not have, so the patch cannot be resolved and nothing
// is written; without this its author sees a rule that stands and a subject that did not move.
func TestAFailedActionIsKeptOnTheRule(t *testing.T) {
	store := entitytest.Store(t)
	eng := patchEngine(t, store)
	if err := eng.AddTrigger(ruleFrom(t, `{name: copies,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: seen, reviewer: .[assignee]}}}`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	got := troubleOf(t, eng, "copies", TroubleFailed)
	if got.Chain != "copies" || got.Subject != prRef("1").String() {
		t.Errorf("trouble = %+v, want the rule's own action, about the pr", got)
	}
	if !strings.Contains(got.Why, "assignee") {
		t.Errorf("why = %q, want it to name the reference that did not resolve", got.Why)
	}
	if got.At == "" || got.Count < 1 {
		t.Errorf("trouble = %+v, want when, and how many times", got)
	}
	if failed, unread := eng.TroubleCounts(); failed < 1 || unread != 0 {
		t.Errorf("counts = %d failed, %d unread", failed, unread)
	}
	if stage := entity.Str(mustGet(t, store, prRef("1")), "stage"); stage != "open" {
		t.Errorf("the subject moved to %q under a patch that failed", stage)
	}

	// A rule that leaves the engine takes its trouble with it: reinstalled with the mistake
	// corrected, it reads clean.
	if err := eng.InstallTrigger(ruleFrom(t, `{name: copies,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: seen}}}`)); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if left := eng.Troubles(); len(left) != 0 {
		t.Errorf("the reinstalled rule is still in trouble: %+v", left)
	}
	entitytest.Set(t, store, prRef("2"), entity.Obj{"stage": "open"}.Payload())
	waitFor(t, store, prRef("2"), func(p entity.Payload) bool { return entity.Str(p, "stage") == "seen" }, "stage seen")
	if left := eng.Troubles(); len(left) != 0 {
		t.Errorf("the corrected rule is still in trouble: %+v", left)
	}
}

// unanswering is a store whose reads as of a commit do not happen while it is off.
type unanswering struct {
	entity.Store
	off atomic.Bool
}

func (u *unanswering) AsOf(rev int64, within ...[]string) (entity.Snapshot, error) {
	if u.off.Load() {
		return nil, errors.New("context deadline exceeded")
	}
	return u.Store.AsOf(rev, within...)
}

// A COMMIT A RULE COULD NOT READ IS KEPT ON THE RULE, with the revision: the rule did not
// evaluate it and does not look at it again, and the subject it would have acted on stays where
// it was with nothing else to say why.
func TestACommitARuleCouldNotReadIsKeptOnTheRule(t *testing.T) {
	store := &unanswering{Store: entitytest.Store(t)}
	eng := New(store, task.AutoResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.Start(ctx)
	if err := eng.AddTrigger(ruleFrom(t, `{name: advances,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: seen}}}`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	store.off.Store(true)
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	got := troubleOf(t, eng, "advances", TroubleUnread)
	if got.Rev == 0 || !strings.Contains(got.Why, "context deadline exceeded") {
		t.Errorf("trouble = %+v, want the commit's revision and the read's failure", got)
	}
	if got.Chain != "" || got.Subject != "" {
		t.Errorf("trouble = %+v: a commit that was not read is the whole rule's", got)
	}
	if failed, unread := eng.TroubleCounts(); failed != 0 || unread < 1 {
		t.Errorf("counts = %d failed, %d unread", failed, unread)
	}
}
