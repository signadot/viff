package main

import (
	"testing"

	"github.com/signadot/verse/internal/config"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/runner/runnerd/stock"
	verseagent "github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/systems"
)

// THE LOCAL RUNNER TAKES A VIRTUAL KEY: a firing whose spawn names one carries it as the
// agent's `verse.portkey/virtual-key`, and `verse up`'s runner offers that annotation, so the
// launch is run on the key and not refused at every firing.
func TestTheLocalRunnerTakesAVirtualKey(t *testing.T) {
	cfg, _ := localRunnerOffers(config.LLMConfig{APIKey: "pk"}, nil)
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	l := runner.Launch{ID: "t1", Prompt: "hi", Agent: &runner.Agent{Name: stock.AgentName,
		Annotations: map[string]string{verseagent.AnnotationVirtualKey: "vk-team"}},
		Environment: systems.Environment{Systems: systems.Systems{"network": {{Attributes: map[string]string{"mode": runner.NetworkOpen}}}}}}
	if err := runnerd.CheckOffer(&l, cfg.Describe([]string{runner.NetworkOpen})); err != nil {
		t.Errorf("a launch on a virtual key was refused: %v", err)
	}
}
