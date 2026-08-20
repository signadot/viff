package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A gate asks its question of a WORLD, and the answer applies to that world. When the subject
// leaves the rule's selection while the motion stands open, the question is moot and the ballot
// cast into it must not carry.
//
// The failure this pins cost a piece of work on staging, twice over. A merge proposal was swept
// to `state: stale` — which its rule's `!not.has-path state` excludes — and the motion, still
// watching only its vote log, reached quorum on the ballot already there, committed `proceed`,
// and merged a branch the sweep had retired. The revoke guard did not help: it rechecks the
// facts the verdict rests on (the ballots still stand) and knows nothing about the state that
// raised the question.
func TestAMotionIsWithdrawnWhenItsSubjectLeavesTheSelection(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	subj := entity.RefAt("merge", "proposal", "p1")
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: merge-proposal
  condition:
    subject: {path: [merge, proposal, *], value: !not.has-path state}
  gates:
  - name: merge-to-main
    mode: ask
    formula: !quorum 1
    onDenied: {patch: {state: declined}}
  action:
    patch: {merged: true}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, subj, entity.Obj{"branch": "auto/x"}.Payload())
	mid := waitMotion(t, store)

	// The sweep marks it stale while the gate stands open, and the monitor is still live.
	entitytest.Set(t, store, subj, entity.Obj{"state": "stale"}.Payload())
	// Then somebody votes approve on what the docket still lists.
	entitytest.Set(t, store, entity.RefAt("vote", mid, "me"),
		entity.Obj{"choice": "approve", "by": "me"}.Payload())

	ref := entity.VerseRef(motionSystem, motionChannelKind, mid)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if statusOf(t, store, ref)["status"] == motionOrphaned {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := statusOf(t, store, ref)
	if got["status"] != motionOrphaned {
		t.Fatalf("status = %v, want the motion withdrawn: %+v", got["status"], got)
	}
	if got["outcome"] != nil {
		t.Fatalf("a moot question was answered anyway: %+v", got)
	}
	// The claim: the action did not run on a subject the charter had already moved on from.
	state := statusOf(t, store, subj)
	if state["merged"] == true {
		t.Fatalf("the action ran on a stale subject: %+v", state)
	}
	// And a withdrawal is not a refusal — nobody voted no, so `onDenied` must not have run.
	if state["state"] != "stale" {
		t.Fatalf("the subject = %+v, want it left as the sweep wrote it (onDenied ran on a "+
			"question nobody refused)", state)
	}
}

// The other half, so this is not a test that passes by refusing everything: a subject that
// stays inside the selection carries on exactly as before, and the ballot decides.
func TestAMotionStillCarriesWhileItsSubjectHolds(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	subj := entity.RefAt("merge", "proposal", "p2")
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: merge-proposal
  condition:
    subject: {path: [merge, proposal, *], value: !not.has-path state}
  gates:
  - {name: merge-to-main, mode: ask, formula: !quorum 1}
  action:
    patch: {state: merged}
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, subj, entity.Obj{"branch": "auto/y"}.Payload())
	mid := waitMotion(t, store)
	entitytest.Set(t, store, entity.RefAt("vote", mid, "me"),
		entity.Obj{"choice": "approve", "by": "me"}.Payload())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if field(t, store, subj, "state") == "merged" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the gate did not carry: subject = %+v, motion = %+v",
		statusOf(t, store, subj),
		statusOf(t, store, entity.VerseRef(motionSystem, motionChannelKind, mid)))
}

// waitMotion waits for a motion channel to open and returns its id.
func waitMotion(t *testing.T, store entity.Store) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range store.List(func(r entity.Ref) bool {
			return r.Reserved() && r.System() == motionSystem && r.Kind() == motionChannelKind
		}) {
			return e.Ref.ID()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no motion opened")
	return ""
}
