package trigger

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// A gate asks its question of a WORLD, and the answer applies to that world. When the subject
// leaves the rule's selection while the question stands, the question is moot: it leaves the
// docket saying why, nothing is decided, and an answer that arrives afterwards finds nothing.
//
// The failure this pins cost a piece of work on staging, twice over. A merge proposal was
// swept to `state: stale` — which its rule's `!not.has-path state` excludes — and the gate,
// watching only its ballots, carried on the one already cast and merged a branch the sweep had
// retired.

// dropping is a registry that records what leaves it unanswered, and why.
type dropping struct {
	*task.Registry
	mu    sync.Mutex
	drops map[string]string
}

func newDropping() *dropping {
	d := &dropping{Registry: task.NewRegistry(), drops: map[string]string{}}
	d.OnDrop(func(q task.GateQuestion, why string) {
		d.mu.Lock()
		d.drops[q.ID] = why
		d.mu.Unlock()
	})
	return d
}

func (d *dropping) droppedWhy(id string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	why, ok := d.drops[id]
	return why, ok
}

const mootProposalCharter = `
- name: merge-proposal
  condition:
    subject: {path: "merge.proposal.*", value: !not.has-path state}
  gates:
  - name: merge-to-main
    mode: ask
    need: 2
    onDenied: {patch: {state: declined}}
  action:
    patch: {merged: true}
`

func TestAQuestionIsWithdrawnWhenItsSubjectLeavesTheSelection(t *testing.T) {
	store := entitytest.Store(t)
	reg := newDropping()
	eng := New(store, reg)
	if err := eng.AddTrigger(mustParseCharter(t, mootProposalCharter)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	subj := entity.RefAt("merge", "proposal", "p1")
	entitytest.Set(t, store, subj, entity.Obj{"branch": "auto/x"}.Payload())
	id := waitQuestionID(t, reg.Registry)

	// The sweep marks it stale while the gate stands open. No vote, no restart.
	entitytest.Set(t, store, subj, entity.Obj{"state": "stale"}.Payload())

	deadline := time.After(3 * time.Second)
	for {
		if why, ok := reg.droppedWhy(id); ok {
			if !strings.Contains(why, "no longer selects") {
				t.Errorf("why = %q, want the rule's own words for a moot question", why)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the question is still standing after its subject left the selection: %+v", reg.Pending())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Then somebody answers what the docket listed a moment ago: nothing is there to take it.
	if reg.Answer(id, task.Choose("proceed", "me")) {
		t.Fatal("a withdrawn question took an answer")
	}
	state := statusOf(t, store, subj)
	if state["merged"] == true {
		t.Fatalf("the action ran on a stale subject: %+v", state)
	}
	// And a withdrawal is not a refusal — nobody said no, so `onDenied` must not have run.
	if state["state"] != "stale" {
		t.Fatalf("the subject = %+v, want it left as the sweep wrote it (onDenied ran on a "+
			"question nobody refused)", state)
	}
}

// The other half, so this is not a test that passes by refusing everything: a subject that
// stays inside the selection carries on exactly as before, and the answer decides.
func TestAQuestionStillCarriesWhileItsSubjectHolds(t *testing.T) {
	store := entitytest.Store(t)
	reg := newDropping()
	eng := New(store, reg)
	if err := eng.AddTrigger(mustParseCharter(t, mootProposalCharter)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	subj := entity.RefAt("merge", "proposal", "p2")
	entitytest.Set(t, store, subj, entity.Obj{"branch": "auto/y"}.Payload())
	id := waitQuestionID(t, reg.Registry)

	// A field that is not what the rule selects on: the question stands through it.
	entitytest.Set(t, store, subj, entity.Obj{"note": "still going"}.Payload())
	time.Sleep(300 * time.Millisecond)
	if _, dropped := reg.droppedWhy(id); dropped {
		t.Fatal("the question was withdrawn by a change that kept its subject selected")
	}
	if !reg.Answer(id, task.Choose("proceed", "me")) {
		t.Fatal("the question was not there to answer")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if statusOf(t, store, subj)["merged"] == true {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the gate did not carry: subject = %+v", statusOf(t, store, subj))
}
