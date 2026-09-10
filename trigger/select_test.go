package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// A provider selection poses a QUESTION whose options are the eligible doers; whoever answers
// settles it, and the engine launches the doer the answer names. The tally and the clock are
// the answer path's (versed), so what these pin is what is asked and what is done with the
// one answer that comes back.

// selecting runs a selection against a registry and returns the question it posed.
func selecting(t *testing.T, eng *Engine, reg *task.Registry, spawn launch.Spec, subj entity.Ref) (chan launch.Fragment, chan bool, task.GateQuestion) {
	t.Helper()
	frag, ok := make(chan launch.Fragment, 1), make(chan bool, 1)
	go func() {
		f, o := eng.selectProvider(spawn, subj)
		frag <- f
		ok <- o
	}()
	deadline := time.After(3 * time.Second)
	for {
		if p := reg.Pending(); len(p) > 0 {
			return frag, ok, p[0].Question
		}
		select {
		case <-deadline:
			t.Fatal("the selection posed no question")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// auto-select picks the first eligible doer and poses NOTHING: nobody is asked, so there is
// no question. It used to reify the pick as a closed channel, which put `outcome=gpt tally=0`
// on the docket — an outcome attributed to a group that never voted.
func TestSelectProviderAuto(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "auto"}

	frag, ok := eng.selectProvider(launch.Spec{Provider: p}, entity.RefAt("change", "pr", "1"))
	if !ok || frag.Driver != "gpt" {
		t.Fatalf("auto select = (%+v, %v), want driver gpt, true", frag, ok)
	}
	if n := len(reg.Pending()); n != 0 {
		t.Fatalf("auto select posed %d questions; the docket is for questions someone is asked", n)
	}
}

// An empty Select means auto, so it takes the same silent path.
func TestSelectProviderDefaultIsSilentAuto(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}}
	if frag, ok := eng.selectProvider(launch.Spec{Provider: p}, entity.RefAt("change", "pr", "11")); !ok || frag.Driver != "gpt" {
		t.Fatalf("default select = (%+v, %v), want driver gpt, true", frag, ok)
	}
	if n := len(reg.Pending()); n != 0 {
		t.Fatalf("an unset select posed %d questions; it defaults to auto, which poses nothing", n)
	}
}

// A vote poses every eligible doer at the threshold, and the answer names the winner.
func TestSelectProviderVotePosesTheDoers(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "vote", Need: 2}
	spawn := launch.Spec{Provider: p, Chain: "review"}
	subj := entity.RefAt("change", "pr", "2")

	frag, ok, q := selecting(t, eng, reg, spawn, subj)
	if q.ID != selectID("review", subj, p) || q.Chain != "review" || q.Mode != task.Ask {
		t.Fatalf("posed %+v, want the derived id, the chain and ask", q)
	}
	if ids := strings.Join(task.OptionIDs(q.Options), ","); ids != "gpt,claude" {
		t.Fatalf("options = %s, want the eligible doers as the spawn listed them", ids)
	}
	for _, o := range q.Options {
		if o.Need != 2 {
			t.Fatalf("option %s needs %d, want the vote's 2", o.ID, o.Need)
		}
	}
	if !reg.Answer(q.ID, task.Choose("claude", "human:x")) {
		t.Fatal("the question was not there to answer")
	}
	if f, o := <-frag, <-ok; !o || f.Driver != "claude" {
		t.Fatalf("vote select = (%+v, %v), want driver claude, true", f, o)
	}
}

// ask-select is a vote with a threshold of one: one party picks.
func TestSelectProviderAsk(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "ask", Need: 3}
	frag, ok, q := selecting(t, eng, reg, launch.Spec{Provider: p, Chain: "review"}, entity.RefAt("change", "pr", "4"))
	for _, o := range q.Options {
		if o.Need != 1 {
			t.Fatalf("option %s needs %d under ask, want 1", o.ID, o.Need)
		}
	}
	reg.Answer(q.ID, task.Choose("claude", "reviewer"))
	if f, o := <-frag, <-ok; !o || f.Driver != "claude" {
		t.Fatalf("ask select = (%+v, %v), want driver claude, true — the first pick decides", f, o)
	}
}

// An answer naming something not on the ballot seats nobody. The answer path refuses such a
// choice before it is recorded; a resolver answering the engine directly is the one way it
// arrives, and then the selection spawns nothing rather than a doer nobody offered.
func TestAnAnswerNotOnTheBallotSeatsNobody(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "ask"}
	frag, ok, q := selecting(t, eng, reg, launch.Spec{Provider: p, Chain: "review"}, entity.RefAt("change", "pr", "3"))
	reg.Answer(q.ID, task.Choose("rogue", "x"))
	if f, o := <-frag, <-ok; o || !f.IsZero() {
		t.Fatalf("select = (%+v, %v), want (zero, false) — a rogue choice must not seat anyone", f, o)
	}
}

// `within` is the question's deadline, with no default: the clock passing settles it on
// nothing, and nothing spawns. The clock is the answer path's; here it is the resolver.
func TestSelectProviderWithinIsTheDeadline(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: "vote", Need: 2, Within: "4h"}
	frag, ok, q := selecting(t, eng, reg, launch.Spec{Provider: p, Chain: "review"}, entity.RefAt("change", "pr", "dl"))
	if q.Deadline == nil || q.Deadline.Within != "4h" || q.Deadline.Default != "" {
		t.Fatalf("deadline = %+v, want 4h with no default", q.Deadline)
	}
	reg.Answer(q.ID, task.Choose(task.NoOption, task.ByDeadline))
	if f, o := <-frag, <-ok; o || !f.IsZero() {
		t.Fatalf("deadline select = (%+v, %v), want (zero, false) — no pick by the deadline", f, o)
	}
}

// The ballot carries each doer's envelope, so what a choice would authorise is beside the
// name it is cast for.
func TestASelectionPosesEachDoersEnvelope(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	entitytest.Set(t, store, entity.RefAt("provider", "treat", "fixer"),
		entity.Obj{"driver": "llm", "grant": "no-delegate", "tools": []any{"read_file", "write_file"}}.Payload())
	entitytest.Set(t, store, entity.RefAt("provider", "treat", "memo"),
		entity.Obj{"driver": "llm", "grant": "read-only"}.Payload())
	spawn := launch.Spec{Grant: "all", Chain: "treat", Provider: &launch.Provider{Capability: "treat", Select: "ask"}}
	_, _, q := selecting(t, eng, reg, spawn, entity.RefAt("demo", "finding", "7"))
	byID := map[string]task.Option{}
	for _, o := range q.Options {
		byID[o.ID] = o
	}
	if f := byID["fixer"]; f.Grant != "no-delegate" || strings.Join(f.Tools, ",") != "read_file,write_file" {
		t.Fatalf("fixer's envelope on the ballot = %+v", f)
	}
	if m := byID["memo"]; m.Grant != "read-only" || len(m.Tools) != 0 {
		t.Fatalf("memo's envelope on the ballot = %+v", m)
	}
	reg.Answer(q.ID, task.Choose("memo", "x"))
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
	frag, ok := eng.selectProvider(launch.Spec{Provider: p}, entity.RefAt("change", "pr", "9"))
	if !ok || frag.Driver != "llm-claude" { // options sorted by id: claude, gpt → auto picks claude
		t.Fatalf("registry auto select = (%+v, %v), want driver llm-claude, true", frag, ok)
	}
	options, _ := eng.eligibleProviders(launch.Spec{Provider: p})
	if len(options) != 2 || options[0] != "claude" || options[1] != "gpt" {
		t.Fatalf("eligible options = %v, want [claude gpt] (the basic tier filtered out)", options)
	}
}

// An empty eligible set poses no question and spawns nothing, so from outside the firing is
// indistinguishable from a trigger that never matched. The three causes are fixed in three
// different places, so the message has to say which one it is.
func TestWhyNoEligibleNamesTheFix(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, nil)

	p := &launch.Provider{Capability: "review", Match: map[string]any{"tier": "premium"}}
	got := eng.whyNoEligible(launch.Spec{Provider: p})
	if !strings.Contains(got, "no provider.review.* is registered") {
		t.Errorf("empty registry: %q", got)
	}
	if !strings.Contains(got, "verse data provider review ") {
		t.Errorf("empty registry should show how to register one: %q", got)
	}
	entitytest.Set(t, store, entity.RefAt("provider", "review", "cheapo"),
		entity.Obj{"driver": "scripted", "tier": "basic"}.Payload())
	got = eng.whyNoEligible(launch.Spec{Provider: p})
	if !strings.Contains(got, "all 1 registered") {
		t.Errorf("excluded: %q", got)
	}
	if !strings.Contains(got, "tier: premium") {
		t.Errorf("excluded should echo the match in tony: %q", got)
	}
	if got := eng.whyNoEligible(launch.Spec{Provider: &launch.Provider{Select: "vote"}}); !strings.Contains(got, "no eligible doers") {
		t.Errorf("empty provider: %q", got)
	}
}

// A question's id is DERIVED from the action, the subject and the spec, so the derivation has
// to be stable: an unstable one silently poses a second question on every re-fire, splitting
// the ballots. And distinct across what should be distinct.
func TestSelectIDIsStableAndDistinct(t *testing.T) {
	subj := entity.RefAt("demo", "pr", "1")
	spec := func() *launch.Provider {
		return &launch.Provider{
			Eligible:   []string{"gpt", "claude"},
			Capability: "review",
			Match:      map[string]any{"tier": "premium", "region": "eu"},
		}
	}
	first := selectID("review", subj, spec())
	for i := range 5 {
		if got := selectID("review", subj, spec()); got != first {
			t.Fatalf("call %d differs — a re-fire would pose a NEW question:\n %s\n %s", i+1, first, got)
		}
	}
	if !strings.HasPrefix(first, "verse.trigger.review.provider"+task.GateSep) {
		t.Errorf("id = %s, want it to read as the rule's provider question", first)
	}
	other := spec()
	other.Eligible = []string{"gpt"}
	if selectID("review", subj, other) == first {
		t.Error("a different eligibility set produced the same id")
	}
	if selectID("review", entity.RefAt("demo", "pr", "2"), spec()) == first {
		t.Error("a different subject produced the same id")
	}
	if selectID("review.onDone", subj, spec()) == first {
		t.Error("a different action produced the same id")
	}
}

var _ = context.Background
