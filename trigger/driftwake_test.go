package trigger

import (
	"context"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// claimCharter is the cursor shape every drive uses: a LITERAL subject whose condition reads
// somewhere else and claims when the two disagree. What wakes it is the read moving — nothing
// about the subject changes at all.
const claimCharter = `
- name: claim
  condition:
    subject:
      path: "drift.cursor.at"
      value: !let
        let: [{tip: !get-path {at: "git.verse.head", path: sha}}]
        in:  {stage: idle, sha: !not .[tip]}
  action:
    patch: {stage: claimed}
`

// A package engine watches one subtree per thing its rules read (Engine.ObserveScoped), which is
// how versed runs an installed charter — a different path from the whole-store Observe the other
// tests take, and the one a deployment actually uses.
func TestALiteralSubjectWakesWhenItsConditionReadMoves(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.Confine(SubtreeAt("drift"))
	eng.SetReads([]Subtree{SubtreeAt("git")})

	head := entity.RefAt("git", "verse", "head")
	at := entity.RefAt("drift", "cursor", "at")
	setPayload(t, store, head, `{sha: aaa111}`)
	setPayload(t, store, at, `{sha: aaa111, stage: idle}`)

	if err := eng.AddTrigger(mustParseCharter(t, claimCharter)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	setPayload(t, store, head, `{sha: bbb222}`)
	waitField(t, store, at, "stage", "claimed")
}

// THE ORDER A DEPLOYMENT ACTUALLY HAS: the charter is installed before the source that fills in
// what it reads, so the read's watch is opened where NOTHING STANDS YET. The node then appears
// (the source's first pass) and later moves, and the move is the one that must wake the rule.
func TestALiteralSubjectWakesWhenAReadThatDidNotExistYetMoves(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.Confine(SubtreeAt("drift"))
	eng.SetReads([]Subtree{SubtreeAt("git")})

	head := entity.RefAt("git", "verse", "head")
	at := entity.RefAt("drift", "cursor", "at")
	setPayload(t, store, at, `{sha: aaa111, stage: idle}`) // the cursor, armed at a known sha

	if err := eng.AddTrigger(mustParseCharter(t, claimCharter)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)

	// The source's first pass: the head appears, carrying the sha the cursor already names, so
	// there is nothing to claim yet.
	setPayload(t, store, head, `{sha: aaa111}`)
	// And then it moves, which is the commit the rule exists to react to.
	setPayload(t, store, head, `{sha: bbb222}`)
	waitField(t, store, at, "stage", "claimed")
}
