package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
)

// A spawn that selects its doer must state the authority that selection happens within. The
// check is at INSTALL because that is where the charter's author is: at fire time there is
// nobody to tell, and the honest default does not exist — an omitted grant is not a small
// ceiling but an unbounded one.
func TestSpawnWithProviderRequiresAGrant(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	tr := Trigger{
		Name:      "treat",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "finding"}},
		Action: Action{Spawn: &launch.Spec{
			Task:     "treat $subject",
			Provider: &launch.Provider{Capability: "treat", Select: "auto"},
		}},
	}
	err := eng.AddTrigger(tr)
	if err == nil {
		t.Fatal("a spawn with `provider:` and no `grant:` installed; there is then no ceiling to check a doer against")
	}
	// The message has to be actionable in both directions — a real ceiling, or an explicit
	// statement that unbounded is meant.
	if !strings.Contains(err.Error(), "grant: all") {
		t.Errorf("error should offer `grant: all` for a deliberately unbounded selection: %v", err)
	}

	tr.Action.Spawn.Grant = "read-only"
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("the same trigger with a ceiling should install: %v", err)
	}
}

// The ceiling matrix over the whole grant vocabulary: which doer grants may run under which
// spawn grant. It is a table rather than a couple of cases because `Grant.Subset` is
// deliberately sound-but-incomplete (predicate-for-predicate identity, or a wildcard parent —
// process/action.go), and what that costs depends entirely on how the named grants are built.
//
// Today it costs nothing: namedGrants composes read-only and no-delegate out of the SAME
// per-verb atoms, so identity finds the real subset relations and every cell below is the
// true answer. That is a property of the vocabulary, not of Subset — the day a grant carries
// a path or tool predicate (`{Verbs:[Write], Paths:[...]}` under a `{Verbs:[Write]}` ceiling),
// identity will refuse a genuine narrowing and a cell here will have to change. Update this
// table then; do not delete it.
func TestCeilingMatrixOverTheGrantVocabulary(t *testing.T) {
	seatable := map[string]map[string]bool{
		// ceiling → doer grant → may it run
		"all":         {"all": true, "no-delegate": true, "read-only": true, "none": true},
		"no-delegate": {"all": false, "no-delegate": true, "read-only": true, "none": true},
		"read-only":   {"all": false, "no-delegate": false, "read-only": true, "none": true},
		"none":        {"all": false, "no-delegate": false, "read-only": false, "none": true},
	}
	for ceiling, row := range seatable {
		for doer, want := range row {
			t.Run(ceiling+"/"+doer, func(t *testing.T) {
				store := entitytest.Store(t)
				eng := New(store, denyResolver{})
				entitytest.Set(t, store, entity.Ref{System: "provider", Kind: "treat", ID: "d"},
					entity.Obj{"driver": "llm", "grant": doer}.Payload())

				spawn := launch.Spec{Grant: ceiling, Provider: &launch.Provider{Capability: "treat", Select: "auto"}}
				options, _ := eng.eligibleProviders(spawn)
				if got := len(options) == 1; got != want {
					t.Fatalf("doer grant %q under ceiling %q: seatable = %v, want %v (%s)",
						doer, ceiling, got, want, eng.whyNoEligible(spawn))
				}
			})
		}
	}
}

// The refusal message names both sides, because the fix is in two different places: narrow
// the doer's entity, or widen the spawn's ceiling. Which one is right is a judgement the
// message cannot make, so it has to supply the facts for making it.
func TestCeilingRefusalNamesBothSides(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	entitytest.Set(t, store, entity.Ref{System: "provider", Kind: "treat", ID: "greedy"},
		entity.Obj{"driver": "llm", "grant": "all"}.Payload())

	why := eng.whyNoEligible(launch.Spec{Grant: "read-only", Provider: &launch.Provider{Capability: "treat", Select: "auto"}})
	for _, want := range []string{"greedy", `"all"`, `"read-only"`, "never widen"} {
		if !strings.Contains(why, want) {
			t.Errorf("refusal %q is missing %q", why, want)
		}
	}
}

// An unknown grant name on a doer refuses the doer rather than resolving to nothing. A typo
// must not be a way to run un-enveloped: NamedGrant already refuses an unknown name rather
// than widening, and eligibility has to inherit that stance.
func TestUnknownDoerGrantIsIneligible(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	entitytest.Set(t, store, entity.Ref{System: "provider", Kind: "treat", ID: "typo"},
		entity.Obj{"driver": "llm", "grant": "reed-only"}.Payload())

	spawn := launch.Spec{Grant: "all", Provider: &launch.Provider{Capability: "treat", Select: "auto"}}
	if options, _ := eng.eligibleProviders(spawn); len(options) != 0 {
		t.Fatalf("a doer with an unresolvable grant was offered: %v", options)
	}
	if why := eng.whyNoEligible(spawn); !strings.Contains(why, "does not resolve") {
		t.Errorf("whyNoEligible = %q, want it to say the grant does not resolve", why)
	}
}

// The fields a provider may not set are refused, not ignored. `vector` is the sharp one: it
// can take every action boundary off the docket without touching authority, so a doer that
// sets it is a doer running under supervision nobody chose. Ignoring it would leave a
// provider entity whose text says one thing and whose process does another.
func TestProviderSettingAnUnhonoredFieldIsIneligible(t *testing.T) {
	for _, field := range unhonoredProviderFields {
		t.Run(field, func(t *testing.T) {
			store := entitytest.Store(t)
			eng := New(store, denyResolver{})
			entitytest.Set(t, store, entity.Ref{System: "provider", Kind: "treat", ID: "sneaky"},
				entity.Obj{"driver": "llm", field: "auto"}.Payload())

			spawn := launch.Spec{Grant: "all", Vector: "ask", Provider: &launch.Provider{Capability: "treat", Select: "auto"}}
			if options, _ := eng.eligibleProviders(spawn); len(options) != 0 {
				t.Fatalf("a doer setting `%s` was offered: %v", field, options)
			}
			if why := eng.whyNoEligible(spawn); !strings.Contains(why, field) {
				t.Errorf("whyNoEligible = %q, want it to name `%s`", why, field)
			}
		})
	}
}

// The reason the ceiling is enforced at ELIGIBILITY and not at the bind: a ballot must only
// ever offer doers that can actually be seated. Checking after the vote would let a group
// deliberate, reach a threshold, and elect a doer that then cannot run — a decision with no
// seat, which no voter could have known they were making.
func TestBallotOffersOnlySeatableDoers(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	entitytest.Set(t, store, entity.Ref{System: "provider", Kind: "treat", ID: "careful"},
		entity.Obj{"driver": "llm", "grant": "read-only"}.Payload())
	entitytest.Set(t, store, entity.Ref{System: "provider", Kind: "treat", ID: "greedy"},
		entity.Obj{"driver": "llm", "grant": "all"}.Payload())

	spawn := launch.Spec{Grant: "read-only", Provider: &launch.Provider{Capability: "treat", Select: "vote", Need: 1}}
	subj := entity.Ref{System: "demo", Kind: "finding", ID: "ballot"}

	options, _ := eng.eligibleProviders(spawn)
	if len(options) != 1 || options[0] != "careful" {
		t.Fatalf("ballot options = %v, want [careful] — greedy exceeds the ceiling", options)
	}

	// And a ballot cast for the refused doer cannot seat it: the motion's options are the
	// eligible set, so a vote for `greedy` is a vote for something not on the ballot.
	did := motionChannelID(providerSpecSeed(subj, spawn.Provider))
	entitytest.Set(t, store, entity.Ref{System: "vote", Kind: did, ID: "x"}, entity.Obj{"choice": "greedy"}.Payload())
	entitytest.Set(t, store, entity.Ref{System: "vote", Kind: did, ID: "y"}, entity.Obj{"choice": "careful"}.Payload())

	frag, ok := eng.selectProvider(spawn, subj)
	if !ok || frag.Grant != "read-only" {
		t.Fatalf("select = (%+v, %v), want the seatable doer", frag, ok)
	}
}

// A doer that carries no grant of its own inherits the spawn's, rather than running
// un-enveloped. Absent is not empty on this axis — process.NamedGrant returns an ok for
// exactly this reason — and a doer that only narrows tools must keep the authority the
// charter gave it.
func TestDoerWithoutGrantInheritsTheSpawns(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	entitytest.Set(t, store, entity.Ref{System: "provider", Kind: "treat", ID: "quiet"},
		entity.Obj{"driver": "llm", "tools": []any{"read_file"}}.Payload())

	spawn := launch.Spec{Grant: "read-only", Provider: &launch.Provider{Capability: "treat", Select: "auto"}}
	frag, ok := eng.selectProvider(spawn, entity.Ref{System: "demo", Kind: "finding", ID: "q"})
	if !ok {
		t.Fatal("a doer with no grant of its own was refused")
	}
	got, err := spawn.Overlay(frag)
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}
	if got.Grant != "read-only" {
		t.Fatalf("grant = %q, want the spawn's read-only to survive a doer that only narrowed tools", got.Grant)
	}
}
