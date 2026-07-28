package trigger

import (
	"context"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// Attribution: an outcome has to be readable back to the DOER that produced it, not only to
// the driver that ran it. Two doers sharing `driver: llm` and differing in frame, tools and
// grant is the case provider selection exists for, so a process entity — and any fact a
// completion hook writes from it — that names only the driver reports two quite different
// spawns as the same work by the same agent.
//
// The two halves are tested together because either alone is insufficient. `doer` on the
// process entity makes the pick observable; `$doer` bindable in a completion hook is what
// lets a charter carry the pick onto the SUBJECT, where scoring reads it. Selection landing
// and selection being learnable from are exactly that difference.
func TestOutcomeIsAttributableToTheDoerThatWon(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	// The spawner mirrors the envelope the way versed's does (Server.spawnerFor): the
	// selection's answer travels on the spec, and the mirror is what publishes it.
	eng.SetSpawner(func(id string, spec launch.Spec) process.Handle {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		p := process.NewProcess(id, sup, answering{r: "SHIPIT"}, noExec{})
		mirror.Reflect(store, id, p, mirror.Meta{
			Subject: spec.Subject, Runner: "local", Chain: spec.Chain, Task: spec.Task,
			Doer: spec.Doer, Grant: spec.Grant, Tools: spec.Tools,
		})
		go p.Run(context.Background())
		return p
	})
	eng.ObserveScoped(t.Context())

	// Two doers that differ in everything a Fragment carries EXCEPT the driver — so the
	// driver cannot distinguish them and only the doer's own id can.
	entitytest.Set(t, store, entity.Ref{System: "provider", Kind: "review", ID: "careful"},
		entity.Obj{"driver": "llm", "tier": "careful", "grant": "read-only"}.Payload())
	entitytest.Set(t, store, entity.Ref{System: "provider", Kind: "review", ID: "fast"},
		entity.Obj{"driver": "llm", "tier": "fast", "grant": "no-delegate"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{
				Task:     "review $subject",
				Grant:    "all", // the ceiling a governed selection happens within
				Provider: &launch.Provider{Capability: "review", Match: map[string]any{"tier": "careful"}, Select: "auto"},
			},
			// The scoring join: the outcome and the doer that produced it, landed on the
			// subject together. Neither is useful for learning without the other.
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed", "by": "$doer", "outcome": "$result"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	// The mirror half: the process says WHICH doer was selected, beside the driver that ran it.
	procRef := mirror.Ref(spawnID(chainID("review"), prRef("1")))
	waitFor(t, store, procRef, func(p entity.Payload) bool {
		return entity.Str(p, "doer") == "provider:review:careful"
	}, `doer = provider:review:careful`)

	// The binding half: a completion hook can read it, so the attribution reaches the subject.
	waitFor(t, store, prRef("1"), func(p entity.Payload) bool {
		return entity.Str(p, "by") == "provider:review:careful" && entity.Str(p, "outcome") == "SHIPIT"
	}, `by = provider:review:careful, outcome = SHIPIT`)
}
