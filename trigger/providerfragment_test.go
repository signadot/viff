package trigger

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// registerTreatments writes the two doers the case in docs/provider-selection.md
// turns on: a fixer and a memo-writer that run the SAME driver and differ in everything else.
// Before a selection could bind a fragment, that difference had nowhere to live, so a charter
// wanting both had to write two triggers and freeze each one's tools and grant at authoring
// time — the registry, longhand.
func registerTreatments(t *testing.T, store entity.Store) {
	t.Helper()
	entitytest.Set(t, store, entity.RefAt("provider", "treat", "fixer"), entity.Obj{
		"driver": "llm", "risk": "low",
		"tools": []any{"read_file", "list_dir", "write_file", "run_bash"},
		"grant": "no-delegate",
		"frame": "You apply a confirmed one-line fix, then build and run the named test.",
	}.Payload())
	entitytest.Set(t, store, entity.RefAt("provider", "treat", "memo"), entity.Obj{
		"driver": "llm", "risk": "high",
		"tools": []any{"read_file", "list_dir"},
		"grant": "read-only",
		"frame": "You put the decision in front of a person.",
	}.Payload())
}

// A selection binds what the winner IS, not only which runtime runs it: the same charter,
// firing on two findings that differ by one attribute, produces two spawns that differ in
// tools, grant and frame — and agree on the driver.
func TestProviderFragmentReachesTheSpawn(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	registerTreatments(t, store)

	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		specs <- spec
		return nil
	})

	// One trigger, one spawn block. Which doer treats a finding is the registry's answer,
	// and the risk attribute the classifier wrote is what asks the question.
	for _, risk := range []string{"low", "high"} {
		if err := eng.AddTrigger(Trigger{
			Name:      "treat-" + risk,
			Condition: Condition{Subject: Match{"system": "demo", "kind": "finding", "value": map[string]any{"risk": risk}}},
			Action: Action{
				OnDone: &Action{Patch: entity.Obj{"risk": "treated"}.Payload()},
				Spawn: &launch.Spec{
					Prompt: "treat $subject",
					Frame:  "a frame the doer overrides",
					Tools:  []string{"read_file", "list_dir", "write_file", "run_bash"},
					Grant:  "no-delegate",
					Provider: &launch.Provider{
						Capability: "treat",
						Match:      map[string]any{"risk": risk},
						Select:     "auto",
					},
				}},
		}); err != nil {
			t.Fatalf("AddTrigger %s: %v", risk, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	got := map[string]launch.Spec{}
	entitytest.Set(t, store, entity.RefAt("demo", "finding", "1"), entity.Obj{"risk": "low"}.Payload())
	entitytest.Set(t, store, entity.RefAt("demo", "finding", "2"), entity.Obj{"risk": "high"}.Payload())
	// Read until both subjects have arrived rather than exactly twice: this charter spawns
	// without a latch, so it is re-evaluated and spawns again at each commit touching
	// demo:finding — which is what the install-time warning says about it. What the test is
	// about is which fragment reaches which spawn, and that is per subject.
	for len(got) < 2 {
		select {
		case s := <-specs:
			got[s.Subject] = s
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of 2 subjects spawned: %+v", len(got), got)
		}
	}

	fix, memo := got["demo:finding:1"], got["demo:finding:2"]
	if fix.Driver != "llm" || memo.Driver != "llm" {
		t.Fatalf("drivers = %q / %q, want both llm — the point is that the driver is what they SHARE", fix.Driver, memo.Driver)
	}
	if !reflect.DeepEqual(fix.Tools, []string{"read_file", "list_dir", "write_file", "run_bash"}) {
		t.Errorf("fixer tools = %v, want the full set the spawn offered", fix.Tools)
	}
	if !reflect.DeepEqual(memo.Tools, []string{"read_file", "list_dir"}) {
		t.Errorf("memo tools = %v, want the doer's narrower set", memo.Tools)
	}
	if fix.Grant != "no-delegate" || memo.Grant != "read-only" {
		t.Errorf("grants = %q / %q, want no-delegate / read-only", fix.Grant, memo.Grant)
	}
	if fix.Frame == memo.Frame || memo.Frame != "You put the decision in front of a person." {
		t.Errorf("frames did not come from the doers: %q / %q", fix.Frame, memo.Frame)
	}
	// The task is the charter's, always: a doer varies how the work is done, not what it is.
	if fix.Prompt != "treat demo:finding:1" {
		t.Errorf("task = %q, want the charter's, bound", fix.Prompt)
	}
	// And each spawn says WHICH doer it got. The driver cannot answer that here — both ran
	// `llm` — which is the whole reason a task carries the doer separately.
	if fix.Doer != "provider:treat:fixer" || memo.Doer != "provider:treat:memo" {
		t.Errorf("doers = %q / %q, want the two provider refs", fix.Doer, memo.Doer)
	}
}

// An inline `eligible` list is a list of NAMES, and a name has no attributes to carry — so it
// contributes a driver and nothing else, exactly as it did before fragments existed.
func TestInlineEligibleCarriesOnlyTheDriver(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "auto"}

	frag, ok := eng.selectProvider(launch.Spec{Provider: p}, entity.RefAt("change", "pr", "inline"))
	if !ok {
		t.Fatal("inline eligible did not select")
	}
	// Its name is its identity AND its driver — an inline list names no entity, so the doer
	// a task reports is the same string the charter typed.
	if !reflect.DeepEqual(frag, launch.Fragment{ID: "gpt", Driver: "gpt"}) {
		t.Fatalf("inline fragment = %+v, want a bare driver", frag)
	}
}

// A provider entity with only attributes contributes only its driver, which is what every
// provider written before this feature looks like. The registry does not need rewriting.
func TestAttributeOnlyProviderStillWorks(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	entitytest.Set(t, store, entity.RefAt("provider", "review", "claude"),
		entity.Obj{"driver": "llm-claude", "tier": "premium"}.Payload())

	frag, ok := eng.selectProvider(launch.Spec{Provider: &launch.Provider{Capability: "review", Select: "auto"}},
		entity.RefAt("change", "pr", "old"))
	if !ok {
		t.Fatal("attribute-only provider did not select")
	}
	if frag.Driver != "llm-claude" || frag.Frame != "" || frag.Grant != "" || len(frag.Tools) != 0 {
		t.Fatalf("fragment = %+v, want the driver alone", frag)
	}
}

// A doer whose tools share nothing with the spawn's is never offered. The spawn cannot be
// narrowed to "no tools" — an empty list means ALL of them — so rather than resolve that at
// launch, the doer is ineligible: it cannot be picked, cannot be voted for, and the empty
// ballot says why.
func TestDisjointToolsIsIneligible(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	entitytest.Set(t, store, entity.RefAt("provider", "treat", "shell"),
		entity.Obj{"driver": "llm", "tools": []any{"run_bash"}}.Payload())

	spawned := make(chan launch.Spec, 1)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		spawned <- spec
		return nil
	})

	spawn := launch.Spec{
		Prompt:   "treat it",
		Tools:    []string{"read_file"}, // the spawn offers a tool the doer does not
		Grant:    "read-only",
		Provider: &launch.Provider{Capability: "treat", Select: "auto"},
	}
	if options, _ := eng.eligibleProviders(spawn); len(options) != 0 {
		t.Fatalf("eligible = %v, want none — the doer and the spawn share no tool", options)
	}
	if why := eng.whyNoEligible(spawn); !strings.Contains(why, "share no tool") {
		t.Errorf("whyNoEligible = %q, want it to name the tool conflict", why)
	}

	subj := entity.RefAt("demo", "finding", "d")
	if _, err := (Action{Spawn: &spawn}).applyOne("treat", occasion{ev: entity.Event{Ref: subj}}, subj, eng, fromSubject); err != nil {
		t.Fatalf("applyOne = %v; an unresolvable selection is a non-spawn, not a failure", err)
	}
	select {
	case s := <-spawned:
		t.Fatalf("it spawned anyway: %+v", s)
	default:
	}
}
