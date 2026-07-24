package trigger

import (
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
)

// auto-select picks the first eligible doer and reifies the choice on a select channel.
func TestSelectProviderAuto(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: launch.SurfaceAuto}
	subj := entity.Ref{System: "change", Kind: "pr", ID: "1"}

	driver, ok := eng.selectProvider(p, subj)
	if !ok || driver != "gpt" {
		t.Fatalf("auto select = (%q, %v), want (gpt, true)", driver, ok)
	}
	ch, found := store.Get(entity.Ref{System: "decision", Kind: "channel", ID: selectionID(subj, p)})
	if !found || ch.Payload["kind"] != "select" || ch.Payload["selected"] != "gpt" || ch.Payload["status"] != "closed" {
		t.Fatalf("select channel = %+v, want kind=select selected=gpt status=closed", ch.Payload)
	}
}

// vote-select picks the option that reaches the plurality threshold over the vote log.
func TestSelectProviderVotePlurality(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: launch.SurfaceVote, Need: 2}
	subj := entity.Ref{System: "change", Kind: "pr", ID: "2"}
	did := selectionID(subj, p)

	// two ballots for claude, one for gpt → claude wins at need=2.
	store.Put(entity.Ref{System: "vote", Kind: did, ID: "x"}, entity.Payload{"choice": "claude"})
	store.Put(entity.Ref{System: "vote", Kind: did, ID: "y"}, entity.Payload{"choice": "claude"})
	store.Put(entity.Ref{System: "vote", Kind: did, ID: "z"}, entity.Payload{"choice": "gpt"})

	driver, ok := eng.selectProvider(p, subj)
	if !ok || driver != "claude" {
		t.Fatalf("vote select = (%q, %v), want (claude, true)", driver, ok)
	}
}

// a ballot for a non-eligible option does not count: a rogue vote can't win; an eligible
// option reaching the threshold does.
func TestPluralityIgnoresIneligible(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: launch.SurfaceVote, Need: 1}
	subj := entity.Ref{System: "change", Kind: "pr", ID: "3"}
	did := selectionID(subj, p)
	store.Put(entity.Ref{System: "vote", Kind: did, ID: "x"}, entity.Payload{"choice": "rogue"}) // ineligible
	store.Put(entity.Ref{System: "vote", Kind: did, ID: "y"}, entity.Payload{"choice": "gpt"})   // eligible → wins at need=1

	driver, ok := eng.selectProvider(p, subj)
	if !ok || driver != "gpt" {
		t.Fatalf("select = (%q, %v), want (gpt, true) — a rogue (ineligible) ballot must not win", driver, ok)
	}
}

// ask-select: a party picks — the first valid pick decides (need is forced to 1).
func TestSelectProviderAsk(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: launch.SurfaceAsk}
	subj := entity.Ref{System: "change", Kind: "pr", ID: "4"}
	did := selectionID(subj, p)

	store.Put(entity.Ref{System: "vote", Kind: did, ID: "reviewer"}, entity.Payload{"choice": "claude"})
	driver, ok := eng.selectProvider(p, subj)
	if !ok || driver != "claude" {
		t.Fatalf("ask select = (%q, %v), want (claude, true) — the first pick decides", driver, ok)
	}
}

// Registry eligibility: providers are entities; a tony match over their attributes filters
// the eligible set. Here tier=premium admits gpt & claude (not the basic one); auto picks
// the first (by id), and the option resolves to that provider's `driver`.
func TestSelectProviderRegistryMatch(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	store.Put(entity.Ref{System: "provider", Kind: "review", ID: "gpt"}, entity.Payload{"driver": "llm-gpt", "tier": "premium"})
	store.Put(entity.Ref{System: "provider", Kind: "review", ID: "claude"}, entity.Payload{"driver": "llm-claude", "tier": "premium"})
	store.Put(entity.Ref{System: "provider", Kind: "review", ID: "cheapo"}, entity.Payload{"driver": "scripted", "tier": "basic"})

	p := &launch.Provider{Capability: "review", Match: map[string]any{"tier": "premium"}, Select: launch.SurfaceAuto}
	subj := entity.Ref{System: "change", Kind: "pr", ID: "9"}

	driver, ok := eng.selectProvider(p, subj)
	if !ok || driver != "llm-claude" { // options sorted by id: claude, gpt → auto picks claude
		t.Fatalf("registry auto select = (%q, %v), want (llm-claude, true)", driver, ok)
	}
	ch, _ := store.Get(entity.Ref{System: "decision", Kind: "channel", ID: selectionID(subj, p)})
	if opts, _ := ch.Payload["options"].([]string); len(opts) != 2 {
		t.Fatalf("eligible options = %v, want 2 premium providers (basic filtered out)", ch.Payload["options"])
	}
}

// A vote-select with a deadline that passes without reaching the threshold resolves to no
// pick — the who-axis analog of an `until` gate defaulting on its deadline. It must return
// (,false) promptly rather than block open-endedly (the pre-deadline hang).
func TestSelectProviderVoteDeadline(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	p := &launch.Provider{Eligible: []string{"gpt", "claude"}, Select: launch.SurfaceVote, Need: 2, Within: "60ms"}
	subj := entity.Ref{System: "change", Kind: "pr", ID: "dl"}

	done := make(chan struct{})
	var driver string
	var ok bool
	go func() { driver, ok = eng.selectProvider(p, subj); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("selectProvider hung past its deadline (should default to no pick)")
	}
	if ok || driver != "" {
		t.Fatalf("deadline select = (%q, %v), want (\"\", false) — no pick by the deadline", driver, ok)
	}
	// The channel records when it closes, so `verse decisions` can show it.
	ch, _ := store.Get(entity.Ref{System: "decision", Kind: "channel", ID: selectionID(subj, p)})
	if _, stamped := ch.Payload["deadline_at"]; !stamped {
		t.Fatalf("select channel missing deadline_at: %+v", ch.Payload)
	}
}

// commitSelection is terminal-once: the first choice latches; a later one is a no-op.
func TestCommitSelectionTerminalOnce(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	ref := entity.Ref{System: "decision", Kind: "channel", ID: "s1"}
	if got := eng.commitSelection(ref, "gpt"); got != "gpt" {
		t.Fatalf("first commit = %q, want gpt", got)
	}
	if got := eng.commitSelection(ref, "claude"); got != "gpt" {
		t.Fatalf("terminal-once violated: second commit = %q, want gpt", got)
	}
}
