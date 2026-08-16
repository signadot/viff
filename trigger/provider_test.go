package trigger

import (
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
)

// auto-select picks the first eligible doer and opens NO motion: nobody is asked, so there
// is no question to pose. It used to reify the pick as a closed channel, which put
// `outcome=gpt tally=0` on the docket — an outcome attributed to a group that never voted,
// on the one surface whose contract is that its answers are votable and attributable. The
// whether-axis has always worked this way; `mode: auto` reaches no channel either.
func TestSelectProviderAuto(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "auto"}
	subj := entity.RefAt("change", "pr", "1")

	frag, ok := eng.selectProvider(launch.Spec{Provider: p}, subj)
	if !ok || frag.Driver != "gpt" {
		t.Fatalf("auto select = (%+v, %v), want driver gpt, true", frag, ok)
	}
	if _, found, _ := store.Get(entity.RefAt("motion", "channel", motionChannelID(providerSpecSeed(subj, p)))); found {
		t.Fatal("auto select opened a motion; the docket is for questions someone is asked")
	}
}

// An empty Select means auto, so it takes the same silent path.
func TestSelectProviderDefaultIsSilentAuto(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}}
	subj := entity.RefAt("change", "pr", "11")

	if frag, ok := eng.selectProvider(launch.Spec{Provider: p}, subj); !ok || frag.Driver != "gpt" {
		t.Fatalf("default select = (%+v, %v), want driver gpt, true", frag, ok)
	}
	if _, found, _ := store.Get(entity.RefAt("motion", "channel", motionChannelID(providerSpecSeed(subj, p)))); found {
		t.Fatal("an unset select opened a motion; it defaults to auto, which poses nothing")
	}
}

// vote-select picks the option that reaches the plurality threshold over the vote log.
func TestSelectProviderVotePlurality(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "vote", Need: 2}
	subj := entity.RefAt("change", "pr", "2")
	did := motionChannelID(providerSpecSeed(subj, p))

	// two ballots for claude, one for gpt → claude wins at need=2.
	entitytest.Set(t, store, entity.RefAt("vote", did, "x"), entity.Obj{"choice": "claude"}.Payload())
	entitytest.Set(t, store, entity.RefAt("vote", did, "y"), entity.Obj{"choice": "claude"}.Payload())
	entitytest.Set(t, store, entity.RefAt("vote", did, "z"), entity.Obj{"choice": "gpt"}.Payload())

	frag, ok := eng.selectProvider(launch.Spec{Provider: p}, subj)
	if !ok || frag.Driver != "claude" {
		t.Fatalf("vote select = (%+v, %v), want driver claude, true", frag, ok)
	}
}

// a ballot for a non-eligible option does not count: a rogue vote can't win; an eligible
// option reaching the threshold does.
func TestPluralityIgnoresIneligible(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "vote", Need: 1}
	subj := entity.RefAt("change", "pr", "3")
	did := motionChannelID(providerSpecSeed(subj, p))
	entitytest.Set(t, store, entity.RefAt("vote", did, "x"), entity.Obj{"choice": "rogue"}.Payload()) // ineligible
	entitytest.Set(t, store, entity.RefAt("vote", did, "y"), entity.Obj{"choice": "gpt"}.Payload())   // eligible → wins at need=1

	frag, ok := eng.selectProvider(launch.Spec{Provider: p}, subj)
	if !ok || frag.Driver != "gpt" {
		t.Fatalf("select = (%+v, %v), want driver gpt, true — a rogue (ineligible) ballot must not win", frag, ok)
	}
}

// ask-select: a party picks — the first valid pick decides (need is forced to 1).
func TestSelectProviderAsk(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "ask"}
	subj := entity.RefAt("change", "pr", "4")
	did := motionChannelID(providerSpecSeed(subj, p))

	entitytest.Set(t, store, entity.RefAt("vote", did, "reviewer"), entity.Obj{"choice": "claude"}.Payload())
	frag, ok := eng.selectProvider(launch.Spec{Provider: p}, subj)
	if !ok || frag.Driver != "claude" {
		t.Fatalf("ask select = (%+v, %v), want driver claude, true — the first pick decides", frag, ok)
	}
}

// Registry eligibility: providers are entities; a tony match over their attributes filters
// the eligible set. Here tier=premium admits gpt & claude (not the basic one); auto picks
// the first (by id), and the option resolves to that provider's `driver`.
func TestSelectProviderRegistryMatch(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	entitytest.Set(t, store, entity.RefAt("provider", "review", "gpt"), entity.Obj{"driver": "llm-gpt", "tier": "premium"}.Payload())
	entitytest.Set(t, store, entity.RefAt("provider", "review", "claude"), entity.Obj{"driver": "llm-claude", "tier": "premium"}.Payload())
	entitytest.Set(t, store, entity.RefAt("provider", "review", "cheapo"), entity.Obj{"driver": "scripted", "tier": "basic"}.Payload())

	p := &launch.Provider{Capability: "review", Match: map[string]any{"tier": "premium"}, Select: "auto"}
	subj := entity.RefAt("change", "pr", "9")

	frag, ok := eng.selectProvider(launch.Spec{Provider: p}, subj)
	if !ok || frag.Driver != "llm-claude" { // options sorted by id: claude, gpt → auto picks claude
		t.Fatalf("registry auto select = (%+v, %v), want driver llm-claude, true", frag, ok)
	}
	// Read the filter off eligibleProviders rather than off a motion: an auto select opens
	// none, and the eligible set is what the match produced regardless of which dial reads it.
	options, _ := eng.eligibleProviders(launch.Spec{Provider: p})
	if len(options) != 2 || options[0] != "claude" || options[1] != "gpt" {
		t.Fatalf("eligible options = %v, want [claude gpt] (the basic tier filtered out)", options)
	}
}

// A vote-select with a deadline that passes without reaching the threshold resolves to no
// pick — the who-axis analog of an `until` gate defaulting on its deadline. It must return
// (,false) promptly rather than block open-endedly (the pre-deadline hang).
func TestSelectProviderVoteDeadline(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "vote", Need: 2, Within: "60ms"}
	subj := entity.RefAt("change", "pr", "dl")

	done := make(chan struct{})
	var frag launch.Fragment
	var ok bool
	go func() { frag, ok = eng.selectProvider(launch.Spec{Provider: p}, subj); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("selectProvider hung past its deadline (should default to no pick)")
	}
	if ok || !frag.IsZero() {
		t.Fatalf("deadline select = (%+v, %v), want (zero, false) — no pick by the deadline", frag, ok)
	}
	// The channel records when it closes, so `verse docket` can show it.
	ch, _, _ := store.Get(entity.RefAt("motion", "channel", motionChannelID(providerSpecSeed(subj, p))))
	if _, stamped := entity.ToObj(ch.Payload)["deadline_at"]; !stamped {
		t.Fatalf("select channel missing deadline_at: %+v", ch.Payload)
	}
}

// An empty eligible set opens no motion and spawns nothing, so from outside the firing is
// indistinguishable from a trigger that never matched. The three causes are fixed in three
// different places, so the message has to say which one it is — that is the whole reason
// this path speaks at all.
func TestWhyNoEligibleNamesTheFix(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, nil)

	// Nothing registered: the answer is "go register a doer".
	p := &launch.Provider{Capability: "review", Match: map[string]any{"tier": "premium"}}
	got := eng.whyNoEligible(launch.Spec{Provider: p})
	if !strings.Contains(got, "no provider:review:* is registered") {
		t.Errorf("empty registry: %q", got)
	}
	if !strings.Contains(got, "verse state put provider:review:") {
		t.Errorf("empty registry should show how to register one: %q", got)
	}

	// Registered but excluded: the answer is "loosen the match", the opposite fix — and
	// the match is echoed in tony, as the charter spells it, not as Go prints a map.
	entitytest.Set(t, store, entity.RefAt("provider", "review", "cheapo"),
		entity.Obj{"driver": "scripted", "tier": "basic"}.Payload())
	got = eng.whyNoEligible(launch.Spec{Provider: p})
	if !strings.Contains(got, "all 1 registered") {
		t.Errorf("excluded: %q", got)
	}
	if !strings.Contains(got, "tier: premium") {
		t.Errorf("excluded should echo the match in tony: %q", got)
	}

	// Neither form declared: the spec itself is incomplete.
	if got := eng.whyNoEligible(launch.Spec{Provider: &launch.Provider{Select: "vote"}}); !strings.Contains(got, "no eligible doers") {
		t.Errorf("empty provider: %q", got)
	}
}
