package trigger

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// The automatic-supervisor charter of docs/auto-supervisor.md, driven through every route it
// claims to have.
//
// TestDocCharartersInstall already checks that the doc's charter installs, but installing is
// the weaker half: what the doc actually claims is a ROUTING — this answer lands unattended,
// that one goes to a person, and no answer at all goes to a person too. A rule set can
// install and still route an approval nobody sanctioned, or strand a change at a stage no
// rule matches, and prose is where both of those hide.
//
// It reads the charter out of the doc rather than restating it, so the thing under test is
// the thing a reader pastes. A supervisor is faked with a driver that answers a fixed
// string: what matters here is the charter's response to an answer, not how the answer was
// reached.
func TestAutoSupervisorRoutes(t *testing.T) {
	charter := docCharter(t, "../docs/auto-supervisor.md", "assess")

	cases := []struct {
		name   string
		answer string // the supervisor's final message ("" = it never answered)
		tests  string // the CI fact, which the supervisor cannot write
		stage  string // where the change ends up
		gates  []string
		review map[string]string // what the subject records about the decision
	}{{
		// Cleared, and inside the remit: the fully automatic route. The gate list being
		// empty is the whole point — nobody was asked anything.
		name:   "cleared within the remit lands unattended",
		answer: `{"decision": "approve", "risk": "low", "why": "docs only"}`,
		tests:  "green",
		stage:  "landed",
		gates:  nil,
		review: map[string]string{"decision": "approve", "by": "assess"},
	}, {
		// The supervisor promotes on its own judgement.
		name:   "an escalation reaches a person",
		answer: `{"decision": "escalate", "risk": "medium", "why": "touches the auth path"}`,
		tests:  "green",
		stage:  "landed",
		gates:  []string{"sign-off@change:pr:1"},
		review: map[string]string{"decision": "escalate", "why": "touches the auth path"},
	}, {
		// The supervisor said yes at a risk it does not get to clear. The charter overrides
		// it, and `why` survives the override so the human at the gate reads both.
		name:   "an approval above the remit is promoted anyway",
		answer: `{"decision": "approve", "risk": "high", "why": "small diff"}`,
		tests:  "green",
		stage:  "landed",
		gates:  []string{"sign-off@change:pr:1"},
		review: map[string]string{
			"decision": "escalate", "why": "small diff",
			"promoted": "outside the supervisor's remit",
		},
	}, {
		// Same approval, and the fact it cannot write is the one that fails. This is the
		// case that says the remit is the charter's: nothing about the supervisor changed.
		name:   "an approval the CI fact does not support is promoted",
		answer: `{"decision": "approve", "risk": "low", "why": "docs only"}`,
		tests:  "red",
		stage:  "landed",
		gates:  []string{"sign-off@change:pr:1"},
		review: map[string]string{"decision": "escalate", "promoted": "outside the supervisor's remit"},
	}, {
		name:   "a rejection closes the change with no gate",
		answer: `{"decision": "reject", "risk": "high", "why": "wrong approach"}`,
		tests:  "green",
		stage:  "closed",
		gates:  nil,
		review: map[string]string{"decision": "reject"},
	}, {
		// The fail-safe direction: a supervisor that produced no answer fails its contract,
		// and onFailed promotes rather than leaving the change unassessed.
		name:   "a supervisor that does not answer promotes by default",
		answer: "",
		tests:  "green",
		stage:  "landed",
		gates:  []string{"sign-off@change:pr:1"},
		review: map[string]string{"decision": "escalate", "risk": "high"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := entitytest.Store(t)
			asked := &recorder{}
			eng := New(store, asked)
			eng.SetSpawner(supervisorSpawner(store, tc.answer))
			for _, tr := range charter {
				if err := eng.AddTrigger(tr); err != nil {
					t.Fatalf("rule %q does not install: %v", tr.Name, err)
				}
			}
			eng.ObserveScoped(t.Context())

			ref := entity.RefAt("change", "pr", "1")
			entitytest.Set(t, store, ref, entity.Obj{"stage": "proposed", "tests": tc.tests}.Payload())

			review := entity.Get(awaitStage(t, store, ref, tc.stage), "review")
			for k, want := range tc.review {
				if got := entity.Str(review, k); got != want {
					t.Errorf("review.%s = %q, want %q (review: %v)", k, got, want, entity.ToObj(review))
				}
			}
			if got := asked.list(); !slices.Equal(got, tc.gates) {
				t.Errorf("gates posed = %v, want %v", got, tc.gates)
			}
		})
	}
}

// awaitStage waits for the subject to settle at stage, and reports what it settled at
// instead when it does not — the charter's answer, which is what a routing failure looks
// like: a change at `assessing` with nothing coming, or `landed` when a person should have
// been asked first.
func awaitStage(t *testing.T, store entity.Store, ref entity.Ref, stage string) entity.Payload {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		e, _, _ := store.Get(ref)
		if entity.Str(e.Payload, "stage") == stage {
			return e.Payload
		}
		select {
		case <-deadline:
			t.Fatalf("%s never reached stage %q; it settled at %v", ref, stage, entity.ToObj(e.Payload))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// supervisorSpawner hosts each spawned task on a driver that answers `answer` — a stand-in
// for the doc's llm supervisor. An empty answer models a run that produced nothing, which is
// what a failed answer contract leaves behind, so onFailed is the branch that fires.
func supervisorSpawner(store entity.Store, answer string) Spawner {
	return func(id string, spec launch.Spec) task.Handle {
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		var p task.Handle
		if answer == "" {
			p = task.NewFailed(id, task.NewRegistry(), "the supervisor produced no answer")
		} else {
			p = task.NewTask(id, sup, answering{r: answer}, noExec{})
		}
		mirror.Reflect(store, id, p, mirror.Meta{
			Subject: spec.Subject, Runner: "local", Chain: spec.Chain, Prompt: spec.Prompt,
		})
		if pr, ok := p.(*task.Task); ok {
			go pr.Run(context.Background())
		}
		return p
	}
}

// recorder is a Resolver that records the questions put to it and approves them — a stand-in
// for the person at `verse gate`. Which questions were asked is the assertion: an automatic
// supervisor is only worth having if the gate it does not raise is genuinely not raised.
type recorder struct {
	mu    sync.Mutex
	asked []string
}

func (r *recorder) Resolve(_ context.Context, q task.GateQuestion, _ task.ToolUse) (task.Resolution, error) {
	r.mu.Lock()
	r.asked = append(r.asked, q.Gate+"@"+q.Subject)
	r.mu.Unlock()
	return task.Resolution{Proceed: true, By: "human:test", Question: q}, nil
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

// docCharter reads the first charter in a doc that carries a rule named `named` — so a doc
// stays the single copy of the example, and a doc edited into something that no longer
// parses fails here rather than being silently skipped (which is what a block that is "not
// a charter" looks like to TestDocCharartersInstall).
func docCharter(t *testing.T, path, named string) []Trigger {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, blk := range charterBlocks(string(src)) {
		trigs, _, _, ok, timedOut := parseWithin(blk.text, 5*time.Second)
		if timedOut {
			t.Fatalf("%s:%d: parsing this block does not terminate", path, blk.line)
		}
		if !ok {
			continue
		}
		for _, tr := range trigs {
			if tr.Name == named {
				return trigs
			}
		}
	}
	t.Fatalf("%s holds no charter with a rule named %q — the example moved, was renamed, or "+
		"stopped parsing as a charter", path, named)
	return nil
}

// The doc's own claim about credentials: with no llm creds, `verse up` builds an llm spawn as
// a task that fails immediately, so this charter routes EVERY change to a person. That is
// the fail-safe direction the doc points at, and it holds only because a failed spawn still
// runs onFailed — which is what this pins.
func TestSupervisorFailurePromotesRatherThanStrands(t *testing.T) {
	charter := docCharter(t, "../docs/auto-supervisor.md", "assess")
	var assess Trigger
	for _, tr := range charter {
		if tr.Name == "assess" {
			assess = tr
		}
	}
	if assess.Action.OnFailed == nil {
		t.Fatal("assess has no onFailed: a supervisor that cannot decide would strand its change")
	}
	review := entity.Get(assess.Action.OnFailed.Patch, "review")
	if d := entity.Str(review, "decision"); d != "escalate" {
		t.Errorf("onFailed writes decision %q, want escalate — a failure must promote, not clear", d)
	}
	if why := entity.Str(review, "why"); !strings.Contains(why, "$err") {
		t.Errorf("onFailed's why = %q, want the failure carried in it ($err)", why)
	}
}
