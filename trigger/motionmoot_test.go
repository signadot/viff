package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

const mootCharter = `
- name: ship
  condition: {subject: {system: demo, kind: rel, value: {stage: open}}}
  gates: [{name: two-of-us, mode: ask, formula: !until [!quorum 2, !deadline "24h"]}]
  action: {patch: {stage: shipped}}
`

func motionStatus(t *testing.T, store entity.Store) (string, string) {
	t.Helper()
	for _, e := range entity.ListUnder(store, func(r entity.Ref) bool {
		return r.Reserved() && r.System() == motionSystem
	}, entity.VerseRoot, motionSystem) {
		o := entity.ToObj(e.Payload)
		status, _ := o["status"].(string)
		why, _ := o["why"].(string)
		return status, why
	}
	return "", ""
}

// A MOTION IS WITHDRAWN WITH NOBODY VOTING, at the change that made it moot rather than at the
// next restart.
//
// TestAMotionIsWithdrawnWhenItsSubjectLeavesTheSelection is the sibling and the other half: it
// withdraws on the BALLOT, at the moment somebody votes into a moot question. This is the case
// where nobody does — where the docket simply carries an entry nobody can honestly answer.
//
// The mechanism was written and correct and was only ever asked on the resume path, so a stale
// question was discovered by restarting rather than by the change that made it stale — it sat on
// the docket offering a decision about work that would never be taken (git-issue
// 1my63vm6h12ks6bng9n0). The monitor watches the subject's own subtree now, beside the vote log.
func TestAMotionIsWithdrawnWithNobodyVoting(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, mootCharter)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	rel := entity.RefAt("demo", "rel", "3")
	setPayload(t, store, rel, `{stage: open}`)

	deadline := time.After(5 * time.Second)
	for {
		if s, _ := motionStatus(t, store); s == "open" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no motion opened")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// It no longer selects. No vote, no restart.
	setPayload(t, store, rel, `{stage: withdrawn}`)

	deadline = time.After(5 * time.Second)
	for {
		s, why := motionStatus(t, store)
		if s == "orphaned" {
			if why == "" {
				t.Error("a withdrawn motion must say why it was withdrawn")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the motion is %q after its subject left the selection — still offering a "+
				"decision about work that will never be taken", s)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// AND A MOTION WHOSE SUBJECT IS STILL SELECTED STAYS OPEN through a change that touches it.
// Withdrawing one wrongly takes a live question away from the people it is in front of, which
// is why stillSelects answers TRUE when it cannot tell.
func TestAMotionSurvivesAChangeThatKeepsItSelected(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, mootCharter)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	rel := entity.RefAt("demo", "rel", "4")
	setPayload(t, store, rel, `{stage: open}`)
	deadline := time.After(5 * time.Second)
	for {
		if s, _ := motionStatus(t, store); s == "open" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no motion opened")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// A field that is not what the rule selects on.
	setPayload(t, store, rel, `{stage: open, note: "still going"}`)
	time.Sleep(500 * time.Millisecond)
	if s, why := motionStatus(t, store); s != "open" {
		t.Fatalf("the motion is %q (%s) — its subject is still selected", s, why)
	}
}
