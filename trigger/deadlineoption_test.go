package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A DEADLINE'S DEFAULT OPTION SAYS SO ON THE BALLOT.
//
// `!until [!quorum 2, !deadline "24h"]` decides on the quorum or on the clock. `deny` is the
// deadline's default and has no threshold of its own, so no number of ballots for it closes the
// motion — two people denied and it stayed open with the subject untouched
// (git-issue 88r53xm6h12ks6bng9n0). Rendered beside the approve option with no `need`, it read
// as the symmetric twin of a choice that can decide.
//
// It is marked rather than removed: a denial is worth recording even when it cannot close
// anything, and dissent on the record is what a vote log is for.
func TestADeadlinesDefaultOptionIsMarkedOnTheBallot(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: ship
  condition: {subject: {system: demo, kind: rel, value: {stage: open}}}
  gates: [{name: two-of-us, mode: ask, formula: !until [!quorum 2, !deadline "24h"]}]
  action: {patch: {stage: shipped}}
`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	setPayload(t, store, entity.RefAt("demo", "rel", "9"), `{stage: open}`)

	opts := waitOptions(t, store)
	var sawApprove, sawDefault bool
	for _, o := range opts {
		om, _ := o.(map[string]any)
		switch om["id"] {
		case "proceed":
			sawApprove = true
			if om["need"] == nil {
				t.Error("the option that CAN decide lost its threshold")
			}
			if om["reachedBy"] != nil {
				t.Errorf("proceed is marked %v — it is reached by its quorum", om["reachedBy"])
			}
		case "deny":
			sawDefault = true
			if om["reachedBy"] != "deadline" {
				t.Errorf("deny renders as %+v — a reader cannot tell it from a choice that "+
					"can reach a threshold", om)
			}
			if om["need"] != nil {
				t.Errorf("deny carries need=%v, which nothing tallies", om["need"])
			}
		}
	}
	if !sawApprove || !sawDefault {
		t.Fatalf("the ballot did not offer both options: %+v", opts)
	}
}

// A motion with no deadline marks nothing: both options carry a formula, and either can close it.
func TestAMotionWithoutADeadlineMarksNoOption(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: ship
  condition: {subject: {system: demo, kind: rel, value: {stage: open}}}
  gates: [{name: two-of-us, mode: ask, formula: !quorum 1}]
  action: {patch: {stage: shipped}}
`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	setPayload(t, store, entity.RefAt("demo", "rel", "10"), `{stage: open}`)

	for _, o := range waitOptions(t, store) {
		om, _ := o.(map[string]any)
		if om["reachedBy"] != nil {
			t.Errorf("%+v is marked, and there is no deadline to reach it by", om)
		}
	}
}

func waitOptions(t *testing.T, store entity.Store) []any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		for _, e := range entity.ListUnder(store, func(r entity.Ref) bool {
			return r.Reserved() && r.System() == motionSystem
		}, entity.VerseRoot, motionSystem) {
			if opts, ok := entity.ToObj(e.Payload)["options"].([]any); ok && len(opts) > 0 {
				return opts
			}
		}
		select {
		case <-deadline:
			t.Fatal("no motion with options opened")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
