package trigger

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
)

// A motion channel id is DERIVED from its spec, so the derivation has to be stable: an
// unstable one silently opens a second channel on every re-fire, splitting the vote log and
// breaking terminal-once.
//
// This is not hypothetical. The obvious implementation — ir.Node.Hash() — is not stable
// across two calls in the same process (tony-format item 16), and this test is what caught
// it.
func TestProviderSpecSeedIsStable(t *testing.T) {
	subj := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	spec := func() *launch.Provider {
		return &launch.Provider{
			Eligible:   []string{"gpt", "claude"},
			Capability: "review",
			Match:      map[string]any{"tier": "premium", "region": "eu"},
		}
	}

	first := providerSpecSeed(subj, spec())
	for i := range 5 {
		if got := providerSpecSeed(subj, spec()); got != first {
			t.Fatalf("call %d differs — a re-fire would open a NEW channel:\n %s\n %s",
				i+1, first, got)
		}
	}
	if id := motionChannelID(first); motionChannelID(providerSpecSeed(subj, spec())) != id {
		t.Error("the derived channel id is not stable")
	}

	// A different spec must land on a different channel, or two selections would share
	// one vote log.
	other := spec()
	other.Eligible = []string{"gpt"}
	if providerSpecSeed(subj, other) == first {
		t.Error("a different eligibility set produced the same seed")
	}
	if providerSpecSeed(entity.Ref{System: "demo", Kind: "pr", ID: "2"}, spec()) == first {
		t.Error("a different subject produced the same seed")
	}
}
